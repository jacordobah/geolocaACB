package httpapi_test

import (
	"bytes"
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
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/geo"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/httpapi"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/redisstore"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/routes"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/server"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/tracking"
	"github.com/redis/go-redis/v9"
)

type redisPing struct{ client *redis.Client }

func (p redisPing) Ping(ctx context.Context) error { return p.client.Ping(ctx).Err() }

type integration struct {
	client  *redis.Client
	store   *redisstore.Store
	tracker *tracking.Service
	http    *httptest.Server
}

func setup(t *testing.T) integration {
	t.Helper()
	addr := os.Getenv("GPS_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("isolated Redis integration only: set GPS_TEST_REDIS_ADDR")
	}
	client := redis.NewClient(&redis.Options{Addr: addr, DialTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second, MaxRetries: 0})
	if err := client.Ping(context.Background()).Err(); err != nil {
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
	observations, err := httpapi.NewObservations(tracker, 4096, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	httpServer := httptest.NewServer(server.HandlerWithTracking(redisPing{client}, time.Second, logger, observations))
	t.Cleanup(func() { httpServer.Close(); _ = client.Close() })
	return integration{client, store, tracker, httpServer}
}

func post(t *testing.T, url, token, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var decoded map[string]any
	if err := json.NewDecoder(res.Body).Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, decoded
}

func body(seq int64, lat float64, captured time.Time) string {
	value, _ := json.Marshal(map[string]any{"sequence": seq, "latitude": lat, "longitude": -74.1, "accuracy_m": 5.0, "captured_at": captured.Format(time.RFC3339Nano)})
	return string(value)
}

func TestRedisHTTPObservationLifecycle(t *testing.T) {
	app := setup(t)
	ctx := context.Background()
	session, token, err := app.tracker.Provision(ctx, "synthetic-guide", "synthetic-trip", "synthetic-route")
	if err != nil {
		t.Fatal(err)
	}
	url := app.http.URL + "/api/v1/tracking-sessions/" + session.ID + "/observations"
	now := time.Now().UTC().Truncate(time.Millisecond)
	first := body(1, 4.6, now)
	status, response := post(t, url, token, first)
	if status != 201 || response["acceptance"] != "accepted" || response["geographic_evaluation"] != "not_evaluated" {
		t.Fatalf("first %d %+v", status, response)
	}
	received := response["received_at"]
	expires := response["expires_at"]
	stored, err := app.store.ReadPosition(ctx, session.ID, 1)
	if err != nil || stored.Geographic != nil || stored.FirstArrival != nil || stored.AccuracyMeters == nil {
		t.Fatalf("stored %+v %v", stored, err)
	}
	expiry, err := app.store.PositionExpiry(ctx, session.ID, 1)
	if err != nil || expiry.Before(now.Add(24*time.Hour)) {
		t.Fatalf("expiry %v %v", expiry, err)
	}
	status, response = post(t, url, token, first)
	if status != 200 || response["acceptance"] != "replayed" || response["received_at"] != received || response["expires_at"] != expires {
		t.Fatalf("replay %d %+v", status, response)
	}
	status, _ = post(t, url, token, body(1, 4.7, now))
	if status != 409 {
		t.Fatalf("conflict %d", status)
	}
	other, otherToken, err := app.tracker.Provision(ctx, "other-guide", "other-trip", "other-route")
	if err != nil {
		t.Fatal(err)
	}
	otherURL := app.http.URL + "/api/v1/tracking-sessions/" + other.ID + "/observations"
	status, _ = post(t, otherURL, otherToken, body(1, 4.6, now))
	if status != 201 {
		t.Fatalf("other session status %d", status)
	}
	for name, credential := range map[string]struct {
		token  string
		status int
	}{"missing": {"", 401}, "wrong": {"invalid", 401}, "other session": {otherToken, 401}} {
		status, _ = post(t, url, credential.token, body(2, 4.6, now))
		if status != credential.status {
			t.Fatalf("%s: %d", name, status)
		}
	}
	invalid := []string{body(2, 91, now), strings.Replace(body(2, 4.6, now), `"accuracy_m":5`, `"accuracy_m":-1`, 1), body(2, 4.6, now.Add(-time.Hour)), body(2, 4.6, now.Add(time.Hour))}
	for _, payload := range invalid {
		status, _ = post(t, url, token, payload)
		if status != 400 && status != 409 {
			t.Fatalf("invalid payload accepted: %d %s", status, payload)
		}
	}
	page, err := app.tracker.History(ctx, session.ID, 0, 100)
	if err != nil || len(page.Positions) != 1 {
		t.Fatalf("rejected writes %+v %v", page, err)
	}
	const workers = 12
	var wg sync.WaitGroup
	results := make(chan int, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body(2, 4.6, now.Add(time.Millisecond))))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+token)
			res, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
			if err != nil {
				results <- 0
				return
			}
			_ = res.Body.Close()
			results <- res.StatusCode
		}()
	}
	wg.Wait()
	close(results)
	created, replayed := 0, 0
	for code := range results {
		if code == 201 {
			created++
		} else if code == 200 {
			replayed++
		} else {
			t.Fatalf("concurrent status %d", code)
		}
	}
	if created != 1 || replayed != workers-1 {
		t.Fatalf("created=%d replayed=%d", created, replayed)
	}
	page, err = app.tracker.History(ctx, session.ID, 0, 100)
	if err != nil || len(page.Positions) != 2 {
		t.Fatalf("duplicate history %+v %v", page, err)
	}
	// Recreating the service and HTTP handler models a new Go process using the same Redis.
	restarted, err := tracking.NewService(app.store, 15*time.Minute, 30*time.Second, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if loaded, err := restarted.Status(ctx, session.ID); err != nil || loaded.LastSequence != 2 {
		t.Fatalf("reloaded %+v %v", loaded, err)
	}
	if err := app.tracker.Finish(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	status, _ = post(t, url, token, body(3, 4.6, now.Add(2*time.Millisecond)))
	if status != 401 {
		t.Fatalf("revoked token status %d", status)
	}
	page, err = app.tracker.History(ctx, session.ID, 0, 100)
	if err != nil || len(page.Positions) != 2 {
		t.Fatalf("history after finish %+v %v", page, err)
	}
	if got, err := app.tracker.Status(ctx, session.ID); err != nil || got.State != tracking.Finished {
		t.Fatalf("finished state %+v %v", got, err)
	}
}

func TestRedisHTTPUnavailable(t *testing.T) {
	_ = setup(t) // ensures this suite only runs when an isolated Redis was supplied
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 20 * time.Millisecond, ReadTimeout: 20 * time.Millisecond, WriteTimeout: 20 * time.Millisecond, MaxRetries: 0})
	defer client.Close()
	store, _ := redisstore.New(client, 24*time.Hour, 5*time.Minute)
	tracker, _ := tracking.NewService(store, 15*time.Minute, 30*time.Second, nil, nil)
	handler, _ := httpapi.NewObservations(tracker, 4096, time.Second)
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/tracking-sessions/{tracking_id}/observations", handler)
	server := httptest.NewServer(mux)
	defer server.Close()
	status, result := post(t, server.URL+"/api/v1/tracking-sessions/synthetic-id/observations", strings.Repeat("A", 43), body(1, 4.6, time.Now().UTC().Truncate(time.Millisecond)))
	if status != 503 || result["error"] != "storage_unavailable" {
		t.Fatalf("outage %d %+v", status, result)
	}
}

