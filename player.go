package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Backend is one audio source SEHO can drive. Two implementations exist:
// *Player (mpv, below) and *SpotifyBackend (librespot piped into a second mpv,
// see spotify_player.go). Load takes a file path for the former and a
// spotify: URI for the latter.
type Backend interface {
	Load(target string) error
	TogglePause() error
	Seek(delta float64) error
	SetVolume(v int) error
	SetAF(chain string) error
	Stop() error

	// Level reports the loudness of the audio actually leaving the backend, in
	// dBFS, and false when no reading is available (nothing decoding).
	Level() (float64, bool)
	Events() <-chan Event
	Close() error
}

// Event is one state change pushed from mpv toward the UI.
// Name is an mpv property name, or the literal "end-file".
// Reason is populated only for "end-file" (mpv's end-file reason, e.g. "error"
// when a track failed to load) and is empty for every other event.
type Event struct {
	Name   string
	Num    float64
	Flag   bool
	Reason string
}

// observed maps our fixed observer IDs to mpv property names.
//
// core-idle and paused-for-cache are what make the difference between "mpv has
// a file loaded" and "audio is actually coming out of it": mpv reports
// core-idle while it is stalled, seeking or played out, and paused-for-cache
// while it is waiting on data. Without them the UI can only assume.
var observed = map[int]string{
	1: "time-pos",
	2: "duration",
	3: "pause",
	4: "volume",
	5: "core-idle",
	6: "paused-for-cache",
}

// highFrequency lists the events safe to drop under load. Everything else is a
// state change: dropping one leaves the UI showing something that is no longer
// true, which is exactly how a finished track went on reading as playing.
var highFrequency = map[string]bool{"time-pos": true}

// mpvReply is one command reply as delivered to the waiting caller of send():
// the raw data payload plus mpv's error string ("success" when it worked).
type mpvReply struct {
	data json.RawMessage
	err  string
}

type Player struct {
	cmd  *exec.Cmd
	conn net.Conn
	sock string

	mu      sync.Mutex
	nextID  int
	pending map[int]chan mpvReply

	events chan Event
	closed chan struct{}
	once   sync.Once

	// metering is false when this mpv cannot run the level meter, so SetAF
	// stops appending a filter that would take the whole EQ chain down with it.
	metering atomic.Bool
}

// StartPlayer spawns mpv in idle mode and attaches to its IPC socket. extra
// carries per-instance flags: a streaming instance adds raw-audio demuxer
// options (see rawAudioArgs) because it reads PCM rather than files.
func StartPlayer(extra ...string) (*Player, error) { return startPlayer(nil, extra...) }

// StartPlayerFD is StartPlayer with file descriptors passed to mpv, which can
// then read one of them as a stream: fds[0] becomes fd 3 in mpv, loadable as
// "fd://3".
//
// This exists because SEHO itself is the writer for the Soloist backend, and a
// FIFO cannot serve that case: POSIX leaves opening a FIFO O_RDWR undefined, and
// on macOS a single read-write handle behaves as a private channel - writes
// through it never reach a separate reader, so mpv sat at core-idle with the
// file loaded and no audio. A pipe has one end each way and no such ambiguity.
func StartPlayerFD(fds []*os.File, extra ...string) (*Player, error) {
	return startPlayer(fds, extra...)
}

func startPlayer(fds []*os.File, extra ...string) (*Player, error) {
	if _, err := exec.LookPath("mpv"); err != nil {
		return nil, errors.New("mpv not found on PATH - install it with: brew install mpv")
	}
	// ponytail: macOS caps unix socket paths at 104 bytes and mpv fails silently
	// past it. os.TempDir() is ~49 here, so this lands near 64. Move to /tmp if
	// a long TMPDIR ever breaks it.
	sock := filepath.Join(os.TempDir(), fmt.Sprintf("seho-%d-%d.sock", os.Getpid(), nextSockID()))
	os.Remove(sock)

	// --no-terminal is mandatory: without it mpv writes to our TTY and corrupts the UI.
	args := append([]string{
		"--idle=yes", "--no-video", "--no-terminal", "--really-quiet",
		"--input-ipc-server=" + sock,
	}, extra...)
	// ponytail: a debug hatch. mpv's own log is the only place some audio-path
	// failures are explained, and --no-terminal hides it.
	if f := os.Getenv("SEHO_MPV_LOG"); f != "" {
		args = append(args, "--log-file="+f+"."+strconv.Itoa(int(nextSockID())), "--msg-level=all=v")
	}
	cmd := exec.Command("mpv", args...)
	cmd.ExtraFiles = fds
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start mpv: %w", err)
	}

	p, err := dialPlayer(sock, cmd)
	if err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		return nil, err
	}
	return p, nil
}

// dialPlayer attaches to an mpv IPC socket that already exists, retrying while
// mpv finishes creating it. cmd is the mpv process to associate with the
// resulting Player (nil in tests, which dial a fake mpv directly) so that a
// failure during observer registration below reaps it via Close() rather than
// leaking it.
func dialPlayer(sock string, cmd *exec.Cmd) (*Player, error) {
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
		cmd:     cmd,
		conn:    conn,
		sock:    sock,
		pending: map[int]chan mpvReply{},
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
	p.probeMeter()
	return p, nil
}

