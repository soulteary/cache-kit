package cache

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	alphafoo "github.com/soulteary/cache-kit/internal/hashtypes/alpha/foo"
	betafoo "github.com/soulteary/cache-kit/internal/hashtypes/beta/foo"
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

// TestHashDistinguishesConcreteTypes is the regression test for following an
// interface without recording what it held. any(int(1)) and any(int64(1)) are
// observably different through Get and GetAll but encoded identically, as did
// two unrelated struct types with the same exported shape -- so swapping one
// for the other left GetHash() unchanged.
func TestHashDistinguishesConcreteTypes(t *testing.T) {
	type shapeA struct{ Name string }
	type shapeB struct{ Name string }

	t.Run("scalars behind any", func(t *testing.T) {
		if defaultHashFunc([]any{int(1)}) == defaultHashFunc([]any{int64(1)}) {
			t.Error("any(int(1)) and any(int64(1)) hash the same")
		}
		if defaultHashFunc([]any{int32(1)}) == defaultHashFunc([]any{uint32(1)}) {
			t.Error("any(int32(1)) and any(uint32(1)) hash the same")
		}
		if defaultHashFunc([]any{"1"}) == defaultHashFunc([]any{[]byte("1")}) {
			t.Error(`any("1") and any([]byte("1")) hash the same`)
		}
	})

	t.Run("struct types with one shape", func(t *testing.T) {
		if defaultHashFunc([]any{shapeA{Name: "x"}}) == defaultHashFunc([]any{shapeB{Name: "x"}}) {
			t.Error("two struct types with the same exported shape hash the same")
		}
	})

	t.Run("in an any-typed field", func(t *testing.T) {
		type holder struct{ V any }
		if defaultHashFunc([]holder{{V: int(1)}}) == defaultHashFunc([]holder{{V: int64(1)}}) {
			t.Error("an any field holding int(1) and int64(1) hashes the same")
		}
	})

	// Identical values still agree. The second slice is built separately so
	// staticcheck does not read this as comparing one expression with itself.
	var boxed any = int64(1)
	if defaultHashFunc([]any{int64(1)}) != defaultHashFunc([]any{boxed}) {
		t.Error("the hash is not stable for identical values")
	}
}

// TestHashCoversTimesOutsideTheUnixNanoRange is the regression test for
// encoding time.Time with UnixNano, which is undefined before 1678 and after
// 2262. The zero time is one such value, so distinct instants could encode
// alike and the same instant could encode differently elsewhere.
func TestHashCoversTimesOutsideTheUnixNanoRange(t *testing.T) {
	type holder struct{ When time.Time }

	var zero time.Time
	far := time.Date(2600, 1, 1, 0, 0, 0, 0, time.UTC)
	farPlus := far.Add(time.Hour)
	old := time.Date(1600, 1, 1, 0, 0, 0, 0, time.UTC)

	// UnixNano wraps modulo 2^64, so two instants exactly 2^64 nanoseconds --
	// about 584.9 years -- apart produce the SAME value. One of these is
	// inside the representable window and the other is not.
	wrapA := time.Date(1800, 1, 1, 0, 0, 0, 0, time.UTC)
	wrapB := wrapA.Add(time.Duration(math.MaxInt64)).Add(time.Duration(math.MaxInt64)).Add(2)

	for _, tc := range []struct {
		name string
		a, b time.Time
	}{
		{"instants 2^64ns apart, indistinguishable to UnixNano", wrapA, wrapB},
		{"zero vs a far future instant", zero, far},
		{"two far future instants", far, farPlus},
		{"a pre-1678 instant vs the zero time", old, zero},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if defaultHashFunc([]holder{{When: tc.a}}) == defaultHashFunc([]holder{{When: tc.b}}) {
				t.Errorf("%s and %s hash the same", tc.a, tc.b)
			}
		})
	}

	// Equal instants in different locations still agree.
	at := time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC)
	if defaultHashFunc([]holder{{When: at}}) != defaultHashFunc([]holder{{When: at.Local()}}) {
		t.Error("the same instant in two locations hashed differently")
	}
}

// TestHashCoversValuesUnderNaNMapKeys is the regression test for reading map
// values with MapIndex. MapKeys returns a NaN key, but NaN is not equal to
// itself, so MapIndex(key) came back invalid and the value was encoded as
// "nil;" -- changing only that value left GetHash() unchanged.
func TestHashCoversValuesUnderNaNMapKeys(t *testing.T) {
	type holder struct{ M map[float64]string }
	nan := math.NaN()

	a := holder{M: map[float64]string{nan: "before"}}
	b := holder{M: map[float64]string{nan: "after"}}

	if defaultHashFunc([]holder{a}) == defaultHashFunc([]holder{b}) {
		t.Error("changing the value stored under a NaN key did not change the hash")
	}
}

