package config

import (
	"strings"
	"testing"
	"time"
)

func lookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func TestParseDefaultsAndOverrides(t *testing.T) {
	defaults, err := Parse(lookup(nil))
	if err != nil {
		t.Fatal(err)
	}
	if defaults.HTTPPort != 8080 || defaults.RedisAddr != "redis:6379" ||
		defaults.RedisDialTimeout != 2*time.Second ||
		defaults.RedisPingTimeout != time.Second ||
		defaults.ShutdownTimeout != 20*time.Second {
		t.Fatalf("unexpected defaults: %+v", defaults)
	}

	cfg, err := Parse(lookup(map[string]string{
		"GPS_HTTP_PORT": "9090", "GPS_REDIS_ADDR": "[::1]:6380",
		"GPS_REDIS_DIAL_TIMEOUT": "500ms", "GPS_REDIS_PING_TIMEOUT": "250ms",
		"GPS_SHUTDOWN_TIMEOUT": "3s",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPPort != 9090 || cfg.RedisAddr != "[::1]:6380" ||
		cfg.RedisDialTimeout != 500*time.Millisecond ||
		cfg.RedisPingTimeout != 250*time.Millisecond ||
		cfg.ShutdownTimeout != 3*time.Second {
		t.Fatalf("unexpected overrides: %+v", cfg)
	}
}

func TestParseRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name, key, value string
	}{
		{"empty port", "GPS_HTTP_PORT", ""},
		{"invalid port", "GPS_HTTP_PORT", "abc"},
		{"zero port", "GPS_HTTP_PORT", "0"},
		{"large port", "GPS_HTTP_PORT", "65536"},
		{"missing redis host", "GPS_REDIS_ADDR", ":6379"},
		{"missing redis port", "GPS_REDIS_ADDR", "redis"},
		{"invalid redis port", "GPS_REDIS_ADDR", "redis:zero"},
		{"zero dial timeout", "GPS_REDIS_DIAL_TIMEOUT", "0s"},
		{"negative ping timeout", "GPS_REDIS_PING_TIMEOUT", "-1s"},
		{"invalid shutdown timeout", "GPS_SHUTDOWN_TIMEOUT", "twenty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(lookup(map[string]string{tt.key: tt.value}))
			if err == nil || !strings.Contains(err.Error(), tt.key) {
				t.Fatalf("expected error naming %s, got %v", tt.key, err)
			}
		})
	}
}
