package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rivo/tview"
)

// spotifyFetchTimeout bounds one browse request. Long enough for a slow
// connection, short enough that a hung call does not look like a frozen UI.
const spotifyFetchTimeout = 20 * time.Second

// openSpotifySection handles a sidebar row below the separator. Every path here
// checks credentials first: a view that renders empty because nobody is signed
// in reads as a broken feature.
func (u *UI) openSpotifySection(section string) {
	if !u.api.Connected() {
		reason := "connect Spotify in settings first (press ,)"
		if u.set.Eff.SpotifyClientID == "" {
			reason = "no Spotify client id yet - press , to set one"
		}
		u.setStatus(errMarkup(reason))
		return
	}

	switch section {
	case "Spotify Search":
		u.focus(u.table)
		u.openSearch(true)
	case "Liked Songs":
		u.focus(u.table)
		go u.loadLiked()
	case "Playlists":
		u.focus(u.table)
		go u.loadPlaylists()
	}
}

// showSpotifyTracks installs a fetched track list as the current view. Called
// on the UI goroutine only.
func (u *UI) showSpotifyTracks(items []item, title string) {
	u.spotifyView = true
	u.base = items
	u.baseTitle = fmt.Sprintf(" SPOTIFY · %s ", tview.Escape(title))
	u.setTracks(items)
	u.table.SetTitle(u.baseTitle)
	if len(items) == 0 {
		u.setStatus(fmt.Sprintf("[%s]nothing came back for %s", mocha.Subtext0.String(), tview.Escape(title)))
		return
	}
	u.setStatus(fmt.Sprintf("[%s]%d track(s) from Spotify · %s",
		mocha.Green.String(), len(items), tview.Escape(title)))
}

// fetching reports progress from a background fetch.
func (u *UI) fetching(what string) {
	u.app.QueueUpdateDraw(func() {
		u.setStatus(fmt.Sprintf("[%s]fetching %s...", mocha.Subtext0.String(), tview.Escape(what)))
	})
}

// reportSpotifyErr turns an API error into one line of status, naming the cause
// the user can actually act on.
func (u *UI) reportSpotifyErr(prefix string, err error) {
	msg := prefix + ": " + err.Error()
	switch {
	case errors.Is(err, ErrNoPremium):
		msg = "Spotify Premium is required for playback; browsing still works"
	case errors.Is(err, ErrNotConnected):
		msg = "Spotify is not connected - press , to sign in"
	}
	u.app.QueueUpdateDraw(func() { u.setStatus(errMarkup(msg)) })
}

// searchSpotify runs a catalogue search. Runs off the UI goroutine.
func (u *UI) searchSpotify(query string) {
	u.fetching("spotify search")
	ctx, cancel := context.WithTimeout(context.Background(), spotifyFetchTimeout)
	defer cancel()

	items, err := u.api.Search(ctx, query, 50)
	if err != nil {
		u.reportSpotifyErr("spotify search", err)
		return
	}
	u.app.QueueUpdateDraw(func() {
		u.showSpotifyTracks(items, "search: "+query)
	})
}

func (u *UI) loadLiked() {
	u.fetching("liked songs")
	ctx, cancel := context.WithTimeout(context.Background(), spotifyFetchTimeout)
	defer cancel()

	// 200 is two pages plus change: enough to browse, bounded so a 10k-track
	// library does not stall the view behind 200 requests.
	items, err := u.api.Liked(ctx, 200)
	if err != nil && len(items) == 0 {
		u.reportSpotifyErr("liked songs", err)
		return
	}
	u.app.QueueUpdateDraw(func() {
		u.showSpotifyTracks(items, "Liked Songs")
		if err != nil {
			u.setStatus(errMarkup("partial list, spotify said: " + err.Error()))
		}
	})
}

// loadPlaylists shows playlists as group pseudo-rows, reusing the same
// mechanism the local Artists/Albums views use: selecting one fetches its
// tracks rather than playing anything.
func (u *UI) loadPlaylists() {
	u.fetching("playlists")
	ctx, cancel := context.WithTimeout(context.Background(), spotifyFetchTimeout)
	defer cancel()

	lists, err := u.api.Playlists(ctx)
	if err != nil && len(lists) == 0 {
		u.reportSpotifyErr("playlists", err)
		return
	}

	rows := make([]item, 0, len(lists))
	for _, pl := range lists {
		desc := fmt.Sprintf("%d tracks", pl.Tracks)
		if pl.Owner != "" {
			desc += " · " + pl.Owner
		}
		rows = append(rows, item{
			title: pl.Name, desc: desc,
			group: true, groupField: "Playlists", playlistID: pl.ID,
			src: srcSpotify,
		})
	}

	u.app.QueueUpdateDraw(func() {
		u.spotifyView = true
		u.base = rows
		u.baseTitle = " SPOTIFY · Playlists "
		u.setTracks(rows)
		u.table.SetTitle(u.baseTitle)
		u.setStatus(fmt.Sprintf("[%s]%d playlist(s) · enter to open",
			mocha.Subtext0.String(), len(rows)))
	})
}

func (u *UI) loadPlaylist(id, name string) {
	u.fetching(name)
	ctx, cancel := context.WithTimeout(context.Background(), spotifyFetchTimeout)
	defer cancel()

	items, err := u.api.PlaylistTracks(ctx, id, 500)
	if err != nil && len(items) == 0 {
		u.reportSpotifyErr("playlist "+name, err)
		return
	}
	u.app.QueueUpdateDraw(func() {
		u.showSpotifyTracks(items, name)
	})
}