// --- Codex review round 4 (PR #3) ---

// promotedID is an unexported struct embedded below. Its exported field is
// promoted, so callers read it as x.ID.
type promotedID struct{ ID string }

type withPromoted struct {
	promotedID
	Name string
}

// TestHashCoversPromotedExportedFields is the regression test for skipping an
// anonymous UNEXPORTED embed. Its own exported fields are promoted, so a
// caller reads them off the public type -- but the encoder skipped the whole
// embedded value, and a change confined to one left GetHash() unchanged.
func TestHashCoversPromotedExportedFields(t *testing.T) {
	a := withPromoted{promotedID: promotedID{ID: "1"}, Name: "x"}
	b := withPromoted{promotedID: promotedID{ID: "2"}, Name: "x"}

	if defaultHashFunc([]withPromoted{a}) == defaultHashFunc([]withPromoted{b}) {
		t.Error("a change to a promoted exported field did not change the hash")
	}
	if a.ID == b.ID {
		t.Fatal("the fixture does not actually promote ID")
	}
}

// TestHashDistinguishesNaNPayloads is the regression test for encoding floats
// with %v, which renders every NaN as the same token. The value Get hands back
// keeps its sign and payload bits, so a caller reading math.Float64bits could
// see a change the hash did not.
func TestHashDistinguishesNaNPayloads(t *testing.T) {
	type holder struct {
		F   float64
		F32 float32
		C   complex128
	}

	quiet := math.Float64frombits(0x7FF8000000000001)
	other := math.Float64frombits(0x7FF8000000000002)
	if !math.IsNaN(quiet) || !math.IsNaN(other) {
		t.Fatal("the fixture values are not NaN")
	}

	if defaultHashFunc([]holder{{F: quiet}}) == defaultHashFunc([]holder{{F: other}}) {
		t.Error("two distinct NaN payloads hashed the same")
	}

	// Signed zero is a real difference too, and %v hides it.
	if defaultHashFunc([]holder{{F: 0}}) == defaultHashFunc([]holder{{F: math.Copysign(0, -1)}}) {
		t.Error("+0 and -0 hashed the same")
	}

	// float32 at its own width.
	f32a := math.Float32frombits(0x7FC00001)
	f32b := math.Float32frombits(0x7FC00002)
	if defaultHashFunc([]holder{{F32: f32a}}) == defaultHashFunc([]holder{{F32: f32b}}) {
		t.Error("two distinct float32 NaN payloads hashed the same")
	}

	// And each complex component.
	if defaultHashFunc([]holder{{C: complex(quiet, 1)}}) == defaultHashFunc([]holder{{C: complex(other, 1)}}) {
		t.Error("distinct NaN payloads in a complex real part hashed the same")
	}
}

// TestTypeTagsUseTheImportPath is the regression test for identifying types
// with reflect.Type.String(), which shortens a named type to "pkg.Name" and is
// documented as not unique: two dependencies declaring the same name under the
// same package name render alike.
func TestTypeTagsUseTheImportPath(t *testing.T) {
	// Through writeTypeTag, so this covers the encoder's call site and not
	// just the helper.
	var sb strings.Builder
	writeTypeTag(&sb, reflect.ValueOf(withPromoted{}))
	tag := sb.String()
	if !strings.Contains(tag, "soulteary/cache-kit") {
		t.Errorf("type tag = %q, want it to carry the import path", tag)
	}
	if strings.Contains(tag, reflect.TypeOf(withPromoted{}).String()+">") {
		t.Errorf("type tag = %q is String()'s shortened spelling; the path is not being used", tag)
	}

	// Unnamed types have no path and fall back to the structural spelling.
	if got := typeName(reflect.TypeOf([]int{})); got != "[]int" {
		t.Errorf("typeName([]int) = %q, want []int", got)
	}
}

// --- Codex review round 6 (PR #3) ---

type hashInner struct{ ID string }

// hashOuterPtr embeds a POINTER to an unexported struct. Go promotes its
// fields exactly as it does for a value embed, so v.ID reads here too.
type hashOuterPtr struct{ *hashInner }

