package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/config"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/httpapi"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/operationshttp"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/outbox"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/redisstore"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/server"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/tracking"
	"github.com/redis/go-redis/v9"
)

type redisHealth struct{ client *redis.Client }

func (r redisHealth) Ping(ctx context.Context) error {
	return r.client.Ping(ctx).Err()
}

type redisLog struct{ logger *slog.Logger }

func (r redisLog) Printf(ctx context.Context, format string, args ...interface{}) {
	r.logger.WarnContext(ctx, "redis client", "message", fmt.Sprintf(format, args...))
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger, os.Args[1:]); err != nil {
		logger.Error("application stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if len(args) == 1 && args[0] == "healthcheck" {
		return checkLocalReadiness(cfg.HTTPPort, cfg.RedisPingTimeout)
	}
	if len(args) != 0 {
		return fmt.Errorf("unknown command")
	}

	redis.SetLogger(redisLog{logger})
	client := redis.NewClient(&redis.Options{
		Addr: cfg.RedisAddr, DialTimeout: cfg.RedisDialTimeout,
		ReadTimeout: cfg.RedisPingTimeout, WriteTimeout: cfg.RedisPingTimeout,
	})
	defer client.Close()
	store, err := redisstore.New(client, cfg.GPSRetention, cfg.GPSSafetyMargin)
	if err != nil {
		return err
	}
	tracker, err := tracking.NewService(store, cfg.GPSMaxObservationAge, cfg.GPSFutureSkew, nil, nil)
	if err != nil {
		return err
	}
	observations, err := httpapi.NewObservations(tracker, cfg.GPSMaxObservationBytes, cfg.GPSRequestTimeout)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.HTTPPort))
	if err != nil {
		return fmt.Errorf("listen HTTP: %w", err)
	}
	defer listener.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	workersCtx, stopWorkers := context.WithCancel(ctx)
	defer stopWorkers()
	recoveryDone := outbox.StartRecovery(workersCtx, store, logger, cfg.RecoveryPoll)
	var deliveryDone <-chan struct{}
	if cfg.OperationsEnabled {
		operationsClient, err := operationshttp.New(cfg.OperationsEventURL, cfg.OperationsToken, cfg.OperationsHTTPTimeout, cfg.OperationsMaxResponse)
		if err != nil {
			return err
		}
		dispatcher, err := outbox.New(store, operationsClient, outbox.Config{Workers: cfg.OutboxWorkers, Poll: cfg.OutboxPoll, Lease: cfg.OutboxLease, RetryMin: cfg.OutboxRetryMin, RetryMax: cfg.OutboxRetryMax, MaxPerHour: cfg.OutboxMaxPerHour, DeliveredRetention: cfg.OutboxDeliveredRetention}, logger)
		if err != nil {
			return err
		}
		deliveryDone = dispatcher.Start(workersCtx)
		logger.Info("Operations event delivery enabled", "workers", cfg.OutboxWorkers)
	} else {
		logger.Info("Operations event delivery disabled; events remain in Redis")
	}
	logger.Info("HTTP server started", "port", cfg.HTTPPort)
	err = server.Serve(ctx, listener, server.HandlerWithTracking(redisHealth{client}, cfg.RedisPingTimeout, logger, observations), cfg.ShutdownTimeout)
	stopWorkers()
	deadline := time.NewTimer(cfg.ShutdownTimeout)
	defer deadline.Stop()
	for _, done := range []<-chan struct{}{recoveryDone, deliveryDone} {
		if done == nil {
			continue
		}
		select {
		case <-done:
		case <-deadline.C:
			logger.Warn("worker shutdown deadline exceeded; claims remain recoverable")
			return err
		}
	}
	if err == nil {
		logger.Info("HTTP server stopped")
	}
	return err
}

func checkLocalReadiness(port int, timeout time.Duration) error {
	client := &http.Client{Timeout: timeout + time.Second}
	response, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/health/ready", port))
	if err != nil {
		return fmt.Errorf("local readiness request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("local readiness returned HTTP %d", response.StatusCode)
	}
	return nil
}
