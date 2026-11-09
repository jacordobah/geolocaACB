package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

type Pinger interface {
	Ping(context.Context) error
}

func Handler(redis Pinger, pingTimeout time.Duration, logger *slog.Logger) http.Handler {
	return HandlerWithTracking(redis, pingTimeout, logger, nil)
}

func HandlerWithTracking(redis Pinger, pingTimeout time.Duration, logger *slog.Logger, observations http.Handler) http.Handler {
	return HandlerWithControl(redis, pingTimeout, logger, observations, nil)
}

// A nil control handler keeps all administrative routes absent in production.
func HandlerWithControl(redis Pinger, pingTimeout time.Duration, logger *slog.Logger, observations, control http.Handler) http.Handler {
	return HandlerWithReads(redis, pingTimeout, logger, observations, control, nil)
}

// Reads is nil in the production composition until a verified identity
// provider has been approved and supplied.
func HandlerWithReads(redis Pinger, pingTimeout time.Duration, logger *slog.Logger, observations, control, reads http.Handler) http.Handler {
	mux := http.NewServeMux()
	if observations != nil {
		mux.Handle("POST /api/v1/tracking-sessions/{tracking_id}/observations", observations)
	}
	if control != nil {
		mux.Handle("POST /api/v1/tracking-sessions", control)
		mux.Handle("GET /api/v1/tracking-sessions/{tracking_id}", control)
		mux.Handle("POST /api/v1/tracking-sessions/{tracking_id}/finish", control)
	}
	if reads != nil {
		mux.Handle("GET /api/v1/tracking-sessions/{tracking_id}/latest-position", reads)
		mux.Handle("GET /api/v1/tracking-sessions/{tracking_id}/positions", reads)
	}
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, _ *http.Request) {
		respond(w, http.StatusOK, map[string]string{"status": "alive"})
	})
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			respond(w, http.StatusBadRequest, map[string]string{"status": "unsupported_query"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), pingTimeout)
		defer cancel()
		if err := redis.Ping(ctx); err != nil {
			logger.Warn("redis readiness check failed", "error", err)
			respond(w, http.StatusServiceUnavailable, map[string]any{
				"status": "not_ready", "dependencies": map[string]string{"redis": "unavailable"},
			})
			return
		}
		respond(w, http.StatusOK, map[string]any{
			"status": "ready", "dependencies": map[string]string{"redis": "available"},
		})
	})
	return mux
}

func respond(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// Serve returns after shutdown has drained in-flight requests or the deadline
// has expired. The caller owns the listener.
func Serve(ctx context.Context, listener net.Listener, handler http.Handler, shutdownTimeout time.Duration) error {
	httpServer := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	serveResult := make(chan error, 1)
	go func() { serveResult <- httpServer.Serve(listener) }()

	select {
	case err := <-serveResult:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve HTTP: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			_ = httpServer.Close()
			return fmt.Errorf("shutdown HTTP: %w", err)
		}
		if err := <-serveResult; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve HTTP: %w", err)
		}
		return nil
	}
}
