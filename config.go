package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
	"time"
)

// Config holds configuration for the cache.
type Config[V any] struct {
	// PrimaryKeyFunc extracts the primary key from a value.
	// This is required for multi-index cache.
	PrimaryKeyFunc KeyFunc[V]

	// HashFunc computes a hash for the cache contents.
	//
	// If nil, defaultHashFunc is used. It walks values with reflection and
	// covers every EXPORTED field regardless of json tags or a custom
	// MarshalJSON, so state hidden from the wire format still participates in
	// change detection. Types whose identity lives in unexported fields,
	// channels or funcs need a HashFunc of their own.
	//
	// Warning: the default is not suitable for types containing sensitive
	// fields (passwords, tokens), as those would be included in the hash
	// input. Types JSON cannot represent -- channels, funcs, cycles, or types
	// whose identity lives in unexported fields -- need a custom HashFunc.
	// Use WithHashFunc to supply one (e.g. only stable, non-sensitive fields
	// in a deterministic order).
	HashFunc HashFunc[V]

	// ValidateFunc validates a value before storing.
	// If nil, all values are accepted.
	ValidateFunc ValidateFunc[V]

	// NormalizeFunc normalizes a value before storing.
	// If nil, values are stored as-is.
	NormalizeFunc NormalizeFunc[V]

	// SortFunc is used for deterministic hash calculation.
	// If nil, values are hashed in insertion order.
	SortFunc func(values []V) []V

	// OnIndexConflict, if set, is called when two values map to the same
	// normalized index key. The later value wins and the earlier one becomes
	// unreachable through that index, which is otherwise entirely silent --
	// a lookup by email can return a different record than the one intended
	// when two entries differ only in case or surrounding whitespace.
	OnIndexConflict func(indexName, key, existingPrimaryKey, newPrimaryKey string)
}

// DefaultConfig returns a default configuration.
// Note: PrimaryKeyFunc must be set before use with MultiIndexCache.
func DefaultConfig[V any]() *Config[V] {
	return &Config[V]{
		HashFunc: defaultHashFunc[V],
	}
}

// WithPrimaryKey sets the primary key extraction function.
func (c *Config[V]) WithPrimaryKey(fn KeyFunc[V]) *Config[V] {
	c.PrimaryKeyFunc = fn
	return c
}

// WithHashFunc sets a custom hash function.
func (c *Config[V]) WithHashFunc(fn HashFunc[V]) *Config[V] {
	c.HashFunc = fn
	return c
}

// WithValidateFunc sets a validation function.
func (c *Config[V]) WithValidateFunc(fn ValidateFunc[V]) *Config[V] {
	c.ValidateFunc = fn
	return c
}

// WithNormalizeFunc sets a normalization function.
func (c *Config[V]) WithNormalizeFunc(fn NormalizeFunc[V]) *Config[V] {
	c.NormalizeFunc = fn
	return c
}

// WithSortFunc sets a sort function for deterministic hashing.
func (c *Config[V]) WithSortFunc(fn func(values []V) []V) *Config[V] {
	c.SortFunc = fn
	return c
}

// defaultHashFunc provides a simple hash implementation using fmt.Sprintf("%v", v) per value.
// See Config.HashFunc documentation for sensitivity and determinism caveats.
// emptyHash is the hash of a cache holding no values. Clear and Set(nil) must
// produce the same value for change detection to be meaningful.
func emptyHash() string { return sha256Hash("empty") }

// defaultHashFunc computes a content hash over the cached values.
//
// It walks each value with reflection rather than using fmt's %v or
// json.Marshal.
//
// %v prints a POINTER as its address, so a MemoryCache[*User] hashed its
// memory layout instead of its contents: every process restart, and every
// reallocation, produced a different hash and change detection reported a
// change that had not happened. time.Time carries a monotonic reading under
// %v with the same effect.
//
// json.Marshal fixed that but introduced a quieter problem: it honours
// `json:"-"` and custom MarshalJSON, so state deliberately kept out of the
// wire format was also kept out of the HASH. Two values differing only in an
// exported `Password string `json:"-"“ field are observably different
// through Get and GetAll yet hash identically, so a real change goes
// undetected -- which is worse than a spurious one. The encoding below
// includes every exported field whatever its tags say.
//
// Each field and element is length-prefixed, so no combination of values can
// produce the same byte string as a different combination.
//
// Values whose identity lives in unexported fields, channels or funcs still
// need a custom HashFunc; they contribute only their type here.
func defaultHashFunc[V any](values []V) string {
	if len(values) == 0 {
		return emptyHash()
	}

	var sb strings.Builder
	for _, v := range values {
		rv := reflect.ValueOf(v)
		// The CONCRETE type, stamped here because reflect.ValueOf unwraps an
		// interface: when V is `any`, int(1) and int64(1) arrive as plain Int
		// and Int64 kinds whose encodings are otherwise identical.
		writeTypeTag(&sb, rv)
		encodeForHash(&sb, rv)
		sb.WriteByte(0x1e) // record separator
	}
	return sha256Hash(sb.String())
}

