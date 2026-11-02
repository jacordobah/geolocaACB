package redisstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/tracking"
	"github.com/redis/go-redis/v9"
)

func lifecycleFixture(t *testing.T) (*Store, tracking.Session, [32]byte) {
	t.Helper()
	store, _ := testStore(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	session := tracking.Session{ID: fmt.Sprintf("test-closure-%d", now.UnixNano()), GuideID: "synthetic-guide", TripID: "synthetic-trip", RouteID: "synthetic-route", State: tracking.Active, StartedAt: now, MaxObservationAge: time.Minute, FutureSkew: time.Second}
	return store, session, sha256.Sum256([]byte("synthetic-only-credential"))
}

func TestLifecycleOutboxAndConcurrentRetries(t *testing.T) {
	store, session, digest := lifecycleFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	errors := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errors <- store.Provision(ctx, session, digest) }()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("concurrent provision: %v", err)
		}
	}
	activation, err := tracking.NewLifecycleEvent(session, tracking.TrackingSessionActivated, session.StartedAt)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := store.Session(ctx, session.ID); err != nil || state.State != tracking.Active {
		t.Fatalf("activation state: %+v %v", state, err)
	}
	if score, err := store.client.ZScore(ctx, outboxKey, activation.ID).Result(); err != nil || int64(score) != session.StartedAt.UnixMilli() {
		t.Fatalf("activation outbox: %v %v", score, err)
	}
	if ttl := store.client.PTTL(ctx, eventKey(activation.ID)).Val(); ttl != -1 {
		t.Fatalf("pending event TTL: %v", ttl)
	}
	finished := session.StartedAt.Add(time.Second)
	finishErrors := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); finishErrors <- store.FinishSession(ctx, session.ID, finished) }()
	}
	wg.Wait()
	close(finishErrors)
	for err := range finishErrors {
		if err != nil {
			t.Fatalf("concurrent finish: %v", err)
		}
	}
	finishEvent, err := tracking.NewLifecycleEvent(session, tracking.TrackingSessionFinished, finished)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.client.Get(ctx, eventKey(finishEvent.ID)).Result(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.client.ZScore(ctx, outboxKey, finishEvent.ID).Result(); err != nil {
		t.Fatal(err)
	}
	if state, err := store.Session(ctx, session.ID); err != nil || state.State != tracking.Finished {
		t.Fatalf("finished state: %+v %v", state, err)
	}
}

