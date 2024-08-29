package music

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/redis/go-redis/v9"
)

// StartMonitoring continuously monitors the directory for new files
func StartMonitoring(directory string, rdb *redis.Client, interval time.Duration) {
	for {
		err := ScanDirectory(directory, rdb)
		if err != nil {
			log.Printf("Error scanning directory: %v", err)
		}

		time.Sleep(interval)
	}
}

// ScanDirectory scans the provided directory and processes music files
func ScanDirectory(directory string, rdb *redis.Client) error {
	ctx := context.Background()
	err := filepath.Walk(directory, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !info.IsDir() && IsMusicFile(info.Name()) {
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			defer file.Close()

			metadata, err := ExtractMetadata(file)
			if err != nil {
				log.Printf("Could not read metadata from file %s: %v", path, err)
				return nil
			}

			key := "music:" + info.Name()
			_, err = rdb.HSet(ctx, key, metadata).Result()
			if err != nil {
				log.Printf("Error adding to Redis: %v", err)
			}

			log.Printf("Added to Redis: %s", info.Name())
		}

		return nil
	})

	return err
}