// TestPromotedFieldsThroughPointerEmbedding is the regression test for the
// promoted-field traversal testing only for Kind() == Struct.
//
// An anonymous *hashInner has Kind() == Pointer, so the encoder skipped the
// embed entirely and two values differing only in the promoted v.ID -- public
// state every caller can read -- produced the same hash.
func TestPromotedFieldsThroughPointerEmbedding(t *testing.T) {
	a := defaultHashFunc([]any{hashOuterPtr{&hashInner{ID: "a"}}})
	b := defaultHashFunc([]any{hashOuterPtr{&hashInner{ID: "b"}}})
	if a == b {
		t.Errorf("a change to a field promoted through a pointer embed did not change the hash (%s)", a)
	}

	// A nil embed is distinct from a populated one, and does not panic.
	nilEmbed := defaultHashFunc([]any{hashOuterPtr{}})
	if nilEmbed == a || nilEmbed == b {
		t.Error("a nil pointer embed hashed the same as a populated one")
	}
}

type hiddenTime = time.Time

// hashOuterTime embeds an unexported ALIAS of time.Time, so time.Time's
// methods are promoted and the instant is ordinary public state.
type hashOuterTime struct {
	hiddenTime
	Name string
}

// TestEmbeddedTimeAliasDoesNotPanic is the regression test for calling
// Interface() on a value reached through an unexported embedded field.
//
// reflect marks such a value read-only and Interface() panics on it, so the
// round-5 promoted-field traversal turned this shape into a panic inside Set
// -- a crash, not a wrong hash.
func TestEmbeddedTimeAliasDoesNotPanic(t *testing.T) {
	first := time.Unix(1700000000, 123)
	second := time.Unix(1800000000, 456)

	var a, b string
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("hashing an embedded time alias panicked: %v", r)
			}
		}()
		a = defaultHashFunc([]any{hashOuterTime{first, "n"}})
		b = defaultHashFunc([]any{hashOuterTime{second, "n"}})
	}()

	if a == b {
		t.Error("two different embedded instants hashed the same")
	}

	// The instant is read, not approximated: the same instant in another
	// location and carrying a monotonic reading must still hash alike.
	utc := defaultHashFunc([]any{hashOuterTime{first.UTC(), "n"}})
	if utc != a {
		t.Error("the same embedded instant hashed differently across locations")
	}
}

// TestCompoundTypeTagsQualifyNamedComponents is the regression test for
// reflect.Type.String() being used as the fallback spelling.
//
// String() shortens package names inside compound types too, so two
// dependencies both named "foo" at different import paths, each declaring
// `type ID int`, made struct{ X foo.ID } spell identically for either -- and
// since the field is then encoded as its integer value, equal values hashed
// alike despite having different concrete types.
func TestCompoundTypeTagsQualifyNamedComponents(t *testing.T) {
	alphaValue := struct{ X alphafoo.ID }{1}
	betaValue := struct{ X betafoo.ID }{1}

	// The premise: the two types are distinct but spell the same.
	alphaType := reflect.TypeOf(alphaValue)
	betaType := reflect.TypeOf(betaValue)
	if alphaType == betaType {
		t.Fatal("the two struct types are identical; the test proves nothing")
	}
	if alphaType.String() != betaType.String() {
		t.Skipf("reflect no longer shortens these alike (%s vs %s)", alphaType, betaType)
	}

	if a, b := defaultHashFunc([]any{alphaValue}), defaultHashFunc([]any{betaValue}); a == b {
		t.Errorf("two distinct struct types with equal values hashed alike (%s)", a)
	}

	// Through the encoder's own entry point, not typeName directly.
	var sb strings.Builder
	writeTypeTag(&sb, reflect.ValueOf([]alphafoo.ID{1}))
	if !strings.Contains(sb.String(), "internal/hashtypes/alpha/foo.ID") {
		t.Errorf("the tag for []foo.ID is %q, want the component's import path", sb.String())
	}

	sb.Reset()
	writeTypeTag(&sb, reflect.ValueOf(map[alphafoo.ID]alphafoo.ID{1: 2}))
	if !strings.Contains(sb.String(), "internal/hashtypes/alpha/foo.ID") {
		t.Errorf("the tag for map[foo.ID]foo.ID is %q, want the component's import path", sb.String())
	}
}

// --- Codex review round 7 (PR #3) ---