func TestRecoverProvisionAndFinishWithoutGPS(t *testing.T) {
	store, session, digest := lifecycleFixture(t)
	ctx := context.Background()
	body, err := store.prepareProvision(ctx, session, digest, nil)
	if err != nil {
		t.Fatal(err)
	}
	if body == "" {
		t.Fatal("missing recovery plan")
	}
	if err := store.RecoverProvision(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Credential(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.Provision(ctx, session, digest); err != nil {
		t.Fatal(err)
	}
	finished := time.Now().UTC().Truncate(time.Millisecond)
	event, err := tracking.NewLifecycleEvent(session, tracking.TrackingSessionFinished, finished)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(event)
	if err := store.client.HSet(ctx, sessionKey(session.ID), "state", "finishing", "finished_ms", ms(finished), "finish_event_payload", string(payload)).Err(); err != nil {
		t.Fatal(err)
	}
	if err := store.client.ZAdd(ctx, finishPendingKey, redis.Z{Score: float64(finished.UnixMilli()), Member: keyID(session.ID)}).Err(); err != nil {
		t.Fatal(err)
	}
	if err := store.RecoverFinish(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	if state, err := store.Session(ctx, session.ID); err != nil || state.State != tracking.Finished {
		t.Fatalf("finish recovery: %+v %v", state, err)
	}
	if _, err := store.client.Get(ctx, eventKey(event.ID)).Result(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.client.ZScore(ctx, outboxKey, event.ID).Result(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Credential(ctx, session.ID); err == nil {
		t.Fatal("credential usable after finish")
	}
}

func TestRecoverPartiallyWrittenActivation(t *testing.T) {
	store, session, digest := lifecycleFixture(t)
	ctx := context.Background()
	if _, err := store.prepareProvision(ctx, session, digest, nil); err != nil {
		t.Fatal(err)
	}
	event, err := tracking.NewLifecycleEvent(session, tracking.TrackingSessionActivated, session.StartedAt)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(event)
	if err := store.client.HSet(ctx, sessionKey(session.ID), map[string]any{
		"id": session.ID, "guide_id": session.GuideID, "route_id": session.RouteID,
		"trip_id": session.TripID, "started_ms": ms(session.StartedAt), "last_sequence": "0",
		"state": "provisioning", "provision_complete": "0", "credential_hash": hex.EncodeToString(digest[:]),
		"max_age_ms": session.MaxObservationAge.Milliseconds(), "future_skew_ms": session.FutureSkew.Milliseconds(),
		"snapshot_hash": "", "transition_enabled": "0",
	}).Err(); err != nil {
		t.Fatal(err)
	}
	if err := store.client.ZAdd(ctx, pendingEventsKey, redis.Z{Score: float64(session.StartedAt.UnixMilli()), Member: event.ID}).Err(); err != nil {
		t.Fatal(err)
	}
	if err := store.client.Set(ctx, eventKey(event.ID), payload, 0).Err(); err != nil {
		t.Fatal(err)
	}
	if err := store.client.ZAdd(ctx, outboxKey, redis.Z{Score: float64(session.StartedAt.UnixMilli()), Member: event.ID}).Err(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Credential(ctx, session.ID); err == nil {
		t.Fatal("partial session was usable")
	}
	if err := store.RecoverProvision(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Credential(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.client.ZScore(ctx, pendingEventsKey, event.ID).Result(); err != redis.Nil {
		t.Fatalf("guard remained: %v", err)
	}
	if count, err := store.client.ZCount(ctx, outboxKey, "-inf", "+inf").Result(); err != nil || count != 1 {
		t.Fatalf("duplicate outbox event %d %v", count, err)
	}
}

func TestFinishLostReplyReconciled(t *testing.T) {
	store, session, digest := lifecycleFixture(t)
	ctx := context.Background()
	if err := store.Provision(ctx, session, digest); err != nil {
		t.Fatal(err)
	}
	hook := &lostCreateReply{}
	store.client.AddHook(hook)
	if err := store.FinishSession(ctx, session.ID, time.Now().UTC().Truncate(time.Millisecond)); err != nil {
		t.Fatalf("lost finish reply: %v", err)
	}
	if !hook.once.Load() {
		t.Fatal("reply loss not injected")
	}
	if state, err := store.Session(ctx, session.ID); err != nil || state.State != tracking.Finished {
		t.Fatalf("finish not reconciled: %+v %v", state, err)
	}
}

func TestLifecycleAOFRestartProbe(t *testing.T) {
	phase := os.Getenv("GPS_TEST_LIFECYCLE_RESTART_PHASE")
	if phase == "" {
		t.Skip("opt-in isolated AOF restart")
	}
	id := os.Getenv("GPS_TEST_LIFECYCLE_RESTART_ID")
	if !validID(id) {
		t.Fatal("test lifecycle ID required")
	}
	store, _ := testStore(t)
	ctx := context.Background()
	if phase == "write" {
		now := time.Now().UTC().Truncate(time.Millisecond)
		session := tracking.Session{ID: id, GuideID: "synthetic-guide", TripID: "synthetic-trip", RouteID: "synthetic-route", State: tracking.Active, StartedAt: now, MaxObservationAge: time.Minute, FutureSkew: time.Second}
		if err := store.Provision(ctx, session, sha256.Sum256([]byte("isolated-only-lifecycle"))); err != nil {
			t.Fatal(err)
		}
		if err := store.FinishSession(ctx, id, now.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		return
	}
	if phase != "read" {
		t.Fatal("invalid phase")
	}
	session, err := store.Session(ctx, id)
	if err != nil || session.State != tracking.Finished || session.FinishedAt == nil {
		t.Fatalf("session after AOF: %+v %v", session, err)
	}
	for _, kind := range []tracking.EventType{tracking.TrackingSessionActivated, tracking.TrackingSessionFinished} {
		event, err := tracking.NewLifecycleEvent(session, kind, *session.FinishedAt)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.client.Get(ctx, eventKey(event.ID)).Result(); err != nil {
			t.Fatalf("event %s after AOF: %v", kind, err)
		}
		if _, err := store.client.ZScore(ctx, outboxKey, event.ID).Result(); err != nil {
			t.Fatalf("outbox %s after AOF: %v", kind, err)
		}
	}
	if err := store.FinishSession(ctx, id, time.Now().UTC().Truncate(time.Millisecond)); err != nil {
		t.Fatalf("idempotent finish after AOF: %v", err)
	}
}
