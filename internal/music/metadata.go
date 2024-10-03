package music

import (
	"log"
	"os"
	"strings"

	"github.com/dhowden/tag"
)

// ExtractMetadata extracts essential metadata from the given file using the dhowden/tag package.
// Logs errors if metadata extraction fails.
func ExtractMetadata(file *os.File) (map[string]interface{}, error) {
	// Use tag.ReadFrom to parse metadata from the file
	metadata, err := tag.ReadFrom(file)
	if err != nil {
		log.Printf("Error reading metadata from file %s: %v", file.Name(), err)
		return nil, err
	}

	// Extracting track and disc information (trackNumber, totalTracks)
	trackNumber, _ := metadata.Track()

	// Returning essential metadata as a map
	return map[string]interface{}{
		"title":       metadata.Title(),  // Title of the track
		"album":       metadata.Album(),  // Album name
		"artist":      metadata.Artist(), // Artist name
		"year":        metadata.Year(),   // Year of release
		"trackNumber": trackNumber,       // Track number on the album
		"path":        file.Name(),       // File path
	}, nil
}

// IsMusicFile checks if a file is a music file based on its extension.
// Logs errors if the file is unsupported or has an invalid extension.
func IsMusicFile(filename string) bool {
	lowerName := strings.ToLower(filename)

	// Check for supported extensions
	if strings.HasSuffix(lowerName, ".mp3") ||
		strings.HasSuffix(lowerName, ".flac") ||
		strings.HasSuffix(lowerName, ".m4a") ||
		strings.HasSuffix(lowerName, ".ogg") {
		return true
	}

	log.Printf("Error: Unsupported file format for file %s", filename)
	return false
}