// TestEmbeddedTimeThroughContainers is the regression test for the addressable
// copy being taken only at the root.
//
// Addressability propagates through struct fields, pointer dereferences and
// slice elements, but two containers break the chain: the dynamic value behind
// an interface and a map value are never addressable. readableValue then had
// nothing to work with, so an unexported embedded time.Time reached through an
// `any` field or a map value encoded as the constant "t?;" -- and two
// different instants, both publicly readable through the promoted methods,
// hashed alike.
func TestEmbeddedTimeThroughContainers(t *testing.T) {
	first := time.Unix(1700000000, 123)
	second := time.Unix(1800000000, 456)

	type container struct {
		Any   any
		Map   map[string]hashOuterTime
		Slice []hashOuterTime
		Ptr   *hashOuterTime
	}

	for _, tc := range []struct {
		name       string
		with       func(time.Time) container
		wantDiffer bool
	}{
		{"interface field", func(at time.Time) container {
			return container{Any: hashOuterTime{at, "n"}}
		}, true},
		{"map value", func(at time.Time) container {
			return container{Map: map[string]hashOuterTime{"k": {at, "n"}}}
		}, true},
		{"map key", func(at time.Time) container {
			return container{Any: map[hashOuterTime]string{{at, "n"}: "v"}}
		}, true},
		// These two were already fine; they guard against a fix that trades
		// one path for another.
		{"slice element", func(at time.Time) container {
			return container{Slice: []hashOuterTime{{at, "n"}}}
		}, true},
		{"pointer", func(at time.Time) container {
			return container{Ptr: &hashOuterTime{at, "n"}}
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := defaultHashFunc([]any{tc.with(first)})
			b := defaultHashFunc([]any{tc.with(second)})
			if (a != b) != tc.wantDiffer {
				t.Errorf("hashes differ = %v, want %v: the embedded instant was lost through this container", a != b, tc.wantDiffer)
			}

			// And the instant is READ, not approximated: the same instant in
			// another location still hashes alike.
			if utc := defaultHashFunc([]any{tc.with(first.UTC())}); utc != a {
				t.Error("the same embedded instant hashed differently across locations")
			}
		})
	}
}

// TestNestedInterfaceKeepsEmbeddedTime: the chain has to survive more than one
// container hop.
func TestNestedInterfaceKeepsEmbeddedTime(t *testing.T) {
	build := func(at time.Time) any {
		return map[string]any{"outer": []any{hashOuterTime{at, "n"}}}
	}
	a := defaultHashFunc([]any{build(time.Unix(1700000000, 0))})
	b := defaultHashFunc([]any{build(time.Unix(1800000000, 0))})
	if a == b {
		t.Error("an embedded instant behind map -> interface -> slice -> interface hashed the same")
	}
}

// TestPointerKeyedMapsAreNotFullyDistinguished pins a LIMIT, not a bug.
//
// Go compares map keys by identity, so two distinct *int both addressing 1 are
// different keys; a content hash sees only what they point at, so the two maps
// below are indistinguishable to it even though m[a] differs between them.
// Hashing the addresses would separate them and would also make the digest
// differ between runs of the same program, which is the one property this hash
// cannot give up.
//
// The test exists so that a future change which appears to "fix" this is
// recognised for what it would cost. A type relying on pointer identity in map
// keys needs its own HashFunc, as Config.HashFunc documents.
func TestPointerKeyedMapsAreNotFullyDistinguished(t *testing.T) {
	one, uno := 1, 1
	a, b := &one, &uno

	swapped := defaultHashFunc([]any{map[*int]string{a: "x", b: "y"}}) ==
		defaultHashFunc([]any{map[*int]string{a: "y", b: "x"}})
	if !swapped {
		t.Error("pointer-keyed maps with equal pointees are now distinguished -- " +
			"confirm the digest is still identical across separate runs of the same binary")
	}

	// What the hash CAN do, and must keep doing: distinguish them whenever the
	// keys differ in content.
	two := 2
	c := &two
	if defaultHashFunc([]any{map[*int]string{a: "x", c: "y"}}) ==
		defaultHashFunc([]any{map[*int]string{a: "y", c: "x"}}) {
		t.Error("pointer keys with DIFFERENT pointees were not distinguished")
	}

	// And the digest is stable for the same content, which is the property
	// being protected.
	if defaultHashFunc([]any{map[*int]string{a: "x"}}) !=
		defaultHashFunc([]any{map[*int]string{b: "x"}}) {
		t.Error("the same content behind different addresses hashed differently; the digest is not reproducible")
	}
}
