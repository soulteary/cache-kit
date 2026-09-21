package rediscache

import (
	"testing"

	cache "github.com/soulteary/cache-kit/v2"
)

func TestHybridCache_BasicOperations(t *testing.T) {
	ctx := t.Context()
	_, client := setupMiniRedis(t)

	memConfig := cache.DefaultConfig[TestUser]().
		WithPrimaryKey(func(u TestUser) string { return u.ID })
	redisConfig := DefaultConfig().WithKeyPrefix("hybrid:")

	hybrid := NewHybrid[TestUser](memConfig, client, redisConfig)

	// Add index
	hybrid.AddIndex("email", func(u TestUser) string { return u.Email })

	// Set data
	users := []TestUser{
		{ID: "1", Email: "user1@example.com"},
		{ID: "2", Email: "user2@example.com"},
	}
	if err := hybrid.Set(ctx, users); err != nil {
		t.Fatalf("Set error: %v", err)
	}

	// Test memory cache access
	user, ok := hybrid.GetByIndex("email", "user1@example.com")
	if !ok {
		t.Error("Expected to find user by email in memory")
	}
	if user.ID != "1" {
		t.Errorf("Expected ID 1, got %s", user.ID)
	}

	// Test GetAll
	all := hybrid.GetAll()
	if len(all) != 2 {
		t.Errorf("Expected 2 items, got %d", len(all))
	}

	// Test Redis persistence
	redisData, err := hybrid.Redis().Get(ctx)
	if err != nil {
		t.Fatalf("Redis Get error: %v", err)
	}
	if len(redisData) != 2 {
		t.Errorf("Expected 2 items in Redis, got %d", len(redisData))
	}
}

func TestHybridCache_LoadFromRedis(t *testing.T) {
	ctx := t.Context()
	_, client := setupMiniRedis(t)

	memConfig := cache.DefaultConfig[TestUser]().
		WithPrimaryKey(func(u TestUser) string { return u.ID })
	redisConfig := DefaultConfig()

	hybrid := NewHybrid[TestUser](memConfig, client, redisConfig)
	hybrid.AddIndex("email", func(u TestUser) string { return u.Email })

	// Set data in Redis directly
	redisCache := New[TestUser](client, redisConfig)
	users := []TestUser{
		{ID: "1", Email: "redis-user@example.com"},
	}
	if err := redisCache.Set(ctx, users); err != nil {
		t.Fatalf("Redis Set error: %v", err)
	}

	// Memory should be empty
	if hybrid.Memory().Len() != 0 {
		t.Error("Expected empty memory cache initially")
	}

	// Load from Redis
	if err := hybrid.LoadFromRedis(ctx); err != nil {
		t.Fatalf("LoadFromRedis error: %v", err)
	}

	// Memory should now have data
	if hybrid.Memory().Len() != 1 {
		t.Errorf("Expected 1 item in memory after load, got %d", hybrid.Memory().Len())
	}

	// Index should work
	user, ok := hybrid.GetByIndex("email", "redis-user@example.com")
	if !ok {
		t.Error("Expected to find user by email after load")
	}
	if user.ID != "1" {
		t.Errorf("Expected ID 1, got %s", user.ID)
	}
}

func TestHybridCache_SyncToRedis(t *testing.T) {
	ctx := t.Context()
	_, client := setupMiniRedis(t)

	memConfig := cache.DefaultConfig[TestUser]().
		WithPrimaryKey(func(u TestUser) string { return u.ID })
	redisConfig := DefaultConfig()

	hybrid := NewHybrid[TestUser](memConfig, client, redisConfig)

	// Set data in memory only
	hybrid.Memory().Set([]TestUser{
		{ID: "1", Email: "memory-user@example.com"},
	})

	// Redis should be empty
	exists, _ := hybrid.Redis().Exists(ctx)
	if exists {
		t.Error("Expected Redis to be empty initially")
	}

	// Sync to Redis
	if err := hybrid.SyncToRedis(ctx); err != nil {
		t.Fatalf("SyncToRedis error: %v", err)
	}

	// Redis should now have data
	redisData, err := hybrid.Redis().Get(ctx)
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

func TestHybridCache_LoadFromRedisError(t *testing.T) {
	ctx := t.Context()
	memConfig := cache.DefaultConfig[TestUser]().
		WithPrimaryKey(func(u TestUser) string { return u.ID })
	redisConfig := DefaultConfig()

	// Use nil client to trigger error
	hybrid := NewHybrid[TestUser](memConfig, nil, redisConfig)

	err := hybrid.LoadFromRedis(ctx)
	if err == nil {
		t.Error("Expected error when loading from Redis with nil client")
	}
}

func TestHybridCache_SyncToRedisError(t *testing.T) {
	ctx := t.Context()
	memConfig := cache.DefaultConfig[TestUser]().
		WithPrimaryKey(func(u TestUser) string { return u.ID })
	redisConfig := DefaultConfig()

	// Use nil client to trigger error
	hybrid := NewHybrid[TestUser](memConfig, nil, redisConfig)
	hybrid.Memory().Set([]TestUser{{ID: "1"}})

	err := hybrid.SyncToRedis(ctx)
	if err == nil {
		t.Error("Expected error when syncing to Redis with nil client")
	}
}

func TestHybridCache_SetError(t *testing.T) {
	ctx := t.Context()
	memConfig := cache.DefaultConfig[TestUser]().
		WithPrimaryKey(func(u TestUser) string { return u.ID })
	redisConfig := DefaultConfig()

	// Use nil client to trigger Redis error
	hybrid := NewHybrid[TestUser](memConfig, nil, redisConfig)

	err := hybrid.Set(ctx, []TestUser{{ID: "1"}})
	if err == nil {
		t.Error("Expected error when setting with nil Redis client")
	}

	// Memory should still be updated even if Redis fails
	if hybrid.Memory().Len() != 1 {
		t.Errorf("Expected memory to have 1 item, got %d", hybrid.Memory().Len())
	}
}
