package redisstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/operationshttp"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/tracking"
	"github.com/redis/go-redis/v9"
)

func deliveryFixture(t *testing.T) (*Store, *redis.Client, tracking.Event, string) {
	t.Helper()
	store, client := testStore(t)
	ctx := context.Background()
	base := redisSnapshotFixture(t)
	snapshot, err := tracking.BuildSnapshotWithTransitions(base.Route, base.Policy, &tracking.TransitionPolicy{ArrivalObservations: 1, DeviationObservations: 2, RecoveryObservations: 2, MaxSampleGap: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	id := fmt.Sprintf("test-i8-%d", now.UnixNano())
	session := tracking.Session{ID: id, GuideID: "synthetic-guide", TripID: "synthetic-trip", RouteID: snapshot.Route.ID, State: tracking.Active, StartedAt: now, MaxObservationAge: time.Minute, FutureSkew: 30 * time.Second, SnapshotHash: snapshot.SnapshotHash}
	secret := bytes.Repeat([]byte{72}, 32)
	if err := store.ProvisionSnapshot(ctx, session, sha256.Sum256(secret), snapshot); err != nil {
		t.Fatal(err)
	}
	service, err := tracking.NewService(store, time.Minute, 30*time.Second, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Receive(ctx, id, base64.RawURLEncoding.EncodeToString(secret), tracking.ObservationInput{Sequence: 1, Longitude: -74.1, Latitude: 4.6, AccuracyMeters: 5, CapturedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	events, err := store.PendingEvents(ctx, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	var event tracking.Event
	for _, candidate := range events {
		if candidate.TrackingID == id && candidate.Sequence > 0 {
			event = candidate
		}
	}
	if event.ID == "" {
		t.Fatal("fixture event missing")
	}
	// These tests exercise delivery of the geographic event specifically.
	activation, err := tracking.NewLifecycleEvent(session, tracking.TrackingSessionActivated, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.ZRem(ctx, outboxKey, activation.ID).Err(); err != nil {
		t.Fatal(err)
	}
	if err := client.Del(ctx, eventKey(activation.ID)).Err(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = client.Del(ctx, sessionKey(id), snapshotKey(id), historyKey(id), positionKey(id, 1), eventKey(event.ID), deliveryKey(event.ID)).Err()
		_ = client.ZRem(ctx, activeKey, keyID(id)).Err()
		_ = client.ZRem(ctx, outboxKey, event.ID).Err()
		_ = client.ZRem(ctx, blockedKey, event.ID).Err()
		_ = client.ZRem(ctx, pendingKey, keyID(id)).Err()
		_ = client.ZRem(ctx, pendingEventsKey, event.ID).Err()
	})
	return store, client, event, id
}

func TestDeliveryClaimAOFRestartProbe(t *testing.T) {
	phase := os.Getenv("GPS_TEST_DELIVERY_RESTART_PHASE")
	if phase == "" {
		t.Skip("opt-in isolated Redis AOF lease restart probe")
	}
	store, client := testStore(t)
	ctx := context.Background()
	trackingID := "test-i8-claim-restart"
	pointID := "synthetic-pe"
	sum := sha256.Sum256([]byte("gps-event-v1\x00" + trackingID + "\x00first_meeting_point_arrival\x00" + pointID + "\x001"))
	id := hex.EncodeToString(sum[:])
	if phase == "write" {
		now := time.Now().UTC().Truncate(time.Millisecond)
		hash := sha256.Sum256([]byte("snapshot"))
		distance := 1.0
		event := tracking.Event{ID: id, Type: tracking.FirstMeetingPointArrival, TrackingID: trackingID, TripID: "synthetic-trip", GuideID: "synthetic-guide", GeoRouteID: "synthetic-route", MeetingPointID: pointID, MeetingPointOrder: 1, Sequence: 1, OccurredAt: now, CapturedAt: now, ObservedFrom: now, EvidenceCount: 1, GeometryFingerprint: hex.EncodeToString(hash[:]), SnapshotHash: hex.EncodeToString(hash[:]), Longitude: -74.1, Latitude: 4.6, AccuracyMeters: 5, RouteDistanceMeters: 1, MeetingPointDistanceMeters: &distance}
		if err := event.Validate(); err != nil {
			t.Fatal(err)
		}
		body, _ := json.Marshal(event)
		if err := client.Set(ctx, eventKey(id), body, 0).Err(); err != nil {
			t.Fatal(err)
		}
		if err := client.ZAdd(ctx, outboxKey, redis.Z{Score: float64(now.UnixMilli()), Member: id}).Err(); err != nil {
			t.Fatal(err)
		}
		claim, ok, err := store.ClaimEvent(ctx, time.Second, 10, time.Hour)
		if err != nil || !ok || claim.Attempts != 1 {
			t.Fatalf("first claim: %+v %v", claim, err)
		}
		return
	}
	if phase != "read" {
		t.Fatal("invalid restart phase")
	}
	defer func() {
		_ = client.Del(ctx, eventKey(id), deliveryKey(id)).Err()
		_ = client.ZRem(ctx, outboxKey, id).Err()
		_ = client.ZRem(ctx, blockedKey, id).Err()
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		claim, ok, err := store.ClaimEvent(ctx, time.Second, 10, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			if claim.Event.ID != id || claim.Attempts != 2 {
				t.Fatalf("recovered claim: %+v", claim)
			}
			if err := store.SettleEvent(ctx, id, claim.Token, "delivered", int64(time.Hour/time.Millisecond), ""); err != nil {
				t.Fatal(err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("lease did not recover after Redis restart")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if state, err := store.DeliveryState(ctx, id); err != nil || state != "delivered" {
		t.Fatalf("delivered state: %s %v", state, err)
	}
}

func TestDeliveryClaimsAndFencing(t *testing.T) {
	store, client, event, _ := deliveryFixture(t)
	ctx := context.Background()
	first, ok, err := store.ClaimEvent(ctx, time.Second, 10, time.Hour)
	if err != nil || !ok || first.Event.ID != event.ID {
		t.Fatalf("claim %+v %v %v", first, ok, err)
	}
	if _, ok, err := store.ClaimEvent(ctx, time.Second, 10, time.Hour); err != nil || ok {
		t.Fatalf("double claim: %v %v", ok, err)
	}
	time.Sleep(1100 * time.Millisecond)
	second, ok, err := store.ClaimEvent(ctx, time.Second, 10, time.Hour)
	if err != nil || !ok || second.Token == first.Token {
		t.Fatalf("reclaim %+v %v", second, err)
	}
	if err := store.SettleEvent(ctx, event.ID, first.Token, "delivered", int64(time.Hour/time.Millisecond), ""); !errors.Is(err, tracking.ErrConflict) {
		t.Fatalf("stale worker acknowledged: %v", err)
	}
	if err := store.SettleEvent(ctx, event.ID, second.Token, "delivered", int64(time.Hour/time.Millisecond), ""); err != nil {
		t.Fatal(err)
	}
	if state, err := store.DeliveryState(ctx, event.ID); err != nil || state != "delivered" {
		t.Fatalf("state %s %v", state, err)
	}
	if count := client.ZCard(ctx, outboxKey).Val(); count != 0 {
		t.Fatalf("outbox not empty: %d", count)
	}
	if ttl := client.PTTL(ctx, eventKey(event.ID)).Val(); ttl <= 0 || ttl > time.Hour {
		t.Fatalf("delivered TTL: %s", ttl)
	}
}

func TestLostReceiverResponseAndIdempotentRetry(t *testing.T) {
	store, client, event, _ := deliveryFixture(t)
	ctx := context.Background()
	var mu sync.Mutex
	seen := map[string]int{}
	effects := map[string]int{}
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var envelope struct {
			Event tracking.Event `json:"event"`
		}
		if json.NewDecoder(r.Body).Decode(&envelope) != nil || envelope.Event.ID != event.ID {
			w.WriteHeader(400)
			return
		}
		mu.Lock()
		seen[event.ID]++
		if effects[event.ID] == 0 {
			effects[event.ID]++
		}
		n := seen[event.ID]
		mu.Unlock()
		if n == 1 {
			if hijacker, ok := w.(http.Hijacker); ok {
				conn, _, _ := hijacker.Hijack()
				_ = conn.Close()
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"schema_version":"gps.ack.v1","event_id":"%s","status":"already_persisted"}`, event.ID)
	}))
	defer receiver.Close()
	httpClient, err := operationshttp.New(receiver.URL, "/api/v1/gps-events", "synthetic-secret", time.Second, 4096)
	if err != nil {
		t.Fatal(err)
	}
	first, ok, err := store.ClaimEvent(ctx, 2*time.Second, 10, time.Hour)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err := httpClient.Deliver(ctx, first.Event); err == nil {
		t.Fatal("lost response treated as ack")
	}
	if err := store.SettleEvent(ctx, event.ID, first.Token, "pending", 50, "transport_failure"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	second, ok, err := store.ClaimEvent(ctx, 2*time.Second, 10, time.Hour)
	if err != nil || !ok {
		t.Fatalf("reclaim: %v %v", ok, err)
	}
	if err := httpClient.Deliver(ctx, second.Event); err != nil {
		t.Fatal(err)
	}
	if err := store.SettleEvent(ctx, event.ID, second.Token, "delivered", int64(time.Hour/time.Millisecond), ""); err != nil {
		t.Fatal(err)
	}
	if seen[event.ID] != 2 || effects[event.ID] != 1 {
		t.Fatalf("seen=%v effects=%v", seen, effects)
	}
	if client.ZScore(ctx, outboxKey, event.ID).Err() != redis.Nil {
		t.Fatal("delivered event remains due")
	}
}

func TestPendingIndexDiscovery(t *testing.T) {
	store, client, _, id := deliveryFixture(t)
	ctx := context.Background()
	if err := client.HSet(ctx, sessionKey(id), "pending_sequence", "2", "pending_payload", "broken", "pending_plan", "broken").Err(); err != nil {
		t.Fatal(err)
	}
	defer client.HDel(ctx, sessionKey(id), "pending_sequence", "pending_payload", "pending_plan")
	if _, err := store.DiscoverPending(ctx, 0, 100); err != nil {
		t.Fatal(err)
	}
	ids, err := store.PendingSessions(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, candidate := range ids {
		if candidate == id {
			found = true
		}
	}
	if !found {
		t.Fatalf("pending session not discovered: %v", ids)
	}
	if err := store.RecoverPending(ctx, id); err == nil {
		t.Fatal("corrupt plan recovered")
	}
	count, err := store.DeferRecovery(ctx, id, time.Second, time.Minute)
	if err != nil || count != 1 {
		t.Fatalf("defer %d %v", count, err)
	}
	if score := client.ZScore(ctx, pendingKey, keyID(id)).Val(); score <= float64(time.Now().UnixMilli()) {
		t.Fatal("unrecoverable plan not deferred")
	}
}

func TestBlockedEventSurvivesSessionFinish(t *testing.T) {
	store, client, event, id := deliveryFixture(t)
	ctx := context.Background()
	claim, ok, err := store.ClaimEvent(ctx, 2*time.Second, 10, time.Hour)
	if err != nil || !ok {
		t.Fatalf("claim %v %v", ok, err)
	}
	if err := store.SettleEvent(ctx, event.ID, claim.Token, "blocked", 0, "receiver_http_401"); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishSession(ctx, id, time.Now().UTC().Truncate(time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if ttl := client.PTTL(ctx, eventKey(event.ID)).Val(); ttl != -1 {
		t.Fatalf("blocked event expired: %s", ttl)
	}
	if client.ZScore(ctx, blockedKey, event.ID).Err() != nil {
		t.Fatal("blocked index missing")
	}
	if client.ZScore(ctx, outboxKey, event.ID).Err() != redis.Nil {
		t.Fatal("blocked event still due")
	}
}

func TestMissingEnvelopeIsBlockedNotDiscarded(t *testing.T) {
	store, client, _, _ := deliveryFixture(t)
	ctx := context.Background()
	phantom := "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	if err := client.ZAdd(ctx, outboxKey, redis.Z{Score: float64(time.Now().Add(-time.Hour).UnixMilli()), Member: phantom}).Err(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = client.Del(ctx, deliveryKey(phantom)).Err()
		_ = client.ZRem(ctx, blockedKey, phantom).Err()
		_ = client.ZRem(ctx, outboxKey, phantom).Err()
	}()
	_, _, _ = store.ClaimEvent(ctx, 2*time.Second, 10, time.Hour)
	if client.ZScore(ctx, blockedKey, phantom).Err() != nil {
		t.Fatal("missing envelope was not retained for intervention")
	}
	if client.ZScore(ctx, outboxKey, phantom).Err() != redis.Nil {
		t.Fatal("missing envelope still loops in due queue")
	}
}

func TestLostRedisSettlementReplyIsReconciled(t *testing.T) {
	store, client, event, _ := deliveryFixture(t)
	ctx := context.Background()
	claim, ok, err := store.ClaimEvent(ctx, 2*time.Second, 10, time.Hour)
	if err != nil || !ok {
		t.Fatalf("claim %v %v", ok, err)
	}
	hook := &lostCreateReply{}
	client.AddHook(hook)
	err = store.SettleEvent(ctx, event.ID, claim.Token, "delivered", int64(time.Hour/time.Millisecond), "")
	if !errors.Is(err, tracking.ErrUncertain) {
		t.Fatalf("lost reply not reported uncertain: %v", err)
	}
	state, err := store.DeliveryState(ctx, event.ID)
	if err != nil || state != "delivered" {
		t.Fatalf("reconciliation: %s %v", state, err)
	}
	if client.ZScore(ctx, outboxKey, event.ID).Err() != redis.Nil {
		t.Fatal("delivered event remained due")
	}
}
