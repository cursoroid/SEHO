package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// SpotifyBackend plays Spotify through librespot, whose PCM output is piped
// into a dedicated mpv instance so the equalizer covers Spotify as well as
// local files.
//
//	librespot --backend pipe ──► fifo ──► mpv (rawaudio) ──► af chain ──► output
//
// Transport commands go to the Spotify Web API (librespot is a Connect device
// and takes its orders from Spotify, not from us), while volume and the filter
// chain go to mpv, which is where the audio actually passes through.
// attempt tracks one librespot spawn. failed closes when librespot rejects the
// credentials, cannot register as a Connect device, or exits before it gets
// there.
//
// There is deliberately no "ready" signal any more. An earlier version treated
// the log line "Authenticated as ..." as readiness, which is wrong: with a
// stale credential librespot authenticates its session and is then refused at
// spirc ("could not initialize spirc: Login request was denied"), so it looks
// ready and never becomes a device. The only trustworthy readiness test is
// Spotify listing the device, so that is what Load waits for.
type attempt struct {
	failed   chan struct{}
	failOnce sync.Once
	reason   string // librespot's own words, for the error the user sees
	oauth    bool   // this spawn took the interactive login path
}

func newAttempt(oauth bool) *attempt {
	return &attempt{failed: make(chan struct{}), oauth: oauth}
}

func (a *attempt) markFailed(reason string) {
	a.failOnce.Do(func() {
		a.reason = reason
		close(a.failed)
	})
}

type SpotifyBackend struct {
	api      *Spotify
	mpv      *Player
	cmd      *exec.Cmd
	fifo     string
	hold     *os.File // dummy writer: see StartSpotifyBackend
	cache    string
	device   string // librespot's advertised name
	cfg      Config // kept for a respawn when cached credentials turn out stale
	announce func(string)

	events chan Event
	closed chan struct{}
	once   sync.Once

	// att is the current spawn attempt's signals. Each spawn gets a fresh one so
	// a dying process can only ever report about its own attempt - resetting
	// shared channels let an old librespot's exit fail the retry that replaced
	// it.
	att atomic.Pointer[attempt]

	mu       sync.Mutex
	deviceID string
	uri      string  // what we last asked Spotify to play
	pos      float64 // seconds, at lastPoll
	dur      float64 // seconds
	playing  bool    // Spotify's own play/pause state
	lastPoll time.Time
	endSent  bool // guards against emitting end-file twice for one track
}

// pollInterval is how often Spotify is asked what it is doing. Anything faster
// buys no accuracy - the API's own progress figure is coarse - and walks toward
// the rate limit.
const pollInterval = time.Second

// tickInterval is how often an interpolated time-pos is emitted between polls.
// The transport bar's shimmer is a function of position, so a once-a-second
// update would make it lurch instead of glide.
const tickInterval = 100 * time.Millisecond

// Patterns matched against librespot's log. It has no machine-readable status
// output, so its own words are the only signal available.
var (
	// oauthURLRe finds the login URL it prints when it needs interactive sign-in.
	oauthURLRe = regexp.MustCompile(`https://accounts\.spotify\.com/\S*authorize\S*`)
	// audioKeyRe marks Spotify refusing the decryption key for a track. Since
	// librespot 0.7 Spotify has withheld audio keys from newer accounts whatever
	// the login method (librespot-org/librespot#1649): the session authenticates,
	// the device registers, playback is accepted, and then every track skips.
	// Nothing here can fix that, so the least SEHO can do is say so instead of
	// leaving a transport bar creeping along in silence.
	audioKeyRe = regexp.MustCompile(`(?i)error audio key|unable to load key`)
	// authFailRe marks credentials librespot will not accept. Spotify's own
	// wording is INVALID_CREDENTIALS inside "Login request was denied", so the
	// separator has to be optional - a space-only pattern misses it.
	authFailRe = regexp.MustCompile(`(?i)(bad|invalid)[ _-]?credentials|login request was denied|login failed|credentials are required|authentication failed|could not initialize spirc`)
)

