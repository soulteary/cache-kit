package redisadapter

import (
	"context"
	"fmt"
	"strings"
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

func TestStorePersistentCustomKeyAndInvalidJSON(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), PipelinePoolSize: -1})
	t.Cleanup(func() { _ = client.Close() })
	store := New[int, string](client, "items:", func(key int) string { return fmt.Sprintf("id-%d", key) })
	ctx := context.Background()
	if err := store.Set(ctx, 7, "value", 0); err != nil {
		t.Fatal(err)
	}
	value, ttl, ok, err := store.Get(ctx, 7)
	if err != nil || !ok || value != "value" || ttl != -1 {
		t.Fatalf("Get = %q, %v, %v, %v", value, ttl, ok, err)
	}
	if err := client.Set(ctx, "items:id-8", "not-json", 0).Err(); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := store.Get(ctx, 8); err == nil || !strings.Contains(err.Error(), "unmarshal") {
		t.Fatalf("invalid JSON error = %v", err)
	}
}

func TestAtomicValueAndTTLPair(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), PipelinePoolSize: -1})
	t.Cleanup(func() { _ = client.Close() })
	store := New[string, string](client, "atomic:", nil)
	ctx := context.Background()
	if err := store.Set(ctx, "key", "persistent", 0); err != nil {
		t.Fatal(err)
	}
	writerResult := make(chan error, 1)
	go func() {
		for i := 0; i < 500; i++ {
			var err error
			if i%2 == 0 {
				err = store.Set(ctx, "key", "persistent", 0)
			} else {
				err = store.Set(ctx, "key", "expiring", time.Minute)
			}
			if err != nil {
				writerResult <- err
				return
			}
		}
		writerResult <- nil
	}()
	for {
		value, ttl, ok, err := store.Get(ctx, "key")
		if err != nil || !ok {
			t.Fatalf("Get = %q, %v, %v, %v", value, ttl, ok, err)
		}
		if (ttl == -1) != (value == "persistent") {
			t.Fatalf("non-atomic pair: value=%q ttl=%v", value, ttl)
		}
		select {
		case err := <-writerResult:
			if err != nil {
				t.Fatal(err)
			}
			return
		default:
		}
	}
}

func TestNilClient(t *testing.T) {
	var client *redis.Client
	store := New[string, string](client, "", nil)
	if _, _, _, err := store.Get(context.Background(), "x"); err == nil || err.Error() != "redis client is nil" {
		t.Fatalf("Get error = %v", err)
	}
}
