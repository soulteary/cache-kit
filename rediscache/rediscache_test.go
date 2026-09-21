package rediscache

import (
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// TestUser is a sample type for testing
type TestUser struct {
	ID    string
	Email string
	Phone string
	Name  string
}

func setupMiniRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("Failed to start miniredis: %v", err)
	}

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})

	t.Cleanup(func() {
		_ = client.Close()
		mr.Close()
	})

	return mr, client
}

func TestRedisCache_BasicOperations(t *testing.T) {
	_, client := setupMiniRedis(t)

	config := DefaultConfig().
		WithKeyPrefix("test:").
		WithTTL(5 * time.Minute)

	cache := New[TestUser](client, config)

	// Test empty cache
	exists, err := cache.Exists()
	if err != nil {
		t.Fatalf("Exists error: %v", err)
	}
	if exists {
		t.Error("Expected empty cache")
	}

	// Test Set
	users := []TestUser{
		{ID: "1", Email: "user1@example.com"},
		{ID: "2", Email: "user2@example.com"},
	}
	if err := cache.Set(users); err != nil {
		t.Fatalf("Set error: %v", err)
	}

	// Test Exists after Set
	exists, err = cache.Exists()
	if err != nil {
		t.Fatalf("Exists error: %v", err)
	}
	if !exists {
		t.Error("Expected cache to exist after Set")
	}

	// Test Get
	got, err := cache.Get()
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("Expected 2 items, got %d", len(got))
	}
	if got[0].Email != "user1@example.com" {
		t.Errorf("Expected user1@example.com, got %s", got[0].Email)
	}

	// Test Clear
	if err := cache.Clear(); err != nil {
		t.Fatalf("Clear error: %v", err)
	}

	exists, err = cache.Exists()
	if err != nil {
		t.Fatalf("Exists error: %v", err)
	}
	if exists {
		t.Error("Expected cache to not exist after Clear")
	}
}

func TestRedisCache_ClearAlsoRemovesVersion(t *testing.T) {
	_, client := setupMiniRedis(t)

	config := DefaultConfig().WithKeyPrefix("test:")
	cache := New[TestUser](client, config)

	if err := cache.Set([]TestUser{{ID: "1"}}); err != nil {
		t.Fatalf("Set error: %v", err)
	}
	versionBefore, err := cache.GetVersion()
	if err != nil {
		t.Fatalf("GetVersion error: %v", err)
	}
	if versionBefore != 1 {
		t.Errorf("Expected version 1 before Clear, got %d", versionBefore)
	}

	if err := cache.Clear(); err != nil {
		t.Fatalf("Clear error: %v", err)
	}

	versionAfter, err := cache.GetVersion()
	if err != nil {
		t.Fatalf("GetVersion error: %v", err)
	}
	if versionAfter != 0 {
		t.Errorf("Expected GetVersion() to return 0 after Clear(), got %d", versionAfter)
	}
}

func TestRedisCache_Version(t *testing.T) {
	_, client := setupMiniRedis(t)

	cache := New[TestUser](client, DefaultConfig())

	// Initial version
	version, err := cache.GetVersion()
	if err != nil {
		t.Fatalf("GetVersion error: %v", err)
	}
	if version != 0 {
		t.Errorf("Expected initial version 0, got %d", version)
	}

	// Version after Set
	if err := cache.Set([]TestUser{{ID: "1"}}); err != nil {
		t.Fatalf("Set error: %v", err)
	}
	version1, err := cache.GetVersion()
	if err != nil {
		t.Fatalf("GetVersion error: %v", err)
	}
	if version1 != 1 {
		t.Errorf("Expected version 1, got %d", version1)
	}

	// Version increments on each Set
	if err := cache.Set([]TestUser{{ID: "2"}}); err != nil {
		t.Fatalf("Set error: %v", err)
	}
	version2, err := cache.GetVersion()
	if err != nil {
		t.Fatalf("GetVersion error: %v", err)
	}
	if version2 != 2 {
		t.Errorf("Expected version 2, got %d", version2)
	}
}

func TestRedisCache_SetWithTTL(t *testing.T) {
	mr, client := setupMiniRedis(t)

	cache := NewWithKey[TestUser](client, "custom:key", DefaultConfig())

	users := []TestUser{{ID: "1"}}
	if err := cache.SetWithTTL(users, 10*time.Second); err != nil {
		t.Fatalf("SetWithTTL error: %v", err)
	}

	// Check TTL was set
	ttl, err := cache.TTL()
	if err != nil {
		t.Fatalf("TTL error: %v", err)
	}
	if ttl <= 0 || ttl > 10*time.Second {
		t.Errorf("Expected TTL around 10s, got %v", ttl)
	}

	// Fast forward time
	mr.FastForward(11 * time.Second)

	// Should be expired
	exists, err := cache.Exists()
	if err != nil {
		t.Fatalf("Exists error: %v", err)
	}
	if exists {
		t.Error("Expected cache to be expired")
	}
}

