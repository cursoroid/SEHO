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
		BorderColor:                 mocha.Surface1,
		TitleColor:                  mocha.Lavender,
		GraphicsColor:               mocha.Surface1,
		PrimaryTextColor:            mocha.Text,
		SecondaryTextColor:          mocha.Subtext0,
		TertiaryTextColor:           mocha.Overlay0,
		InverseTextColor:            mocha.Base,
		ContrastSecondaryTextColor:  mocha.Mauve,
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
	shown   []item // current table contents, and the play queue
	playing int    // index into shown, -1 when nothing is playing

	pos, dur                     float64 // transport clock, seconds
	paused                       bool
	vol                          int
	nowTitle, nowArtist, nowPath string

	// userStopped marks a deliberate stop (currently: quitting) so the
	// end-file it produces does not trigger auto-advance.
	userStopped bool
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

	u.app.SetRoot(u.root, true).SetFocus(u.table)
	return u
}

func textPane(s string) *tview.TextView {
	tv := tview.NewTextView().SetDynamicColors(true).SetText(s)
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

// trackSource lets fuzzy match against title, artist and album at once.
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

// applyFilter repaints the table with fuzzy matches, ranked by score.
func (u *UI) applyFilter(query string) {
	if query == "" {
		u.table.SetTitle(" TRACKS ")
		u.setTracks(u.all)
		return
	}

	matches := fuzzy.FindFrom(query, trackSource(u.all))
	u.shown = make([]item, 0, len(matches))
	u.table.Clear()

	u.paintTableHeader()

	for row, m := range matches {
		it := u.all[m.Index]
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

	u.table.SetTitle(fmt.Sprintf(" TRACKS · search: %s ", query))
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
// to one row per distinct value; picking one is a second filter step handled
// by the search field, so there is no nested navigation state.
func (u *UI) filterBySection(section string) {
	switch section {
	case "All Tracks":
		u.setTracks(u.all)
	case "Recent":
		// listTracks sorts by Redis key (the file path), not recency, so
		// Recent sorts a copy by added_at and takes the newest 50.
		recent := slices.Clone(u.all)
		slices.SortStableFunc(recent, func(a, b item) int {
			return b.addedAt.Compare(a.addedAt) // newest first
		})
		if len(recent) > 50 {
			recent = recent[:50]
		}
		u.setTracks(recent)
	case "Artists", "Albums", "Tags":
		u.setTracks(groupBy(u.all, section))
	}
	u.table.SetTitle(fmt.Sprintf(" TRACKS · %s ", section))
}

// groupBy collapses the library to one representative row per distinct value,
// so the table can act as a browse index without a second widget.
func groupBy(all []item, section string) []item {
	seen := map[string]bool{}
	out := make([]item, 0, 64)
	for _, it := range all {
		var k string
		switch section {
		case "Artists":
			k = it.desc
		case "Albums":
			k = it.album
		case "Tags":
			k = it.tags
		}
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, item{title: k, desc: section, path: it.path, duration: it.duration, group: true})
	}
	return out
}

func (u *UI) refreshHeader() {
	u.header.SetText(fmt.Sprintf("  [%s::b]SEHO[-::-]%*s[%s]%d tracks",
		mocha.Lavender.String(), 50, "", mocha.Subtext0.String(), len(u.all)))
}

// selectedTrack returns the highlighted track. Row 0 is the header.
func (u *UI) selectedTrack() (item, bool) {
	row, _ := u.table.GetSelection()
	if row < 1 || row > len(u.shown) {
		return item{}, false
	}
	return u.shown[row-1], true
}

func (u *UI) playRow(row int) {
	if row < 0 || row >= len(u.shown) {
		return
	}
	u.playing = row
	u.nowTitle, u.nowArtist, u.nowPath = u.shown[row].title, u.shown[row].desc, u.shown[row].path
	u.pos, u.dur = 0, u.shown[row].duration
	u.userStopped = false
	if err := u.pl.Load(u.shown[row].path); err != nil {
		u.setStatus(fmt.Sprintf("[%s]playback failed: %v", mocha.Red.String(), err))
		return
	}
	u.table.Select(row+1, 0)
	u.drawCard(u.shown[row])
}

// advance plays the next row in the queue (u.shown), or parks at the end of
// the list. Called only from inside pumpEvents' QueueUpdateDraw closure -
// playRow, and therefore pl.Load, must run on the tview event-loop goroutine.
func (u *UI) advance() {
	next := u.playing + 1
	if next >= len(u.shown) {
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

// drawCard paints the NOW PLAYING card: embedded album art (or a fallback
// tile) plus the track's title and artist beneath it.
func (u *UI) drawCard(it item) {
	if it.path == "" {
		u.card.SetText("")
		return
	}
	art := AlbumArt(it.path, artCells, artRows)
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

func volMeter(v int) string {
	bars := []rune("▁▃▅▇")
	n := v * len(bars) / 130
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
	line1 := fmt.Sprintf("  [%s]%s[-]  [%s::b]%s[-::-] [%s]· %s%*s[%s]vol %s %d%%",
		iconColor.String(), icon,
		mocha.Text.String(), tview.Escape(title),
		mocha.Subtext0.String(), tview.Escape(u.nowArtist), 4, "",
		mocha.Subtext0.String(), volMeter(u.vol), u.vol)

	frac := 0.0
	if u.dur > 0 {
		frac = u.pos / u.dur
	}
	_, _, w, _ := u.transport.GetInnerRect()
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
		u.setStatus(fmt.Sprintf("[%s]library read failed: %v", mocha.Red.String(), err))
		return
	}
	u.all = items
	u.setTracks(items)
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
					u.setStatus(fmt.Sprintf("[%s]could not play %s", mocha.Red.String(), u.nowTitle))
					if !u.userStopped {
						u.advance()
					}
					return
				case "eof":
					if !u.userStopped {
						u.advance()
					}
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

func (u *UI) setFooter() {
	m := mocha.Mauve.String()
	u.footer.SetText(fmt.Sprintf(
		"  [%s]/[-] search  [%s]space[-] pause  [%s]←→[-] seek  [%s]n/p[-] track  [%s]-/=[-] vol  [%s]s[-] scan  [%s]q[-] quit",
		m, m, m, m, m, m, m))
}

// openSearch replaces the footer row with the search field while active.
func (u *UI) openSearch() {
	u.root.RemoveItem(u.footer)
	u.root.AddItem(u.search, 1, 0, true)
	u.search.SetText("")
	u.app.SetFocus(u.search)
}

// closeSearch restores the footer. clear also resets the filter (escape).
func (u *UI) closeSearch(clear bool) {
	u.root.RemoveItem(u.search)
	u.root.AddItem(u.footer, 1, 0, false)
	if clear {
		u.applyFilter("")
	}
	u.app.SetFocus(u.table)
}

func (u *UI) bindKeys() {
	u.table.SetSelectedFunc(func(row, _ int) { u.playRow(row - 1) })

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
			u.userStopped = true
			u.app.Stop()
			return nil
		case 's':
			go u.scan()
			return nil
		case ' ':
			u.pl.TogglePause()
			return nil
		case 'n':
			u.playRow(u.playing + 1)
			return nil
		case 'p':
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

func (u *UI) cycleFocus(dir int) {
	order := []tview.Primitive{u.table, u.sidebar}
	cur := u.app.GetFocus()
	for i, p := range order {
		if p == cur {
			u.app.SetFocus(order[(i+len(order)+dir)%len(order)])
			return
		}
	}
	u.app.SetFocus(u.table)
}

// scan runs off the UI goroutine; every widget touch goes through QueueUpdateDraw.
func (u *UI) scan() {
	u.app.QueueUpdateDraw(func() {
		u.setStatus(fmt.Sprintf("[%s]scanning %s...", mocha.Subtext0.String(), u.dir))
	})
	n, err := scanDirectory(context.Background(), u.dir, u.rdb)
	u.app.QueueUpdateDraw(func() {
		if err != nil {
			u.setStatus(fmt.Sprintf("[%s]scan failed: %v", mocha.Red.String(), err))
			return
		}
		u.setStatus(fmt.Sprintf("[%s]indexed %d new track(s)", mocha.Green.String(), n))
		u.reload()
	})
}
