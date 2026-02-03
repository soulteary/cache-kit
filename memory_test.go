package cache

import (
	"fmt"
	"sync"
	"testing"
)

// TestUser is a sample type for testing
type TestUser struct {
	ID    string
	Email string
	Phone string
	Name  string
}

func TestMemoryCache_BasicOperations(t *testing.T) {
	config := DefaultConfig[TestUser]().
		WithPrimaryKey(func(u TestUser) string { return u.ID })

	cache := NewMultiIndexCache(config)

	// Test empty cache
	if cache.Len() != 0 {
		t.Errorf("Expected empty cache, got %d items", cache.Len())
	}

	// Test Set
	users := []TestUser{
		{ID: "1", Email: "user1@example.com", Phone: "1111111111", Name: "User 1"},
		{ID: "2", Email: "user2@example.com", Phone: "2222222222", Name: "User 2"},
	}
	cache.Set(users)

	if cache.Len() != 2 {
		t.Errorf("Expected 2 items, got %d", cache.Len())
	}

	// Test Get
	user, ok := cache.Get("1")
	if !ok {
		t.Error("Expected to find user with ID 1")
	}
	if user.Email != "user1@example.com" {
		t.Errorf("Expected email user1@example.com, got %s", user.Email)
	}

	// Test GetAll
	all := cache.GetAll()
	if len(all) != 2 {
		t.Errorf("Expected 2 items from GetAll, got %d", len(all))
	}

	// Test Clear
	cache.Clear()
	if cache.Len() != 0 {
		t.Errorf("Expected empty cache after Clear, got %d items", cache.Len())
	}
}

func TestMemoryCache_MultiIndex(t *testing.T) {
	config := DefaultConfig[TestUser]().
		WithPrimaryKey(func(u TestUser) string { return u.ID })

	cache := NewMultiIndexCache(config)

	// Add indexes
	cache.AddIndex("email", func(u TestUser) string { return u.Email })
	cache.AddIndex("phone", func(u TestUser) string { return u.Phone })

	if !cache.HasIndex("email") {
		t.Error("Expected email index to exist")
	}
	if !cache.HasIndex("phone") {
		t.Error("Expected phone index to exist")
	}
	if cache.HasIndex("nonexistent") {
		t.Error("Expected nonexistent index to not exist")
	}

	// Set data
	users := []TestUser{
		{ID: "1", Email: "user1@example.com", Phone: "1111111111", Name: "User 1"},
		{ID: "2", Email: "user2@example.com", Phone: "2222222222", Name: "User 2"},
	}
	cache.Set(users)

	// Test GetByIndex with email
	user, ok := cache.GetByIndex("email", "user1@example.com")
	if !ok {
		t.Error("Expected to find user by email")
	}
	if user.ID != "1" {
		t.Errorf("Expected ID 1, got %s", user.ID)
	}

	// Test GetByIndex with phone
	user, ok = cache.GetByIndex("phone", "2222222222")
	if !ok {
		t.Error("Expected to find user by phone")
	}
	if user.ID != "2" {
		t.Errorf("Expected ID 2, got %s", user.ID)
	}

	// Test case-insensitive lookup
	user, ok = cache.GetByIndex("email", "USER1@EXAMPLE.COM")
	if !ok {
		t.Error("Expected case-insensitive email lookup to work")
	}

	// Test index key normalization: leading/trailing space
	user, ok = cache.GetByIndex("email", "  user1@example.com  ")
	if !ok {
		t.Error("Expected trimmed email lookup to work")
	}
	if user.ID != "1" {
		t.Errorf("Expected ID 1, got %s", user.ID)
	}

	// Test nonexistent index
	_, ok = cache.GetByIndex("nonexistent", "value")
	if ok {
		t.Error("Expected GetByIndex on nonexistent index to return false")
	}

	// Test nonexistent value
	_, ok = cache.GetByIndex("email", "nonexistent@example.com")
	if ok {
		t.Error("Expected GetByIndex with nonexistent value to return false")
	}

	// Test RemoveIndex
	cache.RemoveIndex("phone")
	if cache.HasIndex("phone") {
		t.Error("Expected phone index to be removed")
	}
}