// timeType is compared against so time.Time hashes by instant rather than by
// its internal representation, which carries a monotonic reading.
var timeType = reflect.TypeOf(time.Time{})

// writeTypeTag records v's concrete type.
//
// It is written wherever the type would otherwise be invisible to the
// encoding: at the top level and behind an interface, where the static type
// says nothing, and for structs, whose field-by-field encoding two unrelated
// types can share exactly. Elsewhere the static type is fixed by the
// surrounding struct field, slice or map, so the tag would only cost bytes.
func writeTypeTag(sb *strings.Builder, v reflect.Value) {
	if !v.IsValid() {
		sb.WriteString("<nil>")
		return
	}
	name := typeName(v.Type())
	fmt.Fprintf(sb, "<%d:%s>", len(name), name)
}

// typeName identifies a type unambiguously.
//
// reflect.Type.String() shortens a named type to "pkg.Name" and is explicitly
// documented as NOT unique: two dependencies declaring the same type name
// under the same package name -- different import paths, identical spelling --
// render alike, so if their exported shapes also match, swapping one value for
// the other left the hash unchanged. The import PATH disambiguates them.
func typeName(t reflect.Type) string {
	if name := t.Name(); name != "" {
		if pkg := t.PkgPath(); pkg != "" {
			return pkg + "." + name
		}
		return name
	}
	return t.String()
}

// encodeForHash writes a deterministic, length-prefixed encoding of v.
func encodeForHash(sb *strings.Builder, v reflect.Value) {
	if !v.IsValid() {
		sb.WriteString("nil;")
		return
	}

	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		// Follow the pointer: its ADDRESS is not content.
		if v.IsNil() {
			sb.WriteString("nil;")
			return
		}
		if v.Kind() == reflect.Interface {
			// What the interface HOLDS is content. Following it blind made
			// any(int(1)) and any(int64(1)) -- and any two struct types with
			// the same exported shape -- encode identically, so swapping one
			// for the other left GetHash() unchanged.
			writeTypeTag(sb, v.Elem())
		}
		encodeForHash(sb, v.Elem())

	case reflect.Struct:
		if v.Type() == timeType {
			// Wall clock only, and in UTC: the monotonic reading and the
			// location pointer are not content.
			// Seconds plus nanoseconds, never UnixNano: that is undefined
			// outside 1678..2262, and it wraps modulo 2^64, so two instants
			// 584.9 years apart encoded identically and the same instant
			// could encode differently on another implementation. Unix() is
			// valid across the whole range.
			t := v.Interface().(time.Time)
			fmt.Fprintf(sb, "t%d.%09d;", t.UTC().Unix(), t.Nanosecond())
			return
		}
		t := v.Type()
		writeTypeTag(sb, v)
		sb.WriteString("{")
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if f.PkgPath != "" {
				// Unexported, so not part of the API -- EXCEPT an anonymous
				// one, whose own exported fields are promoted and are. A
				// public type embedding an unexported struct exposes v.ID to
				// every caller, and skipping the whole embed meant a value
				// differing only there hashed the same.
				if !f.Anonymous || f.Type.Kind() != reflect.Struct {
					continue
				}
				fmt.Fprintf(sb, "%d:%s=", len(f.Name), f.Name)
				encodeForHash(sb, v.Field(i))
				continue
			}
			// The field NAME is included so renaming or reordering fields
			// cannot collide, and no json tag is consulted.
			fmt.Fprintf(sb, "%d:%s=", len(f.Name), f.Name)
			encodeForHash(sb, v.Field(i))
		}
		sb.WriteString("}")

	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.IsNil() {
			sb.WriteString("nil;")
			return
		}
		fmt.Fprintf(sb, "[%d", v.Len())
		for i := 0; i < v.Len(); i++ {
			sb.WriteByte(',')
			encodeForHash(sb, v.Index(i))
		}
		sb.WriteString("]")

	case reflect.Map:
		if v.IsNil() {
			sb.WriteString("nil;")
			return
		}
		// Map iteration order is randomized, so the keys are sorted by their
		// own encoding to keep the digest stable.
		//
		// MapRange, not MapKeys plus MapIndex: a NaN float or complex key is
		// returned by MapKeys but is not equal to itself, so MapIndex(key)
		// comes back invalid and its value was encoded as "nil;" -- changing
		// that value alone left GetHash() unchanged. The iterator keeps each
		// value paired with its key.
		entries := make([]string, 0, v.Len())
		for iter := v.MapRange(); iter.Next(); {
			var entry strings.Builder
			encodeForHash(&entry, iter.Key())
			entry.WriteByte('=')
			encodeForHash(&entry, iter.Value())
			entries = append(entries, entry.String())
		}
		sort.Strings(entries)
		fmt.Fprintf(sb, "m%d", len(entries))
		for _, e := range entries {
			fmt.Fprintf(sb, ",%d:%s", len(e), e)
		}

	case reflect.String:
		fmt.Fprintf(sb, "%d:%s;", len(v.String()), v.String())

	case reflect.Bool:
		fmt.Fprintf(sb, "b%t;", v.Bool())

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		fmt.Fprintf(sb, "i%d;", v.Int())

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		fmt.Fprintf(sb, "u%d;", v.Uint())

	case reflect.Float32:
		// At its OWN width: widening a float32 NaN to float64 is not
		// guaranteed to carry the payload bits through.
		fmt.Fprintf(sb, "f%08x;", math.Float32bits(float32(v.Float())))

	case reflect.Float64:
		// The IEEE bits, not %v. Every NaN renders as the same "NaN" token,
		// while the value handed back by Get keeps its sign and payload bits
		// -- so a caller reading math.Float64bits saw a change the hash did
		// not. The bits also separate +0 from -0, which %v does not.
		fmt.Fprintf(sb, "f%016x;", math.Float64bits(v.Float()))

	case reflect.Complex64:
		c := v.Complex()
		fmt.Fprintf(sb, "c%08x,%08x;",
			math.Float32bits(float32(real(c))), math.Float32bits(float32(imag(c))))

	case reflect.Complex128:
		c := v.Complex()
		fmt.Fprintf(sb, "c%016x,%016x;", math.Float64bits(real(c)), math.Float64bits(imag(c)))

	default:
		// Channels, funcs and anything else with no content to speak of.
		// Only the TYPE contributes, so such a cache needs a custom HashFunc
		// to detect changes.
		fmt.Fprintf(sb, "?%s;", v.Type().String())
	}
}

