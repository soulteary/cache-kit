package hybrid

import (
	"context"
	"github.com/soulteary/cache-kit/v2/ttlcache"
	"testing"
	"time"
)

type fakeStore struct {
	value string
	ttl   time.Duration
	ok    bool
	sets  int
}

func (f *fakeStore) Get(context.Context, string) (string, time.Duration, bool, error) {
	return f.value, f.ttl, f.ok, nil
}
func (f *fakeStore) Set(_ context.Context, _ string, v string, ttl time.Duration) error {
	f.value = v
	f.ttl = ttl
	f.ok = true
	f.sets++
	return nil
}
func (f *fakeStore) Delete(context.Context, string) error { f.ok = false; return nil }

func TestRefillUsesRemainingTTL(t *testing.T) {
	clock := ttlcache.NewManualClock(time.Unix(1, 0))
	l1 := ttlcache.New(ttlcache.Config[string, string]{Clock: clock, DefaultTTL: time.Hour})
	defer func() { _ = l1.Close() }()
	l2 := &fakeStore{value: "body", ttl: 3 * time.Second, ok: true}
	c := New[string, string](l1, l2, RedisFirst)
	v, ok, err := c.Get(context.Background(), "url")
	if err != nil || !ok || v != "body" {
		t.Fatalf("get=%q %v %v", v, ok, err)
	}
	clock.Advance(4 * time.Second)
	if _, ok := l1.Get("url"); ok {
		t.Fatal("L1 outlived L2 remaining TTL")
	}
}