func TestRedisRestartProbe(t *testing.T) {
	phase := os.Getenv("GPS_TEST_RESTART_PHASE")
	if phase == "" {
		t.Skip("opt-in external Redis restart probe")
	}
	app := setup(t)
	ctx := context.Background()
	// Fixed synthetic entropy makes the credential reproducible across test processes.
	seed := bytes.Repeat([]byte{41}, 56)
	id := base64.RawURLEncoding.EncodeToString(seed[:24])
	if phase == "write" {
		service, _ := tracking.NewService(app.store, 15*time.Minute, 30*time.Second, nil, bytes.NewReader(seed))
		session, token, err := service.Provision(ctx, "synthetic-guide", "synthetic-trip", "synthetic-route")
		if err != nil {
			t.Fatal(err)
		}
		if session.ID != id {
			t.Fatal("restart id mismatch")
		}
		url := app.http.URL + "/api/v1/tracking-sessions/" + id + "/observations"
		status, _ := post(t, url, token, body(1, 4.6, time.Now().UTC().Truncate(time.Millisecond)))
		if status != 201 {
			t.Fatalf("write status %d", status)
		}
		return
	}
	if phase != "read" {
		t.Fatal("invalid restart phase")
	}
	stored, err := app.tracker.Status(ctx, id)
	if err != nil || stored.LastSequence != 1 {
		t.Fatalf("restart session %+v %v", stored, err)
	}
	if _, err := app.tracker.Latest(ctx, id); err != nil {
		t.Fatal(err)
	}
	token := base64.RawURLEncoding.EncodeToString(seed[24:])
	if _, err := app.tracker.Authenticate(ctx, id, token); err != nil {
		t.Fatalf("credential after restart: %v", err)
	}
}

