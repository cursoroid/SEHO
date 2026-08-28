package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// spotifyAuthURL and friends are vars, not consts, so tests can point the
// client at an httptest server. Nothing else reassigns them.
var (
	spotifyAuthURL  = "https://accounts.spotify.com/authorize"
	spotifyTokenURL = "https://accounts.spotify.com/api/token"
	spotifyAPIBase  = "https://api.spotify.com/v1"
)

const (
	spotifyRedirect  = "http://127.0.0.1:8898/callback"
	spotifyCallbackA = "127.0.0.1:8898"

	// user-read-playback-state and user-modify-playback-state drive the
	// transport; user-library-read and playlist-read-private drive browsing.
	// Nothing here grants write access to the library.
	spotifyScopes = "user-read-playback-state user-modify-playback-state user-library-read playlist-read-private"
)

// ErrNoPremium is returned when Spotify refuses a playback command because the
// account is not Premium. Browsing keeps working, so this is reported rather
// than fatal.
var ErrNoPremium = errors.New("spotify premium required for playback")

// ErrNotConnected means no usable credentials: either no client id configured
// or no refresh token stored yet.
var ErrNotConnected = errors.New("spotify not connected")

// Spotify is a Web API client. One instance is shared by the browse code and
// the playback backend; it owns token refresh so neither has to think about it.
type Spotify struct {
	clientID string
	http     *http.Client

	mu      sync.Mutex
	access  string
	expiry  time.Time
	refresh string
}

func NewSpotify(clientID string) *Spotify {
	tok, _ := loadRefreshToken()
	return &Spotify{
		clientID: clientID,
		refresh:  tok,
		http:     &http.Client{Timeout: 15 * time.Second},
	}
}

// Connected reports whether a request could plausibly succeed. It is a
// configuration check, not a liveness check.
func (s *Spotify) Connected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clientID != "" && s.refresh != ""
}

func (s *Spotify) SetClientID(id string) {
	s.mu.Lock()
	s.clientID = id
	s.mu.Unlock()
}

// Disconnect forgets the stored credential.
func (s *Spotify) Disconnect() error {
	s.mu.Lock()
	s.refresh, s.access, s.expiry = "", "", time.Time{}
	s.mu.Unlock()
	return clearRefreshToken()
}

// --- OAuth (Authorization Code with PKCE) ----------------------------------

// pkce is one login attempt's verifier/challenge pair.
type pkce struct{ verifier, challenge string }

func newPKCE() (pkce, error) {
	b := make([]byte, 64)
	if _, err := rand.Read(b); err != nil {
		return pkce{}, err
	}
	v := base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(v))
	return pkce{verifier: v, challenge: base64.RawURLEncoding.EncodeToString(sum[:])}, nil
}

