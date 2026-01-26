package cache

import (
	"fmt"
	"testing"
	"time"
)

func TestDefaultConfig(t *testing.T) {
	config := DefaultConfig[TestUser]()

	if config == nil {
		t.Fatal("Expected non-nil config")
	}
	if config.HashFunc == nil {
		t.Error("Expected default hash function")
	}
	if config.PrimaryKeyFunc != nil {
		t.Error("Expected nil primary key function")
	}
	if config.ValidateFunc != nil {
		t.Error("Expected nil validate function")
	}
	if config.NormalizeFunc != nil {
		t.Error("Expected nil normalize function")
	}
}

func TestConfigBuilder(t *testing.T) {
	pkFunc := func(u TestUser) string { return u.ID }
	hashFunc := func(values []TestUser) string { return "custom-hash" }
	validateFunc := func(u TestUser) error {
		if u.ID == "" {
			return fmt.Errorf("empty ID")
		}
		return nil
	}
	normalizeFunc := func(u TestUser) TestUser {
		u.Name = "Normalized"
		return u
	}
	sortFunc := StringSorter(func(u TestUser) string { return u.ID })

	config := DefaultConfig[TestUser]().
		WithPrimaryKey(pkFunc).
		WithHashFunc(hashFunc).
		WithValidateFunc(validateFunc).
		WithNormalizeFunc(normalizeFunc).
		WithSortFunc(sortFunc)

	if config.PrimaryKeyFunc == nil {
		t.Error("Expected primary key function to be set")
	}
	if config.HashFunc == nil {
		t.Error("Expected hash function to be set")
	}
	if config.ValidateFunc == nil {
		t.Error("Expected validate function to be set")
	}
	if config.NormalizeFunc == nil {
		t.Error("Expected normalize function to be set")
	}
	if config.SortFunc == nil {
		t.Error("Expected sort function to be set")
	}

	// Test functions work
	if config.PrimaryKeyFunc(TestUser{ID: "test"}) != "test" {
		t.Error("Primary key function not working")
	}
	if config.HashFunc([]TestUser{}) != "custom-hash" {
		t.Error("Hash function not working")
	}
	if config.ValidateFunc(TestUser{ID: ""}) == nil {
		t.Error("Validate function should return error for empty ID")
	}
	normalized := config.NormalizeFunc(TestUser{Name: "Original"})
	if normalized.Name != "Normalized" {
		t.Error("Normalize function not working")
	}
}

func TestDefaultRedisConfig(t *testing.T) {
	config := DefaultRedisConfig()

	if config == nil {
		t.Fatal("Expected non-nil config")
	}
	if config.KeyPrefix != "cache:" {
		t.Errorf("Expected key prefix 'cache:', got '%s'", config.KeyPrefix)
	}
	if config.VersionKeySuffix != ":version" {
		t.Errorf("Expected version key suffix ':version', got '%s'", config.VersionKeySuffix)
	}
	if config.TTL != 1*time.Hour {
		t.Errorf("Expected TTL 1 hour, got %v", config.TTL)
	}
	if config.OperationTimeout != 5*time.Second {
		t.Errorf("Expected operation timeout 5s, got %v", config.OperationTimeout)
	}
}

func TestRedisConfigBuilder(t *testing.T) {
	config := DefaultRedisConfig().
		WithKeyPrefix("myapp:").
		WithVersionKeySuffix(":ver").
		WithTTL(30 * time.Minute).
		WithOperationTimeout(10 * time.Second)

	if config.KeyPrefix != "myapp:" {
		t.Errorf("Expected key prefix 'myapp:', got '%s'", config.KeyPrefix)
	}
	if config.VersionKeySuffix != ":ver" {
		t.Errorf("Expected version key suffix ':ver', got '%s'", config.VersionKeySuffix)
	}
	if config.TTL != 30*time.Minute {
		t.Errorf("Expected TTL 30 minutes, got %v", config.TTL)
	}
	if config.OperationTimeout != 10*time.Second {
		t.Errorf("Expected operation timeout 10s, got %v", config.OperationTimeout)
	}
}

func TestSha256Hash(t *testing.T) {
	hash1 := sha256Hash("test")
	hash2 := sha256Hash("test")
	hash3 := sha256Hash("different")

	if hash1 != hash2 {
		t.Error("Expected same hash for same input")
	}
	if hash1 == hash3 {
		t.Error("Expected different hash for different input")
	}
	if len(hash1) != 64 {
		t.Errorf("Expected 64 character hash, got %d", len(hash1))
	}
}

func TestDefaultHashFunc(t *testing.T) {
	// Empty slice
	hash1 := defaultHashFunc([]TestUser{})
	if hash1 == "" {
		t.Error("Expected non-empty hash for empty slice")
	}

	// Non-empty slice
	hash2 := defaultHashFunc([]TestUser{{ID: "1"}})
	if hash2 == "" {
		t.Error("Expected non-empty hash for non-empty slice")
	}
	if hash1 == hash2 {
		t.Error("Expected different hash for different data")
	}

	// Same data same hash
	hash3 := defaultHashFunc([]TestUser{{ID: "1"}})
	if hash2 != hash3 {
		t.Error("Expected same hash for same data")
	}
}

func TestStringSorter(t *testing.T) {
	sorter := StringSorter(func(u TestUser) string { return u.ID })

	users := []TestUser{
		{ID: "3", Name: "Third"},
		{ID: "1", Name: "First"},
		{ID: "2", Name: "Second"},
	}

	sorted := sorter(users)

	// Original should be unchanged
	if users[0].ID != "3" {
		t.Error("Expected original slice to be unchanged")
	}

	// Sorted should be in order
	expectedOrder := []string{"1", "2", "3"}
	for i, u := range sorted {
		if u.ID != expectedOrder[i] {
			t.Errorf("Expected ID %s at position %d, got %s", expectedOrder[i], i, u.ID)
		}
	}
}
