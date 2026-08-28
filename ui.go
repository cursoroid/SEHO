package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/gdamore/tcell/v2"
	"github.com/redis/go-redis/v9"
	"github.com/rivo/tview"
	"github.com/sahilm/fuzzy"
)

var sidebarSections = []string{"All Tracks", "Artists", "Albums", "Tags", "Recent"}

type UI struct {
	app *tview.Application
	rdb *redis.Client
	dir string

	// set is the live settings state: File is what Save writes, Eff is what the
	// app reads. The settings page edits it in place.
	set Settings

	// local plays files; spot plays Spotify and is created on first use, so a
	// user who never touches Spotify never spawns librespot. active says which
	// one owns the transport - events from the other are ignored.
	local  *Player
	api    *Spotify
	active source

	// spot is created lazily and read from both the UI goroutine and the
	// goroutine that starts it, so every access goes through spotMu. Its
	// concrete type depends on Config.SpotifyBackend - see ensureSpotify.
	spot   Backend
	spotMu sync.Mutex

	// eq is the live profile. It may differ from the saved one while the sound
	// page is open, which is what makes auditioning a curve possible.
	eq profile

	pages     *tview.Pages
	header    *tview.TextView
	sidebar   *tview.List
	table     *tview.Table
	card      *tview.TextView
	transport *tview.TextView
	footer    *tview.TextView
	search    *tview.InputField
	body      *tview.Flex
	root      *tview.Flex

	all     []item // everything in the library
	base    []item // the view search filters within: all, or a sidebar/group narrowing
	shown   []item // current table contents, and the play queue
	playing int    // index into shown, -1 when nothing is playing

	// baseTitle is the table title for the current base view (e.g. " TRACKS "
	// or " TRACKS · Artists "), restored verbatim when a search is cleared.
	baseTitle string

	// layoutWidth is the terminal width relayout last built the body Flex
	// for, so repeated draws at the same width are a no-op.
	layoutWidth int

	// spotifyView is true when the current base view came from the Spotify API,
	// which is what makes "/" search the catalogue instead of the local list.
	spotifyView bool

	// soloistKeyEdit holds a Soloist API key typed on the settings page until
	// Save stores it in the keychain.
	soloistKeyEdit string

	// searchRemote is true while the search field is querying Spotify rather
	// than fuzzy-filtering the local base view.
	searchRemote bool

	// focusedSidebar tracks whether the sidebar currently holds focus.
	// relayout cannot call u.app.GetFocus()/SetFocus() to find out: it runs
	// from SetBeforeDrawFunc, which tview invokes while Application.draw()
	// already holds the app's lock, and GetFocus/SetFocus take that same
	// lock - calling either there deadlocks the very first draw. This bool
	// is kept in sync by every place that actually moves focus instead.
	focusedSidebar bool

	pos, dur                     float64 // transport clock, seconds
	paused                       bool
	vol                          int
	nowTitle, nowArtist, nowPath string
}

func NewUI(rdb *redis.Client, set Settings, pl *Player) *UI {
	applyTheme()
	u := &UI{
		app: tview.NewApplication(),
		rdb: rdb, dir: set.Eff.MusicDir, set: set,
		local:   pl,
		api:     NewSpotify(set.Eff.SpotifyClientID),
		playing: -1,
	}
	u.eq = startupProfile(set.Eff.EQ)

	u.header = textPane("")
	u.header.SetBorder(true)

	u.sidebar = tview.NewList().ShowSecondaryText(false)
	u.sidebar.SetBorder(true).SetTitle(" LIBRARY ")
	u.sidebar.SetMainTextColor(mocha.Text).
		SetSelectedTextColor(mocha.Base).
		SetSelectedBackgroundColor(mocha.Mauve)
	for _, s := range sidebarSections {
		s := s
		u.sidebar.AddItem(s, "", 0, func() {
			u.filterBySection(s)
			u.focus(u.table)
		})
	}
	// A separator, then the Spotify views. Selecting the separator does
	// nothing; it exists so the two libraries do not read as one list.
	u.sidebar.AddItem(strings.Repeat("─", 12), "", 0, nil)
	for _, s := range spotifySections {
		s := s
		u.sidebar.AddItem(s, "", 0, func() { u.openSpotifySection(s) })
	}

	u.table = tview.NewTable().SetFixed(1, 0).SetSelectable(true, false)
	u.table.SetBorder(true).SetTitle(" TRACKS ")
	u.table.SetSelectedStyle(tcell.StyleDefault.
		Background(mocha.Surface2).Foreground(mocha.Text))

	u.card = textPane("")
	u.card.SetBorder(true).SetTitle(" NOW PLAYING ")

	u.transport = textPane("")
	u.transport.SetBorder(true)

	u.footer = textPane("")

	u.search = tview.NewInputField().SetLabel(" search: ")
	u.search.SetFieldBackgroundColor(mocha.Mantle).
		SetFieldTextColor(mocha.Text).
		SetLabelColor(mocha.Mauve)
	u.search.SetChangedFunc(func(q string) {
		if u.searchRemote {
			return // remote search runs on enter, not per keystroke
		}
		u.applyFilter(q)
	})
	u.search.SetDoneFunc(func(key tcell.Key) {
		if u.searchRemote && key == tcell.KeyEnter {
			q := u.search.GetText()
			u.closeSearch(false)
			if strings.TrimSpace(q) != "" {
				go u.searchSpotify(q)
			}
			return
		}
		u.closeSearch(key == tcell.KeyEscape)
	})

	u.body = tview.NewFlex()
	u.root = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(u.header, headerRows, 0, false).
		AddItem(gutter(), 1, 0, false).
		AddItem(u.body, 0, 1, true).
		AddItem(gutter(), 1, 0, false).
		AddItem(u.transport, transportRows, 0, false).
		AddItem(u.footer, 1, 0, false)

	// The panes fill themselves with base; the ground behind them is crust,
	// one step darker. That contrast - not the rounded corners - is what makes
	// them read as floating, and it is only visible through the gutters.
	u.root.SetBackgroundColor(mocha.Crust)
	u.body.SetBackgroundColor(mocha.Crust)

	u.app.SetBeforeDrawFunc(func(screen tcell.Screen) bool {
		w, _ := screen.Size()
		u.relayout(w)
		return false // false = let tview draw normally
	})

	// Pages exists so the settings and sound pages can take the whole terminal
	// without the responsive body Flex having to know they exist.
	u.pages = tview.NewPages().AddPage(pageMain, u.root, true, true)
	u.app.SetRoot(u.pages, true)
	u.focus(u.table)
	return u
}