// StartSpotifyBackend spawns librespot and the mpv instance that consumes its
// output. announce is called with librespot's OAuth URL when it needs an
// interactive login; the browser is opened for the user as well.
func StartSpotifyBackend(api *Spotify, cfg Config, announce func(string)) (*SpotifyBackend, error) {
	if _, err := exec.LookPath("librespot"); err != nil {
		return nil, errors.New("librespot not found on PATH - install it with: brew install librespot")
	}

	fifo := filepath.Join(os.TempDir(), fmt.Sprintf("seho-spotify-%d.pcm", os.Getpid()))
	os.Remove(fifo)
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		return nil, fmt.Errorf("create audio fifo: %w", err)
	}

	// Opening a fifo write-only BLOCKS until a reader arrives, and mpv only
	// opens it at loadfile - which cannot happen until after this returns. O_RDWR
	// does not block, and holding this handle for the process lifetime means the
	// reader never sees EOF when librespot closes its end between tracks.
	hold, err := os.OpenFile(fifo, os.O_RDWR, 0o600)
	if err != nil {
		os.Remove(fifo)
		return nil, fmt.Errorf("open audio fifo: %w", err)
	}

	mpv, err := StartPlayer(rawAudioArgs("s16le", 44100)...)
	if err != nil {
		hold.Close()
		os.Remove(fifo)
		return nil, err
	}
	if err := mpv.Load(fifo); err != nil {
		mpv.Close()
		hold.Close()
		os.Remove(fifo)
		return nil, fmt.Errorf("attach mpv to the audio fifo: %w", err)
	}

	s := &SpotifyBackend{
		api:      api,
		mpv:      mpv,
		fifo:     fifo,
		hold:     hold,
		cache:    filepath.Join(configDir(), "librespot"),
		device:   cfg.DeviceName,
		cfg:      cfg,
		announce: announce,
		events:   make(chan Event, 64),
		closed:   make(chan struct{}),
	}

	if err := s.spawnLibrespot(cfg, announce); err != nil {
		s.Close()
		return nil, err
	}

	go s.poll()
	go s.tick()
	go forwardMpvHealth(s.mpv, s.emit, s.closed)
	return s, nil
}

// spawnLibrespot starts librespot as a Connect device, using its cached
// credentials when it has them and its own interactive OAuth when it does not.
// The credential is cached under --system-cache afterwards, so the browser trip
// happens once per machine.
//
// Ruling, with evidence: librespot 0.8's --access-token, fed SEHO's own PKCE
// token (with the streaming scope requested), is rejected by Spotify -
// "could not initialize spirc: Login request was denied: INVALID_CREDENTIALS".
// A third-party client's token cannot open a librespot session, so the two
// logins are not an oversight to be tidied away later; they are what Spotify
// permits. Do not re-plumb --access-token without new evidence from upstream.
func (s *SpotifyBackend) spawnLibrespot(cfg Config, announce func(string)) error {
	return s.spawnWith(cfg, announce, false)
}

