# SEHO tview Music Player Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace SEHO's bubbletea menu with a keyboard-driven tview music player — Catppuccin Mocha theme, mpv-backed transport, embedded album art, fuzzy search.

**Architecture:** Five files in `package main`. `player.go` talks to a long-lived `mpv --idle` process over a unix-socket JSON IPC channel and pushes state changes onto a channel. `ui.go` owns the tview widget tree and translates keys into player commands. `library.go` keeps the existing Redis index, gaining a `duration` field. `art.go` turns embedded cover art into truecolor half-block markup. `main.go` wires them together.

**Tech Stack:** Go 1.23+, tview/tcell, sahilm/fuzzy, dhowden/tag, go-redis v9, mpv (runtime), ffprobe (optional runtime).

**Spec:** None. Design was agreed in conversation on 2026-08-28 and is restated in full under "Design Reference" below — that section is the spec for this plan's purposes.

## Global Constraints

- Go 1.23.0 is the go.mod floor. Local toolchain is 1.26.7.
- **Never stage test files.** This repo's convention (user's CLAUDE.md) is that `*_test.go` and helper scripts stay untracked. Write tests, run tests, but every `git add` in this plan lists production files only.
- Never commit on `main`. Work on a branch off the latest `origin/main`.
- Direct dependency budget: exactly `tview`, `tcell/v2`, `sahilm/fuzzy`, `dhowden/tag`, `redis/go-redis/v9`. Adding anything else needs a decision, not an assumption.
- Keyboard only. Do not call `app.EnableMouse(true)`.
- mpv must be started with `--no-terminal`. Without it mpv writes to the shared TTY and shreds the tview render.
- All Mocha hex values are fixed and listed in the Design Reference. Do not invent shades.
- Redis is the only persistence. Album art is never stored in Redis.
- **Verified API facts** (probed 2026-08-28 against tview/tcell latest — do not re-derive):
  - `tcell.Color.String()` returns **uppercase** hex, e.g. `"#CBA6F7"`. tview's tag parser accepts either case, but string assertions in tests must be case-insensitive.
  - Colors built with `tcell.GetColor("#rrggbb")` stringify to hex; named colors like `tcell.ColorRed` stringify to `"red"`. Only use `GetColor` hex values in markup.
  - `tview.NewTableCell` preserves style-tag markup in `.Text` and renders it, so highlighted cells need no extra flag.
  - `tview.Theme` field names used in `applyTheme` all exist.

---

## Design Reference

### Layout

```
╭────────────────────────────────────────────────────────────────────────────╮
│  SEHO                                                      1,284 tracks    │
╰────────────────────────────────────────────────────────────────────────────╯
╭─ LIBRARY ────╮╭─ TRACKS ───────────────────────╮╭─ NOW PLAYING ───────────╮
│ ▸ All Tracks ││  #  TITLE          ARTIST  TIME││                         │
│   Artists    ││  1  Thunderstruck  AC/DC  04:52││   ▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀  │
│   Albums     ││ ▌2  Back In Black  AC/DC  04:15││   ▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀  │
│   Tags       ││  3  Hells Bells    AC/DC  05:12││   ▀▀▀ 28×14 cells ▀▀▀▀▀  │
│   Recent     ││  4  Everlong       Foo Fi 04:11││   ▀▀▀ = 28×28 px ▀▀▀▀▀▀  │
│              ││  5  My Hero        Foo Fi 04:20││   ▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀  │
│              ││                                ││                         │
│              ││                                ││   Back In Black         │
│              ││                                ││   AC/DC · 1980 · rock   │
╰──────────────╯╰────────────────────────────────╯╰─────────────────────────╯
╭────────────────────────────────────────────────────────────────────────────╮
│  ▶  Back In Black · AC/DC                                    vol ▁▃▅▇ 70%  │
│  01:47 ━━━━━━━━━━━━━━●───────────────────────────────────────────── 04:15   │
╰────────────────────────────────────────────────────────────────────────────╯
  / search   space pause   ←→ seek   n next   p prev   s scan   q quit
```

### Catppuccin Mocha palette (exact, do not deviate)

| Role | Token | Hex |
|---|---|---|
| App background | base | `#1e1e2e` |
| Panel background | mantle | `#181825` |
| Unfocused border | surface1 | `#45475a` |
| Focused border, progress fill, search hits | mauve | `#cba6f7` |
| Selected row background | surface2 | `#585b70` |
| Primary text | text | `#cdd6f4` |
| Secondary text / metadata | subtext0 | `#a6adc8` |
| Column headers | overlay0 | `#6c7086` |
| Playing indicator | green | `#a6e3a1` |
| Paused indicator, errors | red | `#f38ba8` |
| Volume meter | peach | `#fab387` |
| Panel titles | lavender | `#b4befe` |

### Widget tree

```
root            Flex, FlexRow
├── header      TextView,  fixed 3 rows (bordered)
├── body        Flex, FlexColumn, proportion 1
│   ├── sidebar List,      fixed 16 cols
│   ├── table   Table,     proportion 1
│   └── card    TextView,  fixed 32 cols
├── transport   TextView,  fixed 4 rows (bordered)
└── footer      TextView,  fixed 1 row
```

### Responsive breakpoints (measured on total terminal width)

| Width | Visible |
|---|---|
| ≥ 110 | sidebar + table + card |
| 80–109 | sidebar + table |
| < 80 | table only |

### Keymap

| Key | Action |
|---|---|
| `tab` / `shift+tab` | cycle focus sidebar → table → sidebar |
| `↑ ↓ pgup pgdn g G` | move within focused list |
| `enter` | play selected track |
| `space` | toggle pause |
| `←` / `→` | seek −5s / +5s |
| `n` / `p` | next / previous row in the visible list |
| `-` / `=` | volume −5 / +5 |
| `/` | focus search input; `esc` clears and returns focus; `enter` returns focus to table |
| `s` | scan music directory |
| `q` / `ctrl+c` | quit |

### Redis schema

Existing hash at `music:<absolute path>` gains one field:

| Field | Type | Notes |
|---|---|---|
| `duration` | seconds, float as string | `0` when unknown; backfilled from mpv on first play |

### Assumptions

1. Duration comes from `ffprobe` at index time. Missing `ffprobe` means `--:--` in the table, backfilled from mpv's `duration` property on first play.
2. The visible (possibly filtered) table order is the queue. `n`/`p` and auto-advance walk it. No separate queue model.
3. Album art is read from the audio file on track change, never cached in Redis.
4. Tracks with no embedded art get a flat color block derived from an FNV hash of the album name.

