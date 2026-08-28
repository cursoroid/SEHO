package main

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/redis/go-redis/v9"
)

type item struct {
	title, desc, album, tags, path string
	duration                       float64
	addedAt                        time.Time
	group                          bool   // true for a groupBy pseudo-row; never "the playing track"
	groupField                     string // when group: which field it was grouped on ("Artists"/"Albums"/"Tags")
}

// setupLog sends log output to logs/seho.log, or nowhere if that is not writable.
// ponytail: never stderr - this is an alt-screen TUI and stray writes corrupt the render.
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
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	closeLog := setupLog()
	defer closeLog()

	dir := cmp.Or(os.Getenv("MUSIC_DIR"), defaultMusicDir())
	rdb := redis.NewClient(&redis.Options{
		Addr: cmp.Or(os.Getenv("REDIS_ADDR"), "127.0.0.1:6379"),
	})
	defer rdb.Close()
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		return fmt.Errorf("redis unreachable: %w", err)
	}

	pl, err := StartPlayer()
	if err != nil {
		return err
	}
	defer pl.Close()

	return NewUI(rdb, dir, pl).Run()
}
