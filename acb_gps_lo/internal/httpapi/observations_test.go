package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/tracking"
)

type receiverFunc func(context.Context, string, string, tracking.ObservationInput) (tracking.Acceptance, error)

func (f receiverFunc) Receive(ctx context.Context, id, token string, input tracking.ObservationInput) (tracking.Acceptance, error) {
	return f(ctx, id, token, input)
}

func request(t *testing.T, receiver Receiver, body, auth, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	handler, err := NewObservations(receiver, 512, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/tracking-sessions/{tracking_id}/observations", handler)
	r := httptest.NewRequest(http.MethodPost, "/api/v1/tracking-sessions/test-id/observations", strings.NewReader(body))
	if auth != "" {
		r.Header.Set("Authorization", auth)
	}
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

const validBody = `{"sequence":1,"latitude":4.6,"longitude":-74.1,"accuracy_m":5,"captured_at":"2026-10-31T12:00:00Z"}`

func TestObservationHTTPValidation(t *testing.T) {
	called := 0
	receiver := receiverFunc(func(_ context.Context, id, token string, input tracking.ObservationInput) (tracking.Acceptance, error) {
		called++
		if id != "test-id" || token != "abc" || input.Sequence != 1 {
			t.Fatal("wrong input")
		}
		return tracking.Acceptance{Position: tracking.Position{Sequence: 1, ReceivedAt: time.Date(2026, 10, 31, 12, 0, 1, 0, time.UTC)}, ExpiresAt: time.Date(2026, 11, 1, 12, 5, 0, 0, time.UTC)}, nil
	})
	w := request(t, receiver, validBody, "Bearer abc", "application/json; charset=utf-8")
	if w.Code != 201 || !strings.Contains(w.Body.String(), `"acceptance":"accepted"`) || !strings.Contains(w.Body.String(), `"geographic_evaluation":"not_evaluated"`) {
		t.Fatalf("response %d %s", w.Code, w.Body.String())
	}
	tests := []struct {
		name, body, auth, ctype string
		status                  int
	}{
		{"no bearer", validBody, "", "application/json", 401},
		{"bad bearer", validBody, "Bearer wrong token", "application/json", 401},
		{"wrong type", validBody, "Bearer abc", "text/plain", 415},
		{"unknown field", strings.TrimSuffix(validBody, "}") + `,"guide_id":"x"}`, "Bearer abc", "application/json", 400},
		{"missing field", `{"sequence":1}`, "Bearer abc", "application/json", 400},
		{"missing timezone", strings.Replace(validBody, "2026-10-31T12:00:00Z", "2026-10-31T12:00:00", 1), "Bearer abc", "application/json", 400},
		{"two objects", validBody + validBody, "Bearer abc", "application/json", 400},
		{"large body", validBody + strings.Repeat(" ", 600), "Bearer abc", "application/json", 413},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := request(t, receiver, tc.body, tc.auth, tc.ctype)
			if w.Code != tc.status {
				t.Fatalf("status %d body %s", w.Code, w.Body.String())
			}
		})
	}
	if called != 1 {
		t.Fatalf("receiver called %d times", called)
	}
}

func TestObservationHTTPDomainErrors(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{tracking.ErrUnauthorized, 401, "unauthorized"}, {tracking.ErrNotFound, 401, "unauthorized"},
		{tracking.ErrInvalid, 400, "invalid_observation"}, {tracking.ErrConflict, 409, "sequence_conflict"},
		{tracking.ErrStale, 409, "stale_observation"}, {tracking.ErrClosed, 401, "unauthorized"},
		{tracking.ErrUncertain, 503, "storage_unavailable"}, {tracking.ErrPending, 503, "storage_unavailable"},
		{errors.New("Redis offline"), 503, "storage_unavailable"},
	}
	for _, tc := range cases {
		w := request(t, receiverFunc(func(context.Context, string, string, tracking.ObservationInput) (tracking.Acceptance, error) {
			return tracking.Acceptance{}, tc.err
		}), validBody, "Bearer abc", "application/json")
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.code) || strings.Contains(w.Body.String(), "Redis offline") {
			t.Fatalf("status %d body %s", w.Code, w.Body.String())
		}
	}
	w := request(t, receiverFunc(func(context.Context, string, string, tracking.ObservationInput) (tracking.Acceptance, error) {
		return tracking.Acceptance{Duplicate: true, Position: tracking.Position{Sequence: 1, ReceivedAt: time.Now()}, ExpiresAt: time.Now().Add(24 * time.Hour)}, nil
	}), validBody, "Bearer abc", "application/json")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "replayed") {
		t.Fatalf("duplicate %d %s", w.Code, w.Body.String())
	}
}