// spawnWith builds the command line. forceOAuth skips the token and goes
// straight to librespot's interactive login.
func (s *SpotifyBackend) spawnWith(cfg Config, announce func(string), forceOAuth bool) error {
	if err := os.MkdirAll(s.cache, 0o700); err != nil {
		return err
	}

	bitrate := cfg.Bitrate
	switch bitrate {
	case 96, 160, 320:
	default:
		bitrate = 320
	}

	args := []string{
		"--name", cfg.DeviceName,
		"--bitrate", fmt.Sprint(bitrate),
		"--backend", "pipe",
		"--device", s.fifo,
		"--cache", s.cache,
		"--system-cache", s.cache,
		"--disable-discovery", // we sign in with credentials; no mDNS needed
		// mpv owns volume, so librespot must not attenuate the PCM as well -
		// two volume curves in series is how you end up inaudible at 40%.
		"--volume-ctrl", "fixed",
		"--initial-volume", "100",
	}
	oauth := forceOAuth || !s.hasCachedCredentials()
	if oauth {
		args = append(args, "--enable-oauth")
	}
	att := newAttempt(oauth)
	s.att.Store(att)

	cmd := exec.Command("librespot", args...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	// Both streams are scanned: librespot logs to stderr, but its OAuth URL is a
	// plain println on stdout. Audio never lands there as long as --device names
	// the fifo, which it always does above - the pipe sink only falls back to
	// stdout when no device is given. ("Using StdoutSink (pipe)" is logged either
	// way; the file is opened lazily in the sink's start(), so that line says
	// nothing about which target is in use.)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start librespot: %w", err)
	}
	s.cmd = cmd

	go s.watchOutput(att, stderr, announce)
	go s.watchOutput(att, stdout, announce)

	// A librespot that dies before it authenticates must not leave Load waiting
	// out its whole timeout: its exit is the answer.
	go func(c *exec.Cmd, att *attempt) {
		err := c.Wait()
		log.Printf("librespot exited: %v", err)
		att.markFailed(fmt.Sprintf("librespot exited: %v", err))
	}(cmd, att)
	return nil
}

// retryWithOAuth restarts librespot on its own interactive login. Reached when
// cached credentials are stale - Spotify does revoke them - which would
// otherwise leave Spotify permanently broken with no way back but deleting a
// cache directory by hand.
func (s *SpotifyBackend) retryWithOAuth(cfg Config, announce func(string)) error {
	if s.cmd != nil && s.cmd.Process != nil {
		s.cmd.Process.Kill()
		s.cmd.Wait()
	}
	log.Print("librespot refused the access token; falling back to its own OAuth login")
	return s.spawnWith(cfg, announce, true)
}

// hasCachedCredentials reports whether librespot already holds a session, so a
// restart does not demand another login.
func (s *SpotifyBackend) hasCachedCredentials() bool {
	_, err := os.Stat(filepath.Join(s.cache, "credentials.json"))
	return err == nil
}

// watchOutput mirrors librespot's log into SEHO's log file and lifts out the
// OAuth URL when one appears.
func (s *SpotifyBackend) watchOutput(att *attempt, r io.Reader, announce func(string)) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 16*1024), 256*1024)
	for sc.Scan() {
		line := sc.Text()
		log.Printf("librespot: %s", line)
		if u := oauthURLRe.FindString(line); u != "" {
			if announce != nil {
				announce(u)
			}
			openBrowser(u)
		}
		switch {
		case authFailRe.MatchString(line):
			att.markFailed(line)
		case audioKeyRe.MatchString(line):
			s.emit(Event{Name: "audio-key-refused"})
		}
	}
}

func (s *SpotifyBackend) Events() <-chan Event { return s.events }

func (s *SpotifyBackend) emit(ev Event) { emitEvent(s.events, s.closed, ev) }

// session returns the Connect device id for our librespot, which is the only
// proof that it actually signed in. On a first run the wait includes however
// long the user takes in the browser, so the budget comes from ctx rather than
// a fixed retry count.
//
// A refused credential is recoverable exactly once: the cached credential is
// deleted and librespot is restarted on its own interactive login. Without that
// a single stale cache file would leave Spotify permanently broken with no way
// back but deleting a directory by hand.
func (s *SpotifyBackend) session(ctx context.Context) (string, error) {
	for round := 0; round < 2; round++ {
		att := s.att.Load()
		if att == nil {
			return "", errors.New("librespot was never started")
		}

		id, err := s.waitForDevice(ctx, att)
		if err == nil {
			return id, nil
		}
		if !errors.Is(err, errLibrespotAuth) || att.oauth || round == 1 {
			return "", err
		}

		// Cached credential refused: clear it and sign in properly.
		os.Remove(filepath.Join(s.cache, "credentials.json"))
		if err := s.retryWithOAuth(s.cfg, s.announce); err != nil {
			return "", err
		}
	}
	return "", errors.New("librespot could not sign in to Spotify")
}

