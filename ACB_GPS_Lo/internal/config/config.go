package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HTTPPort         int
	RedisAddr        string
	RedisDialTimeout time.Duration
	RedisPingTimeout time.Duration
	ShutdownTimeout  time.Duration
}

func Load() (Config, error) {
	return Parse(os.LookupEnv)
}

// Parse accepts a lookup function so configuration can be tested without
// changing the process environment.
func Parse(lookup func(string) (string, bool)) (Config, error) {
	var cfg Config
	var err error

	port, err := value(lookup, "GPS_HTTP_PORT", "8080")
	if err != nil {
		return cfg, err
	}
	cfg.HTTPPort, err = strconv.Atoi(port)
	if err != nil || cfg.HTTPPort < 1 || cfg.HTTPPort > 65535 {
		return Config{}, fmt.Errorf("GPS_HTTP_PORT must be an integer from 1 to 65535")
	}

	cfg.RedisAddr, err = value(lookup, "GPS_REDIS_ADDR", "redis:6379")
	if err != nil {
		return Config{}, err
	}
	host, redisPort, err := net.SplitHostPort(cfg.RedisAddr)
	if err != nil || host == "" {
		return Config{}, fmt.Errorf("GPS_REDIS_ADDR must be a host:port address")
	}
	number, err := strconv.Atoi(redisPort)
	if err != nil || number < 1 || number > 65535 {
		return Config{}, fmt.Errorf("GPS_REDIS_ADDR must contain a valid port")
	}

	cfg.RedisDialTimeout, err = duration(lookup, "GPS_REDIS_DIAL_TIMEOUT", "2s")
	if err != nil {
		return Config{}, err
	}
	cfg.RedisPingTimeout, err = duration(lookup, "GPS_REDIS_PING_TIMEOUT", "1s")
	if err != nil {
		return Config{}, err
	}
	cfg.ShutdownTimeout, err = duration(lookup, "GPS_SHUTDOWN_TIMEOUT", "20s")
	if err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func value(lookup func(string) (string, bool), name, fallback string) (string, error) {
	raw, present := lookup(name)
	if !present {
		return fallback, nil
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("%s must not be empty", name)
	}
	return raw, nil
}

func duration(lookup func(string) (string, bool), name, fallback string) (time.Duration, error) {
	raw, err := value(lookup, name, fallback)
	if err != nil {
		return 0, err
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", name)
	}
	return d, nil
}
