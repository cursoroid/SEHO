package main

import (
	"log"
	"internal/config"
	"internal/music"
	"internal/redis"
)

func main() {
	cfg := config.LoadConfig()
	rdb := redis.InitRedis(cfg.RedisAddress)
	defer rdb.Close()

	err := music.ScanDirectory(cfg.MusicDirectory, rdb)
	if err != nil {
		log.Fatalf("Error scanning music directory: %v", err)
	}

	log.Println("Music library successfully stored in Redis")
}