func (p *Player) Events() <-chan Event { return p.events }

// forwardMpvHealth drains the inner mpv's event channel and passes on only what
// it knows better than the streaming source does: whether audio is actually
// moving through it.
//
// Draining is not optional. A Player's IPC reader feeds this channel, so an
// unread one stalls the reader and every later command times out - which is
// exactly how the capture path went silent-looking while the container was
// happily producing PCM. The position and duration mpv reports for a raw stream
// are meaningless (the stream has no start and no length), so they stop here;
// Soloist and the Web API are authoritative for those.
func forwardMpvHealth(mpv *Player, out func(Event), closed <-chan struct{}) {
	for {
		select {
		case <-closed:
			return
		case ev, ok := <-mpv.Events():
			if !ok {
				return
			}
			switch ev.Name {
			case "core-idle", "paused-for-cache":
				out(ev)
			}
		}
	}
}

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
			p.emit(Event{Name: "end-file", Reason: msg.Reason})
		case msg.Event != "":
			// Other mpv events are not used by the UI.
		default:
			p.mu.Lock()
			ch, ok := p.pending[msg.RequestID]
			delete(p.pending, msg.RequestID)
			p.mu.Unlock()
			if ok {
				ch <- mpvReply{data: msg.Data, err: msg.Error}
			}
		}
	}
	p.emit(Event{Name: "disconnected"})
}

func (p *Player) emit(ev Event) { emitEvent(p.events, p.closed, ev) }

// emitEvent queues one event for the UI. Position updates are dropped when the
// consumer is behind (they arrive ~10x/sec and the next one supersedes them),
// but every other event blocks until it is taken: end-file, pause and the
// idle/cache flags are state changes, and a dropped one leaves the UI
// permanently wrong. The settings page used to block the tview goroutine for
// seconds on docker calls, which was long enough to lose an end-file and leave
// a finished track showing as playing.
//
// Shared by all three backends: they all feed the same UI, and the reasoning
// does not change with the source.
//
// The wait is bounded rather than indefinite. Blocking forever would mean an
// unread channel wedges the goroutine feeding it - for a Player that is the IPC
// reader, so every later command would time out and the backend would look
// dead. A backend whose events nobody reads for emitWait is already broken;
// dropping past that point keeps the failure local and logged.
const emitWait = 5 * time.Second

func emitEvent(events chan Event, closed <-chan struct{}, ev Event) {
	if highFrequency[ev.Name] {
		select {
		case events <- ev:
		case <-closed:
		default:
		}
		return
	}
	select {
	case events <- ev:
		return
	case <-closed:
		return
	default:
	}
	timer := time.NewTimer(emitWait)
	defer timer.Stop()
	select {
	case events <- ev:
	case <-closed:
	case <-timer.C:
		log.Printf("dropped %s event: nothing has read this backend for %s", ev.Name, emitWait)
	}
}

// send writes one command and waits for its reply, returning an error both
// when mpv never answers and when it answers with error != "success".
func (p *Player) send(args ...any) error {
	p.mu.Lock()
	p.nextID++
	id := p.nextID
	ch := make(chan mpvReply, 1)
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
	case r := <-ch:
		if r.err != "" && r.err != "success" {
			return fmt.Errorf("mpv: %s", r.err)
		}
		return nil
	case <-time.After(2 * time.Second):
		p.mu.Lock()
		delete(p.pending, id)
		p.mu.Unlock()
		return fmt.Errorf("mpv did not answer %v", args[0])
	}
}

// get reads one mpv property. Unlike send it returns the payload, which is what
// makes it possible to ask mpv whether audio is genuinely decoding rather than
// inferring it from the outside.
func (p *Player) get(prop string) (json.RawMessage, error) {
	p.mu.Lock()
	p.nextID++
	id := p.nextID
	ch := make(chan mpvReply, 1)
	p.pending[id] = ch
	p.mu.Unlock()

	payload, err := json.Marshal(map[string]any{"command": []any{"get_property", prop}, "request_id": id})
	if err != nil {
		return nil, err
	}
	if _, err := p.conn.Write(append(payload, '\n')); err != nil {
		return nil, err
	}

	select {
	case r := <-ch:
		if r.err != "" && r.err != "success" {
			return nil, fmt.Errorf("mpv: %s", r.err)
		}
		return r.data, nil
	case <-time.After(2 * time.Second):
		p.mu.Lock()
		delete(p.pending, id)
		p.mu.Unlock()
		return nil, fmt.Errorf("mpv did not answer get_property %s", prop)
	}
}

// post writes a command without waiting for mpv's reply. Ordering is preserved
// because all writes go through the same mutex-serialised connection.
// ponytail: transport commands are fire-and-forget on purpose — mpv's
// property-change events are the authoritative record of what actually took
// effect, so an optimistic local update reconciled by the event beats blocking
// the UI goroutine for a reply we would only use to second-guess it.
func (p *Player) post(args ...any) error {
	payload, err := json.Marshal(map[string]any{"command": args})
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err = p.conn.Write(append(payload, '\n'))
	return err
}

