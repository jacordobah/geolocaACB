package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/gatewayurl"
)

type Config struct {
	HTTPPort                 int
	RedisAddr                string
	RedisDialTimeout         time.Duration
	RedisPingTimeout         time.Duration
	ShutdownTimeout          time.Duration
	GatewayBaseURL           string
	GatewayRoutesPath        string
	GatewayRoutesToken       string
	GatewayEventsPath        string
	PathsHTTPTimeout         time.Duration
	PathsMaxResponseBytes    int64
	GPSRetention             time.Duration
	GPSSafetyMargin          time.Duration
	GPSMaxObservationAge     time.Duration
	GPSFutureSkew            time.Duration
	GPSMaxObservationBytes   int64
	GPSRequestTimeout        time.Duration
	RecoveryPoll             time.Duration
	ControlAPIEnabled        bool
	PoliciesFile             string
	ReadPageLimit            int64
	ReadMaxResponseBytes     int
	ReadRequestTimeout       time.Duration
	OperationsEnabled        bool
	OperationsToken          string
	OperationsHTTPTimeout    time.Duration
	OperationsMaxResponse    int64
	OutboxWorkers            int
	OutboxPoll               time.Duration
	OutboxLease              time.Duration
	OutboxRetryMin           time.Duration
	OutboxRetryMax           time.Duration
	OutboxMaxPerHour         int
	OutboxDeliveredRetention time.Duration
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
	for _, legacy := range []string{"GPS_PATHS_BASE_URL", "GPS_OPERATIONS_EVENT_URL"} {
		if raw, present := lookup(legacy); present && strings.TrimSpace(raw) != "" {
			return Config{}, fmt.Errorf("%s is obsolete; configure the API Gateway origin and path", legacy)
		}
	}
	if raw, present := lookup("GPS_GATEWAY_BASE_URL"); present && strings.TrimSpace(raw) != "" {
		cfg.GatewayBaseURL, err = gatewayurl.Origin(raw)
		if err != nil {
			return Config{}, fmt.Errorf("GPS_GATEWAY_BASE_URL must be an HTTP(S) origin without a path")
		}
	}
	if raw, present := lookup("GPS_GATEWAY_ROUTES_PATH"); present && strings.TrimSpace(raw) != "" {
		if err := gatewayurl.RoutePath(raw); err != nil || cfg.GatewayBaseURL == "" {
			return Config{}, fmt.Errorf("GPS_GATEWAY_ROUTES_PATH requires a Gateway origin and a path ending in /{route_id}")
		}
		cfg.GatewayRoutesPath = raw
	}
	if raw, present := lookup("GPS_GATEWAY_EVENTS_PATH"); present && strings.TrimSpace(raw) != "" {
		if err := gatewayurl.EndpointPath(raw); err != nil || strings.Contains(raw, gatewayurl.RouteIDMarker) || cfg.GatewayBaseURL == "" {
			return Config{}, fmt.Errorf("GPS_GATEWAY_EVENTS_PATH requires a Gateway origin and an absolute endpoint path")
		}
		cfg.GatewayEventsPath = raw
	}
	if token, present := lookup("GPS_GATEWAY_ROUTES_TOKEN"); present {
		if strings.ContainsAny(token, "\r\n") {
			return Config{}, fmt.Errorf("GPS_GATEWAY_ROUTES_TOKEN contains invalid characters")
		}
		cfg.GatewayRoutesToken = token
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
	cfg.RecoveryPoll, err = duration(lookup, "GPS_RECOVERY_POLL", "2s")
	if err != nil || cfg.RecoveryPoll < 100*time.Millisecond {
		return Config{}, fmt.Errorf("GPS_RECOVERY_POLL must be at least 100ms")
	}
	controlEnabled, err := value(lookup, "GPS_CONTROL_API_ENABLED", "false")
	if err != nil {
		return Config{}, err
	}
	cfg.ControlAPIEnabled, err = strconv.ParseBool(controlEnabled)
	if err != nil {
		return Config{}, fmt.Errorf("GPS_CONTROL_API_ENABLED must be boolean")
	}
	if cfg.ControlAPIEnabled {
		return Config{}, fmt.Errorf("control API cannot be enabled until a verified identity provider and administrative policy contract are configured")
	}
	if raw, present := lookup("GPS_POLICIES_FILE"); present {
		if raw != "" && (strings.TrimSpace(raw) != raw || strings.ContainsAny(raw, "\x00\r\n")) {
			return Config{}, fmt.Errorf("GPS_POLICIES_FILE must be a path without surrounding whitespace or control characters")
		}
		cfg.PoliciesFile = raw
	}
	readPage, err := value(lookup, "GPS_READ_MAX_PAGE_LIMIT", "100")
	if err != nil {
		return Config{}, err
	}
	cfg.ReadPageLimit, err = strconv.ParseInt(readPage, 10, 64)
	if err != nil || cfg.ReadPageLimit < 1 || cfg.ReadPageLimit > 100 {
		return Config{}, fmt.Errorf("GPS_READ_MAX_PAGE_LIMIT must be 1..100")
	}
	readBytes, err := value(lookup, "GPS_READ_MAX_RESPONSE_BYTES", "1048576")
	if err != nil {
		return Config{}, err
	}
	cfg.ReadMaxResponseBytes, err = strconv.Atoi(readBytes)
	if err != nil || cfg.ReadMaxResponseBytes < 1024 || cfg.ReadMaxResponseBytes > 4<<20 {
		return Config{}, fmt.Errorf("GPS_READ_MAX_RESPONSE_BYTES must be 1024..4194304")
	}
	cfg.ReadRequestTimeout, err = duration(lookup, "GPS_READ_REQUEST_TIMEOUT", "5s")
	if err != nil || cfg.ReadRequestTimeout > 30*time.Second {
		return Config{}, fmt.Errorf("GPS_READ_REQUEST_TIMEOUT must be at most 30s")
	}
	enabled, err := value(lookup, "GPS_OPERATIONS_DELIVERY_ENABLED", "false")
	if err != nil {
		return Config{}, err
	}
	cfg.OperationsEnabled, err = strconv.ParseBool(enabled)
	if err != nil {
		return Config{}, fmt.Errorf("GPS_OPERATIONS_DELIVERY_ENABLED must be boolean")
	}
	if token, ok := lookup("GPS_OPERATIONS_TOKEN"); ok {
		cfg.OperationsToken = strings.TrimSpace(token)
	}
	cfg.OperationsHTTPTimeout, err = duration(lookup, "GPS_OPERATIONS_HTTP_TIMEOUT", "3s")
	if err != nil || cfg.OperationsHTTPTimeout > 30*time.Second {
		return Config{}, fmt.Errorf("GPS_OPERATIONS_HTTP_TIMEOUT must be at most 30s")
	}
	response, err := value(lookup, "GPS_OPERATIONS_MAX_RESPONSE_BYTES", "4096")
	if err != nil {
		return Config{}, err
	}
	cfg.OperationsMaxResponse, err = strconv.ParseInt(response, 10, 64)
	if err != nil || cfg.OperationsMaxResponse < 32 || cfg.OperationsMaxResponse > 1<<20 {
		return Config{}, fmt.Errorf("GPS_OPERATIONS_MAX_RESPONSE_BYTES invalid")
	}
	workers, err := value(lookup, "GPS_OUTBOX_WORKERS", "2")
	if err != nil {
		return Config{}, err
	}
	cfg.OutboxWorkers, err = strconv.Atoi(workers)
	if err != nil || cfg.OutboxWorkers < 1 || cfg.OutboxWorkers > 32 {
		return Config{}, fmt.Errorf("GPS_OUTBOX_WORKERS must be 1..32")
	}
	cfg.OutboxPoll, err = duration(lookup, "GPS_OUTBOX_POLL", "1s")
	if err != nil || cfg.OutboxPoll < 100*time.Millisecond {
		return Config{}, fmt.Errorf("GPS_OUTBOX_POLL must be at least 100ms")
	}
	cfg.OutboxLease, err = duration(lookup, "GPS_OUTBOX_LEASE", "10s")
	if err != nil || cfg.OutboxLease < time.Second || cfg.OutboxLease > time.Hour {
		return Config{}, fmt.Errorf("GPS_OUTBOX_LEASE invalid")
	}
	cfg.OutboxRetryMin, err = duration(lookup, "GPS_OUTBOX_RETRY_MIN", "5s")
	if err != nil || cfg.OutboxRetryMin < time.Second {
		return Config{}, fmt.Errorf("GPS_OUTBOX_RETRY_MIN invalid")
	}
	cfg.OutboxRetryMax, err = duration(lookup, "GPS_OUTBOX_RETRY_MAX", "30m")
	if err != nil || cfg.OutboxRetryMax < cfg.OutboxRetryMin || cfg.OutboxRetryMax > 24*time.Hour {
		return Config{}, fmt.Errorf("GPS_OUTBOX_RETRY_MAX invalid")
	}
	maxPerHour, err := value(lookup, "GPS_OUTBOX_MAX_PER_HOUR", "60")
	if err != nil {
		return Config{}, err
	}
	cfg.OutboxMaxPerHour, err = strconv.Atoi(maxPerHour)
	if err != nil || cfg.OutboxMaxPerHour < 1 || cfg.OutboxMaxPerHour > 3600 {
		return Config{}, fmt.Errorf("GPS_OUTBOX_MAX_PER_HOUR invalid")
	}
	cfg.OutboxDeliveredRetention, err = duration(lookup, "GPS_OUTBOX_DELIVERED_RETENTION", "168h")
	if err != nil || cfg.OutboxDeliveredRetention < time.Hour || cfg.OutboxDeliveredRetention > 365*24*time.Hour {
		return Config{}, fmt.Errorf("GPS_OUTBOX_DELIVERED_RETENTION invalid")
	}
	if cfg.OperationsEnabled && (cfg.GatewayBaseURL == "" || cfg.GatewayEventsPath == "" || cfg.OperationsToken == "") {
		return Config{}, fmt.Errorf("Operations delivery requires Gateway origin, event path and service token")
	}
	if cfg.OutboxLease <= cfg.OperationsHTTPTimeout+time.Second {
		return Config{}, fmt.Errorf("GPS_OUTBOX_LEASE must exceed HTTP timeout by more than one second")
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
