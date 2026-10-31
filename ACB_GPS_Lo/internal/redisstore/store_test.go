package redisstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/tracking"
	"github.com/redis/go-redis/v9"
)

func testStore(t *testing.T) (*Store, *redis.Client) {
	t.Helper()
	addr := os.Getenv("GPS_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("isolated Redis integration only: set GPS_TEST_REDIS_ADDR")
	}
	client := redis.NewClient(&redis.Options{Addr: addr, DialTimeout: time.Second, ReadTimeout: 2 * time.Second, WriteTimeout: 2 * time.Second})
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	store, err := New(client, 24*time.Hour, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return store, client
}

func testFixture(t *testing.T, s *Store) (tracking.Session, tracking.Position) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)
	id := fmt.Sprintf("test-i3-%d", now.UnixNano())
	session := tracking.Session{ID: id, GuideID: "guide-synthetic", RouteID: "route-synthetic", TripID: "trip-synthetic", State: tracking.Active, StartedAt: now}
	if err := s.CreateSession(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	position := tracking.Position{SessionID: id, Sequence: 1, CapturedAt: now, ReceivedAt: now, Longitude: -74.1, Latitude: 4.6}
	return session, position
}

func TestRedisAcceptanceAndQueries(t *testing.T) {
	s, client := testStore(t)
	ctx := context.Background()
	session, p := testFixture(t, s)
	got, err := s.Session(ctx, session.ID)
	if err != nil || got.ID != session.ID || got.State != tracking.Active {
		t.Fatalf("session %+v %v", got, err)
	}
	if err := s.CreateSession(ctx, session); !errors.Is(err, tracking.ErrConflict) {
		t.Fatalf("duplicate session %v", err)
	}
	active, err := s.ActiveSessions(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, id := range active {
		if id == session.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("active session missing")
	}
	id := "PE01"
	distance := 1.0
	p.Geographic = &tracking.GeographicResult{MeetingPointID: &id, MeetingPointDistanceMeters: &distance}
	p.FirstArrival = &tracking.Arrival{MeetingPointID: id, CapturedAt: p.CapturedAt, Sequence: p.Sequence}
	result, err := s.Accept(ctx, p)
	if err != nil || result.Duplicate {
		t.Fatalf("accept %+v %v", result, err)
	}
	ttl, err := client.PTTL(ctx, positionKey(p.SessionID, p.Sequence)).Result()
	if err != nil || ttl < 24*time.Hour {
		t.Fatalf("TTL %v %v", ttl, err)
	}
	result, err = s.Accept(ctx, p)
	if err != nil || !result.Duplicate {
		t.Fatalf("retry %+v %v", result, err)
	}
	ttl2, _ := client.PTTL(ctx, positionKey(p.SessionID, p.Sequence)).Result()
	if ttl2 > ttl+time.Second {
		t.Fatalf("retry extended TTL: %v -> %v", ttl, ttl2)
	}
	firstExpiry, err := client.PExpireTime(ctx, positionKey(p.SessionID, p.Sequence)).Result()
	if err != nil {
		t.Fatal(err)
	}
	conflict := p
	conflict.Longitude = -73.9
	if _, err := s.Accept(ctx, conflict); !errors.Is(err, tracking.ErrConflict) {
		t.Fatalf("conflict %v", err)
	}
	for n := int64(2); n <= 5; n++ {
		next := p
		next.Sequence = n
		next.CapturedAt = p.CapturedAt.Add(time.Duration(n) * time.Second)
		next.ReceivedAt = next.CapturedAt
		next.FirstArrival = nil
		next.Geographic = nil
		if _, err := s.Accept(ctx, next); err != nil {
			t.Fatal(err)
		}
	}
	secondExpiry, err := client.PExpireTime(ctx, positionKey(p.SessionID, 2)).Result()
	if err != nil || secondExpiry <= firstExpiry {
		t.Fatalf("independent expiry %v %v %v", firstExpiry, secondExpiry, err)
	}
	got, err = s.Session(ctx, session.ID)
	if err != nil || got.LastSequence != 5 || got.FirstArrival == nil || got.FirstArrival.MeetingPointID != id {
		t.Fatalf("arrival/session %+v %v", got, err)
	}
	page, err := s.History(ctx, session.ID, 0, 2)
	if err != nil || len(page.Positions) != 2 || page.Positions[0].Sequence != 1 || page.Positions[1].Sequence != 2 || !page.HasMore {
		t.Fatalf("first page %+v %v", page, err)
	}
	page, err = s.History(ctx, session.ID, page.NextSequence, 2)
	if err != nil || len(page.Positions) != 2 || page.Positions[0].Sequence != 3 {
		t.Fatalf("second page %+v %v", page, err)
	}
	latest, err := s.Latest(ctx, session.ID)
	if err != nil || latest.Sequence != 5 {
		t.Fatalf("latest %+v %v", latest, err)
	}
	old := p
	old.Sequence = 6
	old.CapturedAt = p.CapturedAt.Add(-time.Second)
	old.ReceivedAt = p.ReceivedAt
	old.FirstArrival = nil
	if _, err := s.Accept(ctx, old); !errors.Is(err, tracking.ErrStale) {
		t.Fatalf("old capture %v", err)
	}
	if err := s.FinishSession(ctx, session.ID, time.Now().UTC().Truncate(time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	sessionTTL, err := client.PTTL(ctx, sessionKey(session.ID)).Result()
	if err != nil || sessionTTL < 24*time.Hour {
		t.Fatalf("finished session TTL %v %v", sessionTTL, err)
	}
	historyTTL, err := client.PTTL(ctx, historyKey(session.ID)).Result()
	if err != nil || historyTTL < 24*time.Hour {
		t.Fatalf("history TTL %v %v", historyTTL, err)
	}
	if err := s.FinishSession(ctx, session.ID, time.Now().UTC().Truncate(time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	last := p
	last.Sequence = 6
	last.FirstArrival = nil
	if _, err := s.Accept(ctx, last); !errors.Is(err, tracking.ErrClosed) {
		t.Fatalf("closed %v", err)
	}
	if _, err := s.Accept(ctx, p); err != nil {
		t.Fatalf("retry after finish %v", err)
	}
	if exists := client.Exists(ctx, positionKey(p.SessionID, 1)).Val(); exists != 1 {
		t.Fatal("finish deleted position")
	}
}

func TestRedisExpiredReferenceAndRepair(t *testing.T) {
	s, client := testStore(t)
	ctx := context.Background()
	session, p := testFixture(t, s)
	if _, err := s.Accept(ctx, p); err != nil {
		t.Fatal(err)
	}
	p2 := p
	p2.Sequence = 2
	p2.CapturedAt = p.CapturedAt.Add(time.Second)
	p2.ReceivedAt = p2.CapturedAt
	if _, err := s.Accept(ctx, p2); err != nil {
		t.Fatal(err)
	}
	// Accelerated expiry applies only to this isolated synthetic test key.
	if err := client.PExpire(ctx, positionKey(session.ID, 1), 20*time.Millisecond).Err(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	page, err := s.History(ctx, session.ID, 0, 10)
	if err != nil || len(page.Positions) != 1 || page.Positions[0].Sequence != 2 {
		t.Fatalf("expired ref %+v %v", page, err)
	}
	if score := client.ZScore(ctx, historyKey(session.ID), "1").Err(); !errors.Is(score, redis.Nil) {
		t.Fatalf("stale ref remains: %v", score)
	}
	// Simulate a script stopping after the pending marker and position SET.
	p3 := p2
	p3.Sequence = 3
	p3.CapturedAt = p2.CapturedAt.Add(time.Second)
	p3.ReceivedAt = p3.CapturedAt
	fingerprint, body, err := p3.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if err := client.HSet(ctx, sessionKey(session.ID), "pending_sequence", "3", "pending_fingerprint", fingerprint).Err(); err != nil {
		t.Fatal(err)
	}
	if err := client.Set(ctx, positionKey(session.ID, 3), fingerprint+"|"+string(body), 25*time.Hour).Err(); err != nil {
		t.Fatal(err)
	}
	other := p3
	other.Sequence = 4
	if _, err := s.Accept(ctx, other); !errors.Is(err, tracking.ErrPending) {
		t.Fatalf("other sequence not blocked: %v", err)
	}
	if _, err := s.Accept(ctx, p3); err != nil {
		t.Fatalf("repair %v", err)
	}
	got, err := s.Session(ctx, session.ID)
	if err != nil || got.LastSequence != 3 {
		t.Fatalf("repaired session %+v %v", got, err)
	}
}

func TestRedisConcurrentSequence(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	session, p := testFixture(t, s)
	const workers = 20
	var wg sync.WaitGroup
	results := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.Accept(ctx, p); results <- err }()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("same sequence race: %v", err)
		}
	}
	page, err := s.History(ctx, session.ID, 0, 100)
	if err != nil || len(page.Positions) != 1 {
		t.Fatalf("duplicates persisted %+v %v", page, err)
	}
	var wg2 sync.WaitGroup
	errs := make(chan error, workers)
	for n := int64(2); n <= workers+1; n++ {
		p2 := p
		p2.Sequence = n
		p2.CapturedAt = p.CapturedAt.Add(time.Duration(n) * time.Second)
		p2.ReceivedAt = p2.CapturedAt
		wg2.Add(1)
		go func() { defer wg2.Done(); _, err := s.Accept(ctx, p2); errs <- err }()
	}
	wg2.Wait()
	close(errs)
	for err := range errs {
		if err != nil && !errors.Is(err, tracking.ErrStale) {
			t.Fatalf("different sequence race: %v", err)
		}
	}
	page, err = s.History(ctx, session.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(page.Positions); i++ {
		if page.Positions[i].Sequence <= page.Positions[i-1].Sequence {
			t.Fatal("history out of order")
		}
	}
}

func TestRedisUnavailable(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 50 * time.Millisecond, ReadTimeout: 50 * time.Millisecond, WriteTimeout: 50 * time.Millisecond, MaxRetries: 0})
	defer client.Close()
	s, err := New(client, 24*time.Hour, 0)
	if err != nil {
		t.Fatal(err)
	}
	p := tracking.Position{SessionID: "synthetic-unavailable", Sequence: 1, CapturedAt: time.Now().UTC().Truncate(time.Millisecond), ReceivedAt: time.Now().UTC().Truncate(time.Millisecond), Longitude: 0, Latitude: 0}
	_, err = s.Accept(context.Background(), p)
	if !errors.Is(err, tracking.ErrUncertain) {
		t.Fatalf("unavailable acceptance %v", err)
	}
}

func TestRedisMemoryLimit(t *testing.T) {
	s, client := testStore(t)
	ctx := context.Background()
	session, p := testFixture(t, s)
	original, err := client.ConfigGet(ctx, "maxmemory").Result()
	if err != nil {
		t.Fatal(err)
	}
	value := original["maxmemory"]
	if err := client.ConfigSet(ctx, "maxmemory", "1").Err(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := client.ConfigSet(ctx, "maxmemory", value).Err(); err != nil {
			t.Errorf("restore maxmemory: %v", err)
		}
	}()
	if _, err := s.Accept(ctx, p); !errors.Is(err, tracking.ErrUncertain) {
		t.Fatalf("OOM must not be confirmed: %v", err)
	}
	if stored := client.Exists(ctx, positionKey(session.ID, 1)).Val(); stored != 0 {
		t.Fatalf("position unexpectedly stored under OOM: %d", stored)
	}
}

// Two opt-in phases are run around an external restart of the isolated Redis
// container. No test controls a permanent container.
func TestRedisRestartFixture(t *testing.T) {
	phase := os.Getenv("GPS_TEST_RESTART_PHASE")
	if phase == "" {
		t.Skip("restart probe is opt-in")
	}
	id := os.Getenv("GPS_TEST_RESTART_ID")
	if id == "" {
		t.Fatal("GPS_TEST_RESTART_ID is required")
	}
	s, client := testStore(t)
	ctx := context.Background()
	if phase == "write" {
		now := time.Now().UTC().Truncate(time.Millisecond)
		session := tracking.Session{ID: id, GuideID: "guide-synthetic", RouteID: "route-synthetic", TripID: "trip-synthetic", State: tracking.Active, StartedAt: now}
		if err := s.CreateSession(ctx, session); err != nil {
			t.Fatal(err)
		}
		p := tracking.Position{SessionID: id, Sequence: 1, CapturedAt: now, ReceivedAt: now, Longitude: -74.1, Latitude: 4.6}
		if _, err := s.Accept(ctx, p); err != nil {
			t.Fatal(err)
		}
		if err := client.Close(); err != nil {
			t.Fatal(err)
		}
		return
	}
	if phase != "read" {
		t.Fatal("invalid restart phase")
	}
	got, err := s.Session(ctx, id)
	if err != nil || got.LastSequence != 1 {
		t.Fatalf("recovered session %+v %v", got, err)
	}
	latest, err := s.Latest(ctx, id)
	if err != nil || latest.Sequence != 1 {
		t.Fatalf("recovered position %+v %v", latest, err)
	}
	ttl, err := client.PTTL(ctx, positionKey(id, 1)).Result()
	if err != nil || ttl <= 0 {
		t.Fatalf("recovered TTL %v %v", ttl, err)
	}
}