// spotifySections are the sidebar rows below the separator.
var spotifySections = []string{"Spotify Search", "Liked Songs", "Playlists"}

// startupProfile resolves the saved EQ configuration into a live profile,
// preferring hand-edited bands when the config carries them.
func startupProfile(cfg EQConfig) profile {
	p, ok := profileByKey(cfg.Profile)
	if !ok {
		p, _ = profileByKey(defaultProfileName())
	}
	if len(cfg.Bands) > 0 {
		p.Bands = cfg.Bands
		p.Name += " (edited)"
	}
	return p
}

// envLookup exists so applyEnv can be called with the real environment from
// code that must not import os for one call.
func envLookup(key string) string { return os.Getenv(key) }

// relayout hides columns that no longer fit. tview has no breakpoints, so this
// rebuilds the body Flex on width change.
// ponytail: rebuild rather than resize - three states, cheap, and it avoids
// tracking per-item proportions.
func (u *UI) relayout(width int) {
	// refreshHeader recomputes every draw, not just on a width change:
	// GetInnerRect reflects the *previous* draw's computed layout, so on the
	// very first draw (before anything has been laid out) it is still 0.
	// Gating this behind the width check would freeze the header's padding
	// at that first, wrong guess until the next resize.
	u.refreshHeader()

	if width == u.layoutWidth {
		return
	}
	u.layoutWidth = width

	u.body.Clear()
	switch {
	case width >= cardBreakpoint:
		u.body.AddItem(u.sidebar, sidebarCols, 0, false).
			AddItem(gutter(), 1, 0, false).
			AddItem(u.table, 0, 1, true).
			AddItem(gutter(), 1, 0, false).
			AddItem(u.card, cardCols, 0, false)
	case width >= 80:
		u.body.AddItem(u.sidebar, sidebarCols, 0, false).
			AddItem(gutter(), 1, 0, false).
			AddItem(u.table, 0, 1, true)
	default:
		u.body.AddItem(u.table, 0, 1, true)
	}

	// drawCard is only ever called from playRow/advance, never from here, so
	// widening back past the card breakpoint would otherwise leave it blank
	// until the next track change (it was skipped while narrow - see
	// drawCard's u.layoutWidth guard). Repaint from u.nowPath rather than
	// u.playing: u.playing is -1 whenever the playing track is filtered out
	// of the current u.shown, but u.nowPath + u.all always resolve it. Setting
	// the TextView's text here is fine even though relayout runs inside
	// SetBeforeDrawFunc with the app lock held; see the focus fix-up below for
	// what is NOT safe to call from here.
	if width >= cardBreakpoint && u.nowPath != "" {
		u.drawCard(itemByPath(u.all, u.nowPath))
	}

	// Focus may have been on a pane that is now hidden. This cannot call
	// u.app.GetFocus()/SetFocus() directly: relayout runs from
	// SetBeforeDrawFunc, invoked while Application.draw() already holds the
	// app's lock, and both of those methods take that same lock - calling
	// either here deadlocks the very first draw. focusedSidebar is our own
	// tracked copy, and the fix-up is dispatched to run after draw() returns.
	if u.focusedSidebar && width < 80 {
		u.focusedSidebar = false
		go u.app.QueueUpdateDraw(func() { u.focus(u.table) })
	}

	u.setFooter()
}

// textPane builds the fixed-height status panes (header, card, transport,
// footer). SetWrap(false) matters once relayout is width-aware: GetInnerRect
// lags one draw behind an actual resize, so a right-alignment pad computed
// against the previous width can transiently overshoot the new, smaller one.
// Wrapping would spill that overshoot into a second line and blow out these
// panes' fixed heights; clipping it for one frame is the safe failure mode.
func textPane(s string) *tview.TextView {
	tv := tview.NewTextView().SetDynamicColors(true).SetText(s).SetWrap(false)
	tv.SetBackgroundColor(mocha.Base)
	return tv
}

// paintTableHeader writes the column header row. Shared with the search view.
func (u *UI) paintTableHeader() {
	for c, h := range []string{"#", "TITLE", "ARTIST", "TIME"} {
		// The "#" column stays a bare glyph - letter-spacing a single
		// character just looks like a stray mark.
		if c > 0 {
			h = smallCaps(h)
		}
		u.table.SetCell(0, c, tview.NewTableCell(h).
			SetTextColor(mocha.Overlay0).
			SetSelectable(false).
			SetExpansion(map[int]int{0: 0, 1: 3, 2: 2, 3: 0}[c]))
	}
}

// numberCell renders the leftmost column: a marker on the track that is
// actually playing, otherwise its position in the list. Without this there is
// no way to tell what is playing from what is merely selected once you browse
// away from it.
func (u *UI) numberCell(row int, it item) *tview.TableCell {
	if !it.group && it.path != "" && it.path == u.nowPath {
		return cell("▶", mocha.Mauve)
	}
	return cell(fmt.Sprintf("%d", row+1), mocha.Overlay0)
}

// titleColor lifts the playing row out of the list.
func (u *UI) titleColor(it item) tcell.Color {
	if !it.group && it.path != "" && it.path == u.nowPath {
		return mocha.Mauve
	}
	return mocha.Text
}