func TestMemoryCache_DuplicatePrimaryKeys(t *testing.T) {
	config := DefaultConfig[TestUser]().
		WithPrimaryKey(func(u TestUser) string { return u.ID })

	cache := NewMultiIndexCache(config)

	// Set data with duplicate IDs
	users := []TestUser{
		{ID: "1", Email: "first@example.com", Name: "First"},
		{ID: "1", Email: "second@example.com", Name: "Second"}, // Duplicate
		{ID: "2", Email: "third@example.com", Name: "Third"},
	}
	cache.Set(users)

	// Should keep the last one
	if cache.Len() != 2 {
		t.Errorf("Expected 2 items after dedup, got %d", cache.Len())
	}

	user, ok := cache.Get("1")
	if !ok {
		t.Error("Expected to find user with ID 1")
	}
	if user.Email != "second@example.com" {
		t.Errorf("Expected last duplicate to be kept, got email %s", user.Email)
	}
}

func TestMemoryCache_InsertionOrder(t *testing.T) {
	config := DefaultConfig[TestUser]().
		WithPrimaryKey(func(u TestUser) string { return u.ID })

	cache := NewMultiIndexCache(config)

	users := []TestUser{
		{ID: "3", Name: "Third"},
		{ID: "1", Name: "First"},
		{ID: "2", Name: "Second"},
	}
	cache.Set(users)

	all := cache.GetAll()
	if len(all) != 3 {
		t.Fatalf("Expected 3 items, got %d", len(all))
	}

	// Check insertion order is preserved
	expectedOrder := []string{"3", "1", "2"}
	for i, u := range all {
		if u.ID != expectedOrder[i] {
			t.Errorf("Expected ID %s at position %d, got %s", expectedOrder[i], i, u.ID)
		}
	}
}

func TestMemoryCache_Iterate(t *testing.T) {
	config := DefaultConfig[TestUser]().
		WithPrimaryKey(func(u TestUser) string { return u.ID })

	cache := NewMultiIndexCache(config)

	users := []TestUser{
		{ID: "1", Name: "User 1"},
		{ID: "2", Name: "User 2"},
		{ID: "3", Name: "User 3"},
	}
	cache.Set(users)

	// Test full iteration
	var visited []string
	cache.Iterate(func(u TestUser) bool {
		visited = append(visited, u.ID)
		return true
	})
	if len(visited) != 3 {
		t.Errorf("Expected to visit 3 items, got %d", len(visited))
	}

	// Test early stop
	visited = nil
	cache.Iterate(func(u TestUser) bool {
		visited = append(visited, u.ID)
		return len(visited) < 2 // Stop after 2
	})
	if len(visited) != 2 {
		t.Errorf("Expected to visit 2 items with early stop, got %d", len(visited))
	}
}

func TestMemoryCache_Hash(t *testing.T) {
	config := DefaultConfig[TestUser]().
		WithPrimaryKey(func(u TestUser) string { return u.ID })

	cache := NewMultiIndexCache(config)

	// Empty cache hash
	hash1 := cache.GetHash()
	if hash1 != "" {
		t.Error("Expected empty hash for empty cache")
	}

	// Set data
	users := []TestUser{{ID: "1", Name: "User"}}
	cache.Set(users)
	hash2 := cache.GetHash()
	if hash2 == "" {
		t.Error("Expected non-empty hash after Set")
	}

	// Same data should produce same hash
	cache.Set(users)
	hash3 := cache.GetHash()
	if hash2 != hash3 {
		t.Error("Expected same hash for same data")
	}

	// Different data should produce different hash
	cache.Set([]TestUser{{ID: "2", Name: "Other"}})
	hash4 := cache.GetHash()
	if hash2 == hash4 {
		t.Error("Expected different hash for different data")
	}
}

func TestMemoryCache_Validation(t *testing.T) {
	config := DefaultConfig[TestUser]().
		WithPrimaryKey(func(u TestUser) string { return u.ID }).
		WithValidateFunc(func(u TestUser) error {
			if u.Email == "" {
				return fmt.Errorf("email is required")
			}
			return nil
		})

	cache := NewMultiIndexCache(config)

	users := []TestUser{
		{ID: "1", Email: "valid@example.com"},
		{ID: "2", Email: ""}, // Invalid - should be skipped
		{ID: "3", Email: "also-valid@example.com"},
	}
	cache.Set(users)

	if cache.Len() != 2 {
		t.Errorf("Expected 2 valid items, got %d", cache.Len())
	}

	_, ok := cache.Get("2")
	if ok {
		t.Error("Expected invalid user to be skipped")
	}
}

func TestMemoryCache_Normalization(t *testing.T) {
	config := DefaultConfig[TestUser]().
		WithPrimaryKey(func(u TestUser) string { return u.ID }).
		WithNormalizeFunc(func(u TestUser) TestUser {
			u.Name = "Normalized: " + u.Name
			return u
		})

	cache := NewMultiIndexCache(config)

	users := []TestUser{{ID: "1", Name: "Original"}}
	cache.Set(users)

	user, _ := cache.Get("1")
	if user.Name != "Normalized: Original" {
		t.Errorf("Expected normalized name, got %s", user.Name)
	}
}

