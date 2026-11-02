package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/control"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/httpapi"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/server"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/tracking"
)

type readVerifier struct{}

func (readVerifier) Verify(_ context.Context, token string) (control.Principal, error) {
	if token == "observation-token" || token == "invalid" {
		return control.Principal{}, errors.New("unverified")
	}
	p := control.Principal{Subject: "test-reader", Audience: control.Audience, ExpiresAt: time.Now().Add(time.Hour), Capabilities: map[string]bool{control.ReadCapability: true}, TrackingIDs: map[string]bool{"session-test": true}}
	switch token {
	case "valid":
		return p, nil
	case "wrong-scope":
		p.TrackingIDs = map[string]bool{"another-session": true}
		return p, nil
	case "no-capability":
		p.Capabilities = nil
		return p, nil
	case "expired":
		p.ExpiresAt = time.Now().Add(-time.Second)
		return p, nil
	}
	return control.Principal{}, errors.New("unverified")
}

type readMock struct {
	session                 tracking.Session
	position                tracking.Position
	page                    tracking.Page
	latestErr, errorHistory error
	wait                    bool
}

func (m *readMock) authorized(p control.Principal, id string) error {
	if !p.Capabilities[control.ReadCapability] {
		return control.ErrForbidden
	}
	if !p.Allows(time.Now(), control.ReadCapability, m.session.TripID, id) {
		return tracking.ErrNotFound
	}
	return nil
}
func (m *readMock) Latest(ctx context.Context, p control.Principal, id string) (tracking.Session, tracking.Position, error) {
	if err := m.authorized(p, id); err != nil {
		return tracking.Session{}, tracking.Position{}, err
	}
	if m.wait {
		<-ctx.Done()
		return tracking.Session{}, tracking.Position{}, ctx.Err()
	}
	return m.session, m.position, m.latestErr
}
func (m *readMock) History(ctx context.Context, p control.Principal, id string, after, limit int64) (tracking.Session, tracking.Page, error) {
	if err := m.authorized(p, id); err != nil {
		return tracking.Session{}, tracking.Page{}, err
	}
	if m.wait {
		<-ctx.Done()
		return tracking.Session{}, tracking.Page{}, ctx.Err()
	}
	return m.session, m.page, m.errorHistory
}

