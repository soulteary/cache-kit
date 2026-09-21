package rediscache

import (
	"testing"
	"time"
)

func TestDefaultConfig(t *testing.T) {
	config := DefaultConfig()

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
	if config.MaxValueBytes != 16*1024*1024 {
		t.Errorf("Expected default MaxValueBytes 16MB, got %d", config.MaxValueBytes)
	}
}

func TestConfigBuilder(t *testing.T) {
	config := DefaultConfig().
		WithKeyPrefix("myapp:").
		WithVersionKeySuffix(":ver").
		WithTTL(30 * time.Minute).
		WithOperationTimeout(10 * time.Second).
		WithMaxValueBytes(1024)

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
	if config.MaxValueBytes != 1024 {
		t.Errorf("Expected MaxValueBytes 1024, got %d", config.MaxValueBytes)
	}
}

func TestConfig_WithMaxValueBytesZero(t *testing.T) {
	config := DefaultConfig().WithMaxValueBytes(0)
	if config.MaxValueBytes != 0 {
		t.Errorf("Expected MaxValueBytes 0 to disable limit, got %d", config.MaxValueBytes)
	}
}