// errLibrespotAuth marks a credential problem, the one failure worth retrying
// differently.
var errLibrespotAuth = errors.New("librespot credentials refused")

// waitForDevice polls Spotify's device list until our librespot appears, or
// until this attempt reports that it never will.
func (s *SpotifyBackend) waitForDevice(ctx context.Context, att *attempt) (string, error) {
	s.mu.Lock()
	id := s.deviceID
	s.mu.Unlock()
	if id != "" {
		return id, nil
	}

	for {
		if d, err := s.api.DeviceByName(ctx, s.device); err == nil {
			s.mu.Lock()
			s.deviceID = d.ID
			s.mu.Unlock()
			return d.ID, nil
		}

		select {
		case <-time.After(time.Second):
		case <-att.failed:
			return "", fmt.Errorf("%w: %s", errLibrespotAuth, att.reason)
		case <-s.closed:
			return "", errors.New("spotify backend closed")
		case <-ctx.Done():
			return "", errors.New("librespot did not register as a Spotify device in time")
		}
	}
}

// Load plays one Spotify track URI on our librespot device.
func (s *SpotifyBackend) Load(uri string) error {
	// Three minutes covers a first run where the user has to approve a login in
	// the browser; afterwards this is reached in under a second.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	id, err := s.session(ctx)
	if err != nil {
		return err
	}
	if err := s.api.Play(ctx, id, uri); err != nil {
		return err
	}

	s.mu.Lock()
	s.uri, s.pos, s.playing, s.endSent = uri, 0, true, false
	s.lastPoll = time.Now()
	s.mu.Unlock()

	s.emit(Event{Name: "pause", Flag: false})
	return nil
}

func (s *SpotifyBackend) TogglePause() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s.mu.Lock()
	id, playing := s.deviceID, s.playing
	// Optimistic local flip, reconciled by the next poll - the same contract
	// the mpv backend has with its property events.
	s.playing = !playing
	s.mu.Unlock()

	s.emit(Event{Name: "pause", Flag: playing})
	if playing {
		return s.api.Pause(ctx, id)
	}
	return s.api.Resume(ctx, id)
}

func (s *SpotifyBackend) Seek(delta float64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s.mu.Lock()
	id := s.deviceID
	target := s.pos + time.Since(s.lastPoll).Seconds() + delta
	if target < 0 {
		target = 0
	}
	if s.dur > 0 && target > s.dur {
		target = s.dur
	}
	s.pos, s.lastPoll = target, time.Now()
	s.mu.Unlock()

	s.emit(Event{Name: "time-pos", Num: target})
	return s.api.SeekTo(ctx, id, int(target*1000))
}

// SetVolume and SetAF go to mpv, not to Spotify: mpv is where the audio
// actually flows, and routing volume through the Web API would add a network
// round trip to every keypress.
// Stop pauses Spotify. There is no "unload" for a Connect device, and pausing
// is what actually frees the ears.
func (s *SpotifyBackend) Stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s.mu.Lock()
	id, playing := s.deviceID, s.playing
	s.playing = false
	s.mu.Unlock()

	if id == "" || !playing {
		return nil
	}
	return s.api.Pause(ctx, id)
}

func (s *SpotifyBackend) SetVolume(v int) error    { return s.mpv.SetVolume(v) }
func (s *SpotifyBackend) SetAF(chain string) error { return s.mpv.SetAF(chain) }

// Level comes from mpv for the same reason SetAF goes there: mpv is the end of
// the audio path, so it is the only component that knows whether the pipe is
// carrying sound or silence.
func (s *SpotifyBackend) Level() (float64, bool) { return s.mpv.Level() }