func TestRedisCache_Refresh(t *testing.T) {
	mr, client := setupMiniRedis(t)

	config := DefaultConfig().WithTTL(10 * time.Second)
	cache := New[TestUser](client, config)

	if err := cache.Set([]TestUser{{ID: "1"}}); err != nil {
		t.Fatalf("Set error: %v", err)
	}

	// Fast forward 5 seconds
	mr.FastForward(5 * time.Second)

	// Refresh TTL
	if err := cache.Refresh(); err != nil {
		t.Fatalf("Refresh error: %v", err)
	}

	// TTL should be reset to 10 seconds
	ttl, err := cache.TTL()
	if err != nil {
		t.Fatalf("TTL error: %v", err)
	}
	if ttl < 8*time.Second {
		t.Errorf("Expected TTL to be refreshed, got %v", ttl)
	}
}

func TestRedisCache_NilClient(t *testing.T) {
	cache := New[TestUser](nil, nil)

	if _, err := cache.Get(); err == nil {
		t.Error("Expected error with nil client")
	}
	if err := cache.Set([]TestUser{}); err == nil {
		t.Error("Expected error with nil client")
	}
	if _, err := cache.Exists(); err == nil {
		t.Error("Expected error with nil client")
	}
	if _, err := cache.GetVersion(); err == nil {
		t.Error("Expected error with nil client")
	}
	if err := cache.Clear(); err == nil {
		t.Error("Expected error with nil client")
	}
	if _, err := cache.TTL(); err == nil {
		t.Error("Expected error with nil client")
	}
	if err := cache.Refresh(); err == nil {
		t.Error("Expected error with nil client")
	}
}

func TestRedisCache_EmptyGet(t *testing.T) {
	_, client := setupMiniRedis(t)

	cache := New[TestUser](client, nil)

	// Get on non-existent key should return empty slice
	got, err := cache.Get()
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Expected empty slice, got %d items", len(got))
	}
}

func TestRedisCache_SetWithTTLNilClient(t *testing.T) {
	cache := New[TestUser](nil, nil)

	err := cache.SetWithTTL([]TestUser{{ID: "1"}}, 10*time.Second)
	if err == nil {
		t.Error("Expected error with nil client")
	}
}

func TestRedisCache_GetInvalidJSON(t *testing.T) {
	mr, client := setupMiniRedis(t)

	config := DefaultConfig()
	cache := New[TestUser](client, config)

	// Set invalid JSON directly in Redis
	if err := mr.Set(config.KeyPrefix+"data", "invalid-json"); err != nil {
		t.Fatalf("Failed to set invalid JSON: %v", err)
	}

	_, err := cache.Get()
	if err == nil {
		t.Error("Expected error for invalid JSON")
	}
}

func TestRedisCache_GetOversizedValue(t *testing.T) {
	mr, client := setupMiniRedis(t)

	config := DefaultConfig().WithKeyPrefix("test:").WithMaxValueBytes(10)
	cache := New[TestUser](client, config)

	// Set a value larger than MaxValueBytes (10 bytes)
	if err := mr.Set(config.KeyPrefix+"data", "0123456789abcdef"); err != nil {
		t.Fatalf("Failed to set value: %v", err)
	}

	_, err := cache.Get()
	if err == nil {
		t.Error("Expected error when value exceeds MaxValueBytes")
	}
	if err != nil && !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("Expected error message to contain 'exceeds', got: %v", err)
	}
}

func TestRedisCache_DefaultNilConfig(t *testing.T) {
	_, client := setupMiniRedis(t)

	// Test NewRedisCache with nil config
	cache := New[TestUser](client, nil)

	users := []TestUser{{ID: "1", Name: "Test"}}
	if err := cache.Set(users); err != nil {
		t.Fatalf("Set error: %v", err)
	}

	got, err := cache.Get()
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("Expected 1 item, got %d", len(got))
	}
}

func TestRedisCacheWithKey_NilConfig(t *testing.T) {
	_, client := setupMiniRedis(t)

	// Test NewRedisCacheWithKey with nil config
	cache := NewWithKey[TestUser](client, "mykey", nil)

	users := []TestUser{{ID: "1", Name: "Test"}}
	if err := cache.Set(users); err != nil {
		t.Fatalf("Set error: %v", err)
	}

	exists, err := cache.Exists()
	if err != nil {
		t.Fatalf("Exists error: %v", err)
	}
	if !exists {
		t.Error("Expected cache to exist")
	}
}

func TestRedisCache_VersionKey(t *testing.T) {
	_, client := setupMiniRedis(t)

	config := DefaultConfig().
		WithKeyPrefix("myapp:").
		WithVersionKeySuffix(":ver")

	cache := New[TestUser](client, config)

	if err := cache.Set([]TestUser{{ID: "1"}}); err != nil {
		t.Fatalf("Set error: %v", err)
	}

	version, err := cache.GetVersion()
	if err != nil {
		t.Fatalf("GetVersion error: %v", err)
	}
	if version != 1 {
		t.Errorf("Expected version 1, got %d", version)
	}
}

