package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type fakeRedis struct {
	check func(context.Context) error
}

func (f fakeRedis) Ping(ctx context.Context) error { return f.check(ctx) }

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestLiveDoesNotContactRedis(t *testing.T) {
	called := false
	handler := Handler(fakeRedis{func(context.Context) error {
		called = true
		return errors.New("offline")
	}}, time.Second, testLogger())
	request := httptest.NewRequest(http.MethodGet, "/health/live", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || called {
		t.Fatalf("live status=%d, redis called=%v", response.Code, called)
	}
	var body map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body["status"] != "alive" {
		t.Fatalf("unexpected body: %s, %v", response.Body.String(), err)
	}
}

func TestReady(t *testing.T) {
	tests := []struct {
		name   string
		check  func(context.Context) error
		status int
		body   string
	}{
		{"available", func(context.Context) error { return nil }, http.StatusOK, "ready"},
		{"unavailable", func(context.Context) error { return errors.New("offline") }, http.StatusServiceUnavailable, "not_ready"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			Handler(fakeRedis{tt.check}, time.Second, testLogger()).ServeHTTP(
				response, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
			if response.Code != tt.status {
				t.Fatalf("status=%d, wanted %d", response.Code, tt.status)
			}
			var body struct {
				Status string `json:"status"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.Status != tt.body {
				t.Fatalf("unexpected body: %s, %v", response.Body.String(), err)
			}
		})
	}
}

func TestReadyTimeout(t *testing.T) {
	start := time.Now()
	response := httptest.NewRecorder()
	Handler(fakeRedis{func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}}, 15*time.Millisecond, testLogger()).ServeHTTP(
		response, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if response.Code != http.StatusServiceUnavailable || time.Since(start) > time.Second {
		t.Fatalf("timeout not enforced: status=%d duration=%s", response.Code, time.Since(start))
	}
}

func TestReadyDoesNotClaimFutureCapabilities(t *testing.T) {
	response := httptest.NewRecorder()
	Handler(fakeRedis{func(context.Context) error { return nil }}, time.Second, testLogger()).ServeHTTP(
		response, httptest.NewRequest(http.MethodGet, "/health/ready?capability=create-session", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, wanted 400", response.Code)
	}
}

func TestTrackingControlRoutesDispatch(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
	}{
		{"status", http.MethodGet, "/api/v1/tracking-sessions/session-1"},
		{"control", http.MethodGet, "/api/v1/tracking-sessions/session-1/control"},
		{"finish", http.MethodPost, "/api/v1/tracking-sessions/session-1/finish"},
		{"disable", http.MethodPatch, "/api/v1/tracking-sessions/session-1/disable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var seen string
			stub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen = r.Method + " " + r.URL.Path
				w.WriteHeader(http.StatusOK)
			})
			handler := HandlerWithReads(fakeRedis{func(context.Context) error { return nil }}, time.Second, testLogger(), nil, stub, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(tt.method, tt.path, nil))
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d, wanted %d", response.Code, http.StatusOK)
			}
			if want := tt.method + " " + tt.path; seen != want {
				t.Fatalf("handler saw %q, wanted %q", seen, want)
			}
		})
	}
}

func TestTrackingControlRoutesAbsentWithoutHandler(t *testing.T) {
	for _, path := range []string{
		"/api/v1/tracking-sessions/session-1/control",
		"/api/v1/tracking-sessions/session-1/disable",
	} {
		response := httptest.NewRecorder()
		Handler(fakeRedis{func(context.Context) error { return nil }}, time.Second, testLogger()).ServeHTTP(
			response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s status=%d, wanted %d", path, response.Code, http.StatusNotFound)
		}
	}
}

func TestServeDrainsInflightRequest(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		once.Do(func() { close(started) })
		<-release
		w.WriteHeader(http.StatusOK)
	})
	finished := make(chan error, 1)
	go func() { finished <- Serve(ctx, listener, handler, time.Second) }()

	client := &http.Client{Timeout: 2 * time.Second}
	requestResult := make(chan error, 1)
	go func() {
		response, err := client.Get("http://" + listener.Addr().String())
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode != http.StatusOK {
				err = errors.New("unexpected response status")
			}
		}
		requestResult <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not accept request")
	}
	cancel()
	select {
	case err := <-finished:
		t.Fatalf("server exited before request completed: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	if err := <-requestResult; err != nil {
		t.Fatal(err)
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
}
