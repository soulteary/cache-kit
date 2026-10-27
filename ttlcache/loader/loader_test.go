package loader

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/soulteary/cache-kit/v2/ttlcache"
)

func TestCoalescesConcurrentMisses(t *testing.T) {
	l := New[string, int](Config{})
	defer func() { _ = l.Close() }()
	var calls atomic.Int32
	start := make(chan struct{})
	load := func(context.Context, string) (int, error) { calls.Add(1); <-start; return 42, nil }
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := l.GetOrLoad(context.Background(), "x", load, Policy{FreshTTL: time.Minute})
			if err != nil || got.Value != 42 {
				t.Errorf("result=%+v err=%v", got, err)
			}
		}()
	}
	time.Sleep(10 * time.Millisecond)
	close(start)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("loader calls=%d", calls.Load())
	}
}

func TestFreshThenStaleRefresh(t *testing.T) {
	clock := ttlcache.NewManualClock(time.Unix(1, 0))
	l := New[string, int](Config{Clock: clock, MaxRefresh: 1})
	defer func() { _ = l.Close() }()
	var calls atomic.Int32
	load := func(context.Context, string) (int, error) { return int(calls.Add(1)), nil }
	p := Policy{FreshTTL: time.Second, StaleTTL: time.Minute}
	first, err := l.GetOrLoad(context.Background(), "x", load, p)
	if err != nil || first.Value != 1 {
		t.Fatalf("first=%+v %v", first, err)
	}
	clock.Advance(2 * time.Second)
	stale, err := l.GetOrLoad(context.Background(), "x", load, p)
	if err != nil || !stale.Stale || stale.Value != 1 {
		t.Fatalf("stale=%+v %v", stale, err)
	}
	deadline := time.Now().Add(time.Second)
	for calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if calls.Load() != 2 {
		t.Fatalf("refresh calls=%d", calls.Load())
	}
}

func TestWaiterCancellation(t *testing.T) {
	l := New[string, int](Config{})
	defer func() { _ = l.Close() }()
	block := make(chan struct{})
	go l.GetOrLoad(context.Background(), "x", func(context.Context, string) (int, error) { <-block; return 1, nil }, Policy{FreshTTL: time.Minute})
	time.Sleep(time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := l.GetOrLoad(ctx, "x", func(context.Context, string) (int, error) { return 2, nil }, Policy{FreshTTL: time.Minute})
	if err != context.Canceled {
		t.Fatalf("err=%v", err)
	}
	close(block)
}

func TestCloseCancelsBackgroundRefresh(t *testing.T) {
	clock := ttlcache.NewManualClock(time.Unix(1, 0))
	l := New[string, int](Config{Clock: clock, MaxRefresh: 1})
	p := Policy{FreshTTL: time.Second, StaleTTL: time.Minute}
	_, err := l.GetOrLoad(context.Background(), "x", func(context.Context, string) (int, error) { return 1, nil }, p)
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * time.Second)
	started := make(chan struct{})
	_, err = l.GetOrLoad(context.Background(), "x", func(ctx context.Context, _ string) (int, error) {
		close(started)
		<-ctx.Done()
		return 0, ctx.Err()
	}, p)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	done := make(chan struct{})
	go func() { _ = l.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel refresh")
	}
}
