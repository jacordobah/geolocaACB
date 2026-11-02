package httpapi_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/control"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/httpapi"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/redisstore"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/routes"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/server"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/tracking"
	"github.com/redis/go-redis/v9"
)

type unusedRouteReader struct{}

func (unusedRouteReader) Get(context.Context, string) (routes.Route, error) {
	return routes.Route{}, routes.ErrNotFound
}

type unusedPolicies struct{}

func (unusedPolicies) Resolve(context.Context, string) (control.Policy, error) {
	return control.Policy{}, tracking.ErrInvalid
}

type scopedReadVerifier struct{ id string }

func (v scopedReadVerifier) Verify(_ context.Context, token string) (control.Principal, error) {
	if token != "read-only-test-token" {
		return control.Principal{}, control.ErrUnauthenticated
	}
	return control.Principal{Subject: "isolated-reader", Audience: control.Audience, ExpiresAt: time.Now().Add(time.Hour), Capabilities: map[string]bool{control.ReadCapability: true}, TrackingIDs: map[string]bool{v.id: true}}, nil
}

func TestReadHTTPWithIsolatedRedis(t *testing.T) {
	addr := os.Getenv("GPS_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("isolated Redis integration only")
	}
	ctx := context.Background()
	client := redis.NewClient(&redis.Options{Addr: addr})
	defer client.Close()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	store, err := redisstore.New(client, 24*time.Hour, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	tracker, err := tracking.NewService(store, 15*time.Minute, 30*time.Second, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	session, observationToken, err := tracker.Provision(ctx, "synthetic-guide", "synthetic-trip", "synthetic-route")
	if err != nil {
		t.Fatal(err)
	}
	secret := sha256.Sum256([]byte("isolated read integration secret only"))
	controlService, err := control.New(tracker, unusedRouteReader{}, unusedPolicies{}, store, secret[:], time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	reads, err := httpapi.NewReads(controlService, scopedReadVerifier{id: session.ID}, 5*time.Second, 100, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	observations, err := httpapi.NewObservations(tracker, 4096, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	api := serverHandlerForReadTest(reads, observations)
	get := func(path, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	latestPath := "/api/v1/tracking-sessions/" + session.ID + "/latest-position"
	historyPath := "/api/v1/tracking-sessions/" + session.ID + "/positions"
	if w := get(latestPath, "read-only-test-token"); w.Code != 204 {
		t.Fatalf("new session: %d", w.Code)
	}
	base := time.Now().UTC().Truncate(time.Millisecond)
	for _, seq := range []int64{1, 3, 7} {
		captured := base.Add(time.Duration(seq) * time.Millisecond).Format(time.RFC3339Nano)
		body := fmt.Sprintf(`{"sequence":%d,"latitude":4.6,"longitude":-74.1,"accuracy_m":5,"captured_at":%q}`, seq, captured)
		r := httptest.NewRequest("POST", "/api/v1/tracking-sessions/"+session.ID+"/observations", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+observationToken)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != 201 {
			t.Fatalf("observation %d: %d %s", seq, w.Code, w.Body.String())
		}
	}
	if w := get(latestPath, observationToken); w.Code != 401 {
		t.Fatalf("observation token read: %d", w.Code)
	}
	latest := get(latestPath, "read-only-test-token")
	if latest.Code != 200 || !strings.Contains(latest.Body.String(), `"sequence":7`) || !strings.Contains(latest.Body.String(), `"not_evaluated"`) {
		t.Fatalf("latest %d %s", latest.Code, latest.Body.String())
	}
	var page struct {
		Positions []struct {
			Sequence int64 `json:"sequence"`
		} `json:"positions"`
		Next    int64 `json:"next_after_sequence"`
		HasMore bool  `json:"has_more"`
	}
	w := get(historyPath+"?limit=2", "read-only-test-token")
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || w.Code != 200 || len(page.Positions) != 2 || page.Positions[0].Sequence != 1 || page.Positions[1].Sequence != 3 || page.Next != 3 || !page.HasMore {
		t.Fatalf("first page %d %+v %v", w.Code, page, err)
	}
	w = get(historyPath+"?limit=2&after_sequence=3", "read-only-test-token")
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || w.Code != 200 || len(page.Positions) != 1 || page.Positions[0].Sequence != 7 || page.HasMore {
		t.Fatalf("second page %d %+v %v", w.Code, page, err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if w := get(latestPath, "read-only-test-token"); w.Code != 200 {
				t.Errorf("concurrent read %d", w.Code)
			}
		}()
	}
	wg.Wait()
	second, secondToken, err := tracker.Provision(ctx, "second-guide", "second-trip", "second-route")
	if err != nil {
		t.Fatal(err)
	}
	var parallel sync.WaitGroup
	parallel.Add(2)
	go func() {
		defer parallel.Done()
		_, err := tracker.Receive(ctx, second.ID, secondToken, tracking.ObservationInput{Sequence: 1, Latitude: 4.61, Longitude: -74.11, AccuracyMeters: 6, CapturedAt: time.Now().UTC().Truncate(time.Millisecond)})
		if err != nil {
			t.Errorf("second session receive: %v", err)
		}
	}()
	go func() {
		defer parallel.Done()
		if w := get(latestPath, "read-only-test-token"); w.Code != 200 {
			t.Errorf("parallel first session read: %d", w.Code)
		}
	}()
	parallel.Wait()
	secondReads, err := httpapi.NewReads(controlService, scopedReadVerifier{id: second.ID}, 5*time.Second, 100, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	secondRequest := httptest.NewRequest("GET", "/api/v1/tracking-sessions/"+second.ID+"/latest-position", nil)
	secondRequest.Header.Set("Authorization", "Bearer read-only-test-token")
	secondResponse := httptest.NewRecorder()
	serverHandlerForReadTest(secondReads, nil).ServeHTTP(secondResponse, secondRequest)
	if secondResponse.Code != 200 || !strings.Contains(secondResponse.Body.String(), `"sequence":1`) {
		t.Fatalf("second session read: %d %s", secondResponse.Code, secondResponse.Body.String())
	}
	if w := get("/api/v1/tracking-sessions/"+second.ID+"/positions", "read-only-test-token"); w.Code != 404 {
		t.Fatalf("other session visible: %d", w.Code)
	}
	if err := tracker.Finish(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	if w := get(historyPath+"?limit=2", "read-only-test-token"); w.Code != 200 || !strings.Contains(w.Body.String(), `"tracking_state":"finished"`) {
		t.Fatalf("finished history: %d %s", w.Code, w.Body.String())
	}
	// Accelerated expiry affects only synthetic keys in this isolated Redis.
	encoded := base64.RawURLEncoding.EncodeToString([]byte(session.ID))
	for _, seq := range []int64{1, 3, 7} {
		if err := client.PExpire(ctx, fmt.Sprintf("gps:position:%s:%d", encoded, seq), time.Millisecond).Err(); err != nil {
			t.Fatal(err)
		}
	}
	if err := client.HSet(ctx, "gps:session:"+encoded, "max_expiry_ms", time.Now().Add(-time.Second).UnixMilli()).Err(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if w := get(latestPath, "read-only-test-token"); w.Code != 410 {
		t.Fatalf("expired latest: %d %s", w.Code, w.Body.String())
	}
	if w := get(historyPath, "read-only-test-token"); w.Code != 200 || !strings.Contains(w.Body.String(), `"positions":[]`) {
		t.Fatalf("expired history: %d %s", w.Code, w.Body.String())
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if w := get(historyPath, "read-only-test-token"); w.Code != 503 {
		t.Fatalf("Redis unavailable: %d", w.Code)
	}
}

func serverHandlerForReadTest(reads, observations http.Handler) http.Handler {
	return server.HandlerWithReads(healthy{}, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)), observations, nil, reads)
}

func TestReadRestartPhase(t *testing.T) {
	phase := os.Getenv("GPS_TEST_READ_RESTART_PHASE")
	if phase == "" {
		t.Skip("isolated AOF restart test only")
	}
	addr := os.Getenv("GPS_TEST_REDIS_ADDR")
	if addr == "" {
		t.Fatal("isolated Redis address required")
	}
	ctx := context.Background()
	client := redis.NewClient(&redis.Options{Addr: addr})
	defer client.Close()
	for attempt := 0; attempt < 10; attempt++ {
		if client.Ping(ctx).Err() == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	store, err := redisstore.New(client, 24*time.Hour, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	tracker, err := tracking.NewService(store, 15*time.Minute, 30*time.Second, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	const id = "test-read-restart-i92"
	if phase == "write" {
		secret := sha256.Sum256([]byte("isolated restart observation token material"))
		digest := sha256.Sum256(secret[:])
		session := tracking.Session{ID: id, GuideID: "test-guide", TripID: "test-trip", RouteID: "test-route", State: tracking.Active, StartedAt: time.Now().UTC().Truncate(time.Millisecond), MaxObservationAge: 15 * time.Minute, FutureSkew: 30 * time.Second}
		if err := store.Provision(ctx, session, digest); err != nil {
			t.Fatal(err)
		}
		_, err = tracker.Receive(ctx, id, base64.RawURLEncoding.EncodeToString(secret[:]), tracking.ObservationInput{Sequence: 1, Latitude: 4.6, Longitude: -74.1, AccuracyMeters: 5, CapturedAt: time.Now().UTC().Truncate(time.Millisecond)})
		if err != nil {
			t.Fatal(err)
		}
	} else if phase == "read" {
		secret := sha256.Sum256([]byte("isolated restart read service secret"))
		useCase, err := control.New(tracker, unusedRouteReader{}, unusedPolicies{}, store, secret[:], time.Minute, nil)
		if err != nil {
			t.Fatal(err)
		}
		reads, err := httpapi.NewReads(useCase, scopedReadVerifier{id: id}, time.Second, 100, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		handler := serverHandlerForReadTest(reads, nil)
		for _, path := range []string{"/api/v1/tracking-sessions/" + id + "/latest-position", "/api/v1/tracking-sessions/" + id + "/positions"} {
			r := httptest.NewRequest("GET", path, nil)
			r.Header.Set("Authorization", "Bearer read-only-test-token")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != 200 || !strings.Contains(w.Body.String(), `"sequence":1`) {
				t.Fatalf("restart read %s: %d %s", path, w.Code, w.Body.String())
			}
		}
	} else {
		t.Fatalf("unknown phase %q", phase)
	}
	t.Logf("read restart phase %s passed", phase)
}