func TestMemoryCache_ConcurrentAccess(t *testing.T) {
	config := DefaultConfig[TestUser]().
		WithPrimaryKey(func(u TestUser) string { return u.ID })

	cache := NewMultiIndexCache(config)
	cache.AddIndex("email", func(u TestUser) string { return u.Email })

	var wg sync.WaitGroup
	numGoroutines := 100
	numWrites := 10

	// Concurrent writes
	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func(n int) {
			defer wg.Done()
			for j := 0; j < numWrites; j++ {
				users := []TestUser{
					{ID: fmt.Sprintf("%d-%d", n, j), Email: fmt.Sprintf("user%d-%d@example.com", n, j)},
				}
				cache.Set(users)
			}
		}(i)
	}

	// Concurrent reads
	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < numWrites; j++ {
				_ = cache.GetAll()
				_ = cache.Len()
				_ = cache.GetHash()
			}
		}()
	}

	wg.Wait()

	// Just verify no panics occurred
	if cache.Len() < 0 {
		t.Error("Unexpected negative length")
	}
}

func TestMemoryCache_EmptyPrimaryKey(t *testing.T) {
	config := DefaultConfig[TestUser]().
		WithPrimaryKey(func(u TestUser) string { return u.ID })

	cache := NewMultiIndexCache(config)

	users := []TestUser{
		{ID: "", Email: "noid@example.com"}, // Empty ID should be skipped
		{ID: "1", Email: "valid@example.com"},
	}
	cache.Set(users)

	if cache.Len() != 1 {
		t.Errorf("Expected 1 item (empty ID skipped), got %d", cache.Len())
	}
}

func TestMemoryCache_IndexCount(t *testing.T) {
	config := DefaultConfig[TestUser]().
		WithPrimaryKey(func(u TestUser) string { return u.ID })

	cache := NewMultiIndexCache(config)

	if cache.IndexCount() != 0 {
		t.Errorf("Expected 0 indexes, got %d", cache.IndexCount())
	}

	cache.AddIndex("email", func(u TestUser) string { return u.Email })
	cache.AddIndex("phone", func(u TestUser) string { return u.Phone })

	if cache.IndexCount() != 2 {
		t.Errorf("Expected 2 indexes, got %d", cache.IndexCount())
	}

	names := cache.IndexNames()
	if len(names) != 2 {
		t.Errorf("Expected 2 index names, got %d", len(names))
	}
}

func TestMemoryCache_NilConfig(t *testing.T) {
	cache := NewMultiIndexCache[TestUser](nil)
	if cache == nil {
		t.Error("Expected cache to be created with nil config")
	}
}

func TestMemoryCache_AddIndexWithExistingData(t *testing.T) {
	config := DefaultConfig[TestUser]().
		WithPrimaryKey(func(u TestUser) string { return u.ID })

	cache := NewMultiIndexCache(config)

	// Set data first
	users := []TestUser{
		{ID: "1", Email: "user1@example.com", Phone: "1111111111"},
		{ID: "2", Email: "user2@example.com", Phone: "2222222222"},
	}
	cache.Set(users)

	// Add index after data exists - should rebuild index
	cache.AddIndex("email", func(u TestUser) string { return u.Email })

	// Verify index works
	user, ok := cache.GetByIndex("email", "user1@example.com")
	if !ok {
		t.Error("Expected to find user by email after adding index")
	}
	if user.ID != "1" {
		t.Errorf("Expected ID 1, got %s", user.ID)
	}

	// Add another index with empty key values
	cache.AddIndex("name", func(u TestUser) string { return u.Name })
	// Users have empty names, so index should be empty but not error
	_, ok = cache.GetByIndex("name", "")
	// Empty key should not be indexed
	if ok {
		t.Error("Expected empty key to not be indexed")
	}
}

func TestMemoryCache_ClearWithIndexes(t *testing.T) {
	config := DefaultConfig[TestUser]().
		WithPrimaryKey(func(u TestUser) string { return u.ID })

	cache := NewMultiIndexCache(config)
	cache.AddIndex("email", func(u TestUser) string { return u.Email })
	cache.AddIndex("phone", func(u TestUser) string { return u.Phone })

	users := []TestUser{
		{ID: "1", Email: "user1@example.com", Phone: "1111111111"},
	}
	cache.Set(users)

	// Verify data exists
	if cache.Len() != 1 {
		t.Errorf("Expected 1 item, got %d", cache.Len())
	}

	// Clear should reset indexes too
	cache.Clear()

	if cache.Len() != 0 {
		t.Errorf("Expected 0 items after clear, got %d", cache.Len())
	}

	// Indexes should still exist but be empty
	if !cache.HasIndex("email") {
		t.Error("Expected email index to still exist after clear")
	}

	_, ok := cache.GetByIndex("email", "user1@example.com")
	if ok {
		t.Error("Expected index to be empty after clear")
	}
}

