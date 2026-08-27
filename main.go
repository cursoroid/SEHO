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
	closeLog := setupLog()
	defer closeLog()

	dir := cmp.Or(os.Getenv("MUSIC_DIR"), defaultMusicDir())
	rdb := redis.NewClient(&redis.Options{
		Addr: cmp.Or(os.Getenv("REDIS_ADDR"), "127.0.0.1:6379"),
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
