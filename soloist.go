package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SoloistBackend plays Spotify through Soloist, Spotify's own headless client.
//
// Soloist is Linux-only and can emit audio only to PipeWire or PulseAudio, so on
// macOS it runs in a container that captures its own null sink and writes raw
// PCM on stdout. SEHO feeds that into mpv, which is what puts the equalizer in
// the path:
//
//	docker[ soloist -> pulse null sink -> parec ] -> stdout -> fifo -> mpv -> af chain -> output
//
// Control and state go through `soloist ctl` inside the container, which speaks
// Soloist's local WebSocket API for us. That is deliberate: it keeps SEHO free
// of a WebSocket dependency, and `ctl` is the interface Spotify documents.
//
// Preferred over librespot because Soloist is a first-party client: librespot is
// refused audio keys outright on newer Spotify accounts (librespot#1649).
type SoloistBackend struct {
	cfg       Config
	container string
	mpv       *Player
	docker    *exec.Cmd

	// audio is the write end of the pipe mpv reads as fd://3. SEHO is the writer
	// here, which is exactly why this is a pipe and not a FIFO - see
	// StartPlayerFD.
	audio *os.File

	events chan Event
	closed chan struct{}
	once   sync.Once

	// streaming closes when the first PCM byte arrives from the container, which
	// is when mpv can usefully be pointed at the fifo.
	streaming     chan struct{}
	streamingOnce sync.Once

	// ready is closed once `ctl status` reports a logged-in daemon. Soloist
	// gives a machine-readable answer here, so unlike the librespot backend this
	// needs no log scraping.
	ready     chan struct{}
	readyOnce sync.Once

	mu       sync.Mutex
	pos, dur float64
	speed    float64 // playback speed from Soloist's position anchor
	playing  bool
	uri      string
	lastPos  time.Time
	endSent  bool // guards against emitting end-file twice for one track
}

// soloistSampleFormat picks the capture format. 32 bits is the point of the
// lossless tier: Spotify's lossless audio carries more than 16 bits, and a
// 16-bit sink discards that before SEHO ever sees it. 16 bits stays available
// for anyone who would rather halve the data rate.
func soloistSampleFormat(lossless bool) string {
	if lossless {
		return "s32le"
	}
	return "s16le"
}

const (
	soloistRate    = 44100
	soloistWSPort  = 5580
	soloistDataVol = "seho-soloist-data"
)

// StartSoloistBackend launches the container and the mpv instance that consumes
// its audio. The API key is required; pairing must already have happened (see
// PairSoloist), because Spotify Connect discovery cannot reach a container on a
// NAT'd Docker network.
func StartSoloistBackend(cfg Config, apiKey string) (*SoloistBackend, error) {
	if apiKey == "" {
		return nil, errors.New("no Soloist API key - add one in settings (press ,)")
	}
	if err := dockerAvailable(); err != nil {
		return nil, err
	}
	if err := soloistImagePresent(cfg.SoloistImage); err != nil {
		return nil, err
	}

	format := soloistSampleFormat(cfg.Lossless)

	// A pipe, not a FIFO: SEHO copies the container's PCM into it, and mpv reads
	// the other end as fd://3.
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("create audio pipe: %w", err)
	}

	mpv, err := StartPlayerFD([]*os.File{pr}, rawAudioArgs(format, soloistRate)...)
	if err != nil {
		pr.Close()
		pw.Close()
		return nil, err
	}
	// mpv holds its own copy of the descriptor now.
	pr.Close()

	s := &SoloistBackend{
		cfg:       cfg,
		container: fmt.Sprintf("seho-soloist-%d", os.Getpid()),
		audio:     pw,
		mpv:       mpv,
		events:    make(chan Event, 64),
		closed:    make(chan struct{}),
		ready:     make(chan struct{}),
		streaming: make(chan struct{}),
	}

	if err := s.run(apiKey, format); err != nil {
		s.Close()
		return nil, err
	}

	// mpv is pointed at the stream only once audio is actually arriving, so it
	// never has to sit on a silent pipe while Soloist logs in and becomes the
	// active device.
	go func() {
		select {
		case <-s.streaming:
		case <-s.closed:
			return
		case <-time.After(2 * time.Minute):
			log.Print("soloist produced no audio; not attaching mpv")
			return
		}
		if err := s.mpv.Load("fd://3"); err != nil {
			log.Printf("attach mpv to the audio stream: %v", err)
			return
		}
		log.Print("soloist: mpv reading the audio stream")
	}()

	go s.awaitLogin()
	return s, nil
}

