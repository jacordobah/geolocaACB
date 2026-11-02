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
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/tracking"
	"github.com/redis/go-redis/v9"
)

func eventSnapshot(t *testing.T) tracking.GeographicSnapshot {
	t.Helper()
	base := redisSnapshotFixture(t)
	policy := &tracking.TransitionPolicy{ArrivalObservations: 2, ArrivalDwell: time.Second, DeviationObservations: 2, DeviationDwell: time.Second, RecoveryObservations: 2, RecoveryDwell: time.Second, MaxSampleGap: 5 * time.Second}
	snapshot, err := tracking.BuildSnapshotWithTransitions(base.Route, base.Policy, policy)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestEventTransitionsAndOutbox(t *testing.T) {
	store, client := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	snapshot := eventSnapshot(t)
	id := fmt.Sprintf("test-i7-%d", now.UnixNano())
	session := tracking.Session{ID: id, GuideID: "synthetic-guide", TripID: "synthetic-trip", RouteID: snapshot.Route.ID, State: tracking.Active, StartedAt: now, MaxObservationAge: 15 * time.Minute, FutureSkew: 30 * time.Second, SnapshotHash: snapshot.SnapshotHash}
	secret := bytes.Repeat([]byte{84}, 32)
	digest := sha256.Sum256(secret)
	token := base64.RawURLEncoding.EncodeToString(secret)
	var eventIDs []string
	t.Cleanup(func() {
		keys := []string{sessionKey(id), snapshotKey(id), historyKey(id)}
		for seq := int64(1); seq <= 8; seq++ {
			keys = append(keys, positionKey(id, seq))
		}
		for _, eventID := range eventIDs {
			keys = append(keys, eventKey(eventID))
			_ = client.ZRem(ctx, outboxKey, eventID).Err()
		}
		_ = client.Del(ctx, keys...).Err()
		_ = client.ZRem(ctx, activeKey, keyID(id)).Err()
	})
	if err := store.ProvisionSnapshot(ctx, session, digest, snapshot); err != nil {
		t.Fatal(err)
	}
	client.AddHook(&lostCreateReply{}) // simulate one lost Lua reply after a committed write
	service, err := tracking.NewService(store, 15*time.Minute, 30*time.Second, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	send := func(seq int64, lon, lat, accuracy float64) tracking.Acceptance {
		t.Helper()
		accepted, err := service.Receive(ctx, id, token, tracking.ObservationInput{Sequence: seq, Longitude: lon, Latitude: lat, AccuracyMeters: accuracy, CapturedAt: now.Add(time.Duration(seq-1) * time.Second)})
		if err != nil {
			t.Fatalf("sequence %d: %v", seq, err)
		}
		return accepted
	}
	send(1, -74.1, 4.6, 5)
	second := send(2, -74.1, 4.6, 5)
	if second.Position.FirstArrival == nil || second.Position.FirstArrival.MeetingPointID != "synthetic-PE01" {
		t.Fatalf("first arrival: %+v", second.Position.FirstArrival)
	}
	loaded, err := store.Session(ctx, id)
	if err != nil || loaded.FirstArrival == nil || loaded.FirstArrival.Sequence != 2 {
		t.Fatalf("session arrival: %+v %v", loaded, err)
	}
	firstEvents, err := store.PendingEvents(ctx, 0, 10)
	if err != nil || len(firstEvents) != 1 || firstEvents[0].Type != tracking.FirstMeetingPointArrival {
		t.Fatalf("first outbox: %+v %v", firstEvents, err)
	}
	eventIDs = append(eventIDs, firstEvents[0].ID)
	if ttl := client.PTTL(ctx, eventKey(firstEvents[0].ID)).Val(); ttl != -1 {
		t.Fatalf("event has TTL: %s", ttl)
	}
	replay := send(2, -74.1, 4.6, 5)
	if !replay.Duplicate || !replay.ExpiresAt.Equal(second.ExpiresAt) {
		t.Fatal("replay changed result or TTL")
	}
	if _, err := service.Receive(ctx, id, token, tracking.ObservationInput{Sequence: 2, Longitude: -74.101, Latitude: 4.6, AccuracyMeters: 5, CapturedAt: now.Add(time.Second)}); !errors.Is(err, tracking.ErrConflict) {
		t.Fatalf("different replay: %v", err)
	}
	// Recreate Go service with an empty prepared-route cache.
	service, err = tracking.NewService(store, 15*time.Minute, 30*time.Second, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	send(3, -74.101, 4.6, 5)
	send(4, -74.101, 4.6, 5)
	send(5, -74.1005, 4.61, 5)
	send(6, -74.1005, 4.61, 5)
	send(7, -74.1005, 4.6, 5)
	send(8, -74.1005, 4.6, 5)
	events, err := store.PendingEvents(ctx, 0, 10)
	if err != nil || len(events) != 4 {
		t.Fatalf("outbox events: %+v %v", events, err)
	}
	want := map[tracking.EventType]bool{tracking.FirstMeetingPointArrival: false, tracking.MeetingPointArrival: false, tracking.RouteDeviationStarted: false, tracking.RouteDeviationResolved: false}
	for _, event := range events {
		if want[event.Type] {
			t.Fatalf("duplicate event type %s", event.Type)
		}
		want[event.Type] = true
		if event.GuideID != session.GuideID || event.TripID != session.TripID || event.SnapshotHash != snapshot.SnapshotHash || event.GeometryFingerprint != snapshot.GeometryFingerprint {
			t.Fatalf("incomplete envelope: %+v", event)
		}
		if event.ID != firstEvents[0].ID {
			eventIDs = append(eventIDs, event.ID)
		}
	}
	for kind, seen := range want {
		if !seen {
			t.Fatalf("missing %s", kind)
		}
	}
	page, err := store.History(ctx, id, 0, 20)
	if err != nil || len(page.Positions) != 8 {
		t.Fatalf("history %+v %v", page, err)
	}
	state, err := store.GeographicState(ctx, id)
	if err != nil || state.LastSequence != 8 || state.Deviation.Active || state.Points[0].ConfirmedSequence != 2 || state.Points[1].ConfirmedSequence != 4 {
		t.Fatalf("state %+v %v", state, err)
	}
	if err := store.FinishSession(ctx, id, time.Now().UTC().Truncate(time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	stillPending, err := store.PendingEvents(ctx, 0, 10)
	if err != nil || len(stillPending) != 4 {
		t.Fatalf("finish lost events: %+v %v", stillPending, err)
	}
	for _, event := range stillPending {
		if ttl := client.PTTL(ctx, eventKey(event.ID)).Val(); ttl != -1 {
			t.Fatalf("finished event TTL: %s", ttl)
		}
	}
	if ttl := client.PTTL(ctx, positionKey(id, 2)).Val(); ttl < 24*time.Hour {
		t.Fatalf("position shortened: %s", ttl)
	}
	// Simulate positions aging out in this isolated store. Event envelopes and
	// their pending index are independent of GPS-history retention.
	if err := client.Del(ctx, positionKey(id, 1), positionKey(id, 2)).Err(); err != nil {
		t.Fatal(err)
	}
	stillPending, err = store.PendingEvents(ctx, 0, 10)
	if err != nil || len(stillPending) != 4 {
		t.Fatalf("history expiry removed events: %+v %v", stillPending, err)
	}
	phantom := "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	if err := client.ZAdd(ctx, outboxKey, redis.Z{Score: float64(now.UnixMilli()), Member: phantom}).Err(); err != nil {
		t.Fatal(err)
	}
	defer client.ZRem(ctx, outboxKey, phantom)
	if _, err := store.PendingEvents(ctx, 0, 10); !errors.Is(err, tracking.ErrUnavailable) {
		t.Fatalf("missing envelope must be reported, got %v", err)
	}
	if client.ZScore(ctx, outboxKey, phantom).Err() != nil {
		t.Fatal("missing envelope index was silently discarded")
	}
}

func TestPendingTransitionRecoveryAndPrewriteGuard(t *testing.T) {
	store, client := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	base := redisSnapshotFixture(t)
	policy := &tracking.TransitionPolicy{ArrivalObservations: 1, DeviationObservations: 2, RecoveryObservations: 2, MaxSampleGap: 5 * time.Second}
	snapshot, err := tracking.BuildSnapshotWithTransitions(base.Route, base.Policy, policy)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"prewrite", "pending", "after-position", "after-event"} {
		t.Run(mode, func(t *testing.T) {
			id := fmt.Sprintf("test-i7-%s-%d", mode, now.UnixNano())
			session := tracking.Session{ID: id, GuideID: "synthetic-guide", TripID: "synthetic-trip", RouteID: snapshot.Route.ID, State: tracking.Active, StartedAt: now, MaxObservationAge: time.Minute, FutureSkew: 30 * time.Second, SnapshotHash: snapshot.SnapshotHash}
			digest := sha256.Sum256([]byte("synthetic-token"))
			if err := store.ProvisionSnapshot(ctx, session, digest, snapshot); err != nil {
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
				t.Fatal("fixture did not trigger an event")
			}
			eventID := plan.Events[0].ID
			t.Cleanup(func() {
				_ = client.Del(ctx, sessionKey(id), snapshotKey(id), historyKey(id), positionKey(id, 1), eventKey(eventID)).Err()
				_ = client.ZRem(ctx, activeKey, keyID(id)).Err()
				_ = client.ZRem(ctx, outboxKey, eventID).Err()
			})
			if mode == "prewrite" {
				if err := client.HSet(ctx, eventKey(eventID), "wrong_type", "synthetic").Err(); err != nil {
					t.Fatal(err)
				}
				if _, err := store.AcceptTransition(ctx, position, plan); !errors.Is(err, tracking.ErrUnavailable) {
					t.Fatalf("corrupt event key: %v", err)
				}
				if exists := client.Exists(ctx, positionKey(id, 1)).Val(); exists != 0 {
					t.Fatal("position partially written")
				}
				state, err := store.GeographicState(ctx, id)
				if err != nil || state.LastSequence != 0 {
					t.Fatalf("state advanced after rejection: %+v %v", state, err)
				}
				return
			}
			fingerprint, body, err := position.Fingerprint()
			if err != nil {
				t.Fatal(err)
			}
			checksum := sha256.Sum256(body)
			payload := fingerprint + "|" + hex.EncodeToString(checksum[:]) + "|" + string(body)
			planJSON, err := encodePlan(plan)
			if err != nil {
				t.Fatal(err)
			}
			var originalExpiry time.Time
			if mode == "after-position" || mode == "after-event" {
				originalExpiry = now.Add(25 * time.Hour).Truncate(time.Millisecond)
				if err := client.Set(ctx, positionKey(id, 1), payload, 0).Err(); err != nil {
					t.Fatal(err)
				}
				if err := client.PExpireAt(ctx, positionKey(id, 1), originalExpiry).Err(); err != nil {
					t.Fatal(err)
				}
				if err := client.ZAdd(ctx, historyKey(id), redis.Z{Score: 1, Member: "1"}).Err(); err != nil {
					t.Fatal(err)
				}
				stateJSON, _ := json.Marshal(plan.State)
				if err := client.HSet(ctx, sessionKey(id), "geo_state", string(stateJSON)).Err(); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "after-event" {
				eventJSON, _ := json.Marshal(plan.Events[0])
				if err := client.Set(ctx, eventKey(eventID), eventJSON, 0).Err(); err != nil {
					t.Fatal(err)
				}
			}
			if err := client.HSet(ctx, sessionKey(id), "pending_sequence", "1", "pending_fingerprint", fingerprint, "pending_plan", planJSON, "pending_payload", payload).Err(); err != nil {
				t.Fatal(err)
			}
			if _, err := store.ReadPosition(ctx, id, 1); !errors.Is(err, tracking.ErrPending) {
				t.Fatalf("partial result visible: %v", err)
			}
			// A new Go adapter can complete the saved plan after a crash.
			restarted, err := New(client, 24*time.Hour, 5*time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if err := restarted.RecoverPending(ctx, id); err != nil {
				t.Fatalf("repair: %v", err)
			}
			if _, err := restarted.ReadPosition(ctx, id, 1); err != nil {
				t.Fatalf("repaired position: %v", err)
			}
			events, err := restarted.PendingEvents(ctx, 0, 10)
			if err != nil || len(events) == 0 || events[0].ID != eventID {
				t.Fatalf("repaired outbox: %+v %v", events, err)
			}
			if pending := client.HGet(ctx, sessionKey(id), "pending_sequence").Val(); pending != "" {
				t.Fatal("pending marker remained")
			}
			if !originalExpiry.IsZero() {
				expiry, err := restarted.PositionExpiry(ctx, id, 1)
				if err != nil || !expiry.Equal(originalExpiry) {
					t.Fatalf("repair changed original position TTL: %s want %s (%v)", expiry, originalExpiry, err)
				}
			}
		})
	}
}

func TestConcurrentGeographicEventIdempotence(t *testing.T) {
	store, client := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	snapshot := eventSnapshot(t)
	id := fmt.Sprintf("test-i7-concurrent-%d", now.UnixNano())
	session := tracking.Session{ID: id, GuideID: "synthetic-guide", TripID: "synthetic-trip", RouteID: snapshot.Route.ID, State: tracking.Active, StartedAt: now, MaxObservationAge: time.Minute, FutureSkew: 30 * time.Second, SnapshotHash: snapshot.SnapshotHash}
	secret := bytes.Repeat([]byte{85}, 32)
	digest := sha256.Sum256(secret)
	if err := store.ProvisionSnapshot(ctx, session, digest, snapshot); err != nil {
		t.Fatal(err)
	}
	token := base64.RawURLEncoding.EncodeToString(secret)
	t.Cleanup(func() {
		events, _ := store.PendingEvents(ctx, 0, 100)
		for _, event := range events {
			if event.TrackingID == id {
				_ = client.Del(ctx, eventKey(event.ID)).Err()
				_ = client.ZRem(ctx, outboxKey, event.ID).Err()
			}
		}
		_ = client.Del(ctx, sessionKey(id), snapshotKey(id), historyKey(id), positionKey(id, 1), positionKey(id, 2), positionKey(id, 3), positionKey(id, 4), positionKey(id, 5)).Err()
		_ = client.ZRem(ctx, activeKey, keyID(id)).Err()
	})
	service, err := tracking.NewService(store, time.Minute, 30*time.Second, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	input := func(seq int64) tracking.ObservationInput {
		return tracking.ObservationInput{Sequence: seq, Longitude: -74.1, Latitude: 4.6, AccuracyMeters: 5, CapturedAt: now.Add(time.Duration(seq-1) * time.Second)}
	}
	if _, err := service.Receive(ctx, id, token, input(1)); err != nil {
		t.Fatal(err)
	}
	const workers = 16
	var group sync.WaitGroup
	results := make(chan error, workers)
	for i := 0; i < workers; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := service.Receive(ctx, id, token, input(2))
			results <- err
		}()
	}
	group.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("concurrent retry: %v", err)
		}
	}
	events, err := store.PendingEvents(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if event.TrackingID == id {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected one arrival event, got %d", count)
	}
	page, err := store.History(ctx, id, 0, 10)
	if err != nil || len(page.Positions) != 2 {
		t.Fatalf("concurrent history %+v %v", page, err)
	}
	// Different sequences race for the same state revision. Whichever commits
	// first sets the accepted order; a later lower sequence is stale.
	results = make(chan error, 2)
	for _, seq := range []int64{3, 4} {
		group.Add(1)
		go func(seq int64) {
			defer group.Done()
			_, err := service.Receive(ctx, id, token, input(seq))
			results <- err
		}(seq)
	}
	group.Wait()
	close(results)
	for err := range results {
		if err != nil && !errors.Is(err, tracking.ErrStale) {
			t.Fatalf("different sequence race: %v", err)
		}
	}
	state, err := store.GeographicState(ctx, id)
	if err != nil || state.LastSequence != 4 {
		t.Fatalf("state after race %+v %v", state, err)
	}
	page, err = store.History(ctx, id, 0, 10)
	if err != nil || len(page.Positions) < 3 || page.Positions[len(page.Positions)-1].Sequence != state.LastSequence {
		t.Fatalf("history after race %+v %v", page, err)
	}
	// Two independent sessions can advance at the same time without sharing
	// candidates or event IDs.
	otherID := id + "-other"
	other := session
	other.ID = otherID
	other.GuideID = "other-synthetic-guide"
	otherSecret := bytes.Repeat([]byte{88}, 32)
	if err := store.ProvisionSnapshot(ctx, other, sha256.Sum256(otherSecret), snapshot); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		events, _ := store.PendingEvents(ctx, 0, 100)
		for _, event := range events {
			if event.TrackingID == otherID {
				_ = client.Del(ctx, eventKey(event.ID)).Err()
				_ = client.ZRem(ctx, outboxKey, event.ID).Err()
			}
		}
		_ = client.Del(ctx, sessionKey(otherID), snapshotKey(otherID), historyKey(otherID), positionKey(otherID, 1), positionKey(otherID, 2)).Err()
		_ = client.ZRem(ctx, activeKey, keyID(otherID)).Err()
	})
	otherToken := base64.RawURLEncoding.EncodeToString(otherSecret)
	if _, err := service.Receive(ctx, otherID, otherToken, input(1)); err != nil {
		t.Fatal(err)
	}
	results = make(chan error, 2)
	group.Add(2)
	go func() {
		defer group.Done()
		_, err := service.Receive(ctx, otherID, otherToken, input(2))
		results <- err
	}()
	go func() { defer group.Done(); _, err := service.Receive(ctx, id, token, input(5)); results <- err }()
	group.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("cross-session race: %v", err)
		}
	}
	events, err = store.PendingEvents(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, event := range events {
		counts[event.TrackingID]++
	}
	if counts[id] != 1 || counts[otherID] != 1 {
		t.Fatalf("cross-session events %+v", counts)
	}
}

