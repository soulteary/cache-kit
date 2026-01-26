package cache

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

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

	config := DefaultRedisConfig().
		WithKeyPrefix("test:").
		WithTTL(5 * time.Minute)

	cache := NewRedisCache[TestUser](client, config)

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

func TestRedisCache_Version(t *testing.T) {
	_, client := setupMiniRedis(t)

	cache := NewRedisCache[TestUser](client, DefaultRedisConfig())

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

	cache := NewRedisCacheWithKey[TestUser](client, "custom:key", DefaultRedisConfig())

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

	config := DefaultRedisConfig().WithTTL(10 * time.Second)
	cache := NewRedisCache[TestUser](client, config)

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
	cache := NewRedisCache[TestUser](nil, nil)

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

	cache := NewRedisCache[TestUser](client, nil)

	// Get on non-existent key should return empty slice
	got, err := cache.Get()
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Expected empty slice, got %d items", len(got))
	}
}

func TestHybridCache_BasicOperations(t *testing.T) {
	_, client := setupMiniRedis(t)

	memConfig := DefaultConfig[TestUser]().
		WithPrimaryKey(func(u TestUser) string { return u.ID })
	redisConfig := DefaultRedisConfig().WithKeyPrefix("hybrid:")

	cache := NewHybridCache[TestUser](memConfig, client, redisConfig)

	// Add index
	cache.AddIndex("email", func(u TestUser) string { return u.Email })

	// Set data
	users := []TestUser{
		{ID: "1", Email: "user1@example.com"},
		{ID: "2", Email: "user2@example.com"},
	}
	if err := cache.Set(users); err != nil {
		t.Fatalf("Set error: %v", err)
	}

	// Test memory cache access
	user, ok := cache.GetByIndex("email", "user1@example.com")
	if !ok {
		t.Error("Expected to find user by email in memory")
	}
	if user.ID != "1" {
		t.Errorf("Expected ID 1, got %s", user.ID)
	}

	// Test GetAll
	all := cache.GetAll()
	if len(all) != 2 {
		t.Errorf("Expected 2 items, got %d", len(all))
	}

	// Test Redis persistence
	redisData, err := cache.Redis().Get()
	if err != nil {
		t.Fatalf("Redis Get error: %v", err)
	}
	if len(redisData) != 2 {
		t.Errorf("Expected 2 items in Redis, got %d", len(redisData))
	}
}

func TestHybridCache_LoadFromRedis(t *testing.T) {
	_, client := setupMiniRedis(t)

	memConfig := DefaultConfig[TestUser]().
		WithPrimaryKey(func(u TestUser) string { return u.ID })
	redisConfig := DefaultRedisConfig()

	cache := NewHybridCache[TestUser](memConfig, client, redisConfig)
	cache.AddIndex("email", func(u TestUser) string { return u.Email })

	// Set data in Redis directly
	redisCache := NewRedisCache[TestUser](client, redisConfig)
	users := []TestUser{
		{ID: "1", Email: "redis-user@example.com"},
	}
	if err := redisCache.Set(users); err != nil {
		t.Fatalf("Redis Set error: %v", err)
	}

	// Memory should be empty
	if cache.Memory().Len() != 0 {
		t.Error("Expected empty memory cache initially")
	}

	// Load from Redis
	if err := cache.LoadFromRedis(); err != nil {
		t.Fatalf("LoadFromRedis error: %v", err)
	}

	// Memory should now have data
	if cache.Memory().Len() != 1 {
		t.Errorf("Expected 1 item in memory after load, got %d", cache.Memory().Len())
	}

	// Index should work
	user, ok := cache.GetByIndex("email", "redis-user@example.com")
	if !ok {
		t.Error("Expected to find user by email after load")
	}
	if user.ID != "1" {
		t.Errorf("Expected ID 1, got %s", user.ID)
	}
}

