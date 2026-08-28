package main

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/redis/go-redis/v9"
	"github.com/rivo/tview"
	"github.com/sahilm/fuzzy"
)

// Catppuccin Mocha. Do not add shades that are not in this struct.
var mocha = struct {
	Base, Mantle, Surface1, Surface2, Overlay0         tcell.Color
	Text, Subtext0, Mauve, Green, Red, Peach, Lavender tcell.Color
}{
	Base:     tcell.GetColor("#1e1e2e"),
	Mantle:   tcell.GetColor("#181825"),
	Surface1: tcell.GetColor("#45475a"),
	Surface2: tcell.GetColor("#585b70"),
	Overlay0: tcell.GetColor("#6c7086"),
	Text:     tcell.GetColor("#cdd6f4"),
	Subtext0: tcell.GetColor("#a6adc8"),
	Mauve:    tcell.GetColor("#cba6f7"),
	Green:    tcell.GetColor("#a6e3a1"),
	Red:      tcell.GetColor("#f38ba8"),
	Peach:    tcell.GetColor("#fab387"),
	Lavender: tcell.GetColor("#b4befe"),
}

func applyTheme() {
	tview.Styles = tview.Theme{
		PrimitiveBackgroundColor:    mocha.Base,
		ContrastBackgroundColor:     mocha.Surface2,
		MoreContrastBackgroundColor: mocha.Surface1,
		// Accepted gap: the palette design calls for a mauve *focused* border,
		// but tview v0.42's Theme has a single BorderColor with no separate
		// focus variant, so it stays surface1 in both states. Focus is
		// signalled by tview's built-in double-line border rune set instead.
		BorderColor:                mocha.Surface1,
		TitleColor:                 mocha.Lavender,
		GraphicsColor:              mocha.Surface1,
		PrimaryTextColor:           mocha.Text,
		SecondaryTextColor:         mocha.Subtext0,
		TertiaryTextColor:          mocha.Overlay0,
		InverseTextColor:           mocha.Base,
		ContrastSecondaryTextColor: mocha.Mauve,
	}
}

var sidebarSections = []string{"All Tracks", "Artists", "Albums", "Tags", "Recent"}

type UI struct {
	app *tview.Application
	rdb *redis.Client
	dir string
	pl  *Player

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

func NewUI(rdb *redis.Client, dir string, pl *Player) *UI {
	applyTheme()
	u := &UI{
		app: tview.NewApplication(),
		rdb: rdb, dir: dir, pl: pl,
		playing: -1,
	}

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
			u.focusedSidebar = false
			u.app.SetFocus(u.table)
		})
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
	u.search.SetChangedFunc(func(q string) { u.applyFilter(q) })
	u.search.SetDoneFunc(func(key tcell.Key) {
		u.closeSearch(key == tcell.KeyEscape)
	})

	u.body = tview.NewFlex().
		AddItem(u.sidebar, 16, 0, false).
		AddItem(u.table, 0, 1, true).
		AddItem(u.card, 32, 0, false)

	u.root = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(u.header, 3, 0, false).
		AddItem(u.body, 0, 1, true).
		AddItem(u.transport, 4, 0, false).
		AddItem(u.footer, 1, 0, false)

	u.app.SetBeforeDrawFunc(func(screen tcell.Screen) bool {
		w, _ := screen.Size()
		u.relayout(w)
		return false // false = let tview draw normally
	})

	u.app.SetRoot(u.root, true).SetFocus(u.table)
	return u
}

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
	case width >= 110:
		u.body.AddItem(u.sidebar, 16, 0, false).
			AddItem(u.table, 0, 1, true).
			AddItem(u.card, 32, 0, false)
	case width >= 80:
		u.body.AddItem(u.sidebar, 16, 0, false).
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
	if width >= 110 && u.nowPath != "" {
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
		go u.app.QueueUpdateDraw(func() { u.app.SetFocus(u.table) })
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
		u.table.SetCell(0, c, tview.NewTableCell(h).
			SetTextColor(mocha.Overlay0).
			SetSelectable(false).
			SetExpansion(map[int]int{0: 0, 1: 3, 2: 2, 3: 0}[c]))
	}
}

