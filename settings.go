package main

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/redis/go-redis/v9"
	"github.com/rivo/tview"
)

// Page names for the root Pages widget. "main" is the player; the other two are
// full-screen and swap it out entirely.
const (
	pageMain     = "main"
	pageSettings = "settings"
	pageSound    = "sound"
)

var bitrateChoices = []string{"96", "160", "320"}

// showSettings builds and shows the settings page. It is rebuilt on every open
// rather than kept around, so the fields always show current values and there is
// no stale-form state to reconcile.
func (u *UI) showSettings() {
	set := &u.set
	form := tview.NewForm()
	form.SetBackgroundColor(mocha.Base)
	form.SetFieldBackgroundColor(mocha.Mantle).
		SetFieldTextColor(mocha.Text).
		SetLabelColor(mocha.Subtext0).
		SetButtonBackgroundColor(mocha.Surface1).
		SetButtonTextColor(mocha.Text)

	// addField adds one editable value, or a read-only note when an environment
	// variable already decides it. Offering to edit a field the environment
	// overrides would be a lie: Save could not honour it.
	addField := func(label, key, value string, onChange func(string)) {
		if env, locked := set.Env[key]; locked {
			form.AddTextView(label, fmt.Sprintf("%s  (set by %s)", value, env), 0, 1, true, false)
			return
		}
		form.AddInputField(label, value, 0, nil, onChange)
	}

	addField("Music directory", "music_dir", set.File.MusicDir,
		func(s string) { set.File.MusicDir = s })
	addField("Redis address", "redis_addr", set.File.RedisAddr,
		func(s string) { set.File.RedisAddr = s })
	addField("Spotify client id", "spotify_client_id", set.File.SpotifyClientID,
		func(s string) { set.File.SpotifyClientID = s })
	addField("Device name", "device_name", set.File.DeviceName,
		func(s string) { set.File.DeviceName = s })

	if env, locked := set.Env["bitrate"]; locked {
		form.AddTextView("Spotify bitrate", fmt.Sprintf("%d kbps  (set by %s)", set.Eff.Bitrate, env), 0, 1, true, false)
	} else {
		idx := 2 // 320 kbps
		for i, c := range bitrateChoices {
			if c == strconv.Itoa(set.File.Bitrate) {
				idx = i
			}
		}
		form.AddDropDown("Spotify bitrate", bitrateChoices, idx, func(opt string, _ int) {
			if n, err := strconv.Atoi(opt); err == nil {
				set.File.Bitrate = n
			}
		})
	}

	form.AddTextView("Spotify account", u.spotifyAccountLine(), 0, 2, true, false)

	// Which client actually streams. Soloist is Spotify's own and works on
	// accounts librespot cannot stream on at all, so it leads.
	backends := []string{backendSoloist, backendLibrespot}
	if env, locked := set.Env["spotify_backend"]; locked {
		form.AddTextView("Playback client", fmt.Sprintf("%s  (set by %s)", set.Eff.SpotifyBackend, env), 0, 1, true, false)
	} else {
		idx := 0
		if set.File.SpotifyBackend == backendLibrespot {
			idx = 1
		}
		form.AddDropDown("Playback client", backends, idx, func(opt string, _ int) {
			set.File.SpotifyBackend = opt
		})
	}

	form.AddCheckbox("Lossless capture", set.File.Lossless, func(on bool) {
		set.File.Lossless = on
	})

	form.AddInputField("Soloist API key", maskKey(LoadSoloistKey()), 0, nil, func(v string) {
		u.soloistKeyEdit = v
	})
	form.AddTextView("Soloist", u.soloistStatusLine(), 0, 2, true, false)

	if u.api != nil && u.api.Connected() {
		form.AddButton("Disconnect Spotify", func() {
			if err := u.api.Disconnect(); err != nil {
				u.setStatus(errMarkup("disconnect: " + err.Error()))
			}
			u.closePage()
			u.showSettings()
		})
	} else {
		form.AddButton("Connect Spotify", func() { u.connectSpotify() })
	}

	form.AddButton("Save", func() { u.saveSettings() })
	form.AddButton("Cancel", func() { u.closePage() })

	deps := tview.NewTextView().SetDynamicColors(true).SetText(u.dependencyLines())
	deps.SetBackgroundColor(mocha.Base)

	title := textPane(fmt.Sprintf("  [%s::b]SEHO[-::-] [%s]· settings[-]     [%s]esc[-] back  [%s]tab[-] field",
		mocha.Lavender.String(), mocha.Subtext0.String(),
		mocha.Mauve.String(), mocha.Mauve.String()))

	body := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(title, 1, 0, false).
		AddItem(gutter(), 1, 0, false).
		AddItem(form, 0, 1, true).
		AddItem(deps, 4, 0, false)
	body.SetBackgroundColor(mocha.Base)
	body.SetBorder(true).SetBorderColor(mocha.Mauve).SetTitle(" SETTINGS ")

	u.pages.AddAndSwitchToPage(pageSettings, body, true)
	u.app.SetFocus(form)
}