func (u *UI) setTracks(items []item) {
	u.shown = items
	u.table.Clear()

	u.paintTableHeader()

	for i, it := range items {
		u.table.SetCell(i+1, 0, u.numberCell(i, it))
		u.table.SetCell(i+1, 1, cell(tview.Escape(it.title), u.titleColor(it)))
		u.table.SetCell(i+1, 2, cell(tview.Escape(it.desc), mocha.Subtext0))
		u.table.SetCell(i+1, 3, cell(fmtDuration(it.duration), mocha.Subtext0))
	}
	if len(items) > 0 {
		u.table.Select(1, 0)
	}
	u.refreshHeader()
	u.resyncPlaying(u.nowPath)
}

func cell(s string, c tcell.Color) *tview.TableCell {
	return tview.NewTableCell(s).SetTextColor(c)
}

// trackSource lets fuzzy match against a track's title and artist at once.
type trackSource []item

func (t trackSource) Len() int            { return len(t) }
func (t trackSource) String(i int) string { return t[i].title + " " + t[i].desc }

// highlight wraps the bytes at idx in mauve markup. idx are BYTE offsets into s
// (that is what fuzzy.FindFrom returns).
// ponytail: titles are user data and may contain "[...]", which tview eats as a
// tag. Escaping shifts every offset, so a bracketed title gets correct text and
// no highlighting rather than an offset-mapping machine for a cosmetic effect.
func highlight(s string, idx []int) string {
	if strings.ContainsRune(s, '[') {
		return tview.Escape(s)
	}
	if len(idx) == 0 {
		return s
	}
	hit := make(map[int]bool, len(idx))
	for _, i := range idx {
		hit[i] = true
	}
	var b strings.Builder
	on := false
	for i, r := range s { // byte index, matching fuzzy's offsets
		switch {
		case hit[i] && !on:
			b.WriteString("[" + mocha.Mauve.String() + "::b]")
			on = true
		case !hit[i] && on:
			b.WriteString("[-::-]")
			on = false
		}
		b.WriteRune(r)
	}
	if on {
		b.WriteString("[-::-]")
	}
	return b.String()
}

// applyFilter repaints the table with fuzzy matches against the current base
// view (u.base), ranked by score - so search composes with whatever
// sidebar/group narrowing is already applied instead of clobbering it. An
// empty query restores that base view verbatim.
func (u *UI) applyFilter(query string) {
	if query == "" {
		u.table.SetTitle(u.baseTitle)
		u.setTracks(u.base)
		return
	}

	matches := fuzzy.FindFrom(query, trackSource(u.base))
	u.shown = make([]item, 0, len(matches))
	u.table.Clear()

	u.paintTableHeader()

	for row, m := range matches {
		it := u.base[m.Index]
		u.shown = append(u.shown, it)

		// MatchedIndexes are BYTE positions in title+" "+artist; split at the title boundary.
		var titleHits, artistHits []int
		cut := len(it.title)
		for _, i := range m.MatchedIndexes {
			if i < cut {
				titleHits = append(titleHits, i)
			} else if i > cut {
				artistHits = append(artistHits, i-cut-1)
			}
		}

		u.table.SetCell(row+1, 0, u.numberCell(row, it))
		u.table.SetCell(row+1, 1, cell(highlight(it.title, titleHits), u.titleColor(it)))
		u.table.SetCell(row+1, 2, cell(highlight(it.desc, artistHits), mocha.Subtext0))
		u.table.SetCell(row+1, 3, cell(fmtDuration(it.duration), mocha.Subtext0))
	}

	u.table.SetTitle(fmt.Sprintf(" TRACKS · search: %s ", tview.Escape(query)))
	if len(u.shown) > 0 {
		u.table.Select(1, 0)
	}
	u.resyncPlaying(u.nowPath)
}

// resyncPlaying re-points u.playing at the currently loaded track after u.shown
// is rebuilt, or -1 when it is no longer visible. groupBy rows are skipped: a
// group pseudo-row carries a representative track's real path (so it can be
// played), but it is not itself the playing track, and matching it here would
// misdirect n/p and advance() into the grouped list by coincidence of path.
func (u *UI) resyncPlaying(path string) {
	u.playing = -1
	if path == "" {
		return
	}
	for i, it := range u.shown {
		if it.group {
			continue
		}
		if it.path == path {
			u.playing = i
			return
		}
	}
}

// filterBySection narrows the table. Artists/Albums/Tags collapse the library
// to one row per distinct value; picking one is a second filter step (see
// filterByGroup) handled by selecting a row, so there is no nested
// navigation state - the sidebar is filter-only, never a drill-down that plays.
func (u *UI) filterBySection(section string) {
	u.spotifyView = false
	switch section {
	case "All Tracks":
		u.base = u.all
	case "Recent":
		u.base = recentTracks(u.all, 50)
	case "Artists", "Albums", "Tags":
		u.base = groupBy(u.all, section)
	}
	u.baseTitle = fmt.Sprintf(" TRACKS · %s ", section) // section is one of the fixed sidebarSections, not user data
	u.setTracks(u.base)
	u.table.SetTitle(u.baseTitle)
}