func randomState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Authorize runs the full login: it starts a loopback server, opens the
// browser, waits for the redirect, exchanges the code and stores the refresh
// token. It blocks until the user finishes or ctx expires, so callers run it
// off the UI goroutine.
//
// PKCE with no client secret is the only correct choice here: a desktop app
// cannot keep a secret, and Spotify's PKCE flow exists precisely for that.
func (s *Spotify) Authorize(ctx context.Context, announce func(url string)) error {
	s.mu.Lock()
	clientID := s.clientID
	s.mu.Unlock()
	if clientID == "" {
		return errors.New("set a Spotify client id in settings first")
	}

	p, err := newPKCE()
	if err != nil {
		return err
	}
	state, err := randomState()
	if err != nil {
		return err
	}

	ln, err := net.Listen("tcp", spotifyCallbackA)
	if err != nil {
		return fmt.Errorf("cannot listen on %s (another login in progress?): %w", spotifyCallbackA, err)
	}

	type result struct {
		code string
		err  error
	}
	done := make(chan result, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		// The state check is what stops a different page in the browser from
		// feeding us a code for someone else's session.
		switch {
		case q.Get("state") != state:
			http.Error(w, "state mismatch", http.StatusBadRequest)
			done <- result{err: errors.New("state mismatch in callback")}
		case q.Get("error") != "":
			io.WriteString(w, "Spotify login failed: "+q.Get("error"))
			done <- result{err: fmt.Errorf("spotify denied the login: %s", q.Get("error"))}
		default:
			io.WriteString(w, "SEHO is connected. You can close this tab.")
			done <- result{code: q.Get("code")}
		}
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	defer srv.Close()

	authURL := spotifyAuthURL + "?" + url.Values{
		"client_id":             {clientID},
		"response_type":         {"code"},
		"redirect_uri":          {spotifyRedirect},
		"state":                 {state},
		"scope":                 {spotifyScopes},
		"code_challenge_method": {"S256"},
		"code_challenge":        {p.challenge},
	}.Encode()

	if announce != nil {
		announce(authURL)
	}
	openBrowser(authURL)

	select {
	case <-ctx.Done():
		return ctx.Err()
	case r := <-done:
		if r.err != nil {
			return r.err
		}
		return s.exchange(ctx, r.code, p.verifier)
	}
}

func (s *Spotify) exchange(ctx context.Context, code, verifier string) error {
	s.mu.Lock()
	clientID := s.clientID
	s.mu.Unlock()

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {spotifyRedirect},
		"client_id":     {clientID},
		"code_verifier": {verifier},
	}
	tok, err := s.postToken(ctx, form)
	if err != nil {
		return err
	}
	if tok.RefreshToken == "" {
		return errors.New("spotify returned no refresh token")
	}
	s.mu.Lock()
	s.access, s.refresh = tok.AccessToken, tok.RefreshToken
	s.expiry = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	s.mu.Unlock()
	return saveRefreshToken(tok.RefreshToken)
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

func (s *Spotify) postToken(ctx context.Context, form url.Values) (tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, spotifyTokenURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := s.http.Do(req)
	if err != nil {
		return tokenResponse{}, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return tokenResponse{}, fmt.Errorf("spotify token endpoint: %s: %s",
			resp.Status, strings.TrimSpace(string(body)))
	}
	var tok tokenResponse
	if err := json.Unmarshal(body, &tok); err != nil {
		return tokenResponse{}, err
	}
	return tok, nil
}

// AccessToken returns a valid access token, refreshing when it is within a
// minute of expiry. The minute of slack keeps a long request from starting with
// a token that dies mid-flight.
func (s *Spotify) AccessToken(ctx context.Context) (string, error) {
	s.mu.Lock()
	access, expiry, refresh, clientID := s.access, s.expiry, s.refresh, s.clientID
	s.mu.Unlock()

	if access != "" && time.Until(expiry) > time.Minute {
		return access, nil
	}
	if refresh == "" || clientID == "" {
		return "", ErrNotConnected
	}

	tok, err := s.postToken(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refresh},
		"client_id":     {clientID},
	})
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	s.access = tok.AccessToken
	s.expiry = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	// Spotify rotates refresh tokens: keep the new one or the next refresh
	// fails and the user is silently logged out.
	if tok.RefreshToken != "" && tok.RefreshToken != s.refresh {
		s.refresh = tok.RefreshToken
		go func(t string) {
			if err := saveRefreshToken(t); err != nil {
				log.Printf("store rotated spotify refresh token: %v", err)
			}
		}(tok.RefreshToken)
	}
	s.mu.Unlock()
	return tok.AccessToken, nil
}

// --- request plumbing ------------------------------------------------------

// do issues one authenticated API call, retrying once after a 401 (expired
// token) and honouring 429's Retry-After. Callers get a decoded body or an
// error; nobody outside this file touches http status codes.
func (s *Spotify) do(ctx context.Context, method, path string, body any, out any) error {
	for attempt := 0; attempt < 2; attempt++ {
		tok, err := s.AccessToken(ctx)
		if err != nil {
			return err
		}

		var rdr io.Reader
		if body != nil {
			b, err := json.Marshal(body)
			if err != nil {
				return err
			}
			rdr = strings.NewReader(string(b))
		}

		req, err := http.NewRequestWithContext(ctx, method, spotifyAPIBase+path, rdr)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := s.http.Do(req)
		if err != nil {
			return err
		}

		switch resp.StatusCode {
		case http.StatusUnauthorized:
			resp.Body.Close()
			// Force a refresh and try once more.
			s.mu.Lock()
			s.access, s.expiry = "", time.Time{}
			s.mu.Unlock()
			continue
		case http.StatusTooManyRequests:
			wait := retryAfter(resp.Header.Get("Retry-After"))
			resp.Body.Close()
			return fmt.Errorf("spotify rate limited, retry in %s", wait)
		case http.StatusForbidden:
			msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
			resp.Body.Close()
			if strings.Contains(strings.ToLower(string(msg)), "premium") {
				return ErrNoPremium
			}
			return fmt.Errorf("spotify refused the request: %s", strings.TrimSpace(string(msg)))
		case http.StatusNoContent:
			resp.Body.Close()
			return nil
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
			resp.Body.Close()
			return fmt.Errorf("spotify %s: %s", resp.Status, strings.TrimSpace(string(msg)))
		}

		defer resp.Body.Close()
		if out == nil {
			return nil
		}
		return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out)
	}
	return errors.New("spotify session expired")
}

func retryAfter(h string) time.Duration {
	if n, err := strconv.Atoi(h); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	return 5 * time.Second
}

