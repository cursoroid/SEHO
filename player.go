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
// Reason is populated only for "end-file" (mpv's end-file reason, e.g. "error"
// when a track failed to load) and is empty for every other event.
type Event struct {
	Name   string
	Num    float64
	Flag   bool
	Reason string
}

// observed maps our fixed observer IDs to mpv property names.
var observed = map[int]string{1: "time-pos", 2: "duration", 3: "pause", 4: "volume"}

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
}

// StartPlayer spawns mpv in idle mode and attaches to its IPC socket.
func StartPlayer() (*Player, error) {
	if _, err := exec.LookPath("mpv"); err != nil {
		return nil, errors.New("mpv not found on PATH - install it with: brew install mpv")
	}
	// ponytail: macOS caps unix socket paths at 104 bytes and mpv fails silently
	// past it. os.TempDir() is ~49 here, so this lands near 64. Move to /tmp if
	// a long TMPDIR ever breaks it.
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

func (p *Player) emit(ev Event) {
	select {
	case p.events <- ev:
	case <-p.closed:
	default: // ponytail: drop rather than block the reader; time-pos arrives ~10x/sec
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

func (p *Player) Load(path string) error   { return p.send("loadfile", path, "replace") }
func (p *Player) TogglePause() error       { return p.post("cycle", "pause") }
func (p *Player) Seek(delta float64) error { return p.post("seek", delta, "relative") }
func (p *Player) SetVolume(v int) error    { return p.post("set_property", "volume", clampVol(v)) }
func (p *Player) Stop() error              { return p.send("stop") }

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
