package httpapi_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/control"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/httpapi"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/server"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/tracking"
)

type mockVerifier struct{}

func (mockVerifier) Verify(_ context.Context, token string) (control.Principal, error) {
	if token != "trusted-service-test-token" {
		return control.Principal{}, errors.New("unverified")
	}
	return control.Principal{Subject: "verified-test-client", Audience: control.Audience, ExpiresAt: time.Now().Add(time.Hour), Capabilities: map[string]bool{control.CreateCapability: true, control.ReadCapability: true, control.FinishCapability: true}, TripIDs: map[string]bool{"trip-test": true}}, nil
}

type mockController struct{ calls int }

func (m *mockController) Create(_ context.Context, _ control.Principal, _ string, _ control.CreateRequest) (control.Created, error) {
	m.calls++
	return control.Created{Session: tracking.Session{ID: "test-id", TripID: "trip-test", State: tracking.Active}, Credential: "test-only-credential", CredentialStatus: "issued"}, nil
}
func (m *mockController) Status(_ context.Context, _ control.Principal, _ string) (tracking.Session, error) {
	m.calls++
	return tracking.Session{ID: "test-id", State: tracking.Active}, nil
}
func (m *mockController) Finish(_ context.Context, _ control.Principal, _ string) (tracking.Session, error) {
	m.calls++
	return tracking.Session{ID: "test-id", State: tracking.Finished}, nil
}

type healthy struct{}

func (healthy) Ping(context.Context) error { return nil }

func TestControlHTTPAuthenticationAndRoutes(t *testing.T) {
	service := &mockController{}
	controlHandler, err := httpapi.NewControl(service, mockVerifier{}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	handler := server.HandlerWithControl(healthy{}, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, controlHandler)
	request := func(method, path, token, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		r.Header.Set("Idempotency-Key", "test-key")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	body := `{"trip_id":"trip-test","guide_id":"guide-test","geo_route_id":"route-test","policy_id":"policy-test"}`
	for _, token := range []string{"", "wrong", "gps-observation"} {
		if w := request("POST", "/api/v1/tracking-sessions", token, body); w.Code != 401 {
			t.Fatalf("token %q: %d", token, w.Code)
		}
	}
	if service.calls != 0 {
		t.Fatal("unauthenticated call reached use case")
	}
	w := request("POST", "/api/v1/tracking-sessions", "trusted-service-test-token", body)
	if w.Code != 201 || w.Header().Get("Cache-Control") != "no-store" || !bytes.Contains(w.Body.Bytes(), []byte("test-only-credential")) {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	if w = request("GET", "/api/v1/tracking-sessions/test-id", "trusted-service-test-token", ""); w.Code != 200 {
		t.Fatalf("status: %d", w.Code)
	}
	if w = request("POST", "/api/v1/tracking-sessions/test-id/finish", "trusted-service-test-token", ""); w.Code != 200 {
		t.Fatalf("finish: %d", w.Code)
	}
	if service.calls != 3 {
		t.Fatalf("calls: %d", service.calls)
	}
	// The ordinary production mux exposes no administrative control route.
	ordinary := server.HandlerWithTracking(healthy{}, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	r := httptest.NewRequest("POST", "/api/v1/tracking-sessions", bytes.NewBufferString(body))
	w = httptest.NewRecorder()
	ordinary.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatalf("control was enabled by default: %d", w.Code)
	}
}
