package cache

import (
	"testing"
	"time"
)

type user struct {
	ID    string
	Email string
	Seen  time.Time
}

func userConfig() *Config[*user] {
	return DefaultConfig[*user]().WithPrimaryKey(func(u *user) string { return u.ID })
}

// TestHashIsContentBasedForPointers is the regression test for the default
// hash using fmt's %v: for a pointer element type that prints the ADDRESS, so
// the hash tracked memory layout instead of contents and change detection
// fired on every restart or reallocation.
func TestHashIsContentBasedForPointers(t *testing.T) {
	c1 := NewMultiIndexCache(userConfig())
	c2 := NewMultiIndexCache(userConfig())

	// Equal contents held at different addresses must hash equally.
	c1.Set([]*user{{ID: "1", Email: "a@x.com"}, {ID: "2", Email: "b@x.com"}})
	c2.Set([]*user{{ID: "1", Email: "a@x.com"}, {ID: "2", Email: "b@x.com"}})

	if c1.GetHash() != c2.GetHash() {
		t.Fatalf("equal contents hashed differently: %s vs %s", c1.GetHash(), c2.GetHash())
	}

	// Different contents must not.
	c2.Set([]*user{{ID: "1", Email: "a@x.com"}, {ID: "2", Email: "CHANGED@x.com"}})
	if c1.GetHash() == c2.GetHash() {
		t.Error("different contents produced the same hash")
	}
}

// TestHashSeparatorCannotBeForged: records are length-prefixed, so a value
// containing the old "\n" separator cannot imitate two records.
func TestHashSeparatorCannotBeForged(t *testing.T) {
	a := defaultHashFunc([]string{"one\ntwo"})
	b := defaultHashFunc([]string{"one", "two"})
	if a == b {
		t.Error("a value containing the record separator collides with two values")
	}
}

// TestClearAndEmptySetAgreeOnHash: the same (empty) state must have the same
// hash however it was reached, or change detection reports a phantom change.
func TestClearAndEmptySetAgreeOnHash(t *testing.T) {
	c := NewMultiIndexCache(userConfig())
	c.Set([]*user{{ID: "1"}})

	c.Clear()
	afterClear := c.GetHash()

	c.Set(nil)
	afterEmptySet := c.GetHash()

	if afterClear != afterEmptySet {
		t.Errorf("Clear() hash %q != Set(nil) hash %q for an equally empty cache", afterClear, afterEmptySet)
	}
	if afterClear == "" {
		t.Error("Clear() left an empty hash rather than the hash of an empty cache")
	}
}

// TestIterateCallbackCanTouchTheCache is the regression test for holding the
// read lock across the callback: any callback that called back into the cache
// deadlocked, because Go's RWMutex is not reentrant and a waiting writer
// blocks further RLock attempts.
func TestIterateCallbackCanTouchTheCache(t *testing.T) {
	c := NewMultiIndexCache(userConfig())
	c.Set([]*user{{ID: "1"}, {ID: "2"}, {ID: "3"}})

	done := make(chan struct{})
	go func() {
		defer close(done)
		count := 0
		c.Iterate(func(*user) bool {
			count++
			_ = c.Len()       // read
			_, _ = c.Get("1") // read
			return true
		})
		if count != 3 {
			t.Errorf("visited %d values, want 3", count)
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Iterate deadlocked when the callback touched the cache")
	}
}

// TestIndexConflictIsReported: two values whose index keys normalize to the
// same string silently shadow each other, so a lookup can return the wrong
// record. The conflict is now observable.
func TestIndexConflictIsReported(t *testing.T) {
	var conflicts []string
	cfg := userConfig()
	cfg.OnIndexConflict = func(indexName, key, existing, next string) {
		conflicts = append(conflicts, indexName+":"+key+":"+existing+"->"+next)
	}

	c := NewMultiIndexCache(cfg)
	c.AddIndex("email", func(u *user) string { return u.Email })
	// "A@X.com" and "a@x.com" normalize to the same index key.
	c.Set([]*user{{ID: "1", Email: "A@X.com"}, {ID: "2", Email: "a@x.com"}})

	if len(conflicts) == 0 {
		t.Fatal("two values collided on one index key with no notification")
	}

	// The later value wins, which is the documented behaviour.
	got, ok := c.GetByIndex("email", "a@x.com")
	if !ok || got.ID != "2" {
		t.Errorf("GetByIndex returned %v (ok=%v), want the last writer", got, ok)
	}
}