// spotifyAccountLine describes the credential state in words the settings page
// can show without the user opening a log.
func (u *UI) spotifyAccountLine() string {
	switch {
	case u.set.Eff.SpotifyClientID == "":
		return "not configured - create an app at developer.spotify.com,\nredirect URI " + spotifyRedirect
	case u.api == nil || !u.api.Connected():
		return "client id set, not signed in - use Connect Spotify"
	default:
		_, keychain := loadRefreshToken()
		where := "token file (" + tokenFilePath() + ")"
		if keychain {
			where = "login keychain"
		}
		return "connected - refresh token in the " + where
	}
}

// maskKey shows enough of a stored credential to recognise it, and no more.
func maskKey(k string) string {
	if k == "" {
		return ""
	}
	if len(k) <= 10 {
		return "…"
	}
	return k[:6] + "…" + k[len(k)-4:]
}

// soloistStatusLine describes the Soloist side: the container image, whether it
// is paired, and what to do about it if not.
func (u *UI) soloistStatusLine() string {
	if u.set.Eff.SpotifyBackend != backendSoloist {
		return "not in use - playback client is " + u.set.Eff.SpotifyBackend
	}
	if err := dockerAvailable(); err != nil {
		return err.Error()
	}
	if err := soloistImagePresent(u.set.Eff.SoloistImage); err != nil {
		return "image missing - build it: docker build -t " + u.set.Eff.SoloistImage + " ./docker"
	}
	if LoadSoloistKey() == "" {
		return "no API key - create one at developer.spotify.com and paste it above"
	}
	if !soloistPaired() {
		return "not paired - see docker/README.md (one-time, needs an mDNS proxy on macOS)"
	}
	format := "16-bit"
	if u.set.Eff.Lossless {
		format = "32-bit (lossless-preserving)"
	}
	return "ready, capturing " + format
}

// dependencyLines reports the external programs SEHO shells out to, so a
// missing one is visible here rather than only at the moment it fails.
func (u *UI) dependencyLines() string {
	line := func(name, detail string, ok bool) string {
		mark, colour := "✗", mocha.Red
		if ok {
			mark, colour = "✓", mocha.Green
		}
		return fmt.Sprintf("  [%s]%s[-] %-10s [%s]%s", colour.String(), mark, name,
			mocha.Subtext0.String(), tview.Escape(detail))
	}

	librespot := "not on PATH - brew install librespot"
	if librespotInstalled() {
		librespot = "version " + librespotVersion()
		if v := librespotVersion(); v == "" {
			librespot = "installed"
		}
	}

	redisState := "unreachable at " + u.set.Eff.RedisAddr
	if err := u.rdb.Ping(context.Background()).Err(); err == nil {
		redisState = "connected to " + u.set.Eff.RedisAddr
	}

	return strings.Join([]string{
		line("librespot", librespot, librespotInstalled()),
		line("redis", redisState, strings.HasPrefix(redisState, "connected")),
		line("config", configPath(), true),
	}, "\n")
}