// recentTracks returns the newest n items by added_at, newest first.
// listTracks sorts by Redis key (the file path), not recency, so this sorts
// a copy rather than trusting item order. A missing or unparseable added_at
// is the zero time and sorts last.
func recentTracks(all []item, n int) []item {
	out := slices.Clone(all)
	slices.SortStableFunc(out, func(a, b item) int {
		return b.addedAt.Compare(a.addedAt) // newest first
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// groupKey returns an item's key under one grouping field ("Artists",
// "Albums", or "Tags"), applying the same fallback groupBy uses for an empty
// value so filterByGroup can match back to exactly what was grouped.
func groupKey(it item, field string) string {
	switch field {
	case "Artists":
		return it.desc
	case "Albums":
		if it.album == "" {
			return "Unknown album"
		}
		return it.album
	case "Tags":
		return it.tags
	}
	return ""
}

// groupBy collapses the library to one representative row per distinct value,
// so the table can act as a browse index without a second widget. Each
// pseudo-row carries group=true plus the field it was grouped on, so
// selecting one can filter back to the matching real tracks (filterByGroup)
// instead of being mistaken for a playable track.
func groupBy(all []item, section string) []item {
	seen := map[string]bool{}
	out := make([]item, 0, 64)
	for _, it := range all {
		k := groupKey(it, section)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, item{
			title: k, desc: section, path: it.path, duration: it.duration,
			group: true, groupField: section,
		})
	}
	return out
}

// filterByGroup narrows the table to the real tracks matching one group
// value (an artist, album, or tag) picked from a grouped sidebar view. It
// never plays anything - the sidebar/group rows are filter-only.
func (u *UI) filterByGroup(field, key string) {
	out := make([]item, 0, 16)
	for _, it := range u.all {
		if groupKey(it, field) == key {
			out = append(out, it)
		}
	}
	u.base = out
	u.baseTitle = fmt.Sprintf(" TRACKS · %s ", tview.Escape(key))
	u.setTracks(u.base)
	u.table.SetTitle(u.baseTitle)
}

// padWidth returns the padding needed to right-align a block of the given
// parts within a w-cell line, floored at 1 (GetInnerRect can report 0 on the
// very first draw, before layout runs, which would otherwise go negative).
// Widths are counted with tview.TaggedStringWidth rather than a rune count,
// so tags don't count against the width and wide runes (e.g. CJK) count as
// the two display cells they occupy - a plain rune count under-counts those
// by about one cell each, drifting and clipping the right-aligned block.
func padWidth(w int, parts ...string) int {
	used := 0
	for _, p := range parts {
		used += tview.TaggedStringWidth(p)
	}
	return max(1, w-used)
}

// refreshHeader right-aligns the track count to the pane's actual width.
func (u *UI) refreshHeader() {
	_, _, w, _ := u.header.GetInnerRect()
	if w <= 0 {
		return
	}
	label := fmt.Sprintf("%d tracks", len(u.all))

	// Below the wordmark's own width plus room for the count, fall back to
	// plain text rather than rendering a clipped, unreadable banner.
	if w < wordmarkWidth()+len(label)+8 {
		u.header.SetText(fmt.Sprintf("  [%s::b]SEHO[-::-]%*s[%s]%s  ",
			mocha.Lavender.String(), padWidth(w, "  SEHO", label+"  "), "",
			mocha.Subtext0.String(), label))
		return
	}

	// The wordmark is a baked constant, not user data, and contains no '[',
	// so it needs no escaping - see theme.go.
	rows := make([]string, len(wordmark))
	for i, line := range wordmark {
		rows[i] = fmt.Sprintf("  [%s]%s", mocha.Lavender.String(), line)
		if i == 0 {
			rows[i] += fmt.Sprintf("%*s[%s]%s  ",
				padWidth(w, "  "+line, label+"  "), "", mocha.Subtext0.String(), label)
		}
	}
	u.header.SetText(strings.Join(rows, "\n"))
}

// spotify returns the Spotify backend, or nil when it has not been started.
func (u *UI) spotify() Backend {
	u.spotMu.Lock()
	defer u.spotMu.Unlock()
	return u.spot
}

// stopSpotify tears down the Spotify backend so the next play builds a fresh
// one. Reports whether there was anything to tear down. Used when a setting is
// baked into the backend at spawn time and cannot be changed in place.
func (u *UI) stopSpotify() bool {
	u.spotMu.Lock()
	sp := u.spot
	u.spot = nil
	u.spotMu.Unlock()
	if sp == nil {
		return false
	}
	sp.Close()
	if u.active == srcSpotify {
		u.playing, u.nowPath = -1, ""
	}
	return true
}

// current is the backend that owns the transport right now.
func (u *UI) current() Backend {
	if u.active == srcSpotify {
		if sp := u.spotify(); sp != nil {
			return sp
		}
	}
	return u.local
}

// stopOther silences the source that is losing the transport.
func (u *UI) stopOther(next source) {
	switch {
	case next == srcSpotify && u.local != nil:
		u.local.Stop()
	case next == srcLocal:
		if sp := u.spotify(); sp != nil {
			sp.Stop()
		}
	}
}

// ensureSpotify starts librespot and its mpv instance on first Spotify play.
// Lazy on purpose: spawning librespot at startup would demand a login from
// someone who only ever wanted to play their own files.
//
// Never call this from the UI goroutine - it spawns processes and may refresh
// an OAuth token over the network. startSpotifyTrack is the entry point.
//
// Which backend depends on config: Soloist (Spotify's own client, in a
// container) or librespot. Browsing goes through the Web API either way, so an
// API connection is required for both.
func (u *UI) ensureSpotify() (Backend, error) {
	u.spotMu.Lock()
	defer u.spotMu.Unlock()
	if u.spot != nil {
		return u.spot, nil
	}
	if !u.api.Connected() {
		return nil, fmt.Errorf("connect Spotify in settings first (press ,)")
	}

	var (
		sp  Backend
		err error
	)
	if u.set.Eff.SpotifyBackend == backendSoloist {
		sp, err = StartSoloistBackend(u.set.Eff, LoadSoloistKey())
	} else {
		sp, err = StartSpotifyBackend(u.api, u.set.Eff, func(url string) {
			u.app.QueueUpdateDraw(func() {
				u.setStatus(fmt.Sprintf("[%s]librespot sign-in: %s",
					mocha.Subtext0.String(), tview.Escape(url)))
			})
		})
	}
	if err != nil {
		return nil, err
	}

	u.spot = sp
	go u.pumpEvents(srcSpotify, sp.Events())
	sp.SetVolume(u.vol)
	sp.SetAF(u.eqChain())
	return sp, nil
}

// startSpotifyTrack runs the slow half of a Spotify play off the UI goroutine:
// starting librespot, waiting for its session (which on a first run waits for a
// human in a browser) and asking Spotify to play. Doing this inline would
// freeze the whole interface for as long as the login takes.
func (u *UI) startSpotifyTrack(uri, title string) {
	sp, err := u.ensureSpotify()
	if err != nil {
		u.app.QueueUpdateDraw(func() { u.setStatus(errMarkup(err.Error())) })
		return
	}

	u.app.QueueUpdateDraw(func() {
		u.setStatus(fmt.Sprintf("[%s]starting %s on Spotify...",
			mocha.Subtext0.String(), tview.Escape(title)))
	})

	if err := sp.Load(uri); err != nil {
		u.app.QueueUpdateDraw(func() { u.setStatus(errMarkup("spotify playback: " + err.Error())) })
		return
	}
	u.app.QueueUpdateDraw(func() {
		// The transport takes over from here; clear the interim message only if
		// this track is still the one the user is waiting on.
		if u.nowPath == uri {
			u.drawTransport()
		}
	})
}

func (u *UI) playRow(row int) {
	// Belt-and-braces: playRow is the single choke point every play path runs
	// through, so it refuses a group pseudo-row here too, on top of the
	// callers that already know to avoid one (filterByGroup never calls this;
	// bindKeys' SelectedFunc routes a group row to filterByGroup instead).
	if row < 0 || row >= len(u.shown) || u.shown[row].group {
		return
	}
	it := u.shown[row]

	// Only one source plays at a time, and they are separate processes, so the
	// outgoing one has to be told explicitly - otherwise a Spotify track starts
	// on top of the local file that is still playing.
	if it.src != u.active {
		u.stopOther(it.src)
		u.active = it.src
	}

	u.playing = row
	u.nowTitle, u.nowArtist, u.nowPath = it.title, it.desc, it.path
	u.pos, u.dur = 0, it.duration
	u.table.Select(row+1, 0)
	u.repaintMarkers()
	u.drawCard(it)

	if it.src == srcSpotify {
		// Off the UI goroutine: this can wait on a librespot login.
		go u.startSpotifyTrack(it.path, it.title)
		return
	}

	u.local.SetVolume(u.vol)
	u.local.SetAF(u.eqChain())
	if err := u.local.Load(it.path); err != nil {
		u.setStatus(errMarkup("playback failed: " + err.Error()))
		return
	}
}

// repaintMarkers refreshes the playing indicator and row colour in place.
//
// It deliberately rewrites only column 0's text and column 1's *colour*:
// applyFilter builds column 1 with search-match highlighting baked into the
// string, and regenerating that text here would silently destroy it.
func (u *UI) repaintMarkers() {
	for i, it := range u.shown {
		u.table.SetCell(i+1, 0, u.numberCell(i, it))
		if c := u.table.GetCell(i+1, 1); c != nil {
			c.SetTextColor(u.titleColor(it))
		}
	}
}

// firstPlayableRow returns the index of the first non-group row in shown, or
// -1 when there isn't one (e.g. a grouped sidebar view before any group is
// picked). Used by the "nothing has ever played" n path so it never starts
// playback on a group pseudo-row - see filterByGroup/resyncPlaying for why a
// group row must never be mistaken for a playable track.
func firstPlayableRow(shown []item) int {
	for i, it := range shown {
		if !it.group {
			return i
		}
	}
	return -1
}

// nextPlayIndex is the pure index arithmetic behind advance(): what to play
// next given current (u.playing: -1 when the playing track, if any, is not
// visible in this view or nothing is playing at all) and length (len(u.shown)).
// ok is false when there is nothing to advance to - either current is
// already -1, or the queue is exhausted - and current is deliberately never
// treated as "start from the top" in that case.
func nextPlayIndex(current, length int) (next int, ok bool) {
	if current < 0 || current+1 >= length {
		return -1, false
	}
	return current + 1, true
}

// advance plays the next row in the queue (u.shown), or parks at the end of
// the list. Called only from inside pumpEvents' QueueUpdateDraw closure -
// playRow, and therefore pl.Load, must run on the tview event-loop goroutine.
//
// When u.playing is already -1 (nothing playing, or the playing track is not
// visible in the current view - e.g. the user switched to a grouped view
// mid-playback) there is no way to know which row "next" means, so this does
// not walk the queue - it just clears the now-playing state so the transport
// reads "nothing playing" instead of showing a stale track forever. It does
// not snapshot a separate queue to work around this - the visible list is the
// queue, by design.
func (u *UI) advance() {
	if u.playing < 0 {
		u.nowTitle, u.nowArtist, u.nowPath = "", "", ""
		u.drawCard(item{})
		return
	}
	next, ok := nextPlayIndex(u.playing, len(u.shown))
	if !ok {
		u.nowTitle, u.nowArtist, u.nowPath = "", "", ""
		u.playing = -1
		u.drawCard(item{})
		return
	}
	u.playRow(next)
}

const (
	artCells = 28 // cells wide
	artRows  = 14 // cell rows => 28 pixels tall
)

// itemByPath finds the item in items matching path, or the zero item if none
// does. Used to recover the currently-playing item by u.nowPath (rather than
// indexing u.shown by u.playing, which is -1 whenever the playing track is
// filtered out of the current view) so the card can still be repainted.
func itemByPath(items []item, path string) item {
	for _, it := range items {
		if it.path == path {
			return it
		}
	}
	return item{}
}

// drawCard paints the NOW PLAYING card: embedded album art (or a fallback
// tile) plus the track's title and artist beneath it.
func (u *UI) drawCard(it item) {
	if it.path == "" || u.layoutWidth < cardBreakpoint {
		u.card.SetText("")
		return
	}
	_, _, w, h := u.card.GetInnerRect()

	meta := fmt.Sprintf("  [%s::b]%s[-::-]\n  [%s]%s",
		mocha.Text.String(), tview.Escape(it.title),
		mocha.Subtext0.String(), tview.Escape(it.desc))

	// Reserve four rows the art cannot have: the blank above it, the blank
	// below it, and the two metadata lines. The art is
	// square - one cell row is two pixels tall - so its width tracks whichever
	// of height or width runs out first, rather than sitting at a fixed size
	// that the pane may no longer have room for.
	rows := min(artRows, h-4)
	cells := min(artCells, w-2)
	if cells > rows*2 {
		cells = rows * 2
	} else {
		rows = cells / 2
	}
	if rows < 4 || cells < 8 {
		// Too cramped for a legible image; the metadata is worth more.
		u.card.SetText("\n" + meta)
		return
	}

	indent := strings.Repeat(" ", max(0, (w-cells)/2))
	art := AlbumArt(it.path, it.album, cells, rows)
	if it.src == srcSpotify {
		// Spotify tracks have no local file to read tags from; the cover comes
		// from the API as a URL.
		art = AlbumArtURL(it.artURL, it.album, cells, rows)
	}
	lines := strings.Split(strings.TrimRight(art, "\n"), "\n")
	for i, l := range lines {
		lines[i] = indent + l
	}
	u.card.SetText("\n" + strings.Join(lines, "\n") + "\n\n" + meta)
}

const (
	headerRows     = 5   // 3 wordmark rows plus the border
	transportRows  = 5   // cluster, title, bar, plus the border
	cardBreakpoint = 110 // below this the card is not in the layout
	sidebarCols    = 16
	cardCols       = 32
	volMeterCells  = 10
)

// focus moves keyboard focus and recolours the pane borders to match.
//
// tview's Theme has a single BorderColor with no focus variant, which is why
// an earlier pass recorded a mauve focused border as unachievable. That was
// wrong: Box.SetBorderColor is per-widget, so the accent is applied here. The
// focused border rune set is deliberately identical to the unfocused one (see
// applyTheme) so colour is the only signal.
//
// Never call this from relayout: it runs inside SetBeforeDrawFunc while
// Application.draw() holds the app lock, and SetFocus takes that same lock.
func (u *UI) focus(p tview.Primitive) {
	u.focusedSidebar = p == tview.Primitive(u.sidebar)
	border := func(b *tview.Box, active bool) {
		if active {
			b.SetBorderColor(mocha.Mauve)
			return
		}
		b.SetBorderColor(mocha.Surface0)
	}
	border(u.sidebar.Box, u.focusedSidebar)
	border(u.table.Box, p == tview.Primitive(u.table))
	u.app.SetFocus(p)
}

const (
	cometTail       = 3    // cells behind the head that ramp toward peach
	shimmerTail     = 2.5  // half-width of the travelling highlight, in cells
	shimmerStrength = 0.55 // how far the highlight blends toward text colour
	shimmerSpeed    = 8.0  // cells the highlight travels per second of playback
)

// eighths indexes partial-cell fills, 0/8 through 8/8.
var eighths = []rune(" ▏▎▍▌▋▊▉█")

// barCellColor is the colour of one filled cell: a mauve-to-pink gradient along
// the bar, ramped toward peach near the head, brightened where the shimmer is.
func barCellColor(i, full, width int, shimmer float64) string {
	c := lerpHex(mocha.Mauve, mocha.Pink, float64(i)/float64(max(1, width-1)))
	if d := full - i; d <= cometTail {
		c = lerpHex(tcell.GetColor(c), mocha.Peach, 1-float64(d)/float64(cometTail+1))
	}
	if d := math.Abs(float64(i) - shimmer); d < shimmerTail {
		c = lerpHex(tcell.GetColor(c), mocha.Text, (1-d/shimmerTail)*shimmerStrength)
	}
	return c
}

// progressBar renders a width-cell bar at sub-cell precision — eighth-blocks
// give eight times the horizontal resolution of a whole-cell bar, so the head
// glides instead of stepping.
//
// The animation is a pure function of phase, which the caller derives from
// elapsed playback rather than a wall clock. mpv emits time-pos at roughly
// 16Hz while playing and not at all while paused, so this needs no ticker, no
// goroutine, costs nothing when idle, and freezes mid-stride on pause by
// itself — which is also the correct feedback.
func progressBar(frac float64, width int, phase float64) string {
	if width <= 0 {
		return ""
	}
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}

	exact := frac * float64(width)
	full := min(int(exact), width)
	rem := int((exact - float64(full)) * 8)
	shimmer := math.Mod(phase, float64(width)+shimmerTail*2) - shimmerTail

	var b strings.Builder
	b.Grow(width * 16)
	for i := 0; i < width; i++ {
		switch {
		case i < full:
			fmt.Fprintf(&b, "[%s]█", barCellColor(i, full, width, shimmer))
		case i == full && rem > 0:
			fmt.Fprintf(&b, "[%s]%c", mocha.Peach.String(), eighths[rem])
		default:
			fmt.Fprintf(&b, "[%s]░", mocha.Surface0.String())
		}
	}
	return b.String()
}

// volMeter renders volume across cells at sub-cell precision. mpv accepts up
// to 130%, but the meter is scaled to 100 so a normal full volume actually
// looks full; anything above simply stays topped out while the numeric
// percentage beside it stays exact.
func volMeter(v, cells int) string {
	if cells <= 0 {
		return ""
	}
	f := min(1.0, max(0.0, float64(v)/100))
	exact := f * float64(cells)
	full := min(int(exact), cells)
	rem := int((exact - float64(full)) * 8)

	var b strings.Builder
	b.Grow(cells * 12)
	for i := 0; i < cells; i++ {
		switch {
		case i < full:
			fmt.Fprintf(&b, "[%s]█", mocha.Peach.String())
		case i == full && rem > 0:
			fmt.Fprintf(&b, "[%s]%c", mocha.Peach.String(), eighths[rem])
		default:
			fmt.Fprintf(&b, "[%s]░", mocha.Surface0.String())
		}
	}
	return b.String()
}

func (u *UI) drawTransport() {
	_, _, w, _ := u.transport.GetInnerRect()
	if w <= 0 {
		return
	}

	icon, iconColor := "▶", mocha.Green
	if u.paused {
		icon, iconColor = "⏸", mocha.Red
	}
	if u.nowTitle == "" {
		icon, iconColor = "■", mocha.Overlay0
	}

	// Row 1: transport cluster on the left, volume on the right. The prev/next
	// glyphs are indicators, not controls - this is a keyboard-only UI - so
	// they sit in subtext and only the current state is coloured.
	cluster := fmt.Sprintf("  [%s]⏮  [%s]%s[-]  [%s]⏭",
		mocha.Overlay0.String(), iconColor.String(), icon, mocha.Overlay0.String())
	vol := fmt.Sprintf("%s [%s]%3d%%  ", volMeter(u.vol, volMeterCells), mocha.Subtext0.String(), u.vol)
	line1 := cluster + strings.Repeat(" ", padWidth(w, cluster, vol)) + vol

	// Row 2: what is playing. Only emit the separator when there is an artist,
	// otherwise the idle line reads "nothing playing ·" with a dangling middot.
	title := u.nowTitle
	if title == "" {
		title = "nothing playing"
	}
	line2 := fmt.Sprintf("  [%s::b]%s[-::-]", mocha.Text.String(), tview.Escape(title))
	if u.nowArtist != "" {
		line2 += fmt.Sprintf(" [%s]· %s[-]", mocha.Subtext0.String(), tview.Escape(u.nowArtist))
	}

	// Row 3: elapsed, bar, duration. The shimmer phase comes from elapsed
	// playback, so the animation rides mpv's time-pos events and stops when
	// they stop.
	frac := 0.0
	if u.dur > 0 {
		frac = u.pos / u.dur
	}
	barWidth := max(10, w-20)
	line3 := fmt.Sprintf("  [%s]%s [-]%s[%s] %s",
		mocha.Subtext0.String(), fmtDuration(u.pos),
		progressBar(frac, barWidth, u.pos*shimmerSpeed),
		mocha.Subtext0.String(), fmtDuration(u.dur))

	u.transport.SetText(line1 + "\n" + line2 + "\n" + line3)
}

func (u *UI) setStatus(markup string) { u.transport.SetText("  " + markup) }

func (u *UI) reload() {
	items, err := listTracks(context.Background(), u.rdb)
	if err != nil {
		u.setStatus(fmt.Sprintf("[%s]library read failed: %v", mocha.Red.String(), tview.Escape(err.Error())))
		return
	}
	u.all = items
	u.base = items
	u.baseTitle = " TRACKS "
	u.setTracks(items)
	u.table.SetTitle(u.baseTitle)
}

// pumpEvents forwards one backend's state onto the UI goroutine. One goroutine
// runs per backend for the app's life; events from the source that does not
// currently own the transport are dropped, so a paused-but-alive Spotify poller
// cannot rewrite the clock of a local track.
func (u *UI) pumpEvents(src source, events <-chan Event) {
	for ev := range events {
		ev := ev
		u.app.QueueUpdateDraw(func() {
			if src != u.active {
				return
			}
			switch ev.Name {
			case "time-pos":
				u.pos = ev.Num
			case "duration":
				u.dur = ev.Num
				// Backfill tracks indexed without ffprobe.
				// Backfill is for local files only: a Spotify duration belongs
				// to a URI that is not in the Redis index at all.
				if src == srcLocal && u.playing >= 0 && u.playing < len(u.shown) && u.shown[u.playing].duration <= 0 {
					u.shown[u.playing].duration = ev.Num
					// u.shown may be a filtered copy of u.all (applyFilter), so the
					// write above would not otherwise reach the backing library data.
					for i := range u.all {
						if u.all[i].path == u.shown[u.playing].path {
							u.all[i].duration = ev.Num
							break
						}
					}
					go backfillDuration(context.Background(), u.rdb, u.shown[u.playing].path, ev.Num)
				}
			case "pause":
				u.paused = ev.Flag
			case "volume":
				u.vol = int(ev.Num)
			case "end-file":
				u.pos = 0
				// "stop" fires when a track is replaced deliberately (playRow's
				// Load ends whatever was playing before it) - never advance for
				// that, or a manual track change would double-advance past the
				// row the user actually picked. Only "eof" (played out) and
				// "error" (unplayable) should walk the queue forward.
				switch ev.Reason {
				case "error":
					u.setStatus(fmt.Sprintf("[%s]could not play %s", mocha.Red.String(), tview.Escape(u.nowTitle)))
					u.advance()
					return
				case "eof":
					u.advance()
				}
			case "audio-key-refused":
				// Spotify refused the decryption key. Do not advance: every
				// track fails the same way for an affected account, and walking
				// the queue would just spin through the whole list.
				u.nowTitle, u.nowArtist, u.nowPath = "", "", ""
				u.playing = -1
				u.paused = false
				u.drawCard(item{})
				u.setStatus(errMarkup("Spotify refused the decryption key - a known block on newer " +
					"Spotify accounts (librespot issue 1649). Local files are unaffected."))
				return
			case "disconnected":
				u.setStatus(errMarkup("lost connection to mpv"))
				return
			}
			u.drawTransport()
		})
	}
}

func (u *UI) Run() error {
	u.reload()
	u.setFooter()
	u.bindKeys()
	u.vol = clampVol(u.set.Eff.Volume)
	u.local.SetVolume(u.vol)
	u.applyEQ()
	go u.pumpEvents(srcLocal, u.local.Events())
	u.drawTransport()
	err := u.app.Run()
	if sp := u.spotify(); sp != nil {
		sp.Close()
	}
	return err
}

// setFooter rebuilds the key-hint bar. "tab pane" only makes sense once the
// sidebar is actually in the layout, so it tracks u.layoutWidth.
func (u *UI) setFooter() {
	m := mocha.Mauve.String()
	keys := []string{"/[-] search", "space[-] pause", "←→[-] seek", "n/p[-] track", "-/=[-] vol"}
	if u.layoutWidth >= 80 {
		keys = append(keys, "tab[-] pane")
	}
	keys = append(keys, "s[-] scan", ",[-] settings", "e[-] sound", "q[-] quit")

	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "  [%s]%s", m, k)
	}
	u.footer.SetText(b.String())
}

