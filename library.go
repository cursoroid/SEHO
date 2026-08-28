package main

import (
	"cmp"
	"context"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dhowden/tag"
	"github.com/redis/go-redis/v9"
)

const keyPrefix = "music:"

var musicExts = []string{".mp3", ".flac", ".m4a", ".ogg"}

// probeDuration returns the track length in seconds, or 0 if ffprobe is
// unavailable, fails, or takes too long. A zero here is backfilled from mpv
// the first time the track plays.
// ponytail: 10s is generous for a header read and bounds the stall a FIFO or a
// dead network mount would otherwise cause.
func probeDuration(ctx context.Context, path string) float64 {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "ffprobe", "-v", "error",
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
		"duration":    probeDuration(ctx, path),
	}).Err()
	if err != nil {
		return false, err
	}
	return true, nil
}

// listTracks returns every indexed track, ready for the TUI list.
// ponytail: KEYS + one HGETALL per track. Fine for a personal library; switch to
// SCAN and a pipeline if this ever holds more than a few thousand tracks.
func listTracks(ctx context.Context, rdb *redis.Client) ([]item, error) {
	keys, err := rdb.Keys(ctx, keyPrefix+"*").Result()
	if err != nil {
		return nil, err
	}
	slices.Sort(keys)

	items := make([]item, 0, len(keys))
	for _, k := range keys {
		h, err := rdb.HGetAll(ctx, k).Result()
		if err != nil {
			return nil, err
		}
		dur, _ := strconv.ParseFloat(h["duration"], 64)
		// A missing or unparseable added_at sorts oldest (zero time), which is
		// the safe default for Recent - it never displaces a track with a
		// real timestamp.
		addedAt, _ := time.Parse(time.RFC3339, h["added_at"])
		items = append(items, item{
			title:    cmp.Or(h["title"], filepath.Base(h["path"])),
			desc:     cmp.Or(h["artist"], "Unknown artist"),
			album:    h["album"],
			tags:     h["tags"],
			path:     h["path"],
			duration: dur,
			addedAt:  addedAt,
		})
	}
	return items, nil
}