---

## File Structure

| File | Responsibility | Status |
|---|---|---|
| `main.go` | Config from env, Redis ping, player startup, app run, teardown | Rewrite |
| `ui.go` | Mocha theme, widget tree, focus, keymap, search filtering, responsive layout | Create |
| `player.go` | mpv process + JSON IPC client + event channel | Rewrite |
| `library.go` | Directory walk, tag read, Redis index, track loading, duration | Modify |
| `art.go` | Cover extraction, decode, box downsample, half-block markup | Create |
| `library_test.go`, `player_test.go`, `art_test.go` | Tests — **never staged** | Create |

---

## Task 0: Install mpv and confirm the IPC contract

No code. This task de-risks every later task; do not skip it.

**Files:** none

**Interfaces:**
- Consumes: nothing
- Produces: a verified mpv IPC contract. Later tasks assume the exact JSON shapes confirmed here.

- [ ] **Step 1: Install mpv**

```bash
brew install mpv
mpv --version | head -1
```

Expected: a version line, `mpv v0.38` or newer.

- [ ] **Step 2: Start mpv in idle mode with an IPC socket**

```bash
mpv --idle=yes --no-video --no-terminal --input-ipc-server=/tmp/seho-probe.sock &
sleep 1
ls -l /tmp/seho-probe.sock
```

Expected: a socket file exists.

- [ ] **Step 3: Confirm the request/reply shape**

```bash
printf '{"command":["get_property","volume"],"request_id":7}\n' | nc -U /tmp/seho-probe.sock
```

Expected: one JSON line containing `"error":"success"`, `"request_id":7`, and a `"data"` number.
Record the exact shape — `player.go` parses it verbatim.

- [ ] **Step 4: Confirm property-change events push unprompted**

```bash
{ printf '{"command":["observe_property",1,"volume"],"request_id":8}\n'; sleep 1; \
  printf '{"command":["set_property","volume",55],"request_id":9}\n'; sleep 1; } | nc -U /tmp/seho-probe.sock
```

Expected: among the replies, an unsolicited line shaped
`{"event":"property-change","id":1,"name":"volume","data":55.0}`.

- [ ] **Step 5: Tear down**

```bash
printf '{"command":["quit"]}\n' | nc -U /tmp/seho-probe.sock
rm -f /tmp/seho-probe.sock
```

- [ ] **Step 6: Record findings**

If any observed JSON differs from the shapes above, STOP and report the difference before starting Task 1 — the parser in Task 1 is written against these exact shapes.

---

## Task 1: mpv IPC client replacing ffplay

**Files:**
- Rewrite: `player.go` (currently 39 lines wrapping `ffplay`)
- Modify: `main.go:154-171` (construct the player, fail fast when mpv is missing)
- Modify: `main.go:60-66,101-103` (menu actions call the new API)
- Test: `player_test.go` (**not staged**)

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces, all used by later tasks:
  - `type Event struct { Name string; Num float64; Flag bool }`
  - `func StartPlayer() (*Player, error)`
  - `func (p *Player) Events() <-chan Event`
  - `func (p *Player) Load(path string) error`
  - `func (p *Player) TogglePause() error`
  - `func (p *Player) Seek(delta float64) error`
  - `func (p *Player) SetVolume(v int) error`
  - `func (p *Player) Stop() error`
  - `func (p *Player) Close() error`
  - Observer IDs are fixed: `1` = `time-pos`, `2` = `duration`, `3` = `pause`, `4` = `volume`.
  - `Event.Name` carries the mpv property name, or the literal `"end-file"`.

- [ ] **Step 1: Write the failing test**

Create `player_test.go`. It stands up a fake mpv on a unix socket, so no real mpv is needed.

```go
package main

import (
	"bufio"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"
)

// fakeMPV accepts one connection, echoes a success reply for every request,
// and pushes one unsolicited property-change event.
func fakeMPV(t *testing.T) (sock string, got chan string) {
	t.Helper()
	sock = filepath.Join(t.TempDir(), "fake.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	got = make(chan string, 16)
	go func() {
		defer ln.Close()
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.Write([]byte(`{"event":"property-change","id":1,"name":"time-pos","data":12.5}` + "\n"))
		sc := bufio.NewScanner(conn)
		for sc.Scan() {
			line := sc.Text()
			got <- line
			var req struct {
				RequestID int `json:"request_id"`
			}
			json.Unmarshal([]byte(line), &req)
			reply, _ := json.Marshal(map[string]any{
				"error": "success", "data": nil, "request_id": req.RequestID,
			})
			conn.Write(append(reply, '\n'))
		}
	}()
	return sock, got
}

func TestPlayerLoadSendsLoadfile(t *testing.T) {
	sock, got := fakeMPV(t)
	p, err := dialPlayer(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	if err := p.Load("/music/song.flac"); err != nil {
		t.Fatal(err)
	}

	select {
	case line := <-got:
		var req struct{ Command []any }
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			t.Fatal(err)
		}
		if len(req.Command) != 3 || req.Command[0] != "loadfile" ||
			req.Command[1] != "/music/song.flac" || req.Command[2] != "replace" {
			t.Errorf("wrong command: %v", req.Command)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no command reached mpv")
	}
}

func TestPlayerForwardsPropertyChange(t *testing.T) {
	sock, _ := fakeMPV(t)
	p, err := dialPlayer(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	select {
	case ev := <-p.Events():
		if ev.Name != "time-pos" || ev.Num != 12.5 {
			t.Errorf("got %+v, want time-pos 12.5", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event forwarded")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test -run TestPlayer -v ./...`
Expected: FAIL — `undefined: dialPlayer`.

- [ ] **Step 3: Write `player.go`**

Note the split: `dialPlayer` attaches to an existing socket (testable), `StartPlayer` spawns mpv and then calls it.