func readHandler(t *testing.T, m *readMock, timeout time.Duration, maxBytes int) http.Handler {
	t.Helper()
	reads, err := httpapi.NewReads(m, readVerifier{}, timeout, 100, maxBytes)
	if err != nil {
		t.Fatal(err)
	}
	return server.HandlerWithReads(healthy{}, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil, reads)
}
func readRequest(h http.Handler, path, token string, ctx context.Context) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if ctx != nil {
		r = r.WithContext(ctx)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestReadHTTPResponsesAndAuthorization(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	accuracy := 5.0
	position := tracking.Position{SessionID: "session-test", Sequence: 8, Latitude: 4.6, Longitude: -74.1, AccuracyMeters: &accuracy, CapturedAt: now.Add(-time.Minute), ReceivedAt: now.Add(-time.Minute), Geographic: &tracking.GeographicResult{RouteStatus: "inside", MeetingPoints: []tracking.PointEvaluation{{ID: "PE01", Status: "inside", DistanceMeters: 3}}}}
	m := &readMock{session: tracking.Session{ID: "session-test", TripID: "trip-test", State: tracking.Active, LastSequence: 8}, position: position, page: tracking.Page{Positions: []tracking.Position{position}, NextSequence: 8, HasMore: true}}
	h := readHandler(t, m, time.Second, 1<<20)
	for _, token := range []string{"", "invalid", "observation-token", "expired"} {
		if w := readRequest(h, "/api/v1/tracking-sessions/session-test/latest-position", token, nil); w.Code != 401 {
			t.Fatalf("token %q: %d", token, w.Code)
		}
	}
	if w := readRequest(h, "/api/v1/tracking-sessions/session-test/latest-position", "no-capability", nil); w.Code != 403 {
		t.Fatalf("capability: %d", w.Code)
	}
	if w := readRequest(h, "/api/v1/tracking-sessions/session-test/latest-position", "wrong-scope", nil); w.Code != 404 {
		t.Fatalf("scope: %d", w.Code)
	}
	w := readRequest(h, "/api/v1/tracking-sessions/session-test/latest-position", "valid", nil)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("latest: %d %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["tracking_id"] != "session-test" || body["age_ms"].(float64) < 0 || !strings.Contains(w.Body.String(), "PE01") || !strings.Contains(w.Body.String(), "inside") {
		t.Fatalf("latest body %s", w.Body.String())
	}
	for _, forbidden := range []string{"credential_hash", "observation_credential", "trip_id", "session_id", "first_arrival"} {
		if strings.Contains(w.Body.String(), forbidden) {
			t.Fatalf("sensitive field %s leaked", forbidden)
		}
	}
	w = readRequest(h, "/api/v1/tracking-sessions/session-test/positions?after_sequence=3&limit=1", "valid", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"next_after_sequence":8`) || !strings.Contains(w.Body.String(), `"has_more":true`) {
		t.Fatalf("history: %d %s", w.Code, w.Body.String())
	}
	for _, query := range []string{"after_sequence=-1", "after_sequence=bad", "limit=0", "limit=101", "limit=1&limit=2", "other=1", "after_sequence=9007199254740992", "after_sequence=9007199254740993"} {
		if w = readRequest(h, "/api/v1/tracking-sessions/session-test/positions?"+query, "valid", nil); w.Code != 400 {
			t.Fatalf("query %s: %d", query, w.Code)
		}
	}
	if w = readRequest(h, "/api/v1/tracking-sessions/session-test/latest-position?limit=1", "valid", nil); w.Code != 400 {
		t.Fatalf("latest query: %d", w.Code)
	}
	m.latestErr = control.ErrNoObservations
	if w = readRequest(h, "/api/v1/tracking-sessions/session-test/latest-position", "valid", nil); w.Code != 204 {
		t.Fatalf("empty session: %d", w.Code)
	}
	m.latestErr = control.ErrPositionExpired
	if w = readRequest(h, "/api/v1/tracking-sessions/session-test/latest-position", "valid", nil); w.Code != 410 {
		t.Fatalf("expired: %d", w.Code)
	}
	m.latestErr = tracking.ErrNotFound
	if w = readRequest(h, "/api/v1/tracking-sessions/session-test/latest-position", "valid", nil); w.Code != 404 {
		t.Fatalf("missing: %d", w.Code)
	}
	m.latestErr = tracking.ErrUnavailable
	if w = readRequest(h, "/api/v1/tracking-sessions/session-test/latest-position", "valid", nil); w.Code != 503 {
		t.Fatalf("unavailable: %d", w.Code)
	}
	ordinary := server.HandlerWithTracking(healthy{}, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if w = readRequest(ordinary, "/api/v1/tracking-sessions/session-test/latest-position", "valid", nil); w.Code != 404 {
		t.Fatalf("read route enabled by default: %d", w.Code)
	}
}

func TestReadTimeoutCancellationAndSize(t *testing.T) {
	m := &readMock{session: tracking.Session{ID: "session-test", TripID: "trip-test", State: tracking.Finished}, wait: true}
	h := readHandler(t, m, 5*time.Millisecond, 1024)
	if w := readRequest(h, "/api/v1/tracking-sessions/session-test/latest-position", "valid", nil); w.Code != 503 {
		t.Fatalf("timeout: %d", w.Code)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if w := readRequest(h, "/api/v1/tracking-sessions/session-test/positions", "valid", ctx); w.Code != 503 {
		t.Fatalf("cancel: %d", w.Code)
	}
	m.wait = false
	positions := make([]tracking.Position, 30)
	for i := range positions {
		positions[i] = tracking.Position{Sequence: int64(i + 1), Latitude: 4.6, Longitude: -74.1, CapturedAt: time.Now(), ReceivedAt: time.Now()}
	}
	m.page = tracking.Page{Positions: positions, NextSequence: 30}
	if w := readRequest(h, "/api/v1/tracking-sessions/session-test/positions", "valid", nil); w.Code != 422 {
		t.Fatalf("response cap: %d", w.Code)
	}
}