// saveSettings writes the file and applies what can be applied without a
// restart. Anything that cannot is named explicitly rather than silently
// deferred.
func (u *UI) saveSettings() {
	oldRedis := u.set.Eff.RedisAddr
	oldDevice, oldBitrate := u.set.Eff.DeviceName, u.set.Eff.Bitrate

	// Re-derive the effective values so environment overrides keep winning.
	u.set = applyEnv(u.set.File, envLookup)

	if err := u.set.Save(); err != nil {
		u.setStatus(errMarkup("save settings: " + err.Error()))
		return
	}

	u.dir = u.set.Eff.MusicDir
	if u.api != nil {
		u.api.SetClientID(u.set.Eff.SpotifyClientID)
	}

	// An untouched key field shows the masked value, which must never be stored
	// back over the real one.
	if k := strings.TrimSpace(u.soloistKeyEdit); k != "" && !strings.Contains(k, "…") {
		if err := SaveSoloistKey(k); err != nil {
			u.setStatus(errMarkup("store Soloist key: " + err.Error()))
			return
		}
		u.soloistKeyEdit = ""
	}

	var notes []string
	if u.set.Eff.RedisAddr != oldRedis {
		if err := u.reconnectRedis(u.set.Eff.RedisAddr); err != nil {
			u.setStatus(errMarkup("redis " + u.set.Eff.RedisAddr + ": " + err.Error()))
			return
		}
		notes = append(notes, "reconnected to redis")
	}
	if u.spot != nil && (u.set.Eff.DeviceName != oldDevice || u.set.Eff.Bitrate != oldBitrate) {
		notes = append(notes, "device name/bitrate apply after restart")
	}

	u.closePage()
	msg := "settings saved"
	if len(notes) > 0 {
		msg += " - " + strings.Join(notes, ", ")
	}
	u.setStatus(fmt.Sprintf("[%s]%s", mocha.Green.String(), msg))
}

// reconnectRedis swaps the client for one pointed at addr, keeping the old
// connection if the new address does not answer - a settings page that can
// disconnect you from your own library by typo is not acceptable.
func (u *UI) reconnectRedis(addr string) error {
	next := redis.NewClient(&redis.Options{Addr: addr})
	if err := next.Ping(context.Background()).Err(); err != nil {
		next.Close()
		return err
	}
	old := u.rdb
	u.rdb = next
	old.Close()
	u.reload()
	return nil
}

// connectSpotify runs the PKCE login off the UI goroutine and reports the URL
// in case the browser did not open.
func (u *UI) connectSpotify() {
	if u.set.Eff.SpotifyClientID == "" {
		u.setStatus(errMarkup("set a Spotify client id first, then Save"))
		return
	}
	u.api.SetClientID(u.set.Eff.SpotifyClientID)
	u.setStatus(fmt.Sprintf("[%s]opening browser for Spotify sign-in...", mocha.Subtext0.String()))

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()

		err := u.api.Authorize(ctx, func(url string) {
			u.app.QueueUpdateDraw(func() {
				u.setStatus(fmt.Sprintf("[%s]sign in at: %s", mocha.Subtext0.String(), tview.Escape(url)))
			})
		})
		u.app.QueueUpdateDraw(func() {
			if err != nil {
				u.setStatus(errMarkup("spotify login: " + err.Error()))
				return
			}
			u.setStatus(fmt.Sprintf("[%s]Spotify connected", mocha.Green.String()))
			if u.pages.HasPage(pageSettings) {
				u.closePage()
				u.showSettings()
			}
		})
	}()
}

// --- sound page ------------------------------------------------------------

