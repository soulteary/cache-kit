package rediscache_test

import (
	"context"
	"fmt"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	cache "github.com/soulteary/cache-kit/v2"
	"github.com/soulteary/cache-kit/v2/rediscache"
)

type User struct {
	ID    string
	Email string
}

// newTestClient stands in for a real *redis.Client so the examples run under
// go test. In a program this is the client you already have.
func newTestClient() *redis.Client {
	mr, _ := miniredis.Run()
	return redis.NewClient(&redis.Options{Addr: mr.Addr()})
}

// The common case: keep a value set in Redis, with a version that goes up on
// every write so other processes can tell there is something new to read.
func Example() {
	ctx := context.Background()
	client := newTestClient()
	defer func() { _ = client.Close() }()

	c := rediscache.New[User](client, rediscache.DefaultConfig().WithKeyPrefix("users:"))

	if err := c.Set(ctx, []User{{ID: "1", Email: "ada@example.com"}}); err != nil {
		panic(err)
	}

	users, err := c.Get(ctx)
	if err != nil {
		panic(err)
	}
	version, err := c.GetVersion(ctx)
	if err != nil {
		panic(err)
	}

	fmt.Println(users[0].Email, "version", version)
	// Output: ada@example.com version 1
}

// A missing key is an empty result, not an error -- a cold cache is a normal
// state, not a failure.
func ExampleCache_Get() {
	ctx := context.Background()
	client := newTestClient()
	defer func() { _ = client.Close() }()

	c := rediscache.New[User](client, rediscache.DefaultConfig().WithKeyPrefix("cold:"))

	users, err := c.Get(ctx)
	fmt.Println(len(users), err)
	// Output: 0 <nil>
}

// Hybrid serves reads from memory and writes through to Redis, so a lookup
// costs no round trip while the data still outlives the process.
func ExampleHybrid() {
	ctx := context.Background()
	client := newTestClient()
	defer func() { _ = client.Close() }()

	memory := cache.DefaultConfig[User]().WithPrimaryKey(func(u User) string { return u.ID })
	h := rediscache.NewHybrid(memory, client, rediscache.DefaultConfig().WithKeyPrefix("hybrid:"))
	h.AddIndex("email", func(u User) string { return u.Email })

	if err := h.Set(ctx, []User{{ID: "1", Email: "ada@example.com"}}); err != nil {
		panic(err)
	}

	// Served from memory.
	u, ok := h.GetByIndex("email", "ada@example.com")
	fmt.Println(u.ID, ok)

	// A second process starts cold and loads what the first one wrote.
	other := rediscache.NewHybrid(memory, client, rediscache.DefaultConfig().WithKeyPrefix("hybrid:"))
	if err := other.LoadFromRedis(ctx); err != nil {
		panic(err)
	}
	fmt.Println(other.GetAll()[0].Email)
	// Output:
	// 1 true
	// ada@example.com
}