// openSearch replaces the footer row with the search field while active. remote
// switches it from fuzzy-filtering the local view to querying Spotify, which
// only happens on enter - one API call per keystroke would be both slow and
// rude to the rate limiter.
func (u *UI) openSearch(remote bool) {
	u.searchRemote = remote
	u.root.RemoveItem(u.footer)
	u.root.AddItem(u.search, 1, 0, true)
	u.search.SetText("")
	label := " search: "
	if remote {
		label = " spotify: "
	}
	u.search.SetLabel(label)
	u.focusedSidebar = false
	u.app.SetFocus(u.search)
}

// closeSearch restores the footer. clear also resets the filter (escape).
func (u *UI) closeSearch(clear bool) {
	u.root.RemoveItem(u.search)
	u.root.AddItem(u.footer, 1, 0, false)
	u.search.SetLabel(" search: ")
	if clear && !u.searchRemote {
		u.applyFilter("")
	}
	u.searchRemote = false
	u.focus(u.table)
}

func (u *UI) bindKeys() {
	u.table.SetSelectedFunc(func(row, _ int) {
		idx := row - 1
		if idx < 0 || idx >= len(u.shown) {
			return
		}
		// The sidebar is filter-only, never a drill-down: picking a group row
		// (an artist/album/tag) narrows the table to its real tracks rather
		// than playing the pseudo-row's representative file under fake
		// metadata (it would otherwise read e.g. "AC/DC · Artists").
		if it := u.shown[idx]; it.group {
			// A Spotify playlist row fetches its tracks; a local group row
			// filters the library. Same pseudo-row mechanism, two sources.
			if it.playlistID != "" {
				go u.loadPlaylist(it.playlistID, it.title)
				return
			}
			u.filterByGroup(it.groupField, it.title)
			return
		}
		u.playRow(idx)
	})

	u.app.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		// The settings and sound pages own the keyboard while they are up:
		// their fields need plain letters, and 'q' must not quit mid-edit.
		// Escape is the one key this layer still handles, so there is always a
		// way back to the player.
		if name, _ := u.pages.GetFrontPage(); name != pageMain {
			if ev.Key() == tcell.KeyEscape {
				u.closePage()
				return nil
			}
			return ev
		}

		// Let the search field consume everything except escape.
		if u.app.GetFocus() == u.search && ev.Key() != tcell.KeyEscape {
			return ev
		}
		if ev.Key() == tcell.KeyEscape && u.app.GetFocus() == u.search {
			u.closeSearch(true)
			return nil
		}
		switch ev.Key() {
		case tcell.KeyTab:
			u.cycleFocus(1)
			return nil
		case tcell.KeyBacktab:
			u.cycleFocus(-1)
			return nil
		case tcell.KeyLeft:
			u.current().Seek(-5)
			return nil
		case tcell.KeyRight:
			u.current().Seek(5)
			return nil
		}
		switch ev.Rune() {
		case '/':
			u.openSearch(u.spotifyView)
			return nil
		case ',':
			u.showSettings()
			return nil
		case 'e':
			u.showSound()
			return nil
		case 'q':
			u.app.Stop()
			return nil
		case 's':
			go u.scan()
			return nil
		case ' ':
			u.current().TogglePause()
			return nil
		case 'n':
			switch {
			case u.nowPath == "": // nothing has ever played: start at the top
				u.playRow(firstPlayableRow(u.shown))
			case u.playing >= 0: // playing, and visible in this view
				u.playRow(u.playing + 1)
			} // else: playing but filtered out of view - no-op, ambiguous "next"
			return nil
		case 'p':
			if u.playing < 0 {
				return nil
			}
			u.playRow(u.playing - 1)
			return nil
		case '-':
			u.vol = clampVol(u.vol - 5)
			u.current().SetVolume(u.vol)
			return nil
		case '=':
			u.vol = clampVol(u.vol + 5)
			u.current().SetVolume(u.vol)
			return nil
		}
		return ev
	})
}