// run starts the container. Its stdout is the PCM stream and is copied into the
// fifo; its stderr is Soloist's log and goes to the log file.
func (s *SoloistBackend) run(apiKey, format string) error {
	args := []string{
		"run", "--rm", "--name", s.container,
		"--platform", "linux/" + dockerArch(),
		// Published on loopback only: the WebSocket API is a control channel for
		// this machine, not something to expose on a network.
		"-p", fmt.Sprintf("127.0.0.1:%d:%d", soloistWSPort, soloistWSPort),
		"-v", soloistDataVol + ":/data",
		"-e", "SOLOIST_API_KEY=" + apiKey,
		"-e", "SEHO_FORMAT=" + format,
		"-e", "SEHO_RATE=" + strconv.Itoa(soloistRate),
		"-e", "SEHO_WS_PORT=" + strconv.Itoa(soloistWSPort),
		"-e", "SEHO_DEVICE_NAME=" + s.cfg.DeviceName,
		s.cfg.SoloistImage,
	}

	cmd := exec.Command("docker", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start soloist container: %w", err)
	}
	s.docker = cmd

	// The PCM stream. A short copy buffer keeps latency down; the default 32KB
	// would hold about 90ms of 32-bit stereo before passing it on.
	go func() {
		buf := make([]byte, 8192)
		n, err := io.CopyBuffer(&firstByteSignal{w: s.audio, on: s.markStreaming}, stdout, buf)
		log.Printf("soloist audio stream ended after %d bytes: %v", n, err)
	}()

	go func() {
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 0, 16*1024), 256*1024)
		for sc.Scan() {
			log.Printf("soloist: %s", sc.Text())
		}
	}()

	go func() {
		err := cmd.Wait()
		select {
		case <-s.closed:
			return
		default:
		}
		log.Printf("soloist container exited: %v", err)
		s.emit(Event{Name: "disconnected"})
	}()
	return nil
}

// awaitLogin polls `ctl status` until Soloist reports a session, then starts the
// event stream. Soloist keeps its credentials in the data volume, so after the
// first pairing this is satisfied within a second or two of startup.
func (s *SoloistBackend) awaitLogin() {
	for {
		select {
		case <-s.closed:
			return
		case <-time.After(time.Second):
		}
		if s.loggedIn() {
			s.readyOnce.Do(func() {
				close(s.ready)
				go s.trace()
				go s.tick()
			})
			return
		}
	}
}

// loggedIn reads `soloist ctl status`, which prints "logged in: yes" or
// "logged in: no".
func (s *SoloistBackend) loggedIn() bool {
	out, err := s.ctl(context.Background(), "status")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "logged in:") {
			return strings.Contains(line, "yes")
		}
	}
	return false
}

// ctl runs one `soloist ctl` command inside the container.
func (s *SoloistBackend) ctl(ctx context.Context, args ...string) (string, error) {
	full := append([]string{"exec", s.container, "soloist", "ctl"}, args...)
	full = append(full, "-D", "/data")

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "docker", full...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("soloist ctl %s: %w: %s",
			strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// trace streams Soloist's WebSocket events as JSON lines.
//
// Field names come from Spotify's documented WebSocket API (playback_state,
// position_sync, track_changed, volume_changed, with position_ms and
// duration_ms). Parsing is deliberately tolerant: anything missing is left
// alone rather than zeroed, so an unrecognised or renamed field degrades the
// display instead of resetting the transport.
func (s *SoloistBackend) trace() {
	cmd := exec.Command("docker", "exec", s.container, "soloist", "ctl", "trace", "-D", "/data")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("soloist trace: %v", err)
		return
	}
	if err := cmd.Start(); err != nil {
		log.Printf("soloist trace: %v", err)
		return
	}
	go func() {
		<-s.closed
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
	}()

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		// No prefix filtering here: trace lines carry a millisecond timestamp
		// before the JSON, and the connect banner shares a line with the first
		// payload. traceLine locates the object; anything else is ignored there.
		s.handleTrace(sc.Text())
	}
	cmd.Wait()
}

// soloistEvent is the subset of Soloist's WebSocket payload SEHO uses.
//
// Shapes captured from a live daemon (soloist 1.3.7, 2026-08-28), not guessed:
//
//	{"type":"auth_state","logged_in":true,"is_active":false,"device_name":"SEHO"}
//	{"type":"position_sync","position":{"position_ms":30000,"timestamp_ms":...,"speed":0}}
//	{"type":"playback_state","status":"playing","volume":40,"is_active":true,
//	 "item":{"uri":"spotify:track:...","decorations":{"identity":{"name":"..."}},
//	         "playback":{"duration_ms":180386}},
//	 "position":{"position_ms":6869,"timestamp_ms":...,"speed":1}}
//
// The other types (playback_changed, volume_changed) carry the same fields.
// Pointers distinguish absent from zero, so a payload that omits a field leaves
// state alone instead of resetting it.
type soloistEvent struct {
	Type     string `json:"type"`
	Status   string `json:"status"` // "playing" | "paused"
	Volume   *int   `json:"volume"`
	LoggedIn *bool  `json:"logged_in"`
	Position *struct {
		PositionMs  *int     `json:"position_ms"`
		TimestampMs *int64   `json:"timestamp_ms"`
		Speed       *float64 `json:"speed"`
	} `json:"position"`
	Item *struct {
		URI         string `json:"uri"`
		Decorations struct {
			Identity struct {
				Name string `json:"name"`
			} `json:"identity"`
		} `json:"decorations"`
		Playback struct {
			DurationMs *int `json:"duration_ms"`
		} `json:"playback"`
	} `json:"item"`
}

