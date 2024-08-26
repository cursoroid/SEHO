package music

import (
	"os"
	"strings"

	"github.com/dhowden/tag"
)

// ExtractMetadata extracts metadata from the given file
func ExtractMetadata(file *os.File) (map[string]interface{}, error) {
	metadata, err := tag.ReadFrom(file)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"title":  metadata.Title(),
		"artist": metadata.Artist(),
		"album":  metadata.Album(),
		"path":   file.Name(),
	}, nil
}

// IsMusicFile checks if a file is a music file based on its extension
func IsMusicFile(filename string) bool {
	lowerName := strings.ToLower(filename)
	return strings.HasSuffix(lowerName, ".mp3") || strings.HasSuffix(lowerName, ".flac") || strings.HasSuffix(lowerName, ".m4a")
}

