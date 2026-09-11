package cache

import (
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
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

// --- Codex review follow-ups (PR #3) ---

// TestBoundedGetIsAtomic is the regression test for measuring the value with a
// separate STRLEN before fetching it. Another writer replacing the key between
// the two commands delivered an oversized payload anyway -- and the post-fetch
// length check had been removed, so nothing caught it.
func TestBoundedGetIsAtomic(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = client.Close() }()

	cfg := DefaultRedisConfig()
	cfg.KeyPrefix = "atomic:"
	cfg.MaxValueBytes = 64
	c := NewRedisCache[TestUser](client, cfg)

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

	cfg := DefaultRedisConfig()
	cfg.KeyPrefix = "ver:"
	cfg.TTL = time.Minute
	c := NewRedisCache[TestUser](client, cfg)

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

// TestAddIndexReportsRebuildConflicts: populating the cache before calling
// AddIndex is a supported order, but the rebuild wrote normalized keys
// directly and never invoked OnIndexConflict, so collisions in that flow
// passed silently unlike the ones Set reports.
func TestAddIndexReportsRebuildConflicts(t *testing.T) {
	var conflicts [][4]string
	config := DefaultConfig[TestUser]().
		WithPrimaryKey(func(u TestUser) string { return u.ID })
	config.OnIndexConflict = func(index, key, existing, replacement string) {
		conflicts = append(conflicts, [4]string{index, key, existing, replacement})
	}

	cache := NewMultiIndexCache(config)
	cache.Set([]TestUser{
		{ID: "1", Email: "User@Example.com"},
		{ID: "2", Email: "user@example.com"},
	})

	cache.AddIndex("email", func(u TestUser) string { return u.Email })

	if len(conflicts) == 0 {
		t.Fatal("AddIndex rebuilt the index over colliding values without reporting a conflict")
	}
	got := conflicts[len(conflicts)-1]
	if got[0] != "email" || got[1] != "user@example.com" {
		t.Errorf("conflict = %v, want the email index and the normalized key", got)
	}
	// Insertion order decides the winner, so the report is deterministic.
	if got[2] != "1" || got[3] != "2" {
		t.Errorf("conflict existing/replacement = %q/%q, want 1/2 in insertion order", got[2], got[3])
	}
}

// TestIndexConflictCallbackCanReadTheCache is the regression test for invoking
// OnIndexConflict while holding c.mu: a handler inspecting the cache through
// Get, Len or GetAll blocked forever on the same non-reentrant mutex.
func TestIndexConflictCallbackCanReadTheCache(t *testing.T) {
	done := make(chan struct{})

	var cache *MemoryCache[TestUser]
	config := DefaultConfig[TestUser]().
		WithPrimaryKey(func(u TestUser) string { return u.ID })
	config.OnIndexConflict = func(_, _, _, _ string) {
		// Re-entry: these all take the same lock.
		_ = cache.Len()
		_ = cache.GetAll()
		_, _ = cache.Get("1")
		close(done)
	}

	cache = NewMultiIndexCache(config)
	cache.AddIndex("email", func(u TestUser) string { return u.Email })

	go func() {
		cache.Set([]TestUser{
			{ID: "1", Email: "User@Example.com"},
			{ID: "2", Email: "user@example.com"},
		})
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("OnIndexConflict deadlocked reading the cache: it was called while Set held the lock")
	}
}

// --- Codex review round 2 (PR #3) ---

// hiddenState has an exported field kept out of the wire format, which is
// exactly the shape the JSON-based hash could not see.
type hiddenState struct {
	ID       string
	Password string `json:"-"`
}

// customMarshal hides part of its state behind MarshalJSON.
type customMarshal struct {
	ID     string
	Secret string
}

func (c customMarshal) MarshalJSON() ([]byte, error) {
	return []byte(`{"id":"` + c.ID + `"}`), nil
}

// TestHashCoversJSONHiddenState is the regression test for hashing with
// json.Marshal. It honours `json:"-"` and custom MarshalJSON, so state
// deliberately kept out of the wire format was also kept out of the hash: two
// values differing only there are observably different through Get and GetAll
// yet hashed identically, and a real change went undetected.
func TestHashCoversJSONHiddenState(t *testing.T) {
	tagged := defaultHashFunc([]hiddenState{{ID: "1", Password: "a"}})
	taggedChanged := defaultHashFunc([]hiddenState{{ID: "1", Password: "b"}})
	if tagged == taggedChanged {
		t.Error(`a change to a json:"-" field did not change the hash`)
	}

	custom := defaultHashFunc([]customMarshal{{ID: "1", Secret: "a"}})
	customChanged := defaultHashFunc([]customMarshal{{ID: "1", Secret: "b"}})
	if custom == customChanged {
		t.Error("a change hidden by MarshalJSON did not change the hash")
	}

	// Identical values still hash identically.
	if defaultHashFunc([]hiddenState{{ID: "1", Password: "a"}}) != tagged {
		t.Error("the hash is not stable for identical values")
	}
}

// TestHashIsStableAndContentBased guards the properties the previous rounds
// established: pointers hash by content, time.Time by instant, map order does
// not matter, and distinct contents do not collide.
func TestHashIsStableAndContentBased(t *testing.T) {
	type inner struct {
		Tags map[string]int
		When time.Time
	}
	at := time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC)

	a := &inner{Tags: map[string]int{"x": 1, "y": 2}, When: at}
	b := &inner{Tags: map[string]int{"y": 2, "x": 1}, When: at.Local()}

	if defaultHashFunc([]*inner{a}) != defaultHashFunc([]*inner{b}) {
		t.Error("two equal values hashed differently (map order, location or pointer identity leaked in)")
	}

	// A monotonic reading must not change the hash either.
	now := time.Now()
	if defaultHashFunc([]inner{{When: now}}) != defaultHashFunc([]inner{{When: now.Round(0)}}) {
		t.Error("the monotonic clock reading changed the hash")
	}

	c := &inner{Tags: map[string]int{"x": 1, "y": 3}, When: at}
	if defaultHashFunc([]*inner{a}) == defaultHashFunc([]*inner{c}) {
		t.Error("different contents produced the same hash")
	}

	// Record boundaries: two values must not be confusable with one.
	if defaultHashFunc([]string{"ab", "c"}) == defaultHashFunc([]string{"a", "bc"}) {
		t.Error("record boundaries are not encoded; values can be shifted between records")
	}
}