// nextSockID keeps two mpv instances in the same process from colliding on one
// socket path. ponytail: a counter, not a UUID - the pid already scopes it.
var sockSeq atomic.Int32

func nextSockID() int32 { return sockSeq.Add(1) }

// rawAudioArgs configures an mpv instance to read raw stereo PCM from a pipe -
// what librespot's pipe backend and Soloist's captured null sink both produce.
// format is an mpv/ffmpeg sample format ("s16le", "s32le"); s32le is what keeps
// the extra bits of a lossless stream. The buffer settings matter: the default
// cache would put the transport bar a second or more behind what you hear.
func rawAudioArgs(format string, rate int) []string {
	return []string{
		"--demuxer=rawaudio",
		"--demuxer-rawaudio-format=" + format,
		"--demuxer-rawaudio-rate=" + strconv.Itoa(rate),
		"--demuxer-rawaudio-channels=2",
		"--cache=no",
		"--audio-buffer=0.05",
	}
}

// hostArch reports the machine architecture, used to pick a container platform.
func hostArch() string { return runtime.GOARCH }

func (p *Player) Load(path string) error   { return p.send("loadfile", path, "replace") }
func (p *Player) TogglePause() error       { return p.post("cycle", "pause") }
func (p *Player) Seek(delta float64) error { return p.post("seek", delta, "relative") }
func (p *Player) SetVolume(v int) error    { return p.post("set_property", "volume", clampVol(v)) }

// Stop ends playback without quitting mpv, so the instance stays available for
// the next Load. Used when the transport moves to the other source.
func (p *Player) Stop() error { return p.post("stop") }

// SetAF replaces the audio filter chain. An empty chain clears it, which is how
// the flat profile is expressed. Fire-and-forget for the same reason the
// transport commands are: mpv either applies it or logs a filter error, and
// blocking the UI on a reply would buy nothing.
func (p *Player) SetAF(chain string) error { return p.post("set_property", "af", p.withMeter(chain)) }

// The level meter. astats writes per-window loudness into filter metadata,
// which mpv exposes as af-metadata/<label> - so the same IPC socket that
// carries the transport commands can also answer "is sound actually coming out
// of this?". Nothing else mpv reports can: time-pos advances through silence,
// and a stream can be alive and audible-looking while carrying nothing.
const (
	meterLabel  = "sehometer"
	meterFilter = "@" + meterLabel + ":lavfi=[astats=metadata=1:reset=1:length=0.2]"

	// silenceFloor is the RMS level below which audio counts as no sound. astats
	// reports true digital silence as -inf; real music sits far above this even
	// in quiet passages, and a fade-out crossing it briefly is honest.
	silenceFloor = -70.0
)

// withMeter appends the meter to a chain, or returns the chain untouched when
// this mpv build cannot run it.
func (p *Player) withMeter(chain string) string {
	if !p.metering.Load() {
		return chain
	}
	if chain == "" {
		return meterFilter
	}
	return chain + "," + meterFilter
}

// probeMeter asks mpv to install the meter and remembers whether it took. Uses
// send rather than post because the answer is the whole point: an mpv without
// lavfi/astats would reject every later chain that carried it, taking the
// equalizer down with it.
func (p *Player) probeMeter() {
	// ponytail: an escape hatch, because a filter in the audio path is the kind
	// of thing that needs to be removable without a rebuild.
	if os.Getenv("SEHO_NO_METER") != "" {
		return
	}
	if err := p.send("set_property", "af", meterFilter); err != nil {
		log.Printf("mpv: no level metering (%v)", err)
		return
	}
	p.metering.Store(true)
}

// Level reports the loudness of the audio leaving the filter chain, in dBFS,
// and whether a reading was available at all. Unavailable means nothing is
// decoding - mpv drops the metadata when the chain is idle - which is itself an
// answer: no audio is flowing.
func (p *Player) Level() (float64, bool) {
	if !p.metering.Load() {
		return 0, false
	}
	raw, err := p.get("af-metadata/" + meterLabel)
	if err != nil {
		return 0, false
	}
	var md map[string]string
	if json.Unmarshal(raw, &md) != nil {
		return 0, false
	}
	v, ok := md["lavfi.astats.Overall.RMS_level"]
	if !ok {
		return 0, false
	}
	// astats spells digital silence "-inf", which ParseFloat handles, and the
	// caller compares against silenceFloor either way.
	db, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, false
	}
	return db, true
}

func (p *Player) Close() error {
	p.once.Do(func() {
		close(p.closed)
		// Ruling: do not send "quit" through send() and await a reply - mpv is
		// exiting and never answers, guaranteeing a 2s hang on every app close.
		// Write it directly and move on.
		if p.conn != nil {
			payload, _ := json.Marshal(map[string]any{"command": []any{"quit"}})
			p.conn.Write(append(payload, '\n'))
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
