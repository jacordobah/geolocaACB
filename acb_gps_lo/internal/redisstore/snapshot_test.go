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
	"testing"
	"time"

	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/geo"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/routes"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/tracking"
	"github.com/redis/go-redis/v9"
)

func redisSnapshotFixture(t *testing.T) tracking.GeographicSnapshot {
	t.Helper()
	first, second, resolved, verified, version := "synthetic-PE01", "synthetic-PE02", "resolved", "verified", "synthetic-v1"
	route := routes.Route{
		ID: "synthetic-route", Name: "Synthetic route", GeometryVersion: &version, AssociationStatus: &verified,
		Geometry:      geo.LineString{Coordinates: []geo.Coordinate{{Longitude: -74.1, Latitude: 4.6}, {Longitude: -74.1005, Latitude: 4.6}, {Longitude: -74.101, Latitude: 4.6}}},
		MeetingPoints: []routes.MeetingPoint{{ID: first, Name: "PE01", Kind: "meeting_point", Location: geo.Coordinate{Longitude: -74.1, Latitude: 4.6}}, {ID: second, Name: "PE02", Kind: "meeting_point", Location: geo.Coordinate{Longitude: -74.101, Latitude: 4.6}}},
		Endpoints:     routes.Endpoints{Status: &resolved, FirstPointID: &first, Start: routes.Endpoint{PointID: &first}, End: routes.Endpoint{PointID: &second}},
	}
	snapshot, err := tracking.BuildSnapshot(route, tracking.GeographicPolicy{CorridorRadiusMeters: 30, PointRadiusMeters: 20, MaxAccuracyMeters: 25})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestGeographicPayloadChecksum(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	accuracy, distance := 5.0, 0.0
	p := tracking.Position{SessionID: "synthetic-session", Sequence: 1, CapturedAt: now, ReceivedAt: now, Longitude: -74.1, Latitude: 4.6, AccuracyMeters: &accuracy, IdempotencyVersion: 2, Geographic: &tracking.GeographicResult{SnapshotHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", RouteStatus: "inside", RouteDistanceMeters: &distance}}
	inputHash, body, err := p.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	checksum := sha256.Sum256(body)
	raw := inputHash + "|" + hex.EncodeToString(checksum[:]) + "|" + string(body)
	if _, err := decodePosition(raw); err != nil {
		t.Fatalf("valid geographic position: %v", err)
	}
	corrupt := raw[:len(raw)-2] + "0}"
	if _, err := decodePosition(corrupt); !errors.Is(err, tracking.ErrUnavailable) {
		t.Fatalf("tampered geographic result: %v", err)
	}
	if _, err := decodePosition(inputHash + "|" + string(body)); !errors.Is(err, tracking.ErrUnavailable) {
		t.Fatalf("missing result checksum: %v", err)
	}
}

func TestGeographicSnapshotRedisLifecycle(t *testing.T) {
	store, client := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	snapshot := redisSnapshotFixture(t)
	id := fmt.Sprintf("test-i6-%d", now.UnixNano())
	session := tracking.Session{ID: id, GuideID: "synthetic-guide", TripID: "synthetic-trip", RouteID: snapshot.Route.ID, State: tracking.Active, StartedAt: now, MaxObservationAge: 15 * time.Minute, FutureSkew: 30 * time.Second, SnapshotHash: snapshot.SnapshotHash}
	secret := bytes.Repeat([]byte{73}, 32)
	digest := sha256.Sum256(secret)
	token := base64.RawURLEncoding.EncodeToString(secret)
	t.Cleanup(func() {
		_ = client.Del(ctx, sessionKey(id), snapshotKey(id), historyKey(id), positionKey(id, 1), positionKey(id, 2)).Err()
		_ = client.ZRem(ctx, activeKey, keyID(id)).Err()
	})
	if err := store.ProvisionSnapshot(ctx, session, digest, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionSnapshot(ctx, session, digest, snapshot); err != nil {
		t.Fatalf("idempotent creation: %v", err)
	}
	loaded, err := store.Snapshot(ctx, id)
	if err != nil || loaded.SnapshotHash != snapshot.SnapshotHash {
		t.Fatalf("snapshot read: %+v %v", loaded, err)
	}
	loaded.Route.Geometry.Coordinates[0].Latitude = 30
	again, err := store.Snapshot(ctx, id)
	if err != nil || again.Route.Geometry.Coordinates[0].Latitude != 4.6 {
		t.Fatalf("snapshot mutated: %v", err)
	}
	changed := session
	changed.SnapshotHash = "0000000000000000000000000000000000000000000000000000000000000000"
	if err := store.ProvisionSnapshot(ctx, changed, digest, snapshot); !errors.Is(err, tracking.ErrInvalid) {
		t.Fatalf("changed fingerprint: %v", err)
	}
	p := tracking.Position{SessionID: id, Sequence: 1, CapturedAt: now, ReceivedAt: now, Longitude: -74.1, Latitude: 4.6, IdempotencyVersion: 2}
	a := 5.0
	p.AccuracyMeters = &a
	p.Geographic, err = snapshot.Evaluate(p)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := store.Accept(ctx, p)
	if err != nil || accepted.Position.Geographic.RouteStatus != "inside" {
		t.Fatalf("accept: %+v %v", accepted, err)
	}
	before := accepted.ExpiresAt
	duplicate, err := store.Accept(ctx, p)
	if err != nil || !duplicate.Duplicate || !duplicate.ExpiresAt.Equal(before) {
		t.Fatalf("idempotent accept: %+v %v", duplicate, err)
	}
	if before.Before(now.Add(24 * time.Hour)) {
		t.Fatalf("early expiry: %s", before)
	}
	// Recreate the Go adapter while using the same isolated Redis instance.
	restarted, err := New(client, 24*time.Hour, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Snapshot(ctx, id); err != nil {
		t.Fatalf("restart snapshot: %v", err)
	}
	// A fresh service has an empty prepared-route cache. It must recover the
	// immutable snapshot from Redis before evaluating the next observation.
	restartedService, err := tracking.NewService(restarted, 15*time.Minute, 30*time.Second, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := restartedService.Receive(ctx, id, token, tracking.ObservationInput{Sequence: 2, Latitude: 4.6, Longitude: -74.1005, AccuracyMeters: 5, CapturedAt: now})
	if err != nil || second.Position.Geographic == nil || second.Position.Geographic.RouteStatus != "inside" || second.Position.Geographic.SnapshotHash != snapshot.SnapshotHash {
		t.Fatalf("fresh service geographic recovery: %+v %v", second, err)
	}
	if err := store.FinishSession(ctx, id, now); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Snapshot(ctx, id); err != nil {
		t.Fatalf("finished snapshot lost: %v", err)
	}
	ttl, err := client.PTTL(ctx, snapshotKey(id)).Result()
	if err != nil || ttl < 24*time.Hour {
		t.Fatalf("snapshot retention: %s %v", ttl, err)
	}
	if _, err := store.Accept(ctx, tracking.Position{SessionID: id, Sequence: 3, CapturedAt: now, ReceivedAt: now, Longitude: -74.1, Latitude: 4.6, AccuracyMeters: &a, IdempotencyVersion: 2, Geographic: p.Geographic}); !errors.Is(err, tracking.ErrClosed) {
		t.Fatalf("finished write: %v", err)
	}
}

func TestGeographicProvisionRepairsOrphanSnapshot(t *testing.T) {
	store, client := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	snapshot := redisSnapshotFixture(t)
	id := fmt.Sprintf("test-i6-orphan-%d", now.UnixNano())
	session := tracking.Session{ID: id, GuideID: "synthetic-guide", TripID: "synthetic-trip", RouteID: snapshot.Route.ID, State: tracking.Active, StartedAt: now, MaxObservationAge: time.Minute, FutureSkew: time.Second, SnapshotHash: snapshot.SnapshotHash}
	digest := sha256.Sum256([]byte("synthetic-secret"))
	t.Cleanup(func() {
		_ = client.Del(ctx, sessionKey(id), snapshotKey(id)).Err()
		_ = client.ZRem(ctx, activeKey, keyID(id)).Err()
	})
	body, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Set(ctx, snapshotKey(id), snapshot.SnapshotHash+"|"+string(body), 0).Err(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Credential(ctx, id); !errors.Is(err, tracking.ErrNotFound) {
		t.Fatalf("orphan usable: %v", err)
	}
	if err := store.ProvisionSnapshot(ctx, session, digest, snapshot); err != nil {
		t.Fatalf("repair: %v", err)
	}
	if _, _, err := store.Credential(ctx, id); err != nil {
		t.Fatalf("repaired credential: %v", err)
	}
	if _, err := client.ZScore(ctx, activeKey, keyID(id)).Result(); err != nil && !errors.Is(err, redis.Nil) {
		t.Fatal(err)
	}
}

func TestGeographicProvisionLostReplyReconcilesAllKeys(t *testing.T) {
	store, client := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	snapshot := redisSnapshotFixture(t)
	id := fmt.Sprintf("test-i6-reply-%d", now.UnixNano())
	session := tracking.Session{ID: id, GuideID: "synthetic-guide", TripID: "synthetic-trip", RouteID: snapshot.Route.ID, State: tracking.Active, StartedAt: now, MaxObservationAge: time.Minute, FutureSkew: time.Second, SnapshotHash: snapshot.SnapshotHash}
	digest := sha256.Sum256([]byte("synthetic-secret"))
	t.Cleanup(func() {
		_ = client.Del(ctx, sessionKey(id), snapshotKey(id)).Err()
		_ = client.ZRem(ctx, activeKey, keyID(id)).Err()
	})
	client.AddHook(&lostCreateReply{})
	if err := store.ProvisionSnapshot(ctx, session, digest, snapshot); err != nil {
		t.Fatalf("reconcile lost reply: %v", err)
	}
	if _, _, err := store.Credential(ctx, id); err != nil {
		t.Fatalf("confirmed session unusable: %v", err)
	}
	if _, err := store.Snapshot(ctx, id); err != nil {
		t.Fatalf("confirmed snapshot unavailable: %v", err)
	}
}