```go
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// Event is one state change pushed from mpv toward the UI.
// Name is an mpv property name, or the literal "end-file".
type Event struct {
	Name string
	Num  float64
	Flag bool
}

// observed maps our fixed observer IDs to mpv property names.
var observed = map[int]string{1: "time-pos", 2: "duration", 3: "pause", 4: "volume"}

type Player struct {
	cmd  *exec.Cmd
	conn net.Conn
	sock string

	mu      sync.Mutex
	nextID  int
	pending map[int]chan json.RawMessage

	events chan Event
	closed chan struct{}
	once   sync.Once
}

// StartPlayer spawns mpv in idle mode and attaches to its IPC socket.
func StartPlayer() (*Player, error) {
	if _, err := exec.LookPath("mpv"); err != nil {
		return nil, errors.New("mpv not found on PATH - install it with: brew install mpv")
	}
	sock := filepath.Join(os.TempDir(), fmt.Sprintf("seho-%d.sock", os.Getpid()))
	os.Remove(sock)

	// --no-terminal is mandatory: without it mpv writes to our TTY and corrupts the UI.
	cmd := exec.Command("mpv",
		"--idle=yes", "--no-video", "--no-terminal", "--really-quiet",
		"--input-ipc-server="+sock,
	)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start mpv: %w", err)
	}

	p, err := dialPlayer(sock)
	if err != nil {
		cmd.Process.Kill()
		return nil, err
	}
	p.cmd = cmd
	return p, nil
}

// dialPlayer attaches to an mpv IPC socket that already exists, retrying while
// mpv finishes creating it.
func dialPlayer(sock string) (*Player, error) {
	var conn net.Conn
	var err error
	for i := 0; i < 100; i++ {
		conn, err = net.Dial("unix", sock)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		return nil, fmt.Errorf("connect to mpv socket %s: %w", sock, err)
	}

	p := &Player{
		conn:    conn,
		sock:    sock,
		pending: map[int]chan json.RawMessage{},
		events:  make(chan Event, 64),
		closed:  make(chan struct{}),
	}
	go p.readLoop()

	for id, name := range observed {
		if err := p.send("observe_property", id, name); err != nil {
			p.Close()
			return nil, err
		}
	}
	return p, nil
}

func (p *Player) Events() <-chan Event { return p.events }

// readLoop demultiplexes mpv's output: replies go to the waiting caller,
// events go to the events channel.
func (p *Player) readLoop() {
	sc := bufio.NewScanner(p.conn)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var msg struct {
			Event     string          `json:"event"`
			Name      string          `json:"name"`
			Data      json.RawMessage `json:"data"`
			RequestID int             `json:"request_id"`
			Error     string          `json:"error"`
			Reason    string          `json:"reason"`
		}
		if json.Unmarshal(sc.Bytes(), &msg) != nil {
			continue
		}

		switch {
		case msg.Event == "property-change":
			p.emit(Event{Name: msg.Name, Num: asFloat(msg.Data), Flag: asBool(msg.Data)})
		case msg.Event == "end-file":
			p.emit(Event{Name: "end-file"})
		case msg.Event != "":
			// Other mpv events are not used by the UI.
		default:
			p.mu.Lock()
			ch, ok := p.pending[msg.RequestID]
			delete(p.pending, msg.RequestID)
			p.mu.Unlock()
			if ok {
				ch <- msg.Data
			}
		}
	}
	p.emit(Event{Name: "disconnected"})
}

func (p *Player) emit(ev Event) {
	select {
	case p.events <- ev:
	case <-p.closed:
	default: // ponytail: drop rather than block the reader; time-pos arrives ~10x/sec
	}
}

// send writes one command and waits for its reply.
func (p *Player) send(args ...any) error {
	p.mu.Lock()
	p.nextID++
	id := p.nextID
	ch := make(chan json.RawMessage, 1)
	p.pending[id] = ch
	p.mu.Unlock()

	payload, err := json.Marshal(map[string]any{"command": args, "request_id": id})
	if err != nil {
		return err
	}
	if _, err := p.conn.Write(append(payload, '\n')); err != nil {
		return err
	}

	select {
	case <-ch:
		return nil
	case <-time.After(2 * time.Second):
		p.mu.Lock()
		delete(p.pending, id)
		p.mu.Unlock()
		return fmt.Errorf("mpv did not answer %v", args[0])
	}
}

func (p *Player) Load(path string) error      { return p.send("loadfile", path, "replace") }
func (p *Player) TogglePause() error          { return p.send("cycle", "pause") }
func (p *Player) Seek(delta float64) error    { return p.send("seek", delta, "relative") }
func (p *Player) SetVolume(v int) error       { return p.send("set_property", "volume", clampVol(v)) }
func (p *Player) Stop() error                 { return p.send("stop") }

func (p *Player) Close() error {
	p.once.Do(func() {
		close(p.closed)
		if p.conn != nil {
			p.send("quit")
			p.conn.Close()
		}
		if p.cmd != nil && p.cmd.Process != nil {
			p.cmd.Process.Kill()
			p.cmd.Wait()
		}
		os.Remove(p.sock)
	})
	return nil
}

func clampVol(v int) int {
	return max(0, min(130, v)) // mpv accepts up to 130% by default
}

func asFloat(raw json.RawMessage) float64 {
	var f float64
	json.Unmarshal(raw, &f)
	return f
}

func asBool(raw json.RawMessage) bool {
	var b bool
	json.Unmarshal(raw, &b)
	return b
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -run TestPlayer -v ./...`
Expected: both tests PASS.

- [ ] **Step 5: Rewire main.go to the new player**

In `main.go`, replace the `player` construction and the two call sites:

```go
	pl, err := StartPlayer()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer pl.Close()
```

`m := model{list: l, rdb: rdb, dir: dir, player: pl}`, change the `model.player` field type to `*Player`, and in the existing bubbletea switch replace `m.player.play(sel.path)` with `pl.Load(sel.path)`, `m.player.stop()` with `pl.Stop()`.

- [ ] **Step 6: Verify the app still runs end to end**

Run: `go build ./... && go vet ./...`
Then run `go run .`, select "Browse Library", press enter on a track.
Expected: audio plays through mpv, the old bubbletea UI is otherwise unchanged.

- [ ] **Step 7: Commit — production files only**

```bash
git add player.go main.go
git commit -m "feat: play through mpv over JSON IPC instead of ffplay"
```

---

## Task 2: Track duration in the index

**Files:**
- Modify: `library.go` (add `probeDuration`, `fmtDuration`, `duration` field in the HSet)
- Test: `library_test.go` (**not staged**)

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `func probeDuration(path string) float64` — seconds, `0` when unknown
  - `func fmtDuration(sec float64) string` — `"04:15"`, or `"--:--"` when `sec <= 0`
  - Redis field `duration` on every `music:*` hash
  - `item` gains a `duration float64` field, populated by `listTracks`

