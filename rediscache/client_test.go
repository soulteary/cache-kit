package rediscache

import (
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"
)

// The go-redis client types callers actually hold must satisfy Client without
// a wrapper. Taking *redis.Client alone, as the root package used to, left a
// cluster or Sentinel deployment with no way in.
var (
	_ Client = (*redis.Client)(nil)
	_ Client = (*redis.ClusterClient)(nil)
	_ Client = (*redis.Ring)(nil)
	_ Client = (redis.UniversalClient)(nil)
)

// TestTypedNilClientIsNotAPanic covers the hole that taking an interface
// reopens: a plain client == nil misses a nil *redis.Client stored in one,
// which is what a caller gets from an unassigned field or a constructor that
// returned early. Every method used to reject that with an error because the
// field was concretely typed; calling through the interface would panic
// instead.
func TestTypedNilClientIsNotAPanic(t *testing.T) {
	ctx := t.Context()
	var client *redis.Client // nil, but not a nil interface

	c := New[TestUser](client, DefaultConfig().WithKeyPrefix("typednil:"))

	if _, err := c.Get(ctx); err == nil || !strings.Contains(err.Error(), "redis client is nil") {
		t.Errorf("Get() error = %v, want it to report the nil client", err)
	}
	if err := c.Set(ctx, []TestUser{{ID: "1"}}); err == nil {
		t.Error("Set() error = nil, want it to report the nil client")
	}
	if _, err := c.Exists(ctx); err == nil {
		t.Error("Exists() error = nil, want it to report the nil client")
	}
	if _, err := c.GetVersion(ctx); err == nil {
		t.Error("GetVersion() error = nil, want it to report the nil client")
	}
	if _, err := c.TTL(ctx); err == nil {
		t.Error("TTL() error = nil, want it to report the nil client")
	}
	if err := c.Refresh(ctx); err == nil {
		t.Error("Refresh() error = nil, want it to report the nil client")
	}
	if err := c.Clear(ctx); err == nil {
		t.Error("Clear() error = nil, want it to report the nil client")
	}
	if err := c.SetWithTTL(ctx, []TestUser{{ID: "1"}}, 0); err == nil {
		t.Error("SetWithTTL() error = nil, want it to report the nil client")
	}
}
