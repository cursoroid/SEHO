package music

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/dhowden/tag"
	"github.com/joho/godotenv"
)

// LastFMResponse represents the structure of the response from Last.fm API
type LastFMResponse struct {
	Toptags struct {
		Tag []struct {
			Name string `json:"name"`
		} `json:"tag"`
	} `json:"toptags"`
}

// ExtractMetadata extracts essential metadata from the given file using the dhowden/tag package.
// Logs errors if metadata extraction fails.
func ExtractMetadata(file *os.File) (map[string]interface{}, error) {
	// Use tag.ReadFrom to parse metadata from the file
	metadata, err := tag.ReadFrom(file)
	if err != nil {
		log.Printf("error reading metadata from file %s: %v", file.Name(), err)
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

	log.Printf("error: unsupported file format for file %s", filename)
	return false
}

// FetchTags retrieves tags for a given artist and track from the Last.fm API with a timeout.
func FetchTags(artist, track string) ([]string, error) {
	err := godotenv.Load()
	if err != nil {
		log.Fatal(err)
	}
	apiKey := os.Getenv("API_KEY")

	// Encode artist and track to handle special characters
	artistEncoded := url.QueryEscape(artist)
	trackEncoded := url.QueryEscape(track)
  
  url := os.Getenv("API_KEY")
	apiURL := fmt.Sprintf(
		url,
		artistEncoded, trackEncoded, apiKey,
	)

	log.Printf("fetching tags from URL: %s", apiURL)

	// Create an HTTP client with a timeout to avoid hanging requests
	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	// Make the API request
	resp, err := client.Get(apiURL)
	if err != nil {
		return nil, fmt.Errorf("api request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := ioutil.ReadAll(resp.Body)
		log.Printf("api request failed: %s - %s", resp.Status, string(body))
		return nil, fmt.Errorf("api returned non-200 status: %s", resp.Status)
	}

	// Read the response body
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading api response: %v", err)
	}

	var lastFMResponse LastFMResponse
	if err := json.Unmarshal(body, &lastFMResponse); err != nil {
		return nil, fmt.Errorf("error parsing api response: %v", err)
	}

	var tags []string
	for _, tag := range lastFMResponse.Toptags.Tag {
		tags = append(tags, tag.Name)
	}

	log.Printf("fetched tags: %v", tags)
	return tags, nil
}
