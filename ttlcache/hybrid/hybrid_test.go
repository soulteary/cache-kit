package hybrid

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/soulteary/cache-kit/v2/ttlcache"
)

type fakeStore struct {
	value  string
	ttl    time.Duration
	ok     bool
	sets   int
	getErr error
	setErr error
	delErr error
}

func (f *fakeStore) Get(context.Context, string) (string, time.Duration, bool, error) {
	return f.value, f.ttl, f.ok, f.getErr
}
func (f *fakeStore) Set(_ context.Context, _ string, v string, ttl time.Duration) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.value = v
	f.ttl = ttl
	f.ok = true
	f.sets++
	return nil
}
func (f *fakeStore) Delete(context.Context, string) error {
	f.ok = false
	return f.delErr
}

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

func TestGetPaths(t *testing.T) {
	ctx := context.Background()
	t.Run("L1 hit", func(t *testing.T) {
		l1 := ttlcache.New(ttlcache.DefaultConfig[string, string]())
		defer func() { _ = l1.Close() }()
		if err := l1.Set("x", "memory", time.Minute); err != nil {
			t.Fatal(err)
		}
		c := New(l1, &fakeStore{getErr: errors.New("must not run")}, 0)
		got, ok, err := c.Get(ctx, "x")
		if err != nil || !ok || got != "memory" || c.Order != RedisFirst {
			t.Fatalf("Get = %q, %v, %v; order=%v", got, ok, err, c.Order)
		}
	})
	t.Run("L2 miss error and expired", func(t *testing.T) {
		for _, l2 := range []*fakeStore{
			{},
			{getErr: errors.New("get")},
			{value: "old", ok: true, ttl: 0},
		} {
			l1 := ttlcache.New(ttlcache.DefaultConfig[string, string]())
			c := New(l1, l2, RedisFirst)
			_, ok, err := c.Get(ctx, "x")
			if l2.getErr != nil && !errors.Is(err, l2.getErr) {
				t.Fatalf("Get error = %v", err)
			}
			if l2.getErr == nil && (err != nil || ok) {
				t.Fatalf("Get = ok %v, err %v", ok, err)
			}
			_ = c.Close()
		}
	})
}

func TestSetWriteOrdersAndPartialErrors(t *testing.T) {
	ctx := context.Background()
	backendErr := errors.New("backend")
	rollbackErr := errors.New("rollback")

	t.Run("RedisFirst success", func(t *testing.T) {
		l1 := ttlcache.New(ttlcache.DefaultConfig[string, string]())
		c := New(l1, &fakeStore{}, RedisFirst)
		if err := c.Set(ctx, "x", "value", time.Minute); err != nil {
			t.Fatal(err)
		}
		if got, ok := l1.Get("x"); !ok || got != "value" {
			t.Fatalf("L1 = %q, %v", got, ok)
		}
		_ = c.Close()
	})
	t.Run("RedisFirst L2 failure", func(t *testing.T) {
		l1 := ttlcache.New(ttlcache.DefaultConfig[string, string]())
		c := New(l1, &fakeStore{setErr: backendErr}, RedisFirst)
		if err := c.Set(ctx, "x", "value", time.Minute); !errors.Is(err, backendErr) {
			t.Fatalf("Set error = %v", err)
		}
		_ = c.Close()
	})
	t.Run("RedisFirst L1 failure and rollback", func(t *testing.T) {
		l1 := ttlcache.New(ttlcache.Config[string, string]{DefaultTTL: time.Minute, MaxItemCost: 1, Cost: func(string, string) int64 { return 2 }})
		c := New(l1, &fakeStore{delErr: rollbackErr}, RedisFirst)
		err := c.Set(ctx, "x", "value", time.Minute)
		var partial *PartialError
		if !errors.As(err, &partial) || !errors.Is(err, ttlcache.ErrItemTooLarge) || !errors.Is(partial.Rollback, rollbackErr) || !strings.Contains(err.Error(), "rollback") {
			t.Fatalf("Set error = %#v", err)
		}
		_ = c.Close()
	})
	t.Run("MemoryFirst", func(t *testing.T) {
		l1 := ttlcache.New(ttlcache.DefaultConfig[string, string]())
		c := New(l1, &fakeStore{setErr: backendErr}, MemoryFirst)
		err := c.Set(ctx, "x", "value", time.Minute)
		var partial *PartialError
		if !errors.As(err, &partial) || !errors.Is(err, backendErr) || partial.Unwrap() != backendErr {
			t.Fatalf("Set error = %#v", err)
		}
		if got, ok := l1.Get("x"); !ok || got != "value" {
			t.Fatalf("L1 = %q, %v", got, ok)
		}
		_ = c.Close()
	})
}

func TestDeletePartialError(t *testing.T) {
	l1 := ttlcache.New(ttlcache.DefaultConfig[string, string]())
	c := New(l1, &fakeStore{ok: true, delErr: errors.New("delete")}, RedisFirst)
	if err := l1.Set("x", "value", time.Minute); err != nil {
		t.Fatal(err)
	}
	err := c.Delete(context.Background(), "x")
	var partial *PartialError
	if !errors.As(err, &partial) || partial.Operation != "delete" {
		t.Fatalf("Delete error = %#v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}