// showSound builds the equalizer page: the profile list on the left, the
// current curve and its bands on the right. Selecting a profile applies it
// immediately - an EQ you cannot hear while choosing is guesswork.
func (u *UI) showSound() {
	list := tview.NewList().ShowSecondaryText(true)
	list.SetBackgroundColor(mocha.Base)
	list.SetMainTextColor(mocha.Text).
		SetSecondaryTextColor(mocha.Overlay0).
		SetSelectedTextColor(mocha.Base).
		SetSelectedBackgroundColor(mocha.Mauve)
	list.SetBorder(true).SetTitle(" PROFILES ")

	curve := tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	curve.SetBackgroundColor(mocha.Base)
	curve.SetBorder(true).SetTitle(" CURVE ")

	bands := tview.NewTable().SetSelectable(true, false)
	bands.SetBackgroundColor(mocha.Base)
	bands.SetBorder(true).SetTitle(" BANDS ")
	bands.SetSelectedStyle(tcell.StyleDefault.Background(mocha.Surface2).Foreground(mocha.Text))

	redraw := func() {
		curve.SetText(u.curveText(curve))
		u.paintBands(bands)
	}

	for i, p := range profiles {
		p, i := p, i
		list.AddItem(p.Name, p.Source, 0, func() {
			u.selectProfile(profiles[i])
			redraw()
			u.app.SetFocus(bands)
		})
	}
	// Point the list at whatever is active, so opening the page does not
	// silently suggest the first profile is the current one.
	for i, p := range profiles {
		if p.Key == u.eq.Key {
			list.SetCurrentItem(i)
		}
	}
	list.SetChangedFunc(func(i int, _, _ string, _ rune) {
		if i >= 0 && i < len(profiles) {
			u.selectProfile(profiles[i])
			redraw()
		}
	})

	note := speakerNote()
	if note == "" {
		note = "audio filters are applied by mpv, to local files and Spotify alike"
	}
	title := textPane(fmt.Sprintf("  [%s::b]SEHO[-::-] [%s]· sound[-]  [%s]%s",
		mocha.Lavender.String(), mocha.Subtext0.String(),
		mocha.Overlay0.String(), tview.Escape(note)))

	help := textPane(fmt.Sprintf("  [%s]↑↓[-] profile / band  [%s]←→[-] gain ±0.5dB  [%s]0[-] reset band  [%s]r[-] reset curve  [%s]s[-] save  [%s]esc[-] back",
		mocha.Mauve.String(), mocha.Mauve.String(), mocha.Mauve.String(),
		mocha.Mauve.String(), mocha.Mauve.String(), mocha.Mauve.String()))

	right := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(curve, 12, 0, false).
		AddItem(bands, 0, 1, true)

	body := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(title, 1, 0, false).
		AddItem(gutter(), 1, 0, false).
		AddItem(tview.NewFlex().
			AddItem(list, 34, 0, false).
			AddItem(gutter(), 1, 0, false).
			AddItem(right, 0, 1, true), 0, 1, true).
		AddItem(help, 1, 0, false)
	body.SetBackgroundColor(mocha.Crust)

	// Band editing keys live here rather than in the global capture: they only
	// mean anything while this page is up, and the global capture already has
	// left/right bound to seeking.
	body.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if u.app.GetFocus() != tview.Primitive(bands) {
			return ev
		}
		row, _ := bands.GetSelection()
		idx := row - 1
		switch {
		case ev.Key() == tcell.KeyLeft:
			u.nudgeBand(idx, -0.5)
		case ev.Key() == tcell.KeyRight:
			u.nudgeBand(idx, +0.5)
		case ev.Rune() == '0':
			u.setBandGain(idx, 0)
		case ev.Rune() == 'r':
			u.resetCurve()
		case ev.Rune() == 's':
			u.saveEQ()
			return nil
		default:
			return ev
		}
		redraw()
		return nil
	})

	redraw()
	u.pages.AddAndSwitchToPage(pageSound, body, true)
	u.app.SetFocus(list)
}

// selectProfile applies a profile as the live EQ without saving it.
func (u *UI) selectProfile(p profile) {
	u.eq = p
	u.applyEQ()
}

// eqChain is the filter chain the current profile renders to, or empty when the
// equalizer is switched off.
func (u *UI) eqChain() string {
	if !u.set.Eff.EQ.Enabled {
		return ""
	}
	return afChain(u.eq)
}

// applyEQ pushes the current chain to every backend that exists, so switching
// source does not switch sound.
func (u *UI) applyEQ() {
	chain := u.eqChain()
	if u.local != nil {
		u.local.SetAF(chain)
	}
	if sp := u.spotify(); sp != nil {
		sp.SetAF(chain)
	}
}