// sha256Hash computes SHA256 hash of a string.
func sha256Hash(s string) string {
	h := sha256.New()
	h.Write([]byte(s))
	return hex.EncodeToString(h.Sum(nil))
}

// RedisConfig holds configuration for Redis cache.
type RedisConfig struct {
	// KeyPrefix is prepended to all Redis keys. Use a unique prefix per cache to avoid key collision.
	KeyPrefix string

	// VersionKeySuffix is appended to the key prefix for version tracking.
	// Default: ":version"
	VersionKeySuffix string

	// TTL is the default time-to-live for cached data. Must be positive; otherwise a default is used at Set time.
	// Default: 1 hour
	TTL time.Duration

	// OperationTimeout is the timeout for Redis operations.
	// Default: 5 seconds
	OperationTimeout time.Duration

	// MaxValueBytes limits the size of the value read from Redis in Get(). If <= 0, no limit is applied.
	// Default: 16MB.
	//
	// The size is checked with STRLEN before the value is fetched, so an
	// oversized value is never pulled into memory. Checking len(data) after
	// GET, as this used to, only prevented the unmarshal -- the allocation the
	// limit exists to avoid had already happened.
	MaxValueBytes int
}

// Default max value size for Redis Get (16 MiB).
const defaultRedisMaxValueBytes = 16 * 1024 * 1024

// DefaultRedisConfig returns a default Redis configuration.
func DefaultRedisConfig() *RedisConfig {
	return &RedisConfig{
		KeyPrefix:        "cache:",
		VersionKeySuffix: ":version",
		TTL:              1 * time.Hour,
		OperationTimeout: 5 * time.Second,
		MaxValueBytes:    defaultRedisMaxValueBytes,
	}
}

// WithKeyPrefix sets the key prefix.
func (c *RedisConfig) WithKeyPrefix(prefix string) *RedisConfig {
	c.KeyPrefix = prefix
	return c
}

// WithVersionKeySuffix sets the version key suffix.
func (c *RedisConfig) WithVersionKeySuffix(suffix string) *RedisConfig {
	c.VersionKeySuffix = suffix
	return c
}

// WithTTL sets the TTL.
func (c *RedisConfig) WithTTL(ttl time.Duration) *RedisConfig {
	c.TTL = ttl
	return c
}

// WithOperationTimeout sets the operation timeout.
func (c *RedisConfig) WithOperationTimeout(timeout time.Duration) *RedisConfig {
	c.OperationTimeout = timeout
	return c
}

// WithMaxValueBytes sets the maximum allowed size in bytes for a value read from Redis in Get().
// Values larger than this are rejected to prevent OOM. Use 0 or negative to disable the limit.
func (c *RedisConfig) WithMaxValueBytes(n int) *RedisConfig {
	c.MaxValueBytes = n
	return c
}

// StringSorter provides a helper for sorting slices by a string key.
func StringSorter[V any](keyFunc func(V) string) func([]V) []V {
	return func(values []V) []V {
		result := make([]V, len(values))
		copy(result, values)
		sort.Slice(result, func(i, j int) bool {
			return keyFunc(result[i]) < keyFunc(result[j])
		})
		return result
	}
}