func TestEventOutboxRestartProbe(t *testing.T) {
	phase := os.Getenv("GPS_TEST_EVENT_RESTART_PHASE")
	if phase == "" {
		t.Skip("opt-in isolated Redis AOF restart probe")
	}
	store, client := testStore(t)
	ctx := context.Background()
	id := "test-i7-restart-event"
	base := redisSnapshotFixture(t)
	policy := &tracking.TransitionPolicy{ArrivalObservations: 1, DeviationObservations: 2, RecoveryObservations: 2, MaxSampleGap: 5 * time.Second}
	snapshot, err := tracking.BuildSnapshotWithTransitions(base.Route, base.Policy, policy)
	if err != nil {
		t.Fatal(err)
	}
	secret := bytes.Repeat([]byte{87}, 32)
	token := base64.RawURLEncoding.EncodeToString(secret)
	if phase == "write" {
		now := time.Now().UTC().Truncate(time.Millisecond)
		session := tracking.Session{ID: id, GuideID: "synthetic-guide", TripID: "synthetic-trip", RouteID: snapshot.Route.ID, State: tracking.Active, StartedAt: now, MaxObservationAge: 15 * time.Minute, FutureSkew: 30 * time.Second, SnapshotHash: snapshot.SnapshotHash}
		if err := store.ProvisionSnapshot(ctx, session, sha256.Sum256(secret), snapshot); err != nil {
			t.Fatal(err)
		}
		service, err := tracking.NewService(store, 15*time.Minute, 30*time.Second, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		result, err := service.Receive(ctx, id, token, tracking.ObservationInput{Sequence: 1, Longitude: -74.1, Latitude: 4.6, AccuracyMeters: 5, CapturedAt: now})
		if err != nil || result.Position.FirstArrival == nil {
			t.Fatalf("restart write: %+v %v", result, err)
		}
		return
	}
	if phase != "read" {
		t.Fatal("invalid restart probe phase")
	}
	stored, err := store.ReadPosition(ctx, id, 1)
	if err != nil || stored.FirstArrival == nil {
		t.Fatalf("position after restart: %+v %v", stored, err)
	}
	if _, err := store.Snapshot(ctx, id); err != nil {
		t.Fatalf("snapshot after restart: %v", err)
	}
	state, err := store.GeographicState(ctx, id)
	if err != nil || state.LastSequence != 1 {
		t.Fatalf("state after restart: %+v %v", state, err)
	}
	events, err := store.PendingEvents(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var eventID string
	for _, event := range events {
		if event.TrackingID == id {
			eventID = event.ID
			if event.Type != tracking.FirstMeetingPointArrival {
				t.Fatalf("wrong event %+v", event)
			}
		}
	}
	if eventID == "" {
		t.Fatal("event outbox lost after restart")
	}
	if ttl := client.PTTL(ctx, eventKey(eventID)).Val(); ttl != -1 {
		t.Fatalf("event TTL: %s", ttl)
	}
	service, err := tracking.NewService(store, 15*time.Minute, 30*time.Second, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := service.Receive(ctx, id, token, tracking.ObservationInput{Sequence: 1, Longitude: -74.1, Latitude: 4.6, AccuracyMeters: 5, CapturedAt: stored.CapturedAt})
	if err != nil || !replay.Duplicate {
		t.Fatalf("replay after restart: %+v %v", replay, err)
	}
	_ = client.Del(ctx, sessionKey(id), snapshotKey(id), historyKey(id), positionKey(id, 1), eventKey(eventID)).Err()
	_ = client.ZRem(ctx, activeKey, keyID(id)).Err()
	_ = client.ZRem(ctx, outboxKey, eventID).Err()
}

func TestEventOutboxMemoryFailureProbe(t *testing.T) {
	if os.Getenv("GPS_TEST_EVENT_OOM_PROBE") != "1" {
		t.Skip("opt-in isolated noeviction probe")
	}
	store, client := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	base := redisSnapshotFixture(t)
	policy := &tracking.TransitionPolicy{ArrivalObservations: 1, DeviationObservations: 2, RecoveryObservations: 2, MaxSampleGap: 5 * time.Second}
	snapshot, err := tracking.BuildSnapshotWithTransitions(base.Route, base.Policy, policy)
	if err != nil {
		t.Fatal(err)
	}
	id := fmt.Sprintf("test-i7-oom-%d", now.UnixNano())
	session := tracking.Session{ID: id, GuideID: "synthetic-guide", TripID: "synthetic-trip", RouteID: snapshot.Route.ID, State: tracking.Active, StartedAt: now, MaxObservationAge: time.Minute, FutureSkew: 30 * time.Second, SnapshotHash: snapshot.SnapshotHash}
	secret := bytes.Repeat([]byte{89}, 32)
	if err := store.ProvisionSnapshot(ctx, session, sha256.Sum256(secret), snapshot); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.ConfigSet(ctx, "maxmemory", "256mb").Err() }()
	defer func() {
		events, _ := store.PendingEvents(ctx, 0, 100)
		for _, event := range events {
			if event.TrackingID == id {
				_ = client.Del(ctx, eventKey(event.ID)).Err()
				_ = client.ZRem(ctx, outboxKey, event.ID).Err()
			}
		}
		_ = client.Del(ctx, sessionKey(id), snapshotKey(id), historyKey(id), positionKey(id, 1)).Err()
		_ = client.ZRem(ctx, activeKey, keyID(id)).Err()
	}()
	service, err := tracking.NewService(store, time.Minute, 30*time.Second, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	input := tracking.ObservationInput{Sequence: 1, Longitude: -74.1, Latitude: 4.6, AccuracyMeters: 5, CapturedAt: now}
	if err := client.ConfigSet(ctx, "maxmemory", "1").Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Receive(ctx, id, base64.RawURLEncoding.EncodeToString(secret), input); err == nil {
		t.Fatal("OOM falsely accepted observation")
	}
	if err := client.ConfigSet(ctx, "maxmemory", "256mb").Err(); err != nil {
		t.Fatal(err)
	}
	accepted, err := service.Receive(ctx, id, base64.RawURLEncoding.EncodeToString(secret), input)
	if err != nil || accepted.Position.FirstArrival == nil {
		t.Fatalf("retry after OOM: %+v %v", accepted, err)
	}
	events, err := store.PendingEvents(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if event.TrackingID == id {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("OOM retry produced %d events", count)
	}
}
