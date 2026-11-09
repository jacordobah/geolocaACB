package operationshttp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/tracking"
)

func testEvent() tracking.Event {
	now := time.Now().UTC().Truncate(time.Millisecond)
	identifier := sha256.Sum256([]byte("gps-event-v1\x00synthetic-session\x00first_meeting_point_arrival\x00synthetic-pe\x001"))
	sum := sha256.Sum256([]byte("snapshot"))
	hash := hex.EncodeToString(sum[:])
	distance := 1.0
	return tracking.Event{ID: hex.EncodeToString(identifier[:]), Type: tracking.FirstMeetingPointArrival, TrackingID: "synthetic-session", TripID: "synthetic-trip", GuideID: "synthetic-guide", GeoRouteID: "synthetic-route", MeetingPointID: "synthetic-pe", MeetingPointOrder: 1, Sequence: 1, OccurredAt: now, CapturedAt: now, ObservedFrom: now, EvidenceCount: 1, GeometryFingerprint: hash, SnapshotHash: hash, Longitude: -74.1, Latitude: 4.6, AccuracyMeters: 5, RouteDistanceMeters: 1, MeetingPointDistanceMeters: &distance}
}

func TestClientAcknowledgementAndIdempotentReceiver(t *testing.T) {
	event := testEvent()
	if err := event.Validate(); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	received := map[string]int{}
	effects := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("Idempotency-Key") != event.ID || r.Header.Get("Authorization") != "Bearer synthetic-service-token" {
			t.Error("request contract")
			w.WriteHeader(400)
			return
		}
		var body envelope
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.SchemaVersion != "gps.event.v1" || body.Event.ID != event.ID {
			t.Error("event envelope")
			w.WriteHeader(400)
			return
		}
		mu.Lock()
		received[event.ID]++
		if effects[event.ID] == 0 {
			effects[event.ID]++
		}
		count := received[event.ID]
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		status := "persisted"
		if count > 1 {
			status = "already_persisted"
		}
		_ = json.NewEncoder(w).Encode(acknowledgement{SchemaVersion: "gps.ack.v1", EventID: event.ID, Status: status})
	}))
	defer server.Close()
	client, err := New(server.URL, "/api/v1/gps-events", "synthetic-service-token", time.Second, 4096)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := client.Deliver(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	if received[event.ID] != 2 || effects[event.ID] != 1 {
		t.Fatalf("received=%v effects=%v", received, effects)
	}
}

func TestGatewayPathRequiresDurableOperationsAck(t *testing.T) {
	event := testEvent()
	var requests atomic.Int32
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/operations/api/v1/events" || r.Header.Get("Idempotency-Key") != event.ID || r.Header.Get("Authorization") != "Bearer synthetic-service-token" {
			t.Errorf("Gateway request changed: %s %s", r.Method, r.URL.Path)
		}
		var body envelope
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.SchemaVersion != "gps.event.v1" || body.Event.ID != event.ID {
			t.Errorf("event contract changed: %+v %v", body, err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK) // Gateway success alone is not Operations persistence.
	}))
	defer gateway.Close()
	client, err := New(gateway.URL, "/operations/api/v1/events", "synthetic-service-token", time.Second, 4096)
	if err != nil {
		t.Fatal(err)
	}
	failure, ok := client.Deliver(context.Background(), event).(*DeliveryError)
	if !ok || !failure.Permanent || requests.Load() != 1 {
		t.Fatalf("missing Operations ack accepted: failure=%v requests=%d", failure, requests.Load())
	}
}

func TestGatewayFailureDoesNotRedirectToOperations(t *testing.T) {
	event := testEvent()
	var directRequests atomic.Int32
	operations := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		directRequests.Add(1)
		w.WriteHeader(http.StatusCreated)
	}))
	defer operations.Close()
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", operations.URL+"/events")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer gateway.Close()
	client, err := New(gateway.URL, "/operations/events", "synthetic-service-token", time.Second, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Deliver(context.Background(), event); err == nil || directRequests.Load() != 0 {
		t.Fatalf("Gateway redirect followed: err=%v direct=%d", err, directRequests.Load())
	}
}

func TestClientFailures(t *testing.T) {
	event := testEvent()
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		delay     time.Duration
		permanent bool
	}{
		{"rate_limit", 429, "", 0, false}, {"server", 503, "", 0, false}, {"invalid_contract", 400, "", 0, true},
		{"wrong_ack", 200, `{"schema_version":"gps.ack.v1","event_id":"wrong","status":"persisted"}`, 0, true},
		{"malformed_ack", 200, "not json", 0, true}, {"oversized_ack", 200, fmt.Sprintf("%05000d", 1), 0, true},
		{"timeout", 200, "", 50 * time.Millisecond, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.delay > 0 {
					time.Sleep(tc.delay)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client, err := New(server.URL, "/api/v1/gps-events", "test", 10*time.Millisecond, 4096)
			if err != nil {
				t.Fatal(err)
			}
			err = client.Deliver(context.Background(), event)
			failure, ok := err.(*DeliveryError)
			if !ok || failure.Permanent != tc.permanent {
				t.Fatalf("failure=%v", err)
			}
		})
	}
	client, err := New("http://127.0.0.1:1", "/api/v1/gps-events", "test", 10*time.Millisecond, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Deliver(context.Background(), event); err == nil {
		t.Fatal("connection failure accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := client.Deliver(ctx, event); err == nil {
		t.Fatal("cancelled delivery accepted")
	}
}
