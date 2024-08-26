package music

import (
	"log"
	"os"
	"path/filepath"

	"github.com/go-redis/redis/v8"
)

// ScanDirectory scans the provided directory and processes music files
func ScanDirectory(directory string, rdb *redis.Client) error {
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
			rdb.HSet(ctx, key, metadata)

			log.Printf("Added to Redis: %s", info.Name())
		}

		return nil
	})

	return err
}

