package redisadapter

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestStoreRoundTripAndTTL(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), PipelinePoolSize: -1})
	t.Cleanup(func() { _ = client.Close() })
	store := New[string, string](client, "pages:", nil)
	ctx := context.Background()
	if err := store.Set(ctx, "url", "body", time.Minute); err != nil {
		t.Fatal(err)
	}
	value, ttl, ok, err := store.Get(ctx, "url")
	if err != nil || !ok || value != "body" || ttl <= 0 || ttl > time.Minute {
		t.Fatalf("get=%q ttl=%v ok=%v err=%v", value, ttl, ok, err)
	}
	if err := store.Delete(ctx, "url"); err != nil {
		t.Fatal(err)
	}
	_, _, ok, err = store.Get(ctx, "url")
	if err != nil || ok {
		t.Fatalf("deleted key ok=%v err=%v", ok, err)
	}
}