// traceLine splits one `ctl trace` line into its JSON payload. Lines are
// prefixed with a millisecond timestamp ("1787922046125 {...}"), and the first
// line is a human-readable "connected to ws://..." banner - so the payload has
// to be located rather than assumed to start at column zero.
func traceLine(line string) (string, bool) {
	i := strings.IndexByte(line, '{')
	if i < 0 {
		return "", false
	}
	return line[i:], true
}

// handleTrace folds one event into local state and emits what the UI needs.
// Split out from trace so it can be tested without a container.
func (s *SoloistBackend) handleTrace(line string) {
	payload, ok := traceLine(line)
	if !ok {
		return
	}
	var ev soloistEvent
	if json.Unmarshal([]byte(payload), &ev) != nil {
		return
	}

	var (
		emitDur, emitPause, emitPos bool
		finished                    bool
	)

	s.mu.Lock()
	// Soloist anchors position to its own clock and tells us the playback speed,
	// so interpolation needs no wall-clock guessing: position at anchor, moving
	// at speed. speed 0 means parked, which is how a seek while paused arrives.
	if p := ev.Position; p != nil && p.PositionMs != nil {
		s.pos = float64(*p.PositionMs) / 1000
		s.lastPos = time.Now()
		s.speed = 1
		if p.Speed != nil {
			s.speed = *p.Speed
		}
		emitPos = true
	}
	if ev.Item != nil {
		if ev.Item.URI != "" {
			s.uri = ev.Item.URI
		}
		if d := ev.Item.Playback.DurationMs; d != nil {
			if next := float64(*d) / 1000; next != s.dur {
				s.dur = next
				emitDur = true
			}
		}
	}
	switch ev.Status {
	case "playing":
		if !s.playing {
			emitPause = true
		}
		s.playing = true
	case "paused":
		if s.playing {
			emitPause = true
		}
		s.playing = false
	}
	pos, dur, playing := s.pos, s.dur, s.playing
	// A track that has run out and stopped is the end of what SEHO asked for.
	// Soloist would advance its own queue, but SEHO drives one URI at a time and
	// owns the queue itself.
	if dur > 0 && pos >= dur-1 && !playing && !s.endSent {
		s.endSent = true
		finished = true
	}
	if playing && pos < dur-1 {
		s.endSent = false
	}
	s.mu.Unlock()

	if emitDur {
		s.emit(Event{Name: "duration", Num: dur})
	}
	if emitPause {
		s.emit(Event{Name: "pause", Flag: !playing})
	}
	if emitPos {
		s.emit(Event{Name: "time-pos", Num: pos})
	}
	if ev.Volume != nil {
		// Soloist's own volume, not mpv's. Reported so an adjustment made in the
		// Spotify app is visible, but SEHO's own volume keys drive mpv.
		log.Printf("soloist volume is now %d%%", *ev.Volume)
	}
	if finished {
		s.emit(Event{Name: "end-file", Reason: "eof"})
	}
}

// tick emits an interpolated position between Soloist's own sync events, which
// arrive only on change. The transport bar's animation is a function of
// position, so without this it would freeze between events.
func (s *SoloistBackend) tick() {
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-s.closed:
			return
		case <-t.C:
		}
		s.mu.Lock()
		playing, pos, dur, speed, since := s.playing, s.pos, s.dur, s.speed, time.Since(s.lastPos).Seconds()
		s.mu.Unlock()
		if !playing || speed == 0 {
			continue
		}
		p := pos + since*speed
		if dur > 0 && p > dur {
			p = dur
		}
		s.emit(Event{Name: "time-pos", Num: p})
	}
}

func (s *SoloistBackend) Events() <-chan Event { return s.events }

func (s *SoloistBackend) emit(ev Event) {
	select {
	case s.events <- ev:
	case <-s.closed:
	default: // drop rather than stall the producer, as the other backends do
	}
}

