package outbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/geo"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/operationshttp"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/redisstore"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/routes"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/tracking"
	"github.com/redis/go-redis/v9"
)

func fixture(t *testing.T) (*redisstore.Store, *redis.Client, tracking.Event) {
	t.Helper()
	addr := os.Getenv("GPS_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("isolated Redis only")
	}
	// DB 1 separates worker tests from the package's existing Redis fixtures.
	client := redis.NewClient(&redis.Options{Addr: addr, DB: 1})
	if os.Getenv("GPS_TEST_REDIS_ISOLATED") == "1" {
		if err := client.FlushDB(context.Background()).Err(); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = client.Close() })
	store, err := redisstore.New(client, 24*time.Hour, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	trackingID := "test-i8-dispatch-" + now.Format("20060102150405.000000000")
	sum := sha256.Sum256([]byte("gps-event-v1\x00" + trackingID + "\x00first_meeting_point_arrival\x00synthetic-pe\x001"))
	hash := sha256.Sum256([]byte("snapshot"))
	distance := 1.0
	event := tracking.Event{ID: hex.EncodeToString(sum[:]), Type: tracking.FirstMeetingPointArrival, TrackingID: trackingID, TripID: "synthetic-trip", GuideID: "synthetic-guide", GeoRouteID: "synthetic-route", MeetingPointID: "synthetic-pe", MeetingPointOrder: 1, Sequence: 1, OccurredAt: now, CapturedAt: now, ObservedFrom: now, EvidenceCount: 1, GeometryFingerprint: hex.EncodeToString(hash[:]), SnapshotHash: hex.EncodeToString(hash[:]), Longitude: -74.1, Latitude: 4.6, AccuracyMeters: 5, RouteDistanceMeters: 1, MeetingPointDistanceMeters: &distance}
	if err := event.Validate(); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(event)
	ctx := context.Background()
	if err := client.Set(ctx, "gps:event:"+event.ID, data, 0).Err(); err != nil {
		t.Fatal(err)
	}
	if err := client.ZAdd(ctx, "gps:outbox:due", redis.Z{Score: float64(now.UnixMilli()), Member: event.ID}).Err(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = client.Del(ctx, "gps:event:"+event.ID, "gps:delivery:"+event.ID).Err()
		_ = client.ZRem(ctx, "gps:outbox:due", event.ID).Err()
		_ = client.ZRem(ctx, "gps:outbox:blocked", event.ID).Err()
	})
	return store, client, event
}