// --- browse ---------------------------------------------------------------

// apiTrack is the subset of Spotify's track object SEHO displays.
type apiTrack struct {
	Name       string `json:"name"`
	URI        string `json:"uri"`
	DurationMs int    `json:"duration_ms"`
	Album      struct {
		Name   string `json:"name"`
		Images []struct {
			URL    string `json:"url"`
			Width  int    `json:"width"`
			Height int    `json:"height"`
		} `json:"images"`
	} `json:"album"`
	Artists []struct {
		Name string `json:"name"`
	} `json:"artists"`
}

func (t apiTrack) item() item {
	artists := make([]string, 0, len(t.Artists))
	for _, a := range t.Artists {
		artists = append(artists, a.Name)
	}
	desc := strings.Join(artists, ", ")
	if desc == "" {
		desc = "Unknown artist"
	}
	return item{
		title:    t.Name,
		desc:     desc,
		album:    t.Album.Name,
		path:     t.URI,
		duration: float64(t.DurationMs) / 1000,
		src:      srcSpotify,
		artURL:   smallestImage(t.Album.Images),
	}
}

// smallestImage picks the smallest cover Spotify offers: the card renders it at
// about 28 cells, so downloading the 640px version would be waste.
func smallestImage(images []struct {
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}) string {
	best, bestW := "", 1<<30
	for _, im := range images {
		if im.Width > 0 && im.Width < bestW {
			best, bestW = im.URL, im.Width
		}
	}
	if best == "" && len(images) > 0 {
		best = images[len(images)-1].URL
	}
	return best
}

// Search returns tracks matching q.
func (s *Spotify) Search(ctx context.Context, q string, limit int) ([]item, error) {
	var out struct {
		Tracks struct{ Items []apiTrack } `json:"tracks"`
	}
	path := "/search?" + url.Values{
		"q": {q}, "type": {"track"}, "limit": {strconv.Itoa(limit)},
	}.Encode()
	if err := s.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return toItems(out.Tracks.Items), nil
}

// Liked returns saved tracks, newest first, following pagination up to limit.
func (s *Spotify) Liked(ctx context.Context, limit int) ([]item, error) {
	var items []item
	for offset := 0; offset < limit; offset += 50 {
		var out struct {
			Items []struct {
				Track apiTrack `json:"track"`
			} `json:"items"`
			Next string `json:"next"`
		}
		path := "/me/tracks?" + url.Values{
			"limit": {"50"}, "offset": {strconv.Itoa(offset)},
		}.Encode()
		if err := s.do(ctx, http.MethodGet, path, nil, &out); err != nil {
			return items, err
		}
		for _, e := range out.Items {
			if e.Track.URI != "" {
				items = append(items, e.Track.item())
			}
		}
		if out.Next == "" || len(out.Items) == 0 {
			break
		}
	}
	return items, nil
}

// playlist is one entry in the user's playlist list.
type playlist struct {
	ID     string
	Name   string
	Owner  string
	Tracks int
}

func (s *Spotify) Playlists(ctx context.Context) ([]playlist, error) {
	var lists []playlist
	for offset := 0; ; offset += 50 {
		var out struct {
			Items []struct {
				ID    string `json:"id"`
				Name  string `json:"name"`
				Owner struct {
					DisplayName string `json:"display_name"`
				} `json:"owner"`
				Tracks struct {
					Total int `json:"total"`
				} `json:"tracks"`
			} `json:"items"`
			Next string `json:"next"`
		}
		path := "/me/playlists?" + url.Values{
			"limit": {"50"}, "offset": {strconv.Itoa(offset)},
		}.Encode()
		if err := s.do(ctx, http.MethodGet, path, nil, &out); err != nil {
			return lists, err
		}
		for _, e := range out.Items {
			lists = append(lists, playlist{
				ID: e.ID, Name: e.Name, Owner: e.Owner.DisplayName, Tracks: e.Tracks.Total,
			})
		}
		if out.Next == "" || len(out.Items) == 0 {
			break
		}
	}
	return lists, nil
}

// PlaylistTracks returns a playlist's tracks. Episodes and unavailable entries
// come back from Spotify as tracks with an empty URI; they are dropped rather
// than shown as unplayable rows.
func (s *Spotify) PlaylistTracks(ctx context.Context, id string, limit int) ([]item, error) {
	var items []item
	for offset := 0; offset < limit; offset += 100 {
		var out struct {
			Items []struct {
				Track apiTrack `json:"track"`
			} `json:"items"`
			Next string `json:"next"`
		}
		path := "/playlists/" + url.PathEscape(id) + "/tracks?" + url.Values{
			"limit": {"100"}, "offset": {strconv.Itoa(offset)},
		}.Encode()
		if err := s.do(ctx, http.MethodGet, path, nil, &out); err != nil {
			return items, err
		}
		for _, e := range out.Items {
			if e.Track.URI != "" && strings.HasPrefix(e.Track.URI, "spotify:track:") {
				items = append(items, e.Track.item())
			}
		}
		if out.Next == "" || len(out.Items) == 0 {
			break
		}
	}
	return items, nil
}

