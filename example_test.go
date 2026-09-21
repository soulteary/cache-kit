package cache_test

import (
	"fmt"

	cache "github.com/soulteary/cache-kit/v2"
)

type User struct {
	ID    string
	Email string
	Phone string
}

// The common case: index a slice by more than one key and look values up in
// O(1) by any of them.
func Example() {
	config := cache.DefaultConfig[User]().
		WithPrimaryKey(func(u User) string { return u.ID })

	c := cache.NewMultiIndexCache(config)
	c.AddIndex("email", func(u User) string { return u.Email })
	c.AddIndex("phone", func(u User) string { return u.Phone })

	c.Set([]User{
		{ID: "1", Email: "ada@example.com", Phone: "+100"},
		{ID: "2", Email: "grace@example.com", Phone: "+200"},
	})

	byEmail, _ := c.GetByIndex("email", "grace@example.com")
	byPhone, _ := c.GetByIndex("phone", "+100")
	fmt.Println(byEmail.ID, byPhone.ID, c.Len())
	// Output: 2 1 2
}

// Index keys are matched case-insensitively and with surrounding whitespace
// trimmed, so a lookup does not have to reproduce the stored spelling exactly.
func ExampleMemoryCache_GetByIndex() {
	config := cache.DefaultConfig[User]().
		WithPrimaryKey(func(u User) string { return u.ID })

	c := cache.NewMultiIndexCache(config)
	c.AddIndex("email", func(u User) string { return u.Email })
	c.Set([]User{{ID: "1", Email: "Ada@Example.com"}})

	u, ok := c.GetByIndex("email", "  ada@example.COM  ")
	fmt.Println(u.ID, ok)
	// Output: 1 true
}

// GetHash answers "did the contents change", not "is this the same slice". It
// is computed from the values themselves, so the same set hashes the same
// across restarts, machines and reallocations -- which is what makes it usable
// for deciding whether to push an update downstream.
func ExampleMemoryCache_GetHash() {
	config := cache.DefaultConfig[User]().
		WithPrimaryKey(func(u User) string { return u.ID })

	c := cache.NewMultiIndexCache(config)
	c.Set([]User{{ID: "1", Email: "ada@example.com"}})
	first := c.GetHash()

	// Same contents, rebuilt from scratch: no change.
	c.Set([]User{{ID: "1", Email: "ada@example.com"}})
	fmt.Println("unchanged:", c.GetHash() == first)

	// One field differs: change detected.
	c.Set([]User{{ID: "1", Email: "ada@example.org"}})
	fmt.Println("unchanged:", c.GetHash() == first)
	// Output:
	// unchanged: true
	// unchanged: false
}

// Without a SortFunc the hash follows insertion order, so the same set built
// in a different order hashes differently. Sorting first makes the hash depend
// on the contents alone.
func ExampleConfig_WithSortFunc() {
	config := cache.DefaultConfig[User]().
		WithPrimaryKey(func(u User) string { return u.ID }).
		WithSortFunc(cache.StringSorter(func(u User) string { return u.ID }))

	a := cache.NewMultiIndexCache(config)
	a.Set([]User{{ID: "1"}, {ID: "2"}})

	b := cache.NewMultiIndexCache(config)
	b.Set([]User{{ID: "2"}, {ID: "1"}})

	fmt.Println(a.GetHash() == b.GetHash())
	// Output: true
}
