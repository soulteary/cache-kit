package rediscache

import (
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// --- Codex review follow-ups (PR #3) ---

// TestBoundedGetIsAtomic is the regression test for measuring the value with a
// separate STRLEN before fetching it. Another writer replacing the key between
// the two commands delivered an oversized payload anyway -- and the post-fetch
// length check had been removed, so nothing caught it.
func TestBoundedGetIsAtomic(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = client.Close() }()

	cfg := DefaultConfig()
	cfg.KeyPrefix = "atomic:"
	cfg.MaxValueBytes = 64
	c := New[TestUser](client, cfg)

	// Small value: served normally.
	if err := c.Set([]TestUser{{ID: "1", Name: "a"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(); err != nil {
		t.Fatalf("Get() on a small value error = %v", err)
	}

	// Oversized value written behind the cache's back.
	big := `[{"id":"1","name":"` + strings.Repeat("x", 512) + `"}]`
	if err := mr.Set(cfg.KeyPrefix+"data", big); err != nil {
		t.Fatal(err)
	}

	_, err := c.Get()
	if err == nil {
		t.Fatal("Get() returned nil error for a value over MaxValueBytes")
	}
	if !strings.Contains(err.Error(), "exceeds max allowed") {
		t.Errorf("Get() error = %v, want it to report the size limit", err)
	}

	// A missing key is still an empty result, not an error.
	mr.Del(cfg.KeyPrefix + "data")
	values, err := c.Get()
	if err != nil {
		t.Fatalf("Get() on a missing key error = %v", err)
	}
	if len(values) != 0 {
		t.Errorf("Get() on a missing key = %v, want empty", values)
	}
}

// TestRefreshKeepsTheVersionKeyPersistent is the regression test for Refresh
// putting a TTL back on the version key that Set deliberately persists. A
// refreshed cache lost its version when the TTL elapsed, the next Set restarted
// the counter at 1, and a consumer that had seen a higher version stopped
// detecting updates.
func TestRefreshKeepsTheVersionKeyPersistent(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = client.Close() }()

	cfg := DefaultConfig()
	cfg.KeyPrefix = "ver:"
	cfg.TTL = time.Minute
	c := New[TestUser](client, cfg)

	if err := c.Set([]TestUser{{ID: "1"}}); err != nil {
		t.Fatal(err)
	}
	versionKey := cfg.KeyPrefix + "data" + cfg.VersionKeySuffix

	if err := c.Refresh(); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	if ttl := mr.TTL(versionKey); ttl != 0 {
		t.Errorf("version key has TTL %s after Refresh, want it persistent", ttl)
	}

	// Walk past the data TTL: the version must survive and keep counting up.
	before, err := c.GetVersion()
	if err != nil {
		t.Fatal(err)
	}
	mr.FastForward(2 * time.Minute)

	after, err := c.GetVersion()
	if err != nil {
		t.Fatalf("GetVersion() after the data TTL elapsed error = %v", err)
	}
	if after != before {
		t.Errorf("version = %d after the data TTL elapsed, want it preserved at %d", after, before)
	}
}