func (u *UI) setTracks(items []item) {
	u.shown = items
	u.table.Clear()

	u.paintTableHeader()

	for i, it := range items {
		u.table.SetCell(i+1, 0, cell(fmt.Sprintf("%d", i+1), mocha.Overlay0))
		u.table.SetCell(i+1, 1, cell(tview.Escape(it.title), mocha.Text))
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

		u.table.SetCell(row+1, 0, cell(fmt.Sprintf("%d", row+1), mocha.Overlay0))
		u.table.SetCell(row+1, 1, cell(highlight(it.title, titleHits), mocha.Text))
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
	label := fmt.Sprintf("%d tracks", len(u.all))
	_, _, w, _ := u.header.GetInnerRect()
	pad := padWidth(w, "  SEHO", label)
	u.header.SetText(fmt.Sprintf("  [%s::b]SEHO[-::-]%*s[%s]%d tracks",
		mocha.Lavender.String(), pad, "", mocha.Subtext0.String(), len(u.all)))
}

func (u *UI) playRow(row int) {
	// Belt-and-braces: playRow is the single choke point every play path runs
	// through, so it refuses a group pseudo-row here too, on top of the
	// callers that already know to avoid one (filterByGroup never calls this;
	// bindKeys' SelectedFunc routes a group row to filterByGroup instead).
	if row < 0 || row >= len(u.shown) || u.shown[row].group {
		return
	}
	u.playing = row
	u.nowTitle, u.nowArtist, u.nowPath = u.shown[row].title, u.shown[row].desc, u.shown[row].path
	u.pos, u.dur = 0, u.shown[row].duration
	if err := u.pl.Load(u.shown[row].path); err != nil {
		u.setStatus(fmt.Sprintf("[%s]playback failed: %v", mocha.Red.String(), tview.Escape(err.Error())))
		return
	}
	u.table.Select(row+1, 0)
	u.drawCard(u.shown[row])
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
	if it.path == "" || u.layoutWidth < 110 {
		u.card.SetText("")
		return
	}
	art := AlbumArt(it.path, it.album, artCells, artRows)
	u.card.SetText(fmt.Sprintf("\n%s\n  [%s::b]%s[-::-]\n  [%s]%s",
		art, mocha.Text.String(), tview.Escape(it.title), mocha.Subtext0.String(), tview.Escape(it.desc)))
}

// progressBar renders a width-cell bar. Exactly width runes of content,
// with a knob at the boundary.
func progressBar(frac float64, width int) string {
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	fill := int(frac*float64(width) + 0.5)
	knob := ""
	if fill > 0 && fill < width {
		knob = "●"
	}
	body := strings.Repeat("━", fill)
	if knob != "" {
		body = strings.Repeat("━", fill-1) + knob
	}
	return fmt.Sprintf("[%s]%s[%s]%s",
		mocha.Mauve.String(), body,
		mocha.Surface1.String(), strings.Repeat("─", width-fill))
}

// volMeter scales glyphs against 100 (not mpv's 130% overdrive ceiling) so the
// default volume lights every glyph; values above 100 just show a full meter.
// The numeric percentage shown alongside stays exact.
func volMeter(v int) string {
	bars := []rune("▁▃▅▇")
	n := min(len(bars), max(0, v*len(bars)/100))
	out := make([]rune, 0, len(bars))
	for i := range bars {
		if i < n {
			out = append(out, bars[i])
		} else {
			out = append(out, '·')
		}
	}
	return string(out)
}

func (u *UI) drawTransport() {
	icon, iconColor := "▶", mocha.Green
	if u.paused {
		icon, iconColor = "⏸", mocha.Red
	}
	if u.nowTitle == "" {
		icon, iconColor = "■", mocha.Overlay0
	}

	title := u.nowTitle
	if title == "" {
		title = "nothing playing"
	}

	// Only emit the "· artist" separator when there is an artist - otherwise
	// idle playback reads "■  nothing playing ·" with a dangling middot.
	left := fmt.Sprintf("  [%s]%s[-]  [%s::b]%s[-::-]",
		iconColor.String(), icon, mocha.Text.String(), tview.Escape(title))
	if u.nowArtist != "" {
		left += fmt.Sprintf(" [%s]· %s[-]", mocha.Subtext0.String(), tview.Escape(u.nowArtist))
	}

	right := fmt.Sprintf("[%s]vol %s %d%%", mocha.Peach.String(), volMeter(u.vol), u.vol)

	_, _, w, _ := u.transport.GetInnerRect()
	pad := padWidth(w, left, right)
	line1 := left + strings.Repeat(" ", pad) + right

	frac := 0.0
	if u.dur > 0 {
		frac = u.pos / u.dur
	}
	barWidth := max(10, w-24)
	line2 := fmt.Sprintf("  [%s]%s [-]%s[%s] %s",
		mocha.Subtext0.String(), fmtDuration(u.pos),
		progressBar(frac, barWidth),
		mocha.Subtext0.String(), fmtDuration(u.dur))

	u.transport.SetText(line1 + "\n" + line2)
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

// pumpEvents forwards mpv state onto the UI goroutine. Runs for the app's life.
func (u *UI) pumpEvents() {
	for ev := range u.pl.Events() {
		ev := ev
		u.app.QueueUpdateDraw(func() {
			switch ev.Name {
			case "time-pos":
				u.pos = ev.Num
			case "duration":
				u.dur = ev.Num
				// Backfill tracks indexed without ffprobe.
				if u.playing >= 0 && u.playing < len(u.shown) && u.shown[u.playing].duration <= 0 {
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
			case "disconnected":
				u.setStatus(fmt.Sprintf("[%s]lost connection to mpv", mocha.Red.String()))
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
	u.vol = 100
	go u.pumpEvents()
	u.drawTransport()
	return u.app.Run()
}

// setFooter rebuilds the key-hint bar. "tab pane" only makes sense once the
// sidebar is actually in the layout, so it tracks u.layoutWidth.
func (u *UI) setFooter() {
	m := mocha.Mauve.String()
	keys := []string{"/[-] search", "space[-] pause", "←→[-] seek", "n/p[-] track", "-/=[-] vol"}
	if u.layoutWidth >= 80 {
		keys = append(keys, "tab[-] pane")
	}
	keys = append(keys, "s[-] scan", "q[-] quit")

	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "  [%s]%s", m, k)
	}
	u.footer.SetText(b.String())
}

// openSearch replaces the footer row with the search field while active.
func (u *UI) openSearch() {
	u.root.RemoveItem(u.footer)
	u.root.AddItem(u.search, 1, 0, true)
	u.search.SetText("")
	u.focusedSidebar = false
	u.app.SetFocus(u.search)
}

// closeSearch restores the footer. clear also resets the filter (escape).
func (u *UI) closeSearch(clear bool) {
	u.root.RemoveItem(u.search)
	u.root.AddItem(u.footer, 1, 0, false)
	if clear {
		u.applyFilter("")
	}
	u.focusedSidebar = false
	u.app.SetFocus(u.table)
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
			u.filterByGroup(it.groupField, it.title)
			return
		}
		u.playRow(idx)
	})

	u.app.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
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
			u.pl.Seek(-5)
			return nil
		case tcell.KeyRight:
			u.pl.Seek(5)
			return nil
		}
		switch ev.Rune() {
		case '/':
			u.openSearch()
			return nil
		case 'q':
			u.app.Stop()
			return nil
		case 's':
			go u.scan()
			return nil
		case ' ':
			u.pl.TogglePause()
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
			u.pl.SetVolume(u.vol)
			return nil
		case '=':
			u.vol = clampVol(u.vol + 5)
			u.pl.SetVolume(u.vol)
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
			u.focusedSidebar = next == u.sidebar
			u.app.SetFocus(next)
			return
		}
	}
	u.focusedSidebar = false
	u.app.SetFocus(u.table)
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
