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
	empty := true // Flag to check if the directory is empty

	err := filepath.Walk(directory, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// If a music file is found, set empty to false
		if !info.IsDir() && IsMusicFile(info.Name()) {
			empty = false

			file, err := os.Open(path)
			if err != nil {
				log.Printf("Error opening file %s: %v", path, err)
				return nil // Skip the file, continue to next
			}
			defer file.Close()

			metadata, err := ExtractMetadata(file)
			if err != nil {
				log.Printf("Could not read metadata from file %s: %v", path, err)
				return nil // Skip processing this file
			}

			key := "music:" + info.Name()
			_, err = rdb.HSet(ctx, key, metadata).Result()
			if err != nil {
				log.Printf("Error adding to Redis for file %s: %v", path, err)
				return nil // Log error but don't stop the scanning process
			}

			log.Printf("Successfully added to Redis: %s", info.Name())
		}

		return nil
	})

	// If the directory is empty, log an error
	if empty {
		log.Printf("Error: Directory '%s' is empty", directory)
	}

	return err
}
