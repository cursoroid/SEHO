package config

import (
	"log"
	"os"
)

type Config struct {
	RedisAddress   string
	MusicDirectory string
}

func LoadConfig() Config {
	redisAddr := getEnv("REDIS_ADDR", "localhost:6379")
	musicDir := getEnv("MUSIC_DIR", "./music")

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