func TestAutomaticRecoveryCompletesSavedTransition(t *testing.T) {
	store, client, _ := fixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	id := fmt.Sprintf("test-i8-repair-%d", now.UnixNano())
	encoded := base64.RawURLEncoding.EncodeToString([]byte(id))
	pointID := "synthetic-pe"
	status := "resolved"
	verified := "verified"
	version := "synthetic-v1"
	route := routes.Route{ID: "synthetic-route", Name: "Synthetic route", GeometryVersion: &version, AssociationStatus: &verified, Geometry: geo.LineString{Coordinates: []geo.Coordinate{{Longitude: -74.1, Latitude: 4.6}, {Longitude: -74.1005, Latitude: 4.6}}}, MeetingPoints: []routes.MeetingPoint{{ID: pointID, Name: "PE", Kind: "meeting_point", Location: geo.Coordinate{Longitude: -74.1, Latitude: 4.6}}}, Endpoints: routes.Endpoints{Status: &status, FirstPointID: &pointID, Start: routes.Endpoint{PointID: &pointID}, End: routes.Endpoint{PointID: &pointID}}}
	snapshot, err := tracking.BuildSnapshotWithTransitions(route, tracking.GeographicPolicy{CorridorRadiusMeters: 30, PointRadiusMeters: 20, MaxAccuracyMeters: 25}, &tracking.TransitionPolicy{ArrivalObservations: 1, DeviationObservations: 2, RecoveryObservations: 2, MaxSampleGap: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	session := tracking.Session{ID: id, GuideID: "synthetic-guide", TripID: "synthetic-trip", RouteID: route.ID, State: tracking.Active, StartedAt: now, MaxObservationAge: time.Minute, FutureSkew: 30 * time.Second, SnapshotHash: snapshot.SnapshotHash}
	if err := store.ProvisionSnapshot(ctx, session, sha256.Sum256(bytes.Repeat([]byte{22}, 32)), snapshot); err != nil {
		t.Fatal(err)
	}
	accuracy := 5.0
	position := tracking.Position{SessionID: id, Sequence: 1, Longitude: -74.1, Latitude: 4.6, AccuracyMeters: &accuracy, CapturedAt: now, ReceivedAt: now, IdempotencyVersion: 2}
	position.Geographic, err = snapshot.Evaluate(position)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := tracking.Advance(snapshot, tracking.InitialGeographicState(snapshot), session, position)
	if err != nil {
		t.Fatal(err)
	}
	position.FirstArrival = plan.FirstArrival
	if len(plan.Events) != 1 {
		t.Fatalf("fixture events: %d", len(plan.Events))
	}
	fingerprint, body, err := position.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	checksum := sha256.Sum256(body)
	payload := fingerprint + "|" + hex.EncodeToString(checksum[:]) + "|" + string(body)
	stateJSON, _ := json.Marshal(plan.State)
	arrivalJSON, _ := json.Marshal(plan.FirstArrival)
	eventJSON, _ := json.Marshal(plan.Events[0])
	savedPlan, _ := json.Marshal(struct {
		State  string `json:"state"`
		Events []struct {
			ID      string `json:"id"`
			Payload string `json:"payload"`
			Due     int64  `json:"due"`
		} `json:"events"`
		Arrival string `json:"arrival"`
	}{State: string(stateJSON), Events: []struct {
		ID      string `json:"id"`
		Payload string `json:"payload"`
		Due     int64  `json:"due"`
	}{{ID: plan.Events[0].ID, Payload: string(eventJSON), Due: plan.Events[0].OccurredAt.UnixMilli()}}, Arrival: string(arrivalJSON)})
	if err := client.HSet(ctx, "gps:session:"+encoded, "pending_sequence", "1", "pending_fingerprint", fingerprint, "pending_payload", payload, "pending_plan", string(savedPlan)).Err(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = client.Del(ctx, "gps:session:"+encoded, "gps:snapshot:"+encoded, "gps:history:"+encoded, "gps:position:"+encoded+":1", "gps:event:"+plan.Events[0].ID).Err()
		_ = client.ZRem(ctx, "gps:active:sessions", encoded).Err()
		_ = client.ZRem(ctx, "gps:pending:sessions", encoded).Err()
		_ = client.ZRem(ctx, "gps:outbox:due", plan.Events[0].ID).Err()
	})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runCtx, cancel := context.WithCancel(ctx)
	done := StartRecovery(runCtx, store, logger, 100*time.Millisecond)
	deadline := time.After(3 * time.Second)
	for {
		stored, err := store.ReadPosition(ctx, id, 1)
		if err == nil && stored.Sequence == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("valid saved transition not recovered: %v", err)
		case <-time.After(20 * time.Millisecond):
		}
	}
	cancel()
	<-done
	if client.ZScore(ctx, "gps:outbox:due", plan.Events[0].ID).Err() != nil {
		t.Fatal("recovered event not due")
	}
	if client.HGet(ctx, "gps:session:"+encoded, "pending_sequence").Val() != "" {
		t.Fatal("pending marker retained")
	}
	if ttl := client.PTTL(ctx, "gps:position:"+encoded+":1").Val(); ttl < 24*time.Hour {
		t.Fatalf("recovered TTL: %s", ttl)
	}
}

