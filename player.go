package main

import (
	"os/exec"
	"sync"
)

// player runs at most one ffplay process at a time.
type player struct {
	mu  sync.Mutex
	cmd *exec.Cmd
}

func (p *player) play(path string) error {
	p.stop()

	// Output is silenced on purpose: ffplay writing to our stdout would corrupt the TUI.
	cmd := exec.Command("ffplay", "-nodisp", "-autoexit", "-loglevel", "quiet", path)
	if err := cmd.Start(); err != nil {
		return err
	}
	// Reaped here and only here, so stop() never double-Waits the same process.
	// ponytail: the TUI is not notified when a track ends on its own.
	go cmd.Wait()

	p.mu.Lock()
	p.cmd = cmd
	p.mu.Unlock()
	return nil
}

func (p *player) stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd != nil && p.cmd.Process != nil {
		p.cmd.Process.Kill()
		p.cmd = nil
	}
}