func TestRedisHTTPGeographicSnapshotLifecycle(t *testing.T) {
	app := setup(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	first, second, resolved, verified, version := "synthetic-PE01", "synthetic-PE02", "resolved", "verified", "synthetic-v1"
	route := routes.Route{
		ID: "synthetic-route", Name: "Synthetic school route", GeometryVersion: &version, AssociationStatus: &verified,
		Geometry:      geo.LineString{Coordinates: []geo.Coordinate{{Longitude: -74.1, Latitude: 4.6}, {Longitude: -74.1005, Latitude: 4.6}, {Longitude: -74.101, Latitude: 4.6}}},
		MeetingPoints: []routes.MeetingPoint{{ID: first, Name: "PE01", Kind: "meeting_point", Location: geo.Coordinate{Longitude: -74.1, Latitude: 4.6}}, {ID: second, Name: "PE02", Kind: "meeting_point", Location: geo.Coordinate{Longitude: -74.101, Latitude: 4.6}}},
		Endpoints:     routes.Endpoints{Status: &resolved, FirstPointID: &first, Start: routes.Endpoint{PointID: &first}, End: routes.Endpoint{PointID: &second}},
	}
	snapshot, err := tracking.BuildSnapshot(route, tracking.GeographicPolicy{CorridorRadiusMeters: 30, PointRadiusMeters: 20, MaxAccuracyMeters: 25}) // synthetic test policy
	if err != nil {
		t.Fatal(err)
	}
	id := fmt.Sprintf("test-i6-http-%d", now.UnixNano())
	session := tracking.Session{ID: id, GuideID: "synthetic-guide", TripID: "synthetic-trip", RouteID: route.ID, State: tracking.Active, StartedAt: now, MaxObservationAge: 15 * time.Minute, FutureSkew: 30 * time.Second, SnapshotHash: snapshot.SnapshotHash}
	secret := bytes.Repeat([]byte{73}, 32)
	digest := sha256.Sum256(secret)
	token := base64.RawURLEncoding.EncodeToString(secret)
	encodedID := base64.RawURLEncoding.EncodeToString([]byte(id))
	t.Cleanup(func() {
		_ = app.client.Del(ctx, "gps:session:"+encodedID, "gps:snapshot:"+encodedID, "gps:history:"+encodedID, "gps:position:"+encodedID+":1").Err()
		_ = app.client.ZRem(ctx, "gps:active:sessions", encodedID).Err()
	})
	if err := app.store.ProvisionSnapshot(ctx, session, digest, snapshot); err != nil {
		t.Fatal(err)
	}
	url := app.http.URL + "/api/v1/tracking-sessions/" + id + "/observations"
	status, accepted := post(t, url, token, body(1, 4.6, now))
	geoResult, ok := accepted["geographic_evaluation"].(map[string]any)
	if status != 201 || !ok || geoResult["route_status"] != "inside" {
		t.Fatalf("HTTP evaluation %d %+v", status, accepted)
	}
	points, ok := geoResult["meeting_points"].([]any)
	if !ok || len(points) != 2 || points[0].(map[string]any)["status"] != "inside" || points[1].(map[string]any)["status"] != "outside" {
		t.Fatalf("point evaluation %+v", geoResult)
	}
	stored, err := app.store.ReadPosition(ctx, id, 1)
	if err != nil || stored.Geographic == nil || stored.Geographic.SnapshotHash != snapshot.SnapshotHash || stored.FirstArrival != nil {
		t.Fatalf("stored evaluation %+v %v", stored, err)
	}
	status, replay := post(t, url, token, body(1, 4.6, now))
	if status != 200 || replay["acceptance"] != "replayed" || !reflect.DeepEqual(replay["geographic_evaluation"], accepted["geographic_evaluation"]) || replay["expires_at"] != accepted["expires_at"] {
		t.Fatalf("replay %d %+v", status, replay)
	}
	status, _ = post(t, url, token, body(1, 4.61, now))
	if status != 409 {
		t.Fatalf("sequence conflict HTTP %d", status)
	}
	page, err := app.store.History(ctx, id, 0, 10)
	if err != nil || len(page.Positions) != 1 {
		t.Fatalf("history %+v %v", page, err)
	}
	expiry, err := app.store.PositionExpiry(ctx, id, 1)
	if err != nil || expiry.Before(now.Add(24*time.Hour)) {
		t.Fatalf("position expiry %s %v", expiry, err)
	}
	if err := app.tracker.Finish(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.Snapshot(ctx, id); err != nil {
		t.Fatalf("snapshot expired on finish: %v", err)
	}
	status, _ = post(t, url, token, body(2, 4.6, now))
	if status != 401 {
		t.Fatalf("closed session accepted HTTP %d", status)
	}
}

func TestRedisHTTPGeographicArrivalEvent(t *testing.T) {
	app := setup(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	pointID, resolved, verified, version := "synthetic-PE01", "resolved", "verified", "synthetic-v1"
	route := routes.Route{ID: "synthetic-event-route", Name: "Synthetic event route", GeometryVersion: &version, AssociationStatus: &verified,
		Geometry:      geo.LineString{Coordinates: []geo.Coordinate{{Longitude: -74.1, Latitude: 4.6}, {Longitude: -74.1005, Latitude: 4.6}}},
		MeetingPoints: []routes.MeetingPoint{{ID: pointID, Name: "PE01", Kind: "meeting_point", Location: geo.Coordinate{Longitude: -74.1, Latitude: 4.6}}},
		Endpoints:     routes.Endpoints{Status: &resolved, FirstPointID: &pointID, Start: routes.Endpoint{PointID: &pointID}, End: routes.Endpoint{PointID: &pointID}},
	}
	policy := &tracking.TransitionPolicy{ArrivalObservations: 2, ArrivalDwell: time.Second, DeviationObservations: 2, DeviationDwell: time.Second, RecoveryObservations: 2, RecoveryDwell: time.Second, MaxSampleGap: 5 * time.Second}
	snapshot, err := tracking.BuildSnapshotWithTransitions(route, tracking.GeographicPolicy{CorridorRadiusMeters: 30, PointRadiusMeters: 20, MaxAccuracyMeters: 25}, policy)
	if err != nil {
		t.Fatal(err)
	}
	id := fmt.Sprintf("test-i7-http-%d", now.UnixNano())
	session := tracking.Session{ID: id, GuideID: "synthetic-guide", TripID: "synthetic-trip", RouteID: route.ID, State: tracking.Active, StartedAt: now, MaxObservationAge: 15 * time.Minute, FutureSkew: 30 * time.Second, SnapshotHash: snapshot.SnapshotHash}
	secret := bytes.Repeat([]byte{86}, 32)
	digest := sha256.Sum256(secret)
	token := base64.RawURLEncoding.EncodeToString(secret)
	encodedID := base64.RawURLEncoding.EncodeToString([]byte(id))
	t.Cleanup(func() {
		events, _ := app.store.PendingEvents(ctx, 0, 100)
		for _, event := range events {
			if event.TrackingID == id {
				_ = app.client.Del(ctx, "gps:event:"+event.ID).Err()
				_ = app.client.ZRem(ctx, "gps:outbox:due", event.ID).Err()
			}
		}
		_ = app.client.Del(ctx, "gps:session:"+encodedID, "gps:snapshot:"+encodedID, "gps:history:"+encodedID, "gps:position:"+encodedID+":1", "gps:position:"+encodedID+":2").Err()
		_ = app.client.ZRem(ctx, "gps:active:sessions", encodedID).Err()
	})
	if err := app.store.ProvisionSnapshot(ctx, session, digest, snapshot); err != nil {
		t.Fatal(err)
	}
	url := app.http.URL + "/api/v1/tracking-sessions/" + id + "/observations"
	status, first := post(t, url, token, body(1, 4.6, now))
	if status != 201 || first["geographic_evaluation"] == "not_evaluated" {
		t.Fatalf("first HTTP response %d %+v", status, first)
	}
	status, second := post(t, url, token, body(2, 4.6, now.Add(time.Second)))
	if status != 201 || second["acceptance"] != "accepted" {
		t.Fatalf("arrival HTTP response %d %+v", status, second)
	}
	status, retry := post(t, url, token, body(2, 4.6, now.Add(time.Second)))
	if status != 200 || retry["acceptance"] != "replayed" || retry["expires_at"] != second["expires_at"] {
		t.Fatalf("HTTP replay %d %+v", status, retry)
	}
	stored, err := app.store.Session(ctx, id)
	if err != nil || stored.FirstArrival == nil || stored.FirstArrival.Sequence != 2 {
		t.Fatalf("first arrival missing: %+v %v", stored, err)
	}
	events, err := app.store.PendingEvents(ctx, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if event.TrackingID == id {
			count++
			if event.Type != tracking.FirstMeetingPointArrival {
				t.Fatalf("wrong event %+v", event)
			}
		}
	}
	if count != 1 {
		t.Fatalf("HTTP arrival produced %d events", count)
	}
}
