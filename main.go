package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/redis/go-redis/v9"
)

// source is where a row came from. Local rows are indexed in Redis and played
// by path; Spotify rows are fetched live and played by URI.
type source int

const (
	srcLocal source = iota
	srcSpotify
)

type item struct {
	title, desc, album, tags, path string
	duration                       float64
	addedAt                        time.Time
	group                          bool   // true for a groupBy pseudo-row; never "the playing track"
	groupField                     string // when group: which field it was grouped on ("Artists"/"Albums"/"Tags")
	src                            source // srcLocal unless it came from the Spotify API
	artURL                         string // Spotify cover art URL; empty for local files (art is embedded)

	// playlistID is set on a Spotify playlist group row, so selecting it can
	// fetch that playlist's tracks. Empty on every other row.
	playlistID string
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

	// Settings come from the config file with environment variables layered on
	// top, so the documented MUSIC_DIR / REDIS_ADDR workflow keeps working.
	set := loadSettings()

	rdb := redis.NewClient(&redis.Options{Addr: set.Eff.RedisAddr})
	defer rdb.Close()
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		return fmt.Errorf("redis unreachable at %s: %w", set.Eff.RedisAddr, err)
	}

	pl, err := StartPlayer()
	if err != nil {
		return err
	}
	defer pl.Close()

	return NewUI(rdb, set, pl).Run()
}
