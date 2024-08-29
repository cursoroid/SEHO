package main

import (
	"log"
	"SEHO/internal/config"
	"SEHO/internal/music"
	"SEHO/internal/redis"
    "SEHO/internal/logging"
)

func main() {
    //Setup logger and cleanup
    cleanup := logging.SetupLogger()
	defer cleanup()

	cfg := config.LoadConfig()
	rdb := redis.InitRedis(cfg.RedisAddress)
	defer rdb.Close()

	err := music.ScanDirectory(cfg.MusicDirectory, rdb)
	if err != nil {
		log.Fatalf("Error scanning music directory: %v", err)
	}

	log.Println("Music library successfully stored in Redis")
}