func toItems(tracks []apiTrack) []item {
	items := make([]item, 0, len(tracks))
	for _, t := range tracks {
		if t.URI != "" {
			items = append(items, t.item())
		}
	}
	return items
}

// --- player control -------------------------------------------------------

// device is one Spotify Connect endpoint.
type device struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Active bool   `json:"is_active"`
}

func (s *Spotify) Devices(ctx context.Context) ([]device, error) {
	var out struct {
		Devices []device `json:"devices"`
	}
	if err := s.do(ctx, http.MethodGet, "/me/player/devices", nil, &out); err != nil {
		return nil, err
	}
	return out.Devices, nil
}

// DeviceByName finds our librespot instance among the account's devices. It is
// looked up by name because librespot's device id is assigned by Spotify, not
// by us, and changes between runs.
func (s *Spotify) DeviceByName(ctx context.Context, name string) (device, error) {
	devs, err := s.Devices(ctx)
	if err != nil {
		return device{}, err
	}
	for _, d := range devs {
		if d.Name == name {
			return d, nil
		}
	}
	return device{}, fmt.Errorf("device %q not visible to Spotify yet", name)
}

func (s *Spotify) Transfer(ctx context.Context, deviceID string, play bool) error {
	return s.do(ctx, http.MethodPut, "/me/player",
		map[string]any{"device_ids": []string{deviceID}, "play": play}, nil)
}

func (s *Spotify) Play(ctx context.Context, deviceID, uri string) error {
	q := ""
	if deviceID != "" {
		q = "?device_id=" + url.QueryEscape(deviceID)
	}
	return s.do(ctx, http.MethodPut, "/me/player/play"+q,
		map[string]any{"uris": []string{uri}}, nil)
}

func (s *Spotify) Resume(ctx context.Context, deviceID string) error {
	q := ""
	if deviceID != "" {
		q = "?device_id=" + url.QueryEscape(deviceID)
	}
	return s.do(ctx, http.MethodPut, "/me/player/play"+q, map[string]any{}, nil)
}

func (s *Spotify) Pause(ctx context.Context, deviceID string) error {
	q := ""
	if deviceID != "" {
		q = "?device_id=" + url.QueryEscape(deviceID)
	}
	return s.do(ctx, http.MethodPut, "/me/player/pause"+q, nil, nil)
}

func (s *Spotify) SeekTo(ctx context.Context, deviceID string, posMs int) error {
	v := url.Values{"position_ms": {strconv.Itoa(max(0, posMs))}}
	if deviceID != "" {
		v.Set("device_id", deviceID)
	}
	return s.do(ctx, http.MethodPut, "/me/player/seek?"+v.Encode(), nil, nil)
}

// playerState is the poller's view of what Spotify believes is happening.
type playerState struct {
	Playing    bool
	ProgressMs int
	DurationMs int
	URI        string
	Title      string
	Artist     string
	Album      string
	ArtURL     string
	DeviceID   string
	DeviceName string
}

// State reads current playback. ok is false when Spotify has no active player,
// which is not an error: it is the normal state before the first play.
func (s *Spotify) State(ctx context.Context) (st playerState, ok bool, err error) {
	var out struct {
		IsPlaying  bool     `json:"is_playing"`
		ProgressMs int      `json:"progress_ms"`
		Item       apiTrack `json:"item"`
		Device     device   `json:"device"`
	}
	if err := s.do(ctx, http.MethodGet, "/me/player", nil, &out); err != nil {
		return playerState{}, false, err
	}
	if out.Item.URI == "" {
		return playerState{}, false, nil
	}
	it := out.Item.item()
	return playerState{
		Playing:    out.IsPlaying,
		ProgressMs: out.ProgressMs,
		DurationMs: out.Item.DurationMs,
		URI:        out.Item.URI,
		Title:      it.title,
		Artist:     it.desc,
		Album:      it.album,
		ArtURL:     it.artURL,
		DeviceID:   out.Device.ID,
		DeviceName: out.Device.Name,
	}, true, nil
}

// openBrowser is best effort: when it fails the caller has already printed the
// URL for the user to open by hand.
func openBrowser(u string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	if err := cmd.Start(); err != nil {
		log.Printf("open browser: %v", err)
		return
	}
	go cmd.Wait()
}