// cycleFocus only cycles into the sidebar when it is actually in the layout -
// otherwise tab would focus a hidden widget and the visible table would stop
// responding to its own navigation keys.
func (u *UI) cycleFocus(dir int) {
	order := []tview.Primitive{u.table}
	if u.layoutWidth >= 80 {
		order = []tview.Primitive{u.table, u.sidebar}
	}
	cur := u.app.GetFocus()
	for i, p := range order {
		if p == cur {
			next := order[(i+len(order)+dir)%len(order)]
			u.focus(next)
			return
		}
	}
	u.focus(u.table)
}

// scan runs off the UI goroutine; every widget touch goes through QueueUpdateDraw.
func (u *UI) scan() {
	u.app.QueueUpdateDraw(func() {
		u.setStatus(fmt.Sprintf("[%s]scanning %s...", mocha.Subtext0.String(), tview.Escape(u.dir)))
	})
	n, err := scanDirectory(context.Background(), u.dir, u.rdb)
	u.app.QueueUpdateDraw(func() {
		if err != nil {
			u.setStatus(fmt.Sprintf("[%s]scan failed: %v", mocha.Red.String(), tview.Escape(err.Error())))
			return
		}
		u.setStatus(fmt.Sprintf("[%s]indexed %d new track(s)", mocha.Green.String(), n))
		u.reload()
	})
}
