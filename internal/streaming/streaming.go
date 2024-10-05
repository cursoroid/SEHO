// file: internal/streaming/streaming.go

package streaming

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/redis/go-redis/v9"
)

type Streamer struct {
	rdb           *redis.Client
	musicDirectory string
	currentCmd    *exec.Cmd
}

func NewStreamer(rdb *redis.Client, musicDirectory string) *Streamer {
	return &Streamer{
		rdb:           rdb,
		musicDirectory: musicDirectory,
	}
}

func (s *Streamer) StreamMusic() (bool, string, error) {
	ctx := context.Background()
	keys, err := s.rdb.Keys(ctx, "music:*").Result()
	if err != nil {
		return false, "", fmt.Errorf("error fetching music: %v", err)
	}

	if len(keys) == 0 {
		return false, "", fmt.Errorf("no music files found")
	}

	// For simplicity, we'll just play the first song found
	key := keys[0]
	musicData, err := s.rdb.HGetAll(ctx, key).Result()
	if err != nil {
		return false, "", fmt.Errorf("error fetching music data: %v", err)
	}

	filePath := filepath.Join(musicData["path"])

	// Stop any currently playing music
	s.StopStreaming()

	// Use FFmpeg to stream the audio
	s.currentCmd = exec.Command("ffplay", "-nodisp", "-autoexit", filePath)
	
	s.currentCmd.Stdout = os.Stdout
	s.currentCmd.Stderr = os.Stderr
	
	err = s.currentCmd.Start()
	if err != nil {
		return false, "", fmt.Errorf("error starting playback: %v", err)
	}

	go func() {
		err := s.currentCmd.Wait()
		if err != nil {
					fmt.Printf("Playback ended with error: %v\n", err)
				}
		// You might want to send a message back to the app when playback is finished
	}()

	return true, fmt.Sprintf("%s - %s", musicData["artist"], musicData["title"]), nil
}

func (s *Streamer) StopStreaming() {
	if s.currentCmd != nil && s.currentCmd.Process != nil {
		s.currentCmd.Process.Signal(os.Interrupt)
		s.currentCmd.Wait()
		s.currentCmd = nil
	}
}