func TestMemoryCache_HashWithSortFunc(t *testing.T) {
	config := DefaultConfig[TestUser]().
		WithPrimaryKey(func(u TestUser) string { return u.ID }).
		WithSortFunc(StringSorter(func(u TestUser) string { return u.ID }))

	cache := NewMultiIndexCache(config)

	// Insert in non-sorted order
	users := []TestUser{
		{ID: "3", Name: "Third"},
		{ID: "1", Name: "First"},
		{ID: "2", Name: "Second"},
	}
	cache.Set(users)

	hash1 := cache.GetHash()

	// Insert in different order but same data
	cache2 := NewMultiIndexCache(config)
	users2 := []TestUser{
		{ID: "1", Name: "First"},
		{ID: "2", Name: "Second"},
		{ID: "3", Name: "Third"},
	}
	cache2.Set(users2)

	hash2 := cache2.GetHash()

	// With sort function, hash should be the same regardless of insertion order
	if hash1 != hash2 {
		t.Error("Expected same hash with sort function regardless of insertion order")
	}
}

func TestMemoryCache_HashWithCustomHashFunc(t *testing.T) {
	customHash := "my-custom-hash-value"
	config := DefaultConfig[TestUser]().
		WithPrimaryKey(func(u TestUser) string { return u.ID }).
		WithHashFunc(func(users []TestUser) string {
			return customHash
		})

	cache := NewMultiIndexCache(config)

	users := []TestUser{{ID: "1", Name: "User"}}
	cache.Set(users)

	hash := cache.GetHash()
	if hash != customHash {
		t.Errorf("Expected custom hash %s, got %s", customHash, hash)
	}
}

func TestMemoryCache_NoPrimaryKeyFunc(t *testing.T) {
	// Config without primary key function
	config := DefaultConfig[TestUser]()

	cache := NewMultiIndexCache(config)

	// Set(empty) is allowed and does not panic
	cache.Set([]TestUser{})
	if cache.Len() != 0 {
		t.Errorf("Expected 0 items after Set(empty), got %d", cache.Len())
	}

	// Set(non-empty) without PrimaryKeyFunc must panic
	users := []TestUser{
		{ID: "1", Name: "User 1"},
		{ID: "2", Name: "User 2"},
	}
	defer func() {
		if r := recover(); r == nil {
			t.Error("Expected panic when Set(non-empty) with nil PrimaryKeyFunc")
		}
	}()
	cache.Set(users)
}

func TestMemoryCache_EmptyIndexKey(t *testing.T) {
	config := DefaultConfig[TestUser]().
		WithPrimaryKey(func(u TestUser) string { return u.ID })

	cache := NewMultiIndexCache(config)
	cache.AddIndex("name", func(u TestUser) string { return u.Name })

	// User with empty name - should not be indexed by name
	users := []TestUser{
		{ID: "1", Name: "", Email: "user1@example.com"},
		{ID: "2", Name: "Bob", Email: "user2@example.com"},
	}
	cache.Set(users)

	// Should find by name "Bob"
	user, ok := cache.GetByIndex("name", "Bob")
	if !ok {
		t.Error("Expected to find user by name")
	}
	if user.ID != "2" {
		t.Errorf("Expected ID 2, got %s", user.ID)
	}

	// Empty name should not be indexed
	_, ok = cache.GetByIndex("name", "")
	if ok {
		t.Error("Expected empty name to not be indexed")
	}
}

func TestMemoryCache_GetNotFound(t *testing.T) {
	config := DefaultConfig[TestUser]().
		WithPrimaryKey(func(u TestUser) string { return u.ID })

	cache := NewMultiIndexCache(config)

	users := []TestUser{{ID: "1", Name: "User"}}
	cache.Set(users)

	// Get non-existent key
	_, ok := cache.Get("nonexistent")
	if ok {
		t.Error("Expected Get to return false for non-existent key")
	}
}

