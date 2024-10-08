package music

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/redis/go-redis/v9"
)

// StartMonitoring continuously monitors the directory for new files
func StartMonitoring(directory string, rdb *redis.Client) error {
	filesAdded, err := ScanDirectory(directory, rdb)
	if err != nil {
		return fmt.Errorf("error scanning directory: %v", err)
	}

	if filesAdded == 0 {
		log.Println("No new files found in the directory")
	} else {
		log.Printf("Total files added: %d", filesAdded)
	}

	return nil
}

// ScanDirectory scans the provided directory and processes music files
func ScanDirectory(directory string, rdb *redis.Client) (int, error) {
	ctx := context.Background()
	filesAdded := 0

	err := filepath.Walk(directory, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !info.IsDir() && IsMusicFile(info.Name()) {
			key := "music:" + info.Name()

			// Check if the file is already in Redis
			exists, err := rdb.Exists(ctx, key).Result()
			if err != nil {
				log.Printf("Error checking Redis for file %s: %v", path, err)
				return nil // Continue to next file
			}

			if exists == 1 {
				log.Printf("File already processed: %s", info.Name())
				return nil // Skip this file, it's already processed
			}

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

			// Fetch tags for the artist and track
			tags, err := FetchTags(metadata["artist"].(string), metadata["title"].(string))
			if err != nil {
				log.Printf("Error fetching tags for %s: %v", info.Name(), err)
				return nil // Log error but continue
			}

			// Limit tags to top 5
			if len(tags) > 3 {
				tags = tags[:3] // Take only the first 5 tags
			}

			// Add tags to the metadata as a comma-separated string
			metadata["tags"] = strings.Join(tags, ",") // Convert tags slice to a comma-separated string

			// Store metadata with tags in Redis
			_, err = rdb.HSet(ctx, key, metadata).Result()
			if err != nil {
				log.Printf("Error adding to Redis for file %s: %v", path, err)
				return nil // Log error but don't stop the scanning process
			}

			log.Printf("Successfully added to Redis: %s", info.Name())
			filesAdded++
		}
		return nil
	})

	if err != nil {
		return filesAdded, fmt.Errorf("error walking through directory: %v", err)
	}

	return filesAdded, nil
}