// Load plays one Spotify URI. It waits for the Soloist session first, which
// after the initial pairing is immediate.
func (s *SoloistBackend) Load(uri string) error {
	select {
	case <-s.ready:
	case <-s.closed:
		return errors.New("soloist backend closed")
	case <-time.After(90 * time.Second):
		return errors.New("soloist did not sign in - pair it first (see docker/README)")
	}

	if _, err := s.ctl(context.Background(), "play", uri); err != nil {
		return err
	}
	s.mu.Lock()
	// speed 1 so the position interpolates from the moment playback starts,
	// rather than waiting for Soloist's first position anchor.
	s.uri, s.pos, s.playing, s.speed, s.lastPos = uri, 0, true, 1, time.Now()
	s.endSent = false
	s.mu.Unlock()
	s.emit(Event{Name: "pause", Flag: false})
	return nil
}

func (s *SoloistBackend) TogglePause() error {
	s.mu.Lock()
	playing := s.playing
	s.playing = !playing
	s.mu.Unlock()

	s.emit(Event{Name: "pause", Flag: playing})
	cmd := "play"
	if playing {
		cmd = "pause"
	}
	_, err := s.ctl(context.Background(), cmd)
	return err
}

func (s *SoloistBackend) Seek(delta float64) error {
	s.mu.Lock()
	target := s.pos + delta
	if s.playing && !s.lastPos.IsZero() {
		target += time.Since(s.lastPos).Seconds()
	}
	if target < 0 {
		target = 0
	}
	if s.dur > 0 && target > s.dur {
		target = s.dur
	}
	s.pos, s.lastPos = target, time.Now()
	s.mu.Unlock()

	s.emit(Event{Name: "time-pos", Num: target})
	_, err := s.ctl(context.Background(), "seek", strconv.Itoa(int(target*1000)))
	return err
}

// SetVolume and SetAF go to mpv: that is where the audio actually passes
// through, and routing volume through Soloist would also change what every other
// Connect client sees.
func (s *SoloistBackend) SetVolume(v int) error    { return s.mpv.SetVolume(v) }
func (s *SoloistBackend) SetAF(chain string) error { return s.mpv.SetAF(chain) }

func (s *SoloistBackend) Stop() error {
	s.mu.Lock()
	s.playing = false
	s.mu.Unlock()
	_, err := s.ctl(context.Background(), "pause")
	return err
}

func (s *SoloistBackend) Close() error {
	s.once.Do(func() {
		close(s.closed)

		// Stop the container by name: killing `docker run` leaves the container
		// itself running, and the next launch would then collide on the name.
		if s.container != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			exec.CommandContext(ctx, "docker", "stop", "-t", "2", s.container).Run()
			cancel()
		}
		if s.docker != nil && s.docker.Process != nil {
			s.docker.Process.Kill()
			s.docker.Wait()
		}
		if s.mpv != nil {
			s.mpv.Close()
		}
		if s.audio != nil {
			s.audio.Close()
		}
	})
	return nil
}

// firstByteSignal calls on the first time anything is written through it, so the
// audio path can be attached exactly when there is audio to attach it to.
type firstByteSignal struct {
	w  io.Writer
	on func()
}

func (f *firstByteSignal) Write(p []byte) (int, error) {
	if len(p) > 0 && f.on != nil {
		f.on()
	}
	return f.w.Write(p)
}

func (s *SoloistBackend) markStreaming() {
	s.streamingOnce.Do(func() {
		log.Print("soloist: first PCM bytes arrived")
		close(s.streaming)
	})
}

// --- environment checks ----------------------------------------------------

// soloistPaired reports whether Soloist has cached credentials in its data
// volume, which is what pairing produces. Checked by listing the volume rather
// than by starting the daemon: this runs while drawing the settings page.
func soloistPaired() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "run", "--rm",
		"-v", soloistDataVol+":/data", "alpine:3", "sh", "-c",
		"ls /data/credentials.json 2>/dev/null || ls /data/*/credentials.json 2>/dev/null").CombinedOutput()
	return err == nil && strings.Contains(string(out), "credentials.json")
}

func dockerAvailable() error {
	if _, err := exec.LookPath("docker"); err != nil {
		return errors.New("docker not found on PATH - Soloist is Linux-only and runs in a container")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "docker", "info", "--format", "{{.ServerVersion}}").CombinedOutput(); err != nil {
		return fmt.Errorf("docker is not running: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func soloistImagePresent(image string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "image", "inspect", image).CombinedOutput()
	if err != nil {
		return fmt.Errorf("image %s is missing - build it with: "+
			"docker build --platform linux/%s -t %s ./docker (%s)",
			image, dockerArch(), image, strings.TrimSpace(lastLine(string(out))))
	}
	return nil
}

// dockerArch maps Go's architecture onto the Soloist build names.
func dockerArch() string {
	if hostArch() == "arm64" {
		return "arm64"
	}
	return "amd64"
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}