func TestRedisCache_GetWithMaxValueBytesDisabled(t *testing.T) {
	mr, client := setupMiniRedis(t)

	config := DefaultConfig().WithKeyPrefix("test:").WithMaxValueBytes(0)
	cache := New[TestUser](client, config)

	// Set a value larger than default 16MB would allow - use small but > 10 bytes to prove limit is off
	payload := `[{"ID":"1","Email":"a@b.c","Phone":"","Name":"x"}]`
	if err := mr.Set(config.KeyPrefix+"data", payload); err != nil {
		t.Fatalf("Failed to set value: %v", err)
	}

	got, err := cache.Get()
	if err != nil {
		t.Fatalf("Get with MaxValueBytes=0 should not reject by size: %v", err)
	}
	if len(got) != 1 || got[0].ID != "1" {
		t.Errorf("Expected one user with ID 1, got %v", got)
	}
}

func TestRedisCache_GetValueAtExactMaxSize(t *testing.T) {
	mr, client := setupMiniRedis(t)

	// Boundary: value length exactly equal to MaxValueBytes should be accepted (no "exceeds max" error)
	payload := `[{"ID":"1"}]` // 13 bytes
	config := DefaultConfig().WithKeyPrefix("test:").WithMaxValueBytes(len(payload))
	cache := New[TestUser](client, config)

	if err := mr.Set(config.KeyPrefix+"data", payload); err != nil {
		t.Fatalf("Failed to set value: %v", err)
	}

	got, err := cache.Get()
	if err != nil {
		t.Fatalf("Get at exact max size should succeed: %v", err)
	}
	if len(got) != 1 || got[0].ID != "1" {
		t.Errorf("Expected one user with ID 1, got %v", got)
	}
}

func TestRedisCache_TTLFallbackWhenZero(t *testing.T) {
	mr, client := setupMiniRedis(t)

	config := DefaultConfig().
		WithKeyPrefix("ttl:").
		WithTTL(0) // Invalid/zero TTL - should use 1h fallback
	cache := New[TestUser](client, config)

	if err := cache.Set([]TestUser{{ID: "1"}}); err != nil {
		t.Fatalf("Set with TTL=0 should use fallback and succeed: %v", err)
	}

	ttl, err := cache.TTL()
	if err != nil {
		t.Fatalf("TTL error: %v", err)
	}
	if ttl <= 0 {
		t.Errorf("Expected positive TTL when config TTL is 0 (fallback), got %v", ttl)
	}

	// Key should eventually expire with fallback TTL (e.g. 1h); fast-forward to verify if miniredis supports it
	mr.FastForward(2 * time.Hour)
	exists, err := cache.Exists()
	if err != nil {
		t.Fatalf("Exists error: %v", err)
	}
	if exists {
		t.Error("Expected key to expire after 2h (fallback TTL is 1h)")
	}
}

func TestRedisCache_SetWithTTLZeroUsesFallback(t *testing.T) {
	_, client := setupMiniRedis(t)

	config := DefaultConfig().WithKeyPrefix("ttl2:")
	cache := New[TestUser](client, config)

	err := cache.SetWithTTL([]TestUser{{ID: "1"}}, 0)
	if err != nil {
		t.Fatalf("SetWithTTL with 0 should use fallback: %v", err)
	}

	ttl, err := cache.TTL()
	if err != nil {
		t.Fatalf("TTL error: %v", err)
	}
	if ttl <= 0 {
		t.Errorf("Expected positive TTL when SetWithTTL(0), got %v", ttl)
	}
}

func TestRedisCache_EmptyKeyPrefixPanics(t *testing.T) {
	_, client := setupMiniRedis(t)
	config := DefaultConfig().WithKeyPrefix("")

	defer func() {
		if r := recover(); r == nil {
			t.Error("Expected panic when KeyPrefix is empty")
		}
	}()
	New[TestUser](client, config)
}

func TestRedisCache_EmptyKeyPanics(t *testing.T) {
	_, client := setupMiniRedis(t)

	defer func() {
		if r := recover(); r == nil {
			t.Error("Expected panic when key is empty")
		}
	}()
	NewWithKey[TestUser](client, "", DefaultConfig())
}

func TestRedisCache_EmptyVersionKeySuffixPanics(t *testing.T) {
	_, client := setupMiniRedis(t)
	config := DefaultConfig().WithKeyPrefix("x:").WithVersionKeySuffix("")

	defer func() {
		if r := recover(); r == nil {
			t.Error("Expected panic when VersionKeySuffix is empty")
		}
	}()
	New[TestUser](client, config)
}
