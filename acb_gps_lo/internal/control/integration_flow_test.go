package control_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
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
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/operationshttp"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/outbox"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/redisstore"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/server"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/tracking"
	"github.com/redis/go-redis/v9"
)

type flowPolicies struct{}

func (flowPolicies) Resolve(_ context.Context, id string) (control.Policy, error) {
	geographic := tracking.GeographicPolicy{CorridorRadiusMeters: 30, PointRadiusMeters: 20, MaxAccuracyMeters: 25}
	transition := tracking.TransitionPolicy{ArrivalObservations: 1, DeviationObservations: 2, RecoveryObservations: 2, MaxSampleGap: 5 * time.Second}
	ref, err := tracking.NewPolicyReference(id, 1, geographic, transition)
	if err != nil {
		return control.Policy{}, err
	}
	return control.Policy{Geographic: geographic, Transitions: &transition, Reference: ref}, nil
}

type flowVerifier struct{}

func (flowVerifier) Verify(_ context.Context, token string) (control.Principal, error) {
	if token != "isolated-service-token" {
		return control.Principal{}, control.ErrUnauthenticated
	}
	return control.Principal{Subject: "isolated-controller", Audience: control.Audience, ExpiresAt: time.Now().Add(time.Hour), Capabilities: map[string]bool{control.CreateCapability: true, control.ReadCapability: true, control.FinishCapability: true}, TripIDs: map[string]bool{"synthetic-trip": true}}, nil
}

type flowPing struct{ client *redis.Client }

func (p flowPing) Ping(ctx context.Context) error { return p.client.Ping(ctx).Err() }

// This example uses only a synthetic route, a test verifier and a fake receiver.
// None of these are wired into the production binary.
func TestIsolatedContractFlow(t *testing.T) {
	addr := os.Getenv("GPS_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("requires isolated Redis")
	}
	ctx := context.Background()
	client := redis.NewClient(&redis.Options{Addr: addr, DB: 5})
	defer client.Close()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("GPS_TEST_REDIS_ISOLATED") == "1" {
		if err := client.FlushDB(ctx).Err(); err != nil {
			t.Fatal(err)
		}
	}
	store, err := redisstore.New(client, 24*time.Hour, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	tracker, err := tracking.NewService(store, 15*time.Minute, 30*time.Second, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	secret := sha256.Sum256([]byte("isolated test derivation key; never use in production"))
	controller, err := control.New(tracker, reader{route: syntheticRoute()}, flowPolicies{}, store, secret[:], time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	observations, _ := httpapi.NewObservations(tracker, 4096, 3*time.Second)
	controlHandler, _ := httpapi.NewControl(controller, flowVerifier{}, 3*time.Second)
	reads, _ := httpapi.NewReads(controller, flowVerifier{}, 3*time.Second, 100, 1<<20)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	api := server.HandlerWithReads(flowPing{client}, time.Second, logger, observations, controlHandler, reads)
	request := func(method, path, token, body string, headers map[string]string) (int, []byte) {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w.Code, w.Body.Bytes()
	}
	createBody := `{"trip_id":"synthetic-trip","guide_id":"synthetic-guide","geo_route_id":"test-route","policy_id":"isolated-policy"}`
	status, body := request("POST", "/api/v1/tracking-sessions", "isolated-service-token", createBody, map[string]string{"Idempotency-Key": "isolated-request"})
	if status != 201 {
		t.Fatalf("create %d %s", status, body)
	}
	var created struct {
		TrackingID string `json:"tracking_id"`
		Credential string `json:"observation_credential"`
	}
	if err := json.Unmarshal(body, &created); err != nil || created.TrackingID == "" || created.Credential == "" {
		t.Fatalf("created %v %s", err, body)
	}
	status, body = request("POST", "/api/v1/tracking-sessions", "isolated-service-token", createBody, map[string]string{"Idempotency-Key": "isolated-request"})
	if status != 200 || !bytes.Contains(body, []byte(created.TrackingID)) {
		t.Fatalf("idempotent create %d %s", status, body)
	}
	path := "/api/v1/tracking-sessions/" + created.TrackingID
	for seq := 1; seq <= 2; seq++ {
		gps, _ := json.Marshal(map[string]any{"sequence": seq, "latitude": 4.6, "longitude": -74.1, "accuracy_m": 5.0, "captured_at": time.Now().UTC().Truncate(time.Millisecond).Format(time.RFC3339Nano)})
		status, body = request("POST", path+"/observations", created.Credential, string(gps), nil)
		if status != 201 {
			t.Fatalf("observation %d: %d %s", seq, status, body)
		}
	}
	status, body = request("GET", path+"/latest-position", "isolated-service-token", "", nil)
	if status != 200 || !bytes.Contains(body, []byte(`"sequence":2`)) || !bytes.Contains(body, []byte(`"route_status":"inside"`)) {
		t.Fatalf("latest %d %s", status, body)
	}
	status, body = request("GET", path+"/positions?limit=1", "isolated-service-token", "", nil)
	if status != 200 || !bytes.Contains(body, []byte(`"has_more":true`)) {
		t.Fatalf("history %d %s", status, body)
	}
	seen := map[string]int{}
	var mu sync.Mutex
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var envelope struct {
			Event struct {
				ID string `json:"event_id"`
			} `json:"event"`
		}
		if json.NewDecoder(r.Body).Decode(&envelope) != nil || r.Header.Get("Idempotency-Key") != envelope.Event.ID {
			w.WriteHeader(400)
			return
		}
		mu.Lock()
		seen[envelope.Event.ID]++
		count := seen[envelope.Event.ID]
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if count > 1 {
			_, _ = w.Write([]byte(`{"schema_version":"gps.ack.v1","event_id":"` + envelope.Event.ID + `","status":"already_persisted"}`))
		} else {
			_, _ = w.Write([]byte(`{"schema_version":"gps.ack.v1","event_id":"` + envelope.Event.ID + `","status":"persisted"}`))
		}
	}))
	defer receiver.Close()
	deliveryClient, err := operationshttp.New(receiver.URL, "/events", "isolated-test-service-token", time.Second, 4096)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := outbox.New(store, deliveryClient, outbox.Config{Workers: 2, Poll: 100 * time.Millisecond, Lease: 3 * time.Second, RetryMin: time.Second, RetryMax: time.Minute, MaxPerHour: 60, DeliveredRetention: time.Hour}, logger)
	if err != nil {
		t.Fatal(err)
	}
	deliveryCtx, cancel := context.WithCancel(ctx)
	done := dispatcher.Start(deliveryCtx)
	status, body = request("POST", path+"/finish", "isolated-service-token", "", nil)
	if status != 200 {
		t.Fatalf("finish %d %s", status, body)
	}
	status, body = request("GET", path+"/positions?limit=10", "isolated-service-token", "", nil)
	if status != 200 || !bytes.Contains(body, []byte(`"tracking_state":"finished"`)) {
		t.Fatalf("retained history %d %s", status, body)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		pending, _, _, err := store.OutboxStats(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if pending == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("outbox pending %d", pending)
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-done
	mu.Lock()
	count := len(seen)
	mu.Unlock()
	if count < 3 {
		t.Fatalf("expected activation, arrival and finish delivery; got %d", count)
	}
}
