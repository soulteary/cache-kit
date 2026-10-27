package loader

import (
	"context"
	"errors"
	"strings"
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
	loadErr := make(chan error, 1)
	go func() {
		_, err := l.GetOrLoad(context.Background(), "x", func(context.Context, string) (int, error) { <-block; return 1, nil }, Policy{FreshTTL: time.Minute})
		loadErr <- err
	}()
	time.Sleep(time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := l.GetOrLoad(ctx, "x", func(context.Context, string) (int, error) { return 2, nil }, Policy{FreshTTL: time.Minute})
	if err != context.Canceled {
		t.Fatalf("err=%v", err)
	}
	close(block)
	if err := <-loadErr; err != nil {
		t.Fatalf("load err=%v", err)
	}
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

func TestDelayedMissRechecksCacheBeforeLoading(t *testing.T) {
	l := New[string, int](Config{})
	defer func() { _ = l.Close() }()
	l.mu.Lock()
	const callers = 50
	results := make(chan error, callers)
	var loads atomic.Int32
	for i := 0; i < callers; i++ {
		go func() {
			result, err := l.GetOrLoad(context.Background(), "x", func(context.Context, string) (int, error) {
				loads.Add(1)
				return 2, nil
			}, Policy{FreshTTL: time.Hour})
			if err == nil && result.Value != 1 {
				err = errors.New("unexpected value")
			}
			results <- err
		}()
	}
	deadline := time.Now().Add(time.Second)
	for l.cache.Stats().Misses < callers && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if misses := l.cache.Stats().Misses; misses != callers {
		l.mu.Unlock()
		t.Fatalf("initial misses = %d", misses)
	}
	if err := l.cache.Set("x", stored[int]{value: 1, freshUntil: time.Now().Add(time.Hour)}, time.Hour); err != nil {
		l.mu.Unlock()
		t.Fatal(err)
	}
	l.mu.Unlock()
	for i := 0; i < callers; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if loads.Load() != 0 {
		t.Fatalf("backend loads = %d", loads.Load())
	}
}

func TestInitiatingCallerCanCancelWait(t *testing.T) {
	l := New[string, int](Config{})
	defer func() { _ = l.Close() }()
	started := make(chan struct{})
	release := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := l.GetOrLoad(ctx, "x", func(context.Context, string) (int, error) {
			close(started)
			<-release
			return 7, nil
		}, Policy{FreshTTL: time.Minute})
		done <- err
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("GetOrLoad error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("initiating caller did not observe cancellation")
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for {
		result, err := l.GetOrLoad(context.Background(), "x", func(context.Context, string) (int, error) { return 8, nil }, Policy{FreshTTL: time.Minute})
		if err == nil && result.Value == 7 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("shared load was not cached: result=%+v err=%v", result, err)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestForegroundLoadPreservesValuesAndClosesWithLoader(t *testing.T) {
	type contextKey struct{}
	l := New[string, string](Config{})
	valueSeen := make(chan string, 1)
	result := make(chan error, 1)
	ctx := context.WithValue(context.Background(), contextKey{}, "credential")
	go func() {
		_, err := l.GetOrLoad(ctx, "x", func(loadCtx context.Context, _ string) (string, error) {
			valueSeen <- loadCtx.Value(contextKey{}).(string)
			<-loadCtx.Done()
			return "", loadCtx.Err()
		}, Policy{FreshTTL: time.Minute})
		result <- err
	}()
	if got := <-valueSeen; got != "credential" {
		t.Fatalf("context value = %q", got)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("load error = %v", err)
	}
}

func TestPolicyErrorsPanicAndErrorCaching(t *testing.T) {
	l := New[string, int](Config{})
	defer func() { _ = l.Close() }()
	if _, err := l.GetOrLoad(context.Background(), "bad", nil, Policy{}); err == nil {
		t.Fatal("zero FreshTTL accepted")
	}

	var calls atomic.Int32
	want := errors.New("load failed")
	load := func(context.Context, string) (int, error) {
		calls.Add(1)
		return 0, want
	}
	p := Policy{FreshTTL: time.Minute, ErrorTTL: time.Minute}
	for i := 0; i < 2; i++ {
		if _, err := l.GetOrLoad(context.Background(), "error", load, p); !errors.Is(err, want) {
			t.Fatalf("cached error = %v", err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("error loads = %d", calls.Load())
	}

	_, err := l.GetOrLoad(context.Background(), "panic", func(context.Context, string) (int, error) {
		panic("boom")
	}, Policy{FreshTTL: time.Minute})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("panic error = %v", err)
	}
}

func TestJitterBounds(t *testing.T) {
	if got := jitter(time.Minute, 0); got != time.Minute {
		t.Fatalf("zero jitter = %v", got)
	}
	for _, fraction := range []float64{0.5, 2} {
		for i := 0; i < 100; i++ {
			got := jitter(time.Minute, fraction)
			if got < 0 || got > 2*time.Minute {
				t.Fatalf("jitter(%v) = %v", fraction, got)
			}
		}
	}
}

func TestGetAfterClose(t *testing.T) {
	l := New[string, int](Config{})
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := l.GetOrLoad(context.Background(), "x", func(context.Context, string) (int, error) { return 1, nil }, Policy{FreshTTL: time.Minute}); !errors.Is(err, ttlcache.ErrClosed) {
		t.Fatalf("GetOrLoad error = %v", err)
	}
}
