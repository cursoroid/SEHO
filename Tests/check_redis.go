package main

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

func main() {
	rdb := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})

	ctx := context.Background()
	keys, err := rdb.Keys(ctx, "music:*").Result()
	if err != nil {
		panic(err)
	}

	for _, key := range keys {
		val, err := rdb.HGetAll(ctx, key).Result()
		if err != nil {
			fmt.Printf("Error fetching metadata for %s: %v\n", key, err)
			continue
		}

		fmt.Printf("Metadata for %s:\n", key)
		for field, value := range val {
			fmt.Printf("  %s: %s\n", field, value)
		}
	}
}
