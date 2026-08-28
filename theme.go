package main

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// Catppuccin Mocha. Do not add shades that are not in this struct.
var mocha = struct {
	Crust, Base, Mantle, Surface0, Surface1, Surface2, Overlay0 tcell.Color
	Text, Subtext0, Mauve, Pink, Green, Red, Peach, Lavender    tcell.Color
}{
	Crust:    tcell.GetColor("#11111b"),
	Base:     tcell.GetColor("#1e1e2e"),
	Mantle:   tcell.GetColor("#181825"),
	Surface0: tcell.GetColor("#313244"),
	Surface1: tcell.GetColor("#45475a"),
	Surface2: tcell.GetColor("#585b70"),
	Overlay0: tcell.GetColor("#6c7086"),
	Text:     tcell.GetColor("#cdd6f4"),
	Subtext0: tcell.GetColor("#a6adc8"),
	Mauve:    tcell.GetColor("#cba6f7"),
	Pink:     tcell.GetColor("#f5c2e7"),
	Green:    tcell.GetColor("#a6e3a1"),
	Red:      tcell.GetColor("#f38ba8"),
	Peach:    tcell.GetColor("#fab387"),
	Lavender: tcell.GetColor("#b4befe"),
}

// applyTheme installs the palette and the rounded border set.
//
// Depth is what makes the panes read as floating: the application ground is
// crust, one step darker than the base the panes are filled with, so each pane
// sits visibly above it. Without that contrast the rounded corners just look
// like differently-shaped lines on a flat plane.
func applyTheme() {
	tview.Borders.TopLeft = '╭'
	tview.Borders.TopRight = '╮'
	tview.Borders.BottomLeft = '╰'
	tview.Borders.BottomRight = '╯'
	tview.Borders.Horizontal = '─'
	tview.Borders.Vertical = '│'
	// Focus is signalled by border colour (see UI.focus), not by a heavier rune
	// set, so the focused variants stay identical to the unfocused ones.
	tview.Borders.TopLeftFocus = '╭'
	tview.Borders.TopRightFocus = '╮'
	tview.Borders.BottomLeftFocus = '╰'
	tview.Borders.BottomRightFocus = '╯'
	tview.Borders.HorizontalFocus = '─'
	tview.Borders.VerticalFocus = '│'

	tview.Styles = tview.Theme{
		PrimitiveBackgroundColor:    mocha.Base,
		ContrastBackgroundColor:     mocha.Surface2,
		MoreContrastBackgroundColor: mocha.Surface1,
		BorderColor:                 mocha.Surface0,
		TitleColor:                  mocha.Lavender,
		GraphicsColor:               mocha.Surface0,
		PrimaryTextColor:            mocha.Text,
		SecondaryTextColor:          mocha.Subtext0,
		TertiaryTextColor:           mocha.Overlay0,
		InverseTextColor:            mocha.Base,
		ContrastSecondaryTextColor:  mocha.Mauve,
	}
}

// wordmark is "SEHO" in FIGlet's "straight" face, baked at design time.
// ponytail: a FIGlet runtime for one static four-letter word is not worth a
// dependency. Regenerate with go-figure if the name ever changes, and keep
// clear of '[' — tview would parse it as a colour tag.
var wordmark = []string{
	` __  __       __`,
	`(_  |_  |__| /  \`,
	`__) |__ |  | \__/`,
}

// wordmarkWidth is the widest wordmark row.
func wordmarkWidth() int {
	w := 0
	for _, l := range wordmark {
		if n := len([]rune(l)); n > w {
			w = n
		}
	}
	return w
}

// smallCaps renders a column or pane label as spaced small capitals.
//
// The design asked for these labels in FIGlet too, but the geometry rules it
// out: "LIBRARY" needs 29 cells in the narrowest usable face against a 16-cell
// sidebar, and "TIME" needs 14 against a 5-cell column. Letter-spacing keeps
// the crafted feel and fits every pane at one row.
func smallCaps(s string) string {
	return strings.Join(strings.Split(strings.ToUpper(s), ""), " ")
}

// lerpHex blends two palette colours and returns a tview colour tag body.
func lerpHex(a, b tcell.Color, t float64) string {
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	ar, ag, ab := a.RGB()
	br, bg, bb := b.RGB()
	mix := func(x, y int32) int {
		return int(float64(x) + (float64(y)-float64(x))*t)
	}
	return fmt.Sprintf("#%02x%02x%02x", mix(ar, br), mix(ag, bg), mix(ab, bb))
}

// gutter is a one-cell spacer painted in crust.
//
// A nil Flex item reserves the space but draws nothing, so those cells keep
// whatever was behind them and the panes end up sitting on the same tone they
// are filled with - no separation, no float. An explicit Box guarantees the
// darker ground is actually painted.
func gutter() *tview.Box {
	return tview.NewBox().SetBackgroundColor(mocha.Crust)
}