func TestDispatcherConcurrentWorkersAndRestart(t *testing.T) {
	store, _, event := fixture(t)
	var mu sync.Mutex
	seen := 0
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"schema_version":"gps.ack.v1","event_id":"`+event.ID+`","status":"persisted"}`)
	}))
	defer receiver.Close()
	client, err := operationshttp.New(receiver.URL, "/api/v1/gps-events", "synthetic-token", time.Second, 4096)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Workers: 4, Poll: 100 * time.Millisecond, Lease: 2 * time.Second, RetryMin: time.Second, RetryMax: time.Minute, MaxPerHour: 60, DeliveredRetention: time.Hour}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	first, err := New(store, client, cfg, logger)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := first.Start(ctx)
	deadline := time.After(3 * time.Second)
	for {
		state, err := store.DeliveryState(context.Background(), event.ID)
		if err == nil && state == "delivered" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("not delivered")
		case <-time.After(20 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not drain")
	}
	second, err := New(store, client, cfg, logger)
	if err != nil {
		t.Fatal(err)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	done2 := second.Start(ctx2)
	time.Sleep(200 * time.Millisecond)
	cancel2()
	<-done2
	if seen != 1 {
		t.Fatalf("event sent %d times after restart", seen)
	}
}

func TestAutomaticRecoveryMarksIrrecoverablePlan(t *testing.T) {
	store, client, event := fixture(t)
	id := "test-i8-corrupt-" + event.ID[:12]
	encoded := base64.RawURLEncoding.EncodeToString([]byte(id))
	ctx := context.Background()
	_ = client.HSet(ctx, "gps:session:"+encoded, "pending_sequence", "1", "pending_payload", "invalid", "pending_plan", "invalid").Err()
	_ = client.ZAdd(ctx, "gps:active:sessions", redis.Z{Score: float64(time.Now().UnixMilli()), Member: encoded}).Err()
	t.Cleanup(func() {
		_ = client.Del(ctx, "gps:session:"+encoded).Err()
		_ = client.ZRem(ctx, "gps:active:sessions", encoded).Err()
		_ = client.ZRem(ctx, "gps:pending:sessions", encoded).Err()
	})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runCtx, cancel := context.WithCancel(ctx)
	done := StartRecovery(runCtx, store, logger, 100*time.Millisecond)
	deadline := time.After(3 * time.Second)
	for {
		count := client.HGet(ctx, "gps:session:"+encoded, "recovery_failures").Val()
		if count != "" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("recovery did not discover plan")
		case <-time.After(20 * time.Millisecond):
		}
	}
	cancel()
	<-done
	if client.HGet(ctx, "gps:session:"+encoded, "pending_sequence").Val() != "1" {
		t.Fatal("irrecoverable plan was discarded")
	}
}

func TestDispatcherBlocksPermanentFailure(t *testing.T) {
	store, client, event := fixture(t)
	ctx := context.Background()
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer receiver.Close()
	httpClient, err := operationshttp.New(receiver.URL, "/api/v1/gps-events", "synthetic-token", time.Second, 4096)
	if err != nil {
		t.Fatal(err)
	}
	d, err := New(store, httpClient, Config{Workers: 2, Poll: 100 * time.Millisecond, Lease: 2 * time.Second, RetryMin: time.Second, RetryMax: time.Minute, MaxPerHour: 60, DeliveredRetention: time.Hour}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := d.Start(runCtx)
	deadline := time.After(3 * time.Second)
	for {
		state, err := store.DeliveryState(ctx, event.ID)
		if err == nil && state == "blocked" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("permanent rejection was not blocked")
		case <-time.After(20 * time.Millisecond):
		}
	}
	cancel()
	<-done
	if ttl := client.PTTL(ctx, "gps:event:"+event.ID).Val(); ttl != -1 {
		t.Fatalf("blocked envelope expired: %s", ttl)
	}
	if client.ZScore(ctx, "gps:outbox:blocked", event.ID).Err() != nil {
		t.Fatal("blocked index missing")
	}
}

func TestDispatcherSurvivesRedisUnavailable(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 20 * time.Millisecond, ReadTimeout: 20 * time.Millisecond, WriteTimeout: 20 * time.Millisecond, MaxRetries: 0})
	defer client.Close()
	store, err := redisstore.New(client, 24*time.Hour, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := StartRecovery(ctx, store, slog.New(slog.NewTextHandler(io.Discard, nil)), 100*time.Millisecond)
	time.Sleep(250 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("recovery did not stop after Redis outage")
	}
}

func TestDispatcherDrainsInFlightOnCancel(t *testing.T) {
	store, _, event := fixture(t)
	started := make(chan struct{})
	release := make(chan struct{})
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"schema_version":"gps.ack.v1","event_id":"`+event.ID+`","status":"persisted"}`)
	}))
	defer receiver.Close()
	client, err := operationshttp.New(receiver.URL, "/api/v1/gps-events", "synthetic-token", time.Second, 4096)
	if err != nil {
		t.Fatal(err)
	}
	d, err := New(store, client, Config{Workers: 1, Poll: 100 * time.Millisecond, Lease: 2 * time.Second, RetryMin: time.Second, RetryMax: time.Minute, MaxPerHour: 60, DeliveredRetention: time.Hour}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := d.Start(ctx)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("delivery did not start")
	}
	cancel()
	select {
	case <-done:
		t.Fatal("dispatcher stopped before in-flight delivery finished")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("dispatcher did not drain")
	}
	if state, err := store.DeliveryState(context.Background(), event.ID); err != nil || state != "delivered" {
		t.Fatalf("drained delivery: %s %v", state, err)
	}
}