- [ ] **Step 1: Write the failing test**

Append to `library_test.go`:

```go
func TestFmtDuration(t *testing.T) {
	for in, want := range map[float64]string{
		0: "--:--", -1: "--:--", 5: "00:05", 65: "01:05",
		255: "04:15", 3599: "59:59", 3600: "60:00",
	} {
		if got := fmtDuration(in); got != want {
			t.Errorf("fmtDuration(%v) = %q, want %q", in, got, want)
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test -run TestFmtDuration -v ./...`
Expected: FAIL — `undefined: fmtDuration`.

- [ ] **Step 3: Implement in `library.go`**

```go
// probeDuration returns the track length in seconds, or 0 if ffprobe is
// unavailable or cannot read the file. A zero here is backfilled from mpv
// the first time the track plays.
func probeDuration(path string) float64 {
	out, err := exec.Command("ffprobe", "-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1", path).Output()
	if err != nil {
		return 0
	}
	sec, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		return 0
	}
	return sec
}

func fmtDuration(sec float64) string {
	if sec <= 0 {
		return "--:--"
	}
	total := int(sec + 0.5)
	return fmt.Sprintf("%02d:%02d", total/60, total%60)
}
```

Add `"os/exec"` and `"strconv"` to the imports.

In `indexFile`, add to the `HSet` map:

```go
		"duration":    probeDuration(path),
```

In `listTracks`, parse it onto the item:

```go
		dur, _ := strconv.ParseFloat(h["duration"], 64)
		items = append(items, item{
			title:    cmp.Or(h["title"], filepath.Base(h["path"])),
			desc:     cmp.Or(h["artist"], "Unknown artist"),
			path:     h["path"],
			duration: dur,
		})
```

In `main.go`, add `duration float64` to the `item` struct.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 5: Add the duration backfill helper in `library.go`**

```go
// backfillDuration records a duration mpv discovered, for tracks indexed
// without ffprobe. Best effort: a failure here costs only the TIME column.
func backfillDuration(ctx context.Context, rdb *redis.Client, path string, sec float64) {
	if sec <= 0 {
		return
	}
	if err := rdb.HSet(ctx, keyPrefix+path, "duration", sec).Err(); err != nil {
		log.Printf("backfill duration for %s: %v", path, err)
	}
}
```

- [ ] **Step 6: Verify against a real scan**

Run `go run .`, choose "Scan Directory" on a directory whose tracks are not yet indexed, then:

```bash
redis-cli --scan --pattern 'music:*' | head -1 | xargs -I{} redis-cli hget {} duration
```

Expected: a number of seconds, not empty.

- [ ] **Step 7: Commit — production files only**

```bash
git add library.go main.go
git commit -m "feat: index track duration via ffprobe, backfill from mpv"
```

---

## Task 3: tview shell — theme, layout, sidebar, table

This task deletes bubbletea. It must leave a working app: browse and play, no transport bar yet.

**Files:**
- Create: `ui.go`
- Rewrite: `main.go`
- Modify: `go.mod` (add tview/tcell, drop bubbletea/bubbles/lipgloss)

**Interfaces:**
- Consumes: `StartPlayer`, `Load`, `Stop` (Task 1); `listTracks`, `scanDirectory`, `fmtDuration` (Task 2).
- Produces:
  - `type UI struct { ... }` holding `app`, `header`, `sidebar`, `table`, `card`, `transport`, `footer`, `search`, `body`, `root`, plus `all []item`, `shown []item`, `playing int`
  - `func NewUI(rdb *redis.Client, dir string, pl *Player) *UI`
  - `func (u *UI) Run() error`
  - `func (u *UI) setTracks(items []item)` — replaces `shown` and repaints the table
  - `func (u *UI) selectedTrack() (item, bool)`
  - `func (u *UI) playRow(row int)`
  - `var mocha` — the palette struct, field names exactly: `Base, Mantle, Surface1, Surface2, Overlay0, Text, Subtext0, Mauve, Green, Red, Peach, Lavender`

- [ ] **Step 1: Swap the dependencies**

```bash
go get github.com/rivo/tview@latest github.com/gdamore/tcell/v2@latest
```

- [ ] **Step 2: Write `ui.go` — palette and theme**

```go
package main

import (
	"context"
	"fmt"

	"github.com/gdamore/tcell/v2"
	"github.com/redis/go-redis/v9"
	"github.com/rivo/tview"
)

// Catppuccin Mocha. Do not add shades that are not in this struct.
var mocha = struct {
	Base, Mantle, Surface1, Surface2, Overlay0 tcell.Color
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
```

- [ ] **Step 3: Write `ui.go` — the widget tree**

```go
var sidebarSections = []string{"All Tracks", "Artists", "Albums", "Tags", "Recent"}

type UI struct {
	app  *tview.Application
	rdb  *redis.Client
	dir  string
	pl   *Player

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
```

- [ ] **Step 4: Write `ui.go` — table painting and playback**

```go
func (u *UI) setTracks(items []item) {
	u.shown = items
	u.table.Clear()

	for c, h := range []string{"#", "TITLE", "ARTIST", "TIME"} {
		u.table.SetCell(0, c, tview.NewTableCell(h).
			SetTextColor(mocha.Overlay0).
			SetSelectable(false).
			SetExpansion(map[int]int{0: 0, 1: 3, 2: 2, 3: 0}[c]))
	}

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
	if err := u.pl.Load(u.shown[row].path); err != nil {
		u.setStatus(fmt.Sprintf("[%s]playback failed: %v", mocha.Red.String(), err))
		return
	}
	u.table.Select(row+1, 0)
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

func (u *UI) Run() error {
	u.reload()
	u.setFooter()
	u.bindKeys()
	return u.app.Run()
}
```

- [ ] **Step 5: Write `ui.go` — keys and footer (transport keys land in Task 4)**

```go
func (u *UI) setFooter() {
	u.footer.SetText(fmt.Sprintf("  [%s]/[-] search  [%s]enter[-] play  [%s]tab[-] pane  [%s]s[-] scan  [%s]q[-] quit",
		mocha.Mauve.String(), mocha.Mauve.String(), mocha.Mauve.String(),
		mocha.Mauve.String(), mocha.Mauve.String()))
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
		}
		switch ev.Rune() {
		case 'q':
			u.app.Stop()
			return nil
		case 's':
			go u.scan()
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
```

