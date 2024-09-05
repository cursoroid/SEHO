package main

import (
	"SEHO/internal/config"
	"SEHO/internal/logging"
	"SEHO/internal/music"
	"time"

	"github.com/redis/go-redis/v9"
)

func main() {
	//Setup logger and cleanup
	cleanup := logging.SetupLogger()
	defer cleanup()

	cfg := config.LoadConfig()
	rdb := redis.NewClient(&redis.Options{
		Addr: cfg.RedisAddress})

	music.StartMonitoring(cfg.MusicDirectory, rdb, 10*time.Second)
}
