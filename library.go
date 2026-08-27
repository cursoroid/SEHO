package main

import (
	"cmp"
	"context"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/dhowden/tag"
	"github.com/redis/go-redis/v9"
)

const keyPrefix = "music:"

var musicExts = []string{".mp3", ".flac", ".m4a", ".ogg"}

// scanDirectory indexes every music file under dir that Redis has not seen yet.
// Per-file problems are logged and skipped; only an unusable dir is an error.
func scanDirectory(ctx context.Context, dir string, rdb *redis.Client) (int, error) {
	if _, err := os.Stat(dir); err != nil {
		return 0, err
	}

	added := 0
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			log.Printf("walk %s: %v", path, err)
			return nil
		case d.IsDir(), !slices.Contains(musicExts, strings.ToLower(filepath.Ext(path))):
			return nil
		}

		indexed, err := indexFile(ctx, path, rdb)
		if err != nil {
			log.Printf("skipping %s: %v", path, err)
		}
		if indexed {
			added++
		}
		return nil
	})
	return added, err
}

// indexFile stores one track's metadata in Redis. Returns false if it was already there.
func indexFile(ctx context.Context, path string, rdb *redis.Client) (bool, error) {
	// Keyed by full path, not basename - two albums can both hold "01 Intro.mp3".
	key := keyPrefix + path
	exists, err := rdb.Exists(ctx, key).Result()
	if err != nil {
		return false, err
	}
	if exists == 1 {
		return false, nil
	}

	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	md, err := tag.ReadFrom(f)
	if err != nil {
		return false, fmt.Errorf("read metadata: %w", err)
	}
	trackNumber, _ := md.Track()

	err = rdb.HSet(ctx, key, map[string]any{
		"title":       md.Title(),
		"album":       md.Album(),
		"artist":      md.Artist(),
		"year":        md.Year(),
		"trackNumber": trackNumber,
		"path":        path,
		"added_at":    time.Now().Format(time.RFC3339),
	}).Err()
	if err != nil {
		return false, err
	}
	return true, nil
}

// listTracks returns every indexed track, ready for the TUI list.
// ponytail: KEYS + one HGETALL per track. Fine for a personal library; switch to
// SCAN and a pipeline if this ever holds more than a few thousand tracks.
func listTracks(ctx context.Context, rdb *redis.Client) ([]list.Item, error) {
	keys, err := rdb.Keys(ctx, keyPrefix+"*").Result()
	if err != nil {
		return nil, err
	}
	slices.Sort(keys)

	items := make([]list.Item, 0, len(keys))
	for _, k := range keys {
		h, err := rdb.HGetAll(ctx, k).Result()
		if err != nil {
			return nil, err
		}
		items = append(items, item{
			title: cmp.Or(h["title"], filepath.Base(h["path"])),
			desc:  cmp.Or(h["artist"], "Unknown artist"),
			path:  h["path"],
		})
	}
	return items, nil
}