- [ ] **Step 6: Rewrite `main.go`**

Everything bubbletea goes. `item` keeps its fields — `listTracks` in `library.go` still returns `[]item`, so change its signature to `[]item` rather than `[]list.Item` and drop the bubbles import from `library.go`.

```go
package main

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/redis/go-redis/v9"
)

type item struct {
	title, desc, path string
	duration          float64
}

func setupLog() func() {
	if err := os.MkdirAll("logs", 0o755); err == nil {
		f, err := os.OpenFile(filepath.Join("logs", "seho.log"),
			os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err == nil {
			log.SetOutput(f)
			return func() { f.Close() }
		}
	}
	log.SetOutput(io.Discard)
	return func() {}
}

func defaultMusicDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(home, "Music")
}

func main() {
	closeLog := setupLog()
	defer closeLog()

	dir := cmp.Or(os.Getenv("MUSIC_DIR"), defaultMusicDir())
	rdb := redis.NewClient(&redis.Options{
		Addr: cmp.Or(os.Getenv("REDIS_ADDR"), "localhost:6379"),
	})
	defer rdb.Close()
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		fmt.Fprintf(os.Stderr, "redis unreachable: %v\n", err)
		os.Exit(1)
	}

	pl, err := StartPlayer()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer pl.Close()

	if err := NewUI(rdb, dir, pl).Run(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 7: Drop the dead dependencies**

```bash
go mod tidy
go build ./... && go vet ./... && gofmt -l .
```

Expected: builds clean, `gofmt -l` prints nothing, and `go.mod` no longer lists bubbletea, bubbles or lipgloss as direct requirements.

- [ ] **Step 8: Verify visually**

Run `go run .`. Confirm: Mocha colors, bordered header/sidebar/table/transport, `tab` moves focus, `enter` plays, `s` scans, `q` quits.

- [ ] **Step 9: Commit — production files only**

```bash
git add ui.go main.go library.go go.mod go.sum
git commit -m "feat: rebuild UI on tview with Catppuccin Mocha theme"
```

---

## Task 4: Transport bar bound to mpv events

**Files:**
- Modify: `ui.go` (event pump, transport rendering, transport keys)

**Interfaces:**
- Consumes: `Player.Events()`, `TogglePause`, `Seek`, `SetVolume` (Task 1); `backfillDuration` (Task 2).
- Produces:
  - `UI` gains `pos, dur float64`, `paused bool`, `vol int`, `nowTitle, nowArtist string`
  - `func (u *UI) pumpEvents()` — goroutine started by `Run`
  - `func (u *UI) drawTransport()`
  - `func progressBar(frac float64, width int) string`

- [ ] **Step 1: Write the failing test**

Create `ui_test.go`:

```go
package main

import (
	"strings"
	"testing"
)

func TestProgressBar(t *testing.T) {
	const w = 10
	cases := []struct{ frac float64; wantFill int }{
		{0, 0}, {0.5, 5}, {1, 10}, {-1, 0}, {2, 10},
	}
	for _, c := range cases {
		got := progressBar(c.frac, w)
		// The knob replaces the last filled cell, so count both runes.
		fill := strings.Count(got, "━") + strings.Count(got, "●")
		if fill != c.wantFill {
			t.Errorf("progressBar(%v,%d): %d filled cells, want %d", c.frac, w, fill, c.wantFill)
		}
		if n := len([]rune(stripTags(got))); n != w {
			t.Errorf("progressBar(%v,%d): %d runes, want %d", c.frac, w, n, w)
		}
	}
}
```

Add the helper the test needs, in `ui_test.go` (test-only, never staged):

```go
// stripTags removes tview color markup so rune counts can be checked.
func stripTags(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch {
		case r == '[':
			depth++
		case r == ']' && depth > 0:
			depth--
		case depth == 0:
			b.WriteRune(r)
		}
	}
	return b.String()
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test -run TestProgressBar -v ./...`
Expected: FAIL — `undefined: progressBar`.

- [ ] **Step 3: Implement the transport in `ui.go`**

```go
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
```

Add `"strings"` to the `ui.go` imports.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 5: Pump mpv events into the UI**

```go
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
			case "disconnected":
				u.setStatus(fmt.Sprintf("[%s]lost connection to mpv", mocha.Red.String()))
				return
			}
			u.drawTransport()
		})
	}
}
```

In `playRow`, record what is playing before loading:

```go
	u.nowTitle, u.nowArtist = u.shown[row].title, u.shown[row].desc
	u.pos, u.dur = 0, u.shown[row].duration
```

In `Run`, start the pump and paint once:

```go
	u.vol = 100
	go u.pumpEvents()
	u.drawTransport()
```

- [ ] **Step 6: Add the transport keys**

Inside `bindKeys`'s `SetInputCapture`, extend the `ev.Key()` switch:

```go
		case tcell.KeyLeft:
			u.pl.Seek(-5)
			return nil
		case tcell.KeyRight:
			u.pl.Seek(5)
			return nil
```

and the `ev.Rune()` switch:

```go
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
			u.vol -= 5
			u.pl.SetVolume(u.vol)
			return nil
		case '=':
			u.vol += 5
			u.pl.SetVolume(u.vol)
			return nil
