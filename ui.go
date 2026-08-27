package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/redis/go-redis/v9"
	"github.com/rivo/tview"
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

	pos, dur            float64 // transport clock, seconds
	paused              bool
	vol                 int
	nowTitle, nowArtist string
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
		u.sidebar.AddItem(s, "", 0, nil)
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
		u.table.SetCell(i+1, 1, cell(it.title, mocha.Text))
		u.table.SetCell(i+1, 2, cell(it.desc, mocha.Subtext0))
		u.table.SetCell(i+1, 3, cell(fmtDuration(it.duration), mocha.Subtext0))
	}
	if len(items) > 0 {
		u.table.Select(1, 0)
	}
	u.refreshHeader()
}

func cell(s string, c tcell.Color) *tview.TableCell {
	return tview.NewTableCell(s).SetTextColor(c)
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
	u.nowTitle, u.nowArtist = u.shown[row].title, u.shown[row].desc
	u.pos, u.dur = 0, u.shown[row].duration
	if err := u.pl.Load(u.shown[row].path); err != nil {
		u.setStatus(fmt.Sprintf("[%s]playback failed: %v", mocha.Red.String(), err))
		return
	}
	u.table.Select(row+1, 0)
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
		mocha.Text.String(), title,
		mocha.Subtext0.String(), u.nowArtist, 4, "",
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
					go backfillDuration(context.Background(), u.rdb, u.shown[u.playing].path, ev.Num)
				}
			case "pause":
				u.paused = ev.Flag
			case "volume":
				u.vol = int(ev.Num)
			case "end-file":
				u.pos = 0
				if ev.Reason == "error" {
					u.setStatus(fmt.Sprintf("[%s]could not play %s", mocha.Red.String(), u.nowTitle))
					return
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

func (u *UI) bindKeys() {
	u.table.SetSelectedFunc(func(row, _ int) { u.playRow(row - 1) })

	u.app.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		// Let the search field consume everything except escape.
		if u.app.GetFocus() == u.search && ev.Key() != tcell.KeyEscape {
			return ev
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
