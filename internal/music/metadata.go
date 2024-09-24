package music

import (
	"os"
	"strings"

	"github.com/dhowden/tag"
)

// ExtractMetadata extracts detailed metadata from the given file using the dhowden/tag package
func ExtractMetadata(file *os.File) (map[string]interface{}, error) {
	// Use tag.ReadFrom to parse metadata from the file
	metadata, err := tag.ReadFrom(file)
	if err != nil {
		return nil, err
	}

	// Extracting track and disc information (trackNumber, totalTracks, discNumber, totalDiscs)
	trackNumber, totalTracks := metadata.Track()
	discNumber, totalDiscs := metadata.Disc()

	// Extract the album artwork (if available)
	var artworkInfo string
	if picture := metadata.Picture(); picture != nil {
		artworkInfo = picture.MIMEType
	} else {
		artworkInfo = "No artwork available"
	}

	// Returning metadata as a map
	return map[string]interface{}{
		"format":       metadata.Format(),        // Format of the file (MP3, MP4, etc.)
		"fileType":     metadata.FileType(),      // Type of the file
		"title":        metadata.Title(),         // Title of the track
		"album":        metadata.Album(),         // Album name
		"artist":       metadata.Artist(),        // Artist name
		"albumArtist":  metadata.AlbumArtist(),   // Album artist (if different from track artist)
		"composer":     metadata.Composer(),      // Composer of the track
		"genre":        metadata.Genre(),         // Genre of the music
		"year":         metadata.Year(),          // Year of the release
		"trackNumber":  trackNumber,              // Track number on the album
		"totalTracks":  totalTracks,              // Total number of tracks on the album
		"discNumber":   discNumber,               // Disc number (in multi-disc albums)
		"totalDiscs":   totalDiscs,               // Total number of discs in the album
		"picture":      artworkInfo,              // MIME type of the artwork, or "No artwork available"
		"lyrics":       metadata.Lyrics(),        // Lyrics (if available)
		"comment":      metadata.Comment(),       // Comment or description (if any)
		"path":         file.Name(),              // File path
	}, nil
}

// IsMusicFile checks if a file is a music file based on its extension
func IsMusicFile(filename string) bool {
	lowerName := strings.ToLower(filename)
	// Supported extensions for music files (MP3, MP4, OGG, FLAC)
	return strings.HasSuffix(lowerName, ".mp3") || strings.HasSuffix(lowerName, ".flac") ||
	       strings.HasSuffix(lowerName, ".m4a") || strings.HasSuffix(lowerName, ".ogg")
}