```

Note: `←`/`→` are captured globally, so they no longer reach the table. The table is row-selectable only, so nothing is lost.

Update `setFooter` to list the new keys:

```go
func (u *UI) setFooter() {
	m := mocha.Mauve.String()
	u.footer.SetText(fmt.Sprintf(
		"  [%s]/[-] search  [%s]space[-] pause  [%s]←→[-] seek  [%s]n/p[-] track  [%s]-/=[-] vol  [%s]s[-] scan  [%s]q[-] quit",
		m, m, m, m, m, m, m))
}
```

- [ ] **Step 7: Verify by hand**

Run `go run .`, play a track. Confirm: the clock advances, the bar fills, `space` flips ▶/⏸ and freezes the clock, `←`/`→` jump 5s, `-`/`=` move the volume meter.

- [ ] **Step 8: Commit — production files only**

```bash
git add ui.go
git commit -m "feat: transport bar driven by mpv property events"
```

---

## Task 5: Fuzzy search with hit highlighting

**Files:**
- Modify: `ui.go` (search field, filter, highlight)
- Modify: `go.mod` (add sahilm/fuzzy)

**Interfaces:**
- Consumes: `UI.all`, `UI.setTracks` (Task 3).
- Produces:
  - `func (u *UI) applyFilter(query string)`
  - `func highlight(s string, idx []int) string` — wraps matched runes in mauve markup
  - `type trackSource []item` implementing `fuzzy.Source`

- [ ] **Step 1: Add the dependency**

```bash
go get github.com/sahilm/fuzzy@latest
```

- [ ] **Step 2: Write the failing test**

Append to `ui_test.go`:

```go
func TestHighlight(t *testing.T) {
	got := highlight("Back In Black", []int{0, 1, 2, 3})
	if stripTags(got) != "Back In Black" {
		t.Errorf("highlight changed the text: %q", stripTags(got))
	}
	// tcell.Color.String() emits uppercase hex.
	if !strings.Contains(strings.ToUpper(got), "#CBA6F7") {
		t.Error("matched runes are not mauve")
	}
	if plain := highlight("Everlong", nil); plain != "Everlong" {
		t.Errorf("no matches should mean no markup, got %q", plain)
	}
}

