package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HTTPPort               int
	RedisAddr              string
	RedisDialTimeout       time.Duration
	RedisPingTimeout       time.Duration
	ShutdownTimeout        time.Duration
	PathsBaseURL           string
	PathsHTTPTimeout       time.Duration
	PathsMaxResponseBytes  int64
	GPSRetention           time.Duration
	GPSSafetyMargin        time.Duration
	GPSMaxObservationAge   time.Duration
	GPSFutureSkew          time.Duration
	GPSMaxObservationBytes int64
	GPSRequestTimeout      time.Duration
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
	cfg.PathsBaseURL, err = optionalURL(lookup, "GPS_PATHS_BASE_URL")
	if err != nil {
		return Config{}, err
	}
	cfg.PathsHTTPTimeout, err = duration(lookup, "GPS_PATHS_HTTP_TIMEOUT", "3s")
	if err != nil {
		return Config{}, err
	}
	maxBytes, err := value(lookup, "GPS_PATHS_MAX_RESPONSE_BYTES", "2097152")
	if err != nil {
		return Config{}, err
	}
	cfg.PathsMaxResponseBytes, err = strconv.ParseInt(maxBytes, 10, 64)
	if err != nil || cfg.PathsMaxResponseBytes < 1 || cfg.PathsMaxResponseBytes > 64<<20 {
		return Config{}, fmt.Errorf("GPS_PATHS_MAX_RESPONSE_BYTES must be between 1 and 67108864")
	}
	cfg.GPSRetention, err = duration(lookup, "GPS_POSITION_RETENTION", "24h")
	if err != nil || cfg.GPSRetention < 24*time.Hour || cfg.GPSRetention > 365*24*time.Hour {
		return Config{}, fmt.Errorf("GPS_POSITION_RETENTION must be between 24h and 8760h")
	}
	cfg.GPSSafetyMargin, err = nonnegativeDuration(lookup, "GPS_POSITION_SAFETY_MARGIN", "5m")
	if err != nil || cfg.GPSSafetyMargin > 24*time.Hour {
		return Config{}, fmt.Errorf("GPS_POSITION_SAFETY_MARGIN must be between 0s and 24h")
	}
	cfg.GPSMaxObservationAge, err = duration(lookup, "GPS_MAX_OBSERVATION_AGE", "15m")
	if err != nil || cfg.GPSMaxObservationAge > 24*time.Hour || cfg.GPSMaxObservationAge%time.Millisecond != 0 {
		return Config{}, fmt.Errorf("GPS_MAX_OBSERVATION_AGE must be between 1ms and 24h")
	}
	cfg.GPSFutureSkew, err = nonnegativeDuration(lookup, "GPS_FUTURE_SKEW", "30s")
	if err != nil || cfg.GPSFutureSkew > 5*time.Minute || cfg.GPSFutureSkew%time.Millisecond != 0 {
		return Config{}, fmt.Errorf("GPS_FUTURE_SKEW must be between 0s and 5m")
	}
	bytes, err := value(lookup, "GPS_MAX_OBSERVATION_BYTES", "4096")
	if err != nil {
		return Config{}, err
	}
	cfg.GPSMaxObservationBytes, err = strconv.ParseInt(bytes, 10, 64)
	if err != nil || cfg.GPSMaxObservationBytes < 128 || cfg.GPSMaxObservationBytes > 65536 {
		return Config{}, fmt.Errorf("GPS_MAX_OBSERVATION_BYTES must be between 128 and 65536")
	}
	cfg.GPSRequestTimeout, err = duration(lookup, "GPS_REQUEST_TIMEOUT", "5s")
	if err != nil || cfg.GPSRequestTimeout > 30*time.Second {
		return Config{}, fmt.Errorf("GPS_REQUEST_TIMEOUT must be between 1ns and 30s")
	}
	return cfg, nil
}

func nonnegativeDuration(lookup func(string) (string, bool), name, fallback string) (time.Duration, error) {
	raw, err := value(lookup, name, fallback)
	if err != nil {
		return 0, err
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("%s must be nonnegative", name)
	}
	return d, nil
}

func optionalURL(lookup func(string) (string, bool), name string) (string, error) {
	raw, present := lookup(name)
	if !present || strings.TrimSpace(raw) == "" {
		return "", nil
	}
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" ||
		parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", fmt.Errorf("%s must be an http(s) origin without path, query, fragment or credentials", name)
	}
	return strings.TrimRight(raw, "/"), nil
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
