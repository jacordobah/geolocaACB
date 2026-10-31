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
		defaults.ShutdownTimeout != 20*time.Second ||
		defaults.PathsBaseURL != "" || defaults.PathsHTTPTimeout != 3*time.Second ||
		defaults.PathsMaxResponseBytes != 2<<20 || defaults.GPSRetention != 24*time.Hour || defaults.GPSSafetyMargin != 5*time.Minute {
		t.Fatalf("unexpected defaults: %+v", defaults)
	}

	cfg, err := Parse(lookup(map[string]string{
		"GPS_HTTP_PORT": "9090", "GPS_REDIS_ADDR": "[::1]:6380",
		"GPS_REDIS_DIAL_TIMEOUT": "500ms", "GPS_REDIS_PING_TIMEOUT": "250ms",
		"GPS_SHUTDOWN_TIMEOUT":   "3s",
		"GPS_PATHS_BASE_URL":     "http://geoloca-api:8000/",
		"GPS_PATHS_HTTP_TIMEOUT": "700ms", "GPS_PATHS_MAX_RESPONSE_BYTES": "4096",
		"GPS_POSITION_RETENTION": "48h", "GPS_POSITION_SAFETY_MARGIN": "0s",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPPort != 9090 || cfg.RedisAddr != "[::1]:6380" ||
		cfg.RedisDialTimeout != 500*time.Millisecond ||
		cfg.RedisPingTimeout != 250*time.Millisecond ||
		cfg.ShutdownTimeout != 3*time.Second ||
		cfg.PathsBaseURL != "http://geoloca-api:8000" ||
		cfg.PathsHTTPTimeout != 700*time.Millisecond || cfg.PathsMaxResponseBytes != 4096 || cfg.GPSRetention != 48*time.Hour || cfg.GPSSafetyMargin != 0 {
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
		{"URL with user info", "GPS_PATHS_BASE_URL", "http://user@python:8000"},
		{"URL with path", "GPS_PATHS_BASE_URL", "http://python:8000/api/v1"},
		{"URL without scheme", "GPS_PATHS_BASE_URL", "python:8000"},
		{"invalid HTTP timeout", "GPS_PATHS_HTTP_TIMEOUT", "0s"},
		{"zero response limit", "GPS_PATHS_MAX_RESPONSE_BYTES", "0"},
		{"huge response limit", "GPS_PATHS_MAX_RESPONSE_BYTES", "67108865"},
		{"short retention", "GPS_POSITION_RETENTION", "23h"},
		{"negative safety margin", "GPS_POSITION_SAFETY_MARGIN", "-1s"},
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