func TestMemoryCache_SetEmptySlice(t *testing.T) {
	config := DefaultConfig[TestUser]().
		WithPrimaryKey(func(u TestUser) string { return u.ID })

	cache := NewMultiIndexCache(config)
	cache.Set([]TestUser{{ID: "1"}})
	if cache.Len() != 1 {
		t.Fatalf("Expected 1 item, got %d", cache.Len())
	}

	cache.Set([]TestUser{})
	if cache.Len() != 0 {
		t.Errorf("Expected 0 items after Set(empty slice), got %d", cache.Len())
	}
	h := cache.GetHash()
	if h == "" {
		t.Error("Expected non-empty hash for empty cache state")
	}
}

func TestMemoryCache_SetNilSlice(t *testing.T) {
	config := DefaultConfig[TestUser]().
		WithPrimaryKey(func(u TestUser) string { return u.ID })

	cache := NewMultiIndexCache(config)
	cache.Set([]TestUser{{ID: "1"}})

	// Set(nil) is valid in Go; len(nil) is 0, so we iterate over 0 items and end up with empty cache
	cache.Set(nil)
	if cache.Len() != 0 {
		t.Errorf("Expected 0 items after Set(nil), got %d", cache.Len())
	}
}

func TestMemoryCache_HashWithNilHashFunc(t *testing.T) {
	config := &Config[TestUser]{
		PrimaryKeyFunc: func(u TestUser) string { return u.ID },
		HashFunc:       nil, // Explicitly nil
	}

	cache := NewMultiIndexCache(config)

	users := []TestUser{{ID: "1", Name: "User"}}
	cache.Set(users)

	hash := cache.GetHash()
	if hash == "" {
		t.Error("Expected non-empty hash with nil HashFunc")
	}
}

func TestMemoryCache_IterateEmpty(t *testing.T) {
	config := DefaultConfig[TestUser]().
		WithPrimaryKey(func(u TestUser) string { return u.ID })

	cache := NewMultiIndexCache(config)

	// Iterate on empty cache
	count := 0
	cache.Iterate(func(u TestUser) bool {
		count++
		return true
	})

	if count != 0 {
		t.Errorf("Expected 0 iterations on empty cache, got %d", count)
	}
}

func TestMemoryCache_GetAllEmpty(t *testing.T) {
	config := DefaultConfig[TestUser]().
		WithPrimaryKey(func(u TestUser) string { return u.ID })

	cache := NewMultiIndexCache(config)

	all := cache.GetAll()
	if len(all) != 0 {
		t.Errorf("Expected empty slice, got %d items", len(all))
	}
}

func BenchmarkMemoryCache_Get(b *testing.B) {
	config := DefaultConfig[TestUser]().
		WithPrimaryKey(func(u TestUser) string { return u.ID })

	cache := NewMultiIndexCache(config)

	users := make([]TestUser, 1000)
	for i := 0; i < 1000; i++ {
		users[i] = TestUser{ID: fmt.Sprintf("%d", i), Email: fmt.Sprintf("user%d@example.com", i)}
	}
	cache.Set(users)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cache.Get("500")
	}
}

func BenchmarkMemoryCache_GetByIndex(b *testing.B) {
	config := DefaultConfig[TestUser]().
		WithPrimaryKey(func(u TestUser) string { return u.ID })

	cache := NewMultiIndexCache(config)
	cache.AddIndex("email", func(u TestUser) string { return u.Email })

	users := make([]TestUser, 1000)
	for i := 0; i < 1000; i++ {
		users[i] = TestUser{ID: fmt.Sprintf("%d", i), Email: fmt.Sprintf("user%d@example.com", i)}
	}
	cache.Set(users)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cache.GetByIndex("email", "user500@example.com")
	}
}

func BenchmarkMemoryCache_Set(b *testing.B) {
	config := DefaultConfig[TestUser]().
		WithPrimaryKey(func(u TestUser) string { return u.ID })

	cache := NewMultiIndexCache(config)
	cache.AddIndex("email", func(u TestUser) string { return u.Email })

	users := make([]TestUser, 100)
	for i := 0; i < 100; i++ {
		users[i] = TestUser{ID: fmt.Sprintf("%d", i), Email: fmt.Sprintf("user%d@example.com", i)}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cache.Set(users)
	}
}

func BenchmarkMemoryCache_ConcurrentRead(b *testing.B) {
	config := DefaultConfig[TestUser]().
		WithPrimaryKey(func(u TestUser) string { return u.ID })

	cache := NewMultiIndexCache(config)

	users := make([]TestUser, 1000)
	for i := 0; i < 1000; i++ {
		users[i] = TestUser{ID: fmt.Sprintf("%d", i), Email: fmt.Sprintf("user%d@example.com", i)}
	}
	cache.Set(users)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = cache.GetAll()
		}
	})
}