func (u *UI) nudgeBand(idx int, delta float64) {
	if idx < 0 || idx >= len(u.eq.Bands) {
		return
	}
	u.setBandGain(idx, u.eq.Bands[idx].Gain+delta)
}

func (u *UI) setBandGain(idx int, gain float64) {
	if idx < 0 || idx >= len(u.eq.Bands) {
		return
	}
	// A high-pass or low-pass has no gain; afChain ignores it, and letting the
	// arrows drift a value the table renders as "-" would persist a lie to the
	// config file.
	if k := u.eq.Bands[idx].Kind; k == bandHighPass || k == bandLowPass {
		return
	}
	// Copy before mutating: u.eq.Bands may still alias the baked-in profile
	// table, and editing that would corrupt the profile for the whole session.
	bands := make([]band, len(u.eq.Bands))
	copy(bands, u.eq.Bands)
	bands[idx].Gain = clampGain(gain)
	u.eq.Bands = bands
	u.applyEQ()
}

// resetCurve restores the selected profile's published values, discarding edits.
func (u *UI) resetCurve() {
	if p, ok := profileByKey(u.eq.Key); ok {
		u.eq = p
		u.applyEQ()
	}
}

// saveEQ persists the current profile and, when it has been edited, its bands.
func (u *UI) saveEQ() {
	u.set.File.EQ.Enabled = true
	u.set.File.EQ.Profile = u.eq.Key
	u.set.File.EQ.Bands = nil
	if p, ok := profileByKey(u.eq.Key); ok && !sameBands(p.Bands, u.eq.Bands) {
		u.set.File.EQ.Bands = u.eq.Bands
	}
	u.set = applyEnv(u.set.File, envLookup)
	if err := u.set.Save(); err != nil {
		u.setStatus(errMarkup("save eq: " + err.Error()))
		return
	}
	u.closePage()
	u.setStatus(fmt.Sprintf("[%s]sound profile saved: %s", mocha.Green.String(), tview.Escape(u.eq.Name)))
}

func sameBands(a, b []band) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// curveText draws one bar per band, using the same eighth-block glyphs as the
// transport meters. ponytail: bars, not a computed transfer function - the bars
// say what each band does, and a real magnitude plot would need biquad maths
// for a picture nobody tunes by.
func (u *UI) curveText(tv *tview.TextView) string {
	_, _, w, _ := tv.GetInnerRect()
	if len(u.eq.Bands) == 0 {
		return fmt.Sprintf("\n  [%s]%s\n\n  [%s]no filters - audio passes through untouched",
			mocha.Text.String(), tview.Escape(u.eq.Name), mocha.Subtext0.String())
	}

	const rows = 6 // half above the zero line, half below
	cellsPer := max(3, min(8, (max(20, w)-4)/max(1, len(u.eq.Bands))))

	var b strings.Builder
	fmt.Fprintf(&b, "  [%s]%s  [%s]preamp %+.1f dB\n",
		mocha.Text.String(), tview.Escape(u.eq.Name), mocha.Subtext0.String(), u.eq.Preamp)

	for r := rows; r >= -rows; r-- {
		if r == 0 {
			fmt.Fprintf(&b, "  [%s]%4s ┼%s\n", mocha.Overlay0.String(), "0",
				strings.Repeat("─", cellsPer*len(u.eq.Bands)))
			continue
		}
		label := ""
		if r == rows {
			label = fmt.Sprintf("%+d", int(maxCurveDB))
		} else if r == -rows {
			label = fmt.Sprintf("%d", -int(maxCurveDB))
		}
		fmt.Fprintf(&b, "  [%s]%4s ┤", mocha.Overlay0.String(), label)
		for _, bd := range u.eq.Bands {
			b.WriteString(barSegment(bd, r, rows, cellsPer))
		}
		b.WriteString("\n")
	}

	// Frequency labels, one per band, clipped to the column width.
	fmt.Fprintf(&b, "       ")
	for _, bd := range u.eq.Bands {
		fmt.Fprintf(&b, "[%s]%-*s", mocha.Subtext0.String(), cellsPer, freqLabel(bd.Freq))
	}
	return b.String()
}

