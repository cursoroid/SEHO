package config

import (
	"os"
    "path/filepath"
    "strings"
)

type Config struct {
	RedisAddress   string
	MusicDirectory string
}

func LoadConfig() Config {
	redisAddr := getEnv("REDIS_ADDR", "localhost:6379")
	musicDir := getEnv("MUSIC_DIR", "./music")

	if strings.HasPrefix(musicDir, "~") {
		homeDir, err := os.UserHomeDir()
		if err == nil {
			musicDir = filepath.Join(homeDir, strings.TrimPrefix(musicDir, "~"))
		}
	}

	return Config{
		RedisAddress:   redisAddr,
		MusicDirectory: musicDir,
	}
}

func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}