func TestHighlightIsRuneSafe(t *testing.T) {
	// Index 0 must highlight the first rune, not the first byte.
	got := highlight("Étude", []int{0})
	if stripTags(got) != "Étude" {
		t.Errorf("multibyte text mangled: %q", stripTags(got))
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test -run TestHighlight -v ./...`
Expected: FAIL — `undefined: highlight`.

- [ ] **Step 4: Implement search in `ui.go`**

```go
// trackSource lets fuzzy match against title, artist and album at once.
type trackSource []item

func (t trackSource) Len() int            { return len(t) }
func (t trackSource) String(i int) string { return t[i].title + " " + t[i].desc }

// highlight wraps the runes at idx in mauve tview markup.
func highlight(s string, idx []int) string {
	if len(idx) == 0 {
		return s
	}
	hit := make(map[int]bool, len(idx))
	for _, i := range idx {
		hit[i] = true
	}
	var b strings.Builder
	on := false
	for i, r := range []rune(s) {
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

	for c, h := range []string{"#", "TITLE", "ARTIST", "TIME"} {
		u.table.SetCell(0, c, tview.NewTableCell(h).
			SetTextColor(mocha.Overlay0).
			SetSelectable(false).
			SetExpansion(map[int]int{0: 0, 1: 3, 2: 2, 3: 0}[c]))
	}

	for row, m := range matches {
		it := u.all[m.Index]
		u.shown = append(u.shown, it)

		// MatchedIndexes are positions in title+" "+artist; split at the title boundary.
		var titleHits, artistHits []int
		cut := len([]rune(it.title))
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
}
```

Table cells need markup enabled — in `cell`, the text is passed through tview's dynamic-colors parser only when the cell is created from markup, which `tview.NewTableCell` does by default. No change needed.

Add `"github.com/sahilm/fuzzy"` to the imports.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 6: Wire the search field into the layout**

The search input replaces the footer row while active.

```go
func (u *UI) openSearch() {
	u.root.RemoveItem(u.footer)
	u.root.AddItem(u.search, 1, 0, true)
	u.search.SetText("")
	u.app.SetFocus(u.search)
}

func (u *UI) closeSearch(clear bool) {
	u.root.RemoveItem(u.search)
	u.root.AddItem(u.footer, 1, 0, false)
	if clear {
		u.applyFilter("")
	}
	u.app.SetFocus(u.table)
}
```

In `NewUI`, after building `u.search`:

```go
	u.search.SetChangedFunc(func(q string) { u.applyFilter(q) })
	u.search.SetDoneFunc(func(key tcell.Key) {
		u.closeSearch(key == tcell.KeyEscape)
	})
```

In `bindKeys`'s `ev.Rune()` switch:

```go
		case '/':
			u.openSearch()
			return nil
```

And the escape branch, before the rune switch:

```go
		if ev.Key() == tcell.KeyEscape && u.app.GetFocus() == u.search {
			u.closeSearch(true)
			return nil
		}
```

- [ ] **Step 7: Verify by hand**

Run `go run .`, press `/`, type `bib`. Confirm: `Back In Black` ranks first, matched letters are mauve, `enter` returns focus to the table with the filter held, `esc` clears it.

- [ ] **Step 8: Commit — production files only**

```bash
git add ui.go go.mod go.sum
git commit -m "feat: fuzzy track search with match highlighting"
```

---

## Task 6: Album art and the Now Playing card

**Files:**
- Create: `art.go`
- Modify: `ui.go` (render the card on track change)
- Test: `art_test.go` (**not staged**)

**Interfaces:**
- Consumes: `UI.playRow` (Task 3), `mocha` (Task 3).
- Produces:
  - `func AlbumArt(path string, w, h int) string` — tview markup, `h` lines of `w` cells; falls back to a hashed color block
  - `func halfBlocks(img image.Image, w, h int) string`
  - `func boxAverage(img image.Image, gx, gy, gw, gh int) color.RGBA`
  - `func (u *UI) drawCard(it item)`
  - Card art is **28 cells wide × 14 rows** = 28×28 pixels.

- [ ] **Step 1: Write the failing test**

Create `art_test.go`:

```go
package main

import (
	"image"
	"image/color"
	"strings"
	"testing"
)

// solid builds a 4x4 image split top-red / bottom-blue.
func solid() image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		c := color.RGBA{255, 0, 0, 255}
		if y >= 2 {
			c = color.RGBA{0, 0, 255, 255}
		}
		for x := 0; x < 4; x++ {
			img.Set(x, y, c)
		}
	}
	return img
}

func TestBoxAverageSamplesTheRightRegion(t *testing.T) {
	img := solid()
	// A 1x2 grid: cell (0,0) is the red half, cell (0,1) is the blue half.
	if got := boxAverage(img, 0, 0, 1, 2); got.R != 255 || got.B != 0 {
		t.Errorf("top cell = %+v, want red", got)
	}
	if got := boxAverage(img, 0, 1, 1, 2); got.B != 255 || got.R != 0 {
		t.Errorf("bottom cell = %+v, want blue", got)
	}
}

func TestHalfBlocksShape(t *testing.T) {
	out := halfBlocks(solid(), 2, 1) // 2 cells wide, 1 row => 2x2 pixels
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1", len(lines))
	}
	if n := strings.Count(out, "▀"); n != 2 {
		t.Errorf("got %d block runes, want 2", n)
	}
	// One row of cells covers pixel rows 0 and 1, both red here.
	if !strings.Contains(out, "#ff0000:#ff0000") {
		t.Errorf("expected a red-on-red cell, got %q", out)
	}
}

func TestAlbumArtFallsBackWithoutFile(t *testing.T) {
	out := AlbumArt("/nonexistent/track.mp3", 4, 2)
	if strings.Count(out, "▀") != 8 {
		t.Errorf("fallback should still fill 4x2 cells, got %q", out)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test -run 'TestBoxAverage|TestHalfBlocks|TestAlbumArt' -v ./...`
Expected: FAIL — `undefined: boxAverage`.

- [ ] **Step 3: Write `art.go`**

```go
package main

import (
	"bytes"
	"fmt"
	"hash/fnv"
	"image"
	"image/color"
	_ "image/jpeg" // embedded cover art is almost always JPEG
	_ "image/png"
	"os"
	"strings"

	"github.com/dhowden/tag"
)

// AlbumArt renders a track's embedded cover as tview markup: h lines of w
// cells, each cell two stacked pixels. Falls back to a flat block keyed off
// the file path when there is no usable picture.
func AlbumArt(path string, w, h int) string {
	img, err := coverImage(path)
	if err != nil {
		return fallbackBlock(path, w, h)
	}
	return halfBlocks(img, w, h)
}

func coverImage(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	md, err := tag.ReadFrom(f)
	if err != nil {
		return nil, err
	}
	pic := md.Picture()
	if pic == nil || len(pic.Data) == 0 {
		return nil, fmt.Errorf("no embedded picture in %s", path)
	}
	img, _, err := image.Decode(bytes.NewReader(pic.Data))
	return img, err
}

// halfBlocks maps the image onto a w×h grid of cells. Each cell is the
// upper-half-block rune with foreground = upper pixel, background = lower.
func halfBlocks(img image.Image, w, h int) string {
	var b strings.Builder
	b.Grow(w * h * 24)
	for cy := 0; cy < h; cy++ {
		for cx := 0; cx < w; cx++ {
			top := boxAverage(img, cx, cy*2, w, h*2)
			bot := boxAverage(img, cx, cy*2+1, w, h*2)
			fmt.Fprintf(&b, "[#%02x%02x%02x:#%02x%02x%02x]▀",
				top.R, top.G, top.B, bot.R, bot.G, bot.B)
		}
		b.WriteString("[-:-]\n")
	}
	return b.String()
}

// boxAverage averages every source pixel falling inside cell (gx,gy) of a
// gw×gh grid. Averaging rather than sampling is what keeps a 500px cover
// legible at 28px.
func boxAverage(img image.Image, gx, gy, gw, gh int) color.RGBA {
	bd := img.Bounds()
	x0 := bd.Min.X + gx*bd.Dx()/gw
	x1 := bd.Min.X + (gx+1)*bd.Dx()/gw
	y0 := bd.Min.Y + gy*bd.Dy()/gh
	y1 := bd.Min.Y + (gy+1)*bd.Dy()/gh
	if x1 <= x0 {
		x1 = x0 + 1
	}
	if y1 <= y0 {
		y1 = y0 + 1
	}

	var r, g, bl, n uint64
	for y := y0; y < y1 && y < bd.Max.Y; y++ {
		for x := x0; x < x1 && x < bd.Max.X; x++ {
			cr, cg, cb, _ := img.At(x, y).RGBA()
			r += uint64(cr >> 8)
			g += uint64(cg >> 8)
			bl += uint64(cb >> 8)
			n++
		}
	}
	if n == 0 {
		return color.RGBA{0, 0, 0, 255}
	}
	return color.RGBA{uint8(r / n), uint8(g / n), uint8(bl / n), 255}
}

// fallbackBlock derives a stable muted color from the path so tracks without
// art still get a distinct, non-jarring tile.
func fallbackBlock(seed string, w, h int) string {
	sum := fnv.New32a()
	sum.Write([]byte(seed))
	v := sum.Sum32()
	// Bias toward Mocha's darker surfaces rather than full-saturation noise.
	c := color.RGBA{
		R: uint8(0x30 + v&0x3f),
		G: uint8(0x30 + (v>>8)&0x3f),
		B: uint8(0x40 + (v>>16)&0x3f),
		A: 255,
	}
	cell := fmt.Sprintf("[#%02x%02x%02x:#%02x%02x%02x]▀", c.R, c.G, c.B, c.R, c.G, c.B)
	var b strings.Builder
	for y := 0; y < h; y++ {
		b.WriteString(strings.Repeat(cell, w))
		b.WriteString("[-:-]\n")
	}
	return b.String()
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 5: Render the card in `ui.go`**

```go
const (
	artCells = 28 // cells wide
	artRows  = 14 // cell rows => 28 pixels tall
)

func (u *UI) drawCard(it item) {
	if it.path == "" {
		u.card.SetText("")
		return
	}
	art := AlbumArt(it.path, artCells, artRows)
	u.card.SetText(fmt.Sprintf("\n%s\n  [%s::b]%s[-::-]\n  [%s]%s",
		art, mocha.Text.String(), it.title, mocha.Subtext0.String(), it.desc))
}
```

Call it at the end of `playRow`:

```go
	u.drawCard(u.shown[row])
```

Widen the card to fit 28 cells plus borders and padding — in `NewUI`, change the card's fixed width:

```go
		AddItem(u.card, 32, 0, false)
```

(32 = 28 art cells + 2 borders + 2 padding. Already correct from Task 3; confirm it was not changed.)

- [ ] **Step 6: Verify by hand**

Run `go run .` in a terminal at least 110 columns wide with `COLORTERM=truecolor`. Play a track with embedded art.
Expected: a recognizable 28×28 cover in the card, title and artist beneath. Play a track with no art: a muted solid tile instead.

- [ ] **Step 7: Commit — production files only**

```bash
git add art.go ui.go
git commit -m "feat: render embedded album art as truecolor half-blocks"
```

---

## Task 7: Auto-advance and sidebar filters

**Files:**
- Modify: `ui.go`

**Interfaces:**
- Consumes: `UI.playRow`, `UI.all`, `UI.shown`, `pumpEvents` (Tasks 3–4).
- Produces:
  - `func (u *UI) advance()` — plays the next row, stops at the end of the list
  - `func (u *UI) filterBySection(section string)`

- [ ] **Step 1: Auto-advance on end-file**

`end-file` fires on natural end AND on `stop`/`loadfile`, so guard against advancing when a track was replaced deliberately. Track intent with a flag.

Add to `UI`: `userStopped bool`.

In `playRow`, before `Load`: `u.userStopped = false`.
Add a `stop` path used by `q` and any explicit stop: `u.userStopped = true`.

In `pumpEvents`, replace the `end-file` case:

```go
			case "end-file":
				u.pos = 0
				if !u.userStopped {
					u.advance()
				}
```

```go
// advance plays the next row, or parks at the end of the list.
func (u *UI) advance() {
	next := u.playing + 1
	if next >= len(u.shown) {
		u.nowTitle, u.nowArtist = "", ""
		u.playing = -1
		u.drawCard(item{})
		return
	}
	u.playRow(next)
}
```

- [ ] **Step 2: Implement sidebar filtering**

```go
// filterBySection narrows the table. Artists/Albums/Tags collapse the library
// to one row per distinct value; picking one is a second filter step handled
// by the search field, so there is no nested navigation state.
func (u *UI) filterBySection(section string) {
	switch section {
	case "All Tracks":
		u.setTracks(u.all)
	case "Recent":
		// listTracks already sorts by key; Recent shows the newest 50 by added_at,
		// which listTracks exposes in item order, so take the tail.
		n := len(u.all)
		u.setTracks(u.all[max(0, n-50):])
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
		out = append(out, item{title: k, desc: section, path: it.path, duration: it.duration})
	}
	return out
}
```

`item` needs two more fields for this. In `main.go`:

```go
type item struct {
	title, desc, album, tags, path string
	duration                       float64
}
```

And in `library.go`'s `listTracks`, populate them:

```go
			album: h["album"],
			tags:  h["tags"],
```

Note: `tags` is no longer written by `indexFile` (Last.fm was removed), so the Tags section is empty until something populates it. Leave the section in place; it costs one line and lights up if tagging returns.

- [ ] **Step 3: Wire the sidebar selection**

In `NewUI`, replace the plain `AddItem` loop:

```go
	for _, s := range sidebarSections {
		s := s
		u.sidebar.AddItem(s, "", 0, func() {
			u.filterBySection(s)
			u.app.SetFocus(u.table)
		})
	}
```

- [ ] **Step 4: Verify by hand**

Run `go run .`. Let a short track play to its end — the next row should start automatically. Press `tab` to the sidebar, pick "Artists" — the table should show one row per artist. Press `q` mid-track — no advance, clean exit.

- [ ] **Step 5: Commit — production files only**

```bash
git add ui.go main.go library.go
git commit -m "feat: auto-advance on track end, sidebar section filters"
```

---

## Task 8: Responsive breakpoints and contextual footer

**Files:**
- Modify: `ui.go`

**Interfaces:**
- Consumes: `UI.body`, `UI.sidebar`, `UI.card`, `UI.footer` (Tasks 3–6).
- Produces:
  - `func (u *UI) relayout(width int)`
  - `UI` gains `layoutWidth int` so relayout is idempotent per width

- [ ] **Step 1: Implement relayout**

```go
// relayout hides columns that no longer fit. tview has no breakpoints, so this
// rebuilds the body Flex on width change.
// ponytail: rebuild rather than resize - three states, cheap, and it avoids
// tracking per-item proportions.
func (u *UI) relayout(width int) {
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

	// Focus may have been on a pane that is now hidden.
	if f := u.app.GetFocus(); f == u.sidebar && width < 80 {
		u.app.SetFocus(u.table)
	}
}
```

Add `layoutWidth int` to `UI`.

- [ ] **Step 2: Hook it to the draw cycle**

In `NewUI`, before `SetRoot`:

```go
	u.app.SetBeforeDrawFunc(func(screen tcell.Screen) bool {
		w, _ := screen.Size()
		u.relayout(w)
		return false // false = let tview draw normally
	})
```

- [ ] **Step 3: Make the footer contextual**

```go
func (u *UI) setFooter() {
	m := mocha.Mauve.String()
	keys := []string{"/[-] search", "space[-] pause", "←→[-] seek", "n/p[-] track"}
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
```

Call `u.setFooter()` at the end of `relayout` so it tracks the width.

- [ ] **Step 4: Verify by hand**

Run `go run .` and resize the terminal.
Expected: below ~110 cols the Now Playing card disappears; below ~80 the sidebar goes too; the table never gets a horizontal scrollbar and the footer drops `tab` when the sidebar is hidden.

- [ ] **Step 5: Full verification sweep**

```bash
gofmt -l .
go vet ./...
go test ./...
go build ./...
```

Expected: `gofmt -l` silent, vet silent, tests PASS, build clean.

- [ ] **Step 6: Update the README**

Replace the Requirements and Usage sections: `mpv` replaces `ffplay` as the playback requirement, `ffprobe` is listed as optional (duration), and the keymap table from the Design Reference replaces the current four-row table. Add a line noting truecolor is needed for album art.

- [ ] **Step 7: Commit — production files only**

```bash
git add ui.go README.md
git commit -m "feat: responsive breakpoints and contextual key hints"
```

---

## Self-Review Notes

Checked against the Design Reference:

| Design item | Task |
|---|---|
| mpv IPC playback | 0, 1 |
| Catppuccin Mocha palette | 3 |
| Header / sidebar / table / card / transport / footer | 3, 4, 6, 8 |
| Album art, 28×28 half-blocks | 6 |
| Fuzzy search + mauve highlighting | 5 |
| Keyboard-only, contextual footer | 3, 4, 8 |
| Transport: elapsed, duration, pause, seek, volume | 4 |
| Duration via ffprobe + mpv backfill | 2, 4 |
| Visible list is the queue; auto-advance | 7 |
| Responsive breakpoints | 8 |
| Redis `duration` field | 2 |
| No art in Redis | 6 |
| Errors: Redis, mpv missing, socket drop, load failure, no art | 1, 3, 4, 6 |

Known gaps accepted by design:
- The "Tags" sidebar section is inert until something writes the `tags` field again (Last.fm was removed on 2026-08-28). Task 7 Step 2 notes this.
- No volume persistence across restarts. mpv defaults to 100 each launch.
