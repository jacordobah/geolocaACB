package redisstore

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/tracking"
	"github.com/redis/go-redis/v9"
)

func TestPolicyDefinitionRegistryImmutableAndConcurrent(t *testing.T) {
	store, client := testStore(t)
	ctx := context.Background()
	geoPolicy := tracking.GeographicPolicy{CorridorRadiusMeters: 30, PointRadiusMeters: 20, MaxAccuracyMeters: 25}
	transition := tracking.TransitionPolicy{ArrivalObservations: 2, ArrivalDwell: time.Second, DeviationObservations: 2, DeviationDwell: time.Second, RecoveryObservations: 2, RecoveryDwell: time.Second, MaxSampleGap: 5 * time.Second}
	ref, err := tracking.NewPolicyReference("synthetic-registry-v1", 1, geoPolicy, transition)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Del(ctx, policyDefinitionKey(ref.PolicyID)).Err() })
	var wg sync.WaitGroup
	errCh := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errCh <- store.RegisterPolicyDefinitions(ctx, []tracking.PolicyReference{ref})
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	ttl, err := client.TTL(ctx, policyDefinitionKey(ref.PolicyID)).Result()
	if err != nil || ttl != -1 {
		t.Fatalf("registry key must not expire: %v %v", ttl, err)
	}
	geoPolicy.PointRadiusMeters = 21
	changed, _ := tracking.NewPolicyReference(ref.PolicyID, 1, geoPolicy, transition)
	if err := store.RegisterPolicyDefinitions(ctx, []tracking.PolicyReference{changed}); !errors.Is(err, tracking.ErrConflict) {
		t.Fatalf("changed ID accepted: %v", err)
	}
	if err := store.RegisterPolicyDefinitions(ctx, []tracking.PolicyReference{ref}); err != nil {
		t.Fatalf("restart registration failed: %v", err)
	}
}

func TestVersionedSnapshotRedisRoundTrip(t *testing.T) {
	store, client := testStore(t)
	ctx := context.Background()
	legacy := redisSnapshotFixture(t)
	transitions := tracking.TransitionPolicy{ArrivalObservations: 2, ArrivalDwell: time.Second, DeviationObservations: 2, DeviationDwell: time.Second, RecoveryObservations: 2, RecoveryDwell: time.Second, MaxSampleGap: 5 * time.Second}
	ref, err := tracking.NewPolicyReference("synthetic-snapshot-v1", 1, legacy.Policy, transitions)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := tracking.BuildSnapshotWithPolicy(legacy.Route, legacy.Policy, transitions, ref)
	if err != nil {
		t.Fatal(err)
	}
	id := "synthetic-policy-session-" + time.Now().Format("150405.000000000")
	t.Cleanup(func() {
		_ = client.Del(ctx, sessionKey(id), snapshotKey(id)).Err()
		_ = client.ZRem(ctx, activeKey, keyID(id)).Err()
	})
	now := time.Now().UTC().Truncate(time.Millisecond)
	session := tracking.Session{ID: id, TripID: "synthetic-trip", GuideID: "synthetic-guide", RouteID: snapshot.Route.ID, State: tracking.Active, StartedAt: now, MaxObservationAge: time.Minute, FutureSkew: time.Second, SnapshotHash: snapshot.SnapshotHash}
	if err := store.ProvisionSnapshot(ctx, session, [32]byte{1}, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionSnapshot(ctx, session, [32]byte{1}, snapshot); err != nil {
		t.Fatalf("retry: %v", err)
	}
	loaded, err := store.Snapshot(ctx, id)
	if err != nil || loaded.PolicyReference == nil || *loaded.PolicyReference != ref || loaded.Policy.PointRadiusMeters != 20 {
		t.Fatalf("round trip: %+v %v", loaded, err)
	}
	// A new store instance represents a restarted Go process, without reloading YAML.
	restarted, err := New(client, 24*time.Hour, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	again, err := restarted.Snapshot(ctx, id)
	if err != nil || again.SnapshotHash != snapshot.SnapshotHash {
		t.Fatalf("recovery: %+v %v", again, err)
	}
	if legacy.Validate() != nil {
		t.Fatal("legacy snapshot validation regressed")
	}
}

func TestPolicyRegistrationLostReplyReconciles(t *testing.T) {
	store, client := testStore(t)
	_ = store
	ref := tracking.PolicyReference{PolicyID: "synthetic-lost-reply-v1", Revision: 1, DefinitionHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	t.Cleanup(func() { _ = client.Del(context.Background(), policyDefinitionKey(ref.PolicyID)).Err() })
	hooked := redis.NewClient(client.Options())
	hooked.AddHook(&lostCreateReply{})
	defer hooked.Close()
	other, err := New(hooked, 24*time.Hour, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.RegisterPolicyDefinitions(context.Background(), []tracking.PolicyReference{ref}); err != nil {
		t.Fatalf("lost Redis reply: %v", err)
	}
}

// Run SETUP, restart only the isolated Redis container with its AOF volume,
// then run VERIFY. Never point these modes at a shared Redis instance.
func TestPolicyAOFRecovery(t *testing.T) {
	mode := os.Getenv("GPS_TEST_POLICY_AOF")
	if mode == "" {
		t.Skip("explicit isolated AOF setup/verify only")
	}
	if os.Getenv("GPS_TEST_REDIS_ISOLATED") != "1" {
		t.Fatal("AOF test requires isolated Redis")
	}
	addr := os.Getenv("GPS_TEST_REDIS_ADDR")
	client := redis.NewClient(&redis.Options{Addr: addr})
	defer client.Close()
	ctx := context.Background()
	store, err := New(client, 24*time.Hour, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	legacy := redisSnapshotFixture(t)
	transitions := tracking.TransitionPolicy{ArrivalObservations: 2, ArrivalDwell: time.Second, DeviationObservations: 2, DeviationDwell: time.Second, RecoveryObservations: 2, RecoveryDwell: time.Second, MaxSampleGap: 5 * time.Second}
	ref, err := tracking.NewPolicyReference("synthetic-aof-v1", 1, legacy.Policy, transitions)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := tracking.BuildSnapshotWithPolicy(legacy.Route, legacy.Policy, transitions, ref)
	if err != nil {
		t.Fatal(err)
	}
	const id = "synthetic-policy-aof-session"
	if mode == "setup" {
		if err := store.RegisterPolicyDefinitions(ctx, []tracking.PolicyReference{ref}); err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC().Truncate(time.Millisecond)
		session := tracking.Session{ID: id, TripID: "synthetic-trip", GuideID: "synthetic-guide", RouteID: snapshot.Route.ID, State: tracking.Active, StartedAt: now, MaxObservationAge: time.Minute, FutureSkew: time.Second, SnapshotHash: snapshot.SnapshotHash}
		if err := store.ProvisionSnapshot(ctx, session, [32]byte{1}, snapshot); err != nil {
			t.Fatal(err)
		}
		return
	}
	if mode != "verify" {
		t.Fatal("unknown AOF mode")
	}
	loaded, err := store.Snapshot(ctx, id)
	if err != nil || loaded.SnapshotHash != snapshot.SnapshotHash || loaded.PolicyReference == nil || *loaded.PolicyReference != ref {
		t.Fatalf("AOF snapshot missing: %+v %v", loaded, err)
	}
	changed := ref
	changed.DefinitionHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err := store.RegisterPolicyDefinitions(ctx, []tracking.PolicyReference{changed}); !errors.Is(err, tracking.ErrConflict) {
		t.Fatalf("AOF registry lost definition: %v", err)
	}
}