func TestHybridCache_SyncToRedis(t *testing.T) {
	_, client := setupMiniRedis(t)

	memConfig := DefaultConfig[TestUser]().
		WithPrimaryKey(func(u TestUser) string { return u.ID })
	redisConfig := DefaultRedisConfig()

	cache := NewHybridCache[TestUser](memConfig, client, redisConfig)

	// Set data in memory only
	cache.Memory().Set([]TestUser{
		{ID: "1", Email: "memory-user@example.com"},
	})

	// Redis should be empty
	exists, _ := cache.Redis().Exists()
	if exists {
		t.Error("Expected Redis to be empty initially")
	}

	// Sync to Redis
	if err := cache.SyncToRedis(); err != nil {
		t.Fatalf("SyncToRedis error: %v", err)
	}

	// Redis should now have data
	redisData, err := cache.Redis().Get()
	if err != nil {
		t.Fatalf("Redis Get error: %v", err)
	}
	if len(redisData) != 1 {
		t.Errorf("Expected 1 item in Redis after sync, got %d", len(redisData))
	}
	if redisData[0].Email != "memory-user@example.com" {
		t.Errorf("Expected memory-user@example.com, got %s", redisData[0].Email)
	}
}

func TestRedisCache_SetWithTTLNilClient(t *testing.T) {
	cache := NewRedisCache[TestUser](nil, nil)

	err := cache.SetWithTTL([]TestUser{{ID: "1"}}, 10*time.Second)
	if err == nil {
		t.Error("Expected error with nil client")
	}
}

func TestRedisCache_GetInvalidJSON(t *testing.T) {
	mr, client := setupMiniRedis(t)

	config := DefaultRedisConfig()
	cache := NewRedisCache[TestUser](client, config)

	// Set invalid JSON directly in Redis
	if err := mr.Set(config.KeyPrefix+"data", "invalid-json"); err != nil {
		t.Fatalf("Failed to set invalid JSON: %v", err)
	}

	_, err := cache.Get()
	if err == nil {
		t.Error("Expected error for invalid JSON")
	}
}

func TestRedisCache_DefaultNilConfig(t *testing.T) {
	_, client := setupMiniRedis(t)

	// Test NewRedisCache with nil config
	cache := NewRedisCache[TestUser](client, nil)

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
	cache := NewRedisCacheWithKey[TestUser](client, "mykey", nil)

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

func TestHybridCache_LoadFromRedisError(t *testing.T) {
	memConfig := DefaultConfig[TestUser]().
		WithPrimaryKey(func(u TestUser) string { return u.ID })
	redisConfig := DefaultRedisConfig()

	// Use nil client to trigger error
	cache := NewHybridCache[TestUser](memConfig, nil, redisConfig)

	err := cache.LoadFromRedis()
	if err == nil {
		t.Error("Expected error when loading from Redis with nil client")
	}
}

func TestHybridCache_SyncToRedisError(t *testing.T) {
	memConfig := DefaultConfig[TestUser]().
		WithPrimaryKey(func(u TestUser) string { return u.ID })
	redisConfig := DefaultRedisConfig()

	// Use nil client to trigger error
	cache := NewHybridCache[TestUser](memConfig, nil, redisConfig)
	cache.Memory().Set([]TestUser{{ID: "1"}})

	err := cache.SyncToRedis()
	if err == nil {
		t.Error("Expected error when syncing to Redis with nil client")
	}
}

func TestHybridCache_SetError(t *testing.T) {
	memConfig := DefaultConfig[TestUser]().
		WithPrimaryKey(func(u TestUser) string { return u.ID })
	redisConfig := DefaultRedisConfig()

	// Use nil client to trigger Redis error
	cache := NewHybridCache[TestUser](memConfig, nil, redisConfig)

	err := cache.Set([]TestUser{{ID: "1"}})
	if err == nil {
		t.Error("Expected error when setting with nil Redis client")
	}

	// Memory should still be updated even if Redis fails
	if cache.Memory().Len() != 1 {
		t.Errorf("Expected memory to have 1 item, got %d", cache.Memory().Len())
	}
}

func TestRedisCache_VersionKey(t *testing.T) {
	_, client := setupMiniRedis(t)

	config := DefaultRedisConfig().
		WithKeyPrefix("myapp:").
		WithVersionKeySuffix(":ver")

	cache := NewRedisCache[TestUser](client, config)

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
