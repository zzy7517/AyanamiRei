package middleware

import (
	"os"
	"testing"

	"github.com/redis/go-redis/v9"
)

const redisTestDB = 13

func newRedisTestClient(t *testing.T) *redis.Client {
	t.Helper()
	url := os.Getenv("REDIS_TEST_URL")
	if url == "" {
		t.Skip("REDIS_TEST_URL not set")
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatalf("parse REDIS_TEST_URL: %v", err)
	}
	opts.DB = redisTestDB
	client := redis.NewClient(opts)
	if err := client.FlushDB(t.Context()).Err(); err != nil {
		t.Fatalf("flush Redis test DB: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}