// poll asks Spotify what is playing once a second and reconciles local state.
func (s *SpotifyBackend) poll() {
	t := time.NewTicker(pollInterval)
	defer t.Stop()
	for {
		select {
		case <-s.closed:
			return
		case <-t.C:
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		st, ok, err := s.api.State(ctx)
		cancel()
		if err != nil {
			// A transient poll failure is not worth shouting about; the
			// interpolation keeps the bar moving and the next poll corrects it.
			log.Printf("spotify poll: %v", err)
			continue
		}
		if !ok {
			continue
		}
		s.reconcile(st)
	}
}

// reconcile folds one poll result into local state and emits whatever the UI
// needs to know. Split out from poll so it can be tested without a network.
func (s *SpotifyBackend) reconcile(st playerState) {
	pos := float64(st.ProgressMs) / 1000
	dur := float64(st.DurationMs) / 1000

	s.mu.Lock()
	wasURI, wasDur, wasPlaying := s.uri, s.dur, s.playing
	// A track we did not ask for means someone moved playback elsewhere in the
	// Spotify app. Follow it rather than fight it.
	s.uri, s.pos, s.dur, s.playing = st.URI, pos, dur, st.Playing
	s.lastPoll = time.Now()
	if st.URI != wasURI {
		s.endSent = false
	}
	// Spotify stops at the end of a single-URI play. Near the end and not
	// playing is the only reliable signal that the track finished, since the
	// API has no "ended" event.
	finished := !st.Playing && dur > 0 && pos >= dur-3 && !s.endSent
	if finished {
		s.endSent = true
	}
	s.mu.Unlock()

	if dur != wasDur {
		s.emit(Event{Name: "duration", Num: dur})
	}
	if st.Playing != wasPlaying {
		s.emit(Event{Name: "pause", Flag: !st.Playing})
	}
	s.emit(Event{Name: "time-pos", Num: pos})
	if finished {
		s.emit(Event{Name: "end-file", Reason: "eof"})
	}
}

// tick emits an interpolated position between polls. Spotify answers about once
// a second; the progress bar needs finer motion than that, and the position is
// simply the last poll plus the wall time since it.
func (s *SpotifyBackend) tick() {
	t := time.NewTicker(tickInterval)
	defer t.Stop()
	for {
		select {
		case <-s.closed:
			return
		case <-t.C:
		}

		s.mu.Lock()
		playing, pos, dur, since := s.playing, s.pos, s.dur, time.Since(s.lastPoll).Seconds()
		s.mu.Unlock()
		if !playing {
			continue
		}
		p := pos + since
		if dur > 0 && p > dur {
			p = dur
		}
		s.emit(Event{Name: "time-pos", Num: p})
	}
}

func (s *SpotifyBackend) Close() error {
	s.once.Do(func() {
		close(s.closed)

		// Pause first: librespot exiting mid-track leaves Spotify believing the
		// device is still active, and the account then shows a phantom player.
		if s.api != nil {
			s.mu.Lock()
			id, playing := s.deviceID, s.playing
			s.mu.Unlock()
			if id != "" && playing {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				s.api.Pause(ctx, id)
				cancel()
			}
		}

		if s.cmd != nil && s.cmd.Process != nil {
			s.cmd.Process.Kill()
			s.cmd.Wait()
		}
		if s.mpv != nil {
			s.mpv.Close()
		}
		if s.hold != nil {
			s.hold.Close()
		}
		if s.fifo != "" {
			os.Remove(s.fifo)
		}
	})
	return nil
}

// librespotInstalled is used by the settings page to report the dependency.
func librespotInstalled() bool {
	_, err := exec.LookPath("librespot")
	return err == nil
}

// librespotVersion is shown on the settings page; empty when librespot is
// missing or refuses to answer.
func librespotVersion() string {
	out, err := exec.Command("librespot", "--version").Output()
	if err != nil {
		return ""
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) >= 2 {
		return fields[1]
	}
	return ""
}