// maxCurveDB is the vertical range of the curve display.
const maxCurveDB = 12.0

// barSegment renders one band's column on one row of the curve. A high-pass or
// low-pass has no gain, so it draws as a marker on the zero line's neighbour
// rather than a bar that would imply a boost or a cut.
func barSegment(bd band, row, rows, cells int) string {
	filled := int(math.Round(float64(rows) * bd.Gain / maxCurveDB))
	glyph, colour := "█", mocha.Mauve
	if bd.Gain < 0 {
		colour = mocha.Peach
	}
	switch bd.Kind {
	case bandHighPass, bandLowPass:
		if row != 1 {
			return strings.Repeat(" ", cells)
		}
		return fmt.Sprintf("[%s]%s", mocha.Overlay0.String(),
			pad("▚▚", cells))
	}
	inBar := (bd.Gain > 0 && row > 0 && row <= filled) ||
		(bd.Gain < 0 && row < 0 && row >= filled)
	if !inBar {
		return strings.Repeat(" ", cells)
	}
	return fmt.Sprintf("[%s]%s", colour.String(), pad(strings.Repeat(glyph, max(1, cells-1)), cells))
}

func pad(s string, cells int) string {
	if w := tview.TaggedStringWidth(s); w < cells {
		return s + strings.Repeat(" ", cells-w)
	}
	return s
}

func freqLabel(f float64) string {
	if f >= 1000 {
		return fmt.Sprintf("%.4gk", f/1000)
	}
	return fmt.Sprintf("%.0f", f)
}

// paintBands fills the band table: what each filter is, and what it is doing.
func (u *UI) paintBands(t *tview.Table) {
	t.Clear()
	for c, h := range []string{"#", "TYPE", "FREQ", "GAIN", "Q"} {
		label := h
		if c > 0 {
			label = smallCaps(h)
		}
		t.SetCell(0, c, tview.NewTableCell(label).
			SetTextColor(mocha.Overlay0).SetSelectable(false))
	}
	names := map[bandKind]string{
		bandPeak: "peak", bandLowShelf: "low shelf", bandHighShelf: "high shelf",
		bandHighPass: "high pass", bandLowPass: "low pass",
	}
	for i, bd := range u.eq.Bands {
		gain := fmt.Sprintf("%+.1f dB", bd.Gain)
		if bd.Kind == bandHighPass || bd.Kind == bandLowPass {
			gain = "-"
		}
		t.SetCell(i+1, 0, cell(strconv.Itoa(i+1), mocha.Overlay0))
		t.SetCell(i+1, 1, cell(names[bd.Kind], mocha.Text))
		t.SetCell(i+1, 2, cell(fmt.Sprintf("%.0f Hz", bd.Freq), mocha.Subtext0))
		t.SetCell(i+1, 3, cell(gain, gainColour(bd)))
		t.SetCell(i+1, 4, cell(fmt.Sprintf("%.2f", bd.Q), mocha.Subtext0))
	}
	if len(u.eq.Bands) > 0 {
		row, _ := t.GetSelection()
		if row < 1 || row > len(u.eq.Bands) {
			t.Select(1, 0)
		}
	}
}

func gainColour(bd band) tcell.Color {
	switch {
	case bd.Kind == bandHighPass || bd.Kind == bandLowPass:
		return mocha.Overlay0
	case bd.Gain > 0:
		return mocha.Mauve
	case bd.Gain < 0:
		return mocha.Peach
	}
	return mocha.Subtext0
}

// closePage returns to the player.
func (u *UI) closePage() {
	for _, name := range []string{pageSettings, pageSound} {
		if u.pages.HasPage(name) {
			u.pages.RemovePage(name)
		}
	}
	u.pages.SwitchToPage(pageMain)
	u.focus(u.table)
}

func errMarkup(msg string) string {
	return fmt.Sprintf("[%s]%s", mocha.Red.String(), tview.Escape(msg))
}
