package control_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/control"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/geo"
	policycatalog "github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/policies"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/redisstore"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/routes"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/tracking"
	"github.com/redis/go-redis/v9"
)

type reader struct {
	route routes.Route
	err   error
}

func (r reader) Get(context.Context, string) (routes.Route, error) { return r.route, r.err }

type policies struct{}

func (policies) Resolve(_ context.Context, id string) (control.Policy, error) {
	geographic := tracking.GeographicPolicy{CorridorRadiusMeters: 30, PointRadiusMeters: 20, MaxAccuracyMeters: 25}
	transition := tracking.TransitionPolicy{ArrivalObservations: 1, DeviationObservations: 2, RecoveryObservations: 2, MaxSampleGap: 5 * time.Second}
	ref, err := tracking.NewPolicyReference(id, 1, geographic, transition)
	if err != nil {
		return control.Policy{}, err
	}
	return control.Policy{Geographic: geographic, Transitions: &transition, Reference: ref}, nil
}

type uncertainReservation struct {
	saved  control.Reservation
	failed bool
}

func (r *uncertainReservation) Lookup(context.Context, string, string) (control.Reservation, bool, error) {
	return r.saved, r.saved.Session.ID != "", nil
}
func (r *uncertainReservation) Reserve(_ context.Context, _, _ string, record control.Reservation) (control.Reservation, bool, error) {
	r.saved = record
	if !r.failed {
		r.failed = true
		return control.Reservation{}, false, tracking.ErrUncertain
	}
	return record, false, nil
}

type fakeTracker struct {
	session   tracking.Session
	position  tracking.Position
	page      tracking.Page
	latestErr error
}

func (f *fakeTracker) ObservationLimits() (time.Duration, time.Duration) {
	return 15 * time.Minute, 30 * time.Second
}
func (f *fakeTracker) ProvisionPrepared(_ context.Context, s tracking.Session, _ []byte, _ *tracking.GeographicSnapshot) error {
	f.session = s
	return nil
}
func (f *fakeTracker) Status(_ context.Context, id string) (tracking.Session, error) {
	if f.session.ID != id {
		return tracking.Session{}, tracking.ErrNotFound
	}
	return f.session, nil
}
func (f *fakeTracker) Finish(_ context.Context, id string) error {
	if f.session.ID != id {
		return tracking.ErrNotFound
	}
	f.session.State = tracking.Finished
	return nil
}
func (f *fakeTracker) Latest(context.Context, string) (tracking.Position, error) {
	return f.position, f.latestErr
}
func (f *fakeTracker) History(context.Context, string, int64, int64) (tracking.Page, error) {
	return f.page, nil
}

func TestReadAuthorizationAndAvailability(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	f := &fakeTracker{session: tracking.Session{ID: "test-tracking", TripID: "test-trip", State: tracking.Active}}
	secret := sha256.Sum256([]byte("isolated read test secret only"))
	svc, err := control.New(f, reader{route: syntheticRoute()}, policies{}, &uncertainReservation{}, secret[:], time.Minute, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	p := principal()
	if _, _, err = svc.Latest(context.Background(), p, "test-tracking"); !errors.Is(err, control.ErrNoObservations) {
		t.Fatalf("no observations: %v", err)
	}
	f.session.LastSequence = 1
	f.latestErr = tracking.ErrNotFound
	expired := now.Add(-time.Second)
	f.session.LastPositionExpiresAt = &expired
	if _, _, err = svc.Latest(context.Background(), p, "test-tracking"); !errors.Is(err, control.ErrPositionExpired) {
		t.Fatalf("expired: %v", err)
	}
	future := now.Add(time.Hour)
	f.session.LastPositionExpiresAt = &future
	if _, _, err = svc.Latest(context.Background(), p, "test-tracking"); !errors.Is(err, tracking.ErrUnavailable) {
		t.Fatalf("unexpected missing live position: %v", err)
	}
	f.position = tracking.Position{SessionID: "test-tracking", Sequence: 1}
	f.latestErr = nil
	if _, got, err := svc.Latest(context.Background(), p, "test-tracking"); err != nil || got.Sequence != 1 {
		t.Fatalf("latest: %+v %v", got, err)
	}
	if _, _, err = svc.History(context.Background(), p, "test-tracking", -1, 1); !errors.Is(err, tracking.ErrInvalid) {
		t.Fatalf("bad cursor: %v", err)
	}
	if _, _, err = svc.History(context.Background(), p, "test-tracking", 0, 101); !errors.Is(err, tracking.ErrInvalid) {
		t.Fatalf("bad page limit: %v", err)
	}
	other := p
	other.TripIDs = map[string]bool{"other-trip": true}
	if _, _, err = svc.Latest(context.Background(), other, "test-tracking"); !errors.Is(err, tracking.ErrNotFound) {
		t.Fatalf("other trip leaked: %v", err)
	}
	noRead := p
	noRead.Capabilities = map[string]bool{}
	if _, _, err = svc.History(context.Background(), noRead, "test-tracking", 0, 1); !errors.Is(err, control.ErrForbidden) {
		t.Fatalf("missing capability: %v", err)
	}
}

func TestUncertainReservationRecoversWithoutSecondSession(t *testing.T) {
	store := &uncertainReservation{}
	tracker := &fakeTracker{}
	secret := sha256.Sum256([]byte("isolated unit test key only, not production"))
	svc, err := control.New(tracker, reader{route: syntheticRoute()}, policies{}, store, secret[:], time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := control.CreateRequest{TripID: "test-trip", GuideID: "test-guide", GeoRouteID: "test-route", PolicyID: "test-policy"}
	if _, err = svc.Create(context.Background(), principal(), "uncertain", request); !errors.Is(err, tracking.ErrUncertain) {
		t.Fatalf("uncertain write accepted: %v", err)
	}
	if tracker.session.ID != "" {
		t.Fatal("session created after uncertain reservation")
	}
	replayed, err := svc.Create(context.Background(), principal(), "uncertain", request)
	if err != nil || replayed.Session.ID != store.saved.Session.ID || replayed.Credential == "" {
		t.Fatalf("recovery: %+v %v", replayed, err)
	}
}

func TestPolicyCatalogChangePreservesReservedSessionOnRetry(t *testing.T) {
	fixture := []byte("schema_version: 1\npolicies:\n  - policy_id: demo-v1\n    revision: 1\n    geographic:\n      corridor_radius_meters: 30\n      point_radius_meters: 20\n      max_accuracy_meters: 25\n    transitions:\n      arrival_observations: 2\n      arrival_dwell: 1s\n      deviation_observations: 2\n      deviation_dwell: 1s\n      recovery_observations: 2\n      recovery_dwell: 1s\n      max_sample_gap: 5s\n")
	old, err := policycatalog.Parse(fixture)
	if err != nil {
		t.Fatal(err)
	}
	reservation := &uncertainReservation{failed: true}
	tracker := &fakeTracker{}
	secret := sha256.Sum256([]byte("isolated policy selection secret only"))
	service, err := control.New(tracker, reader{route: syntheticRoute()}, old, reservation, secret[:], time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := control.CreateRequest{TripID: "test-trip", GuideID: "test-guide", GeoRouteID: "test-route", PolicyID: "demo-v1"}
	created, err := service.Create(context.Background(), principal(), "catalog-key", request)
	if err != nil {
		t.Fatal(err)
	}
	if created.Session.SnapshotHash != reservation.saved.Snapshot.SnapshotHash || reservation.saved.Snapshot.PolicyReference.PolicyID != "demo-v1" {
		t.Fatal("versioned snapshot not reserved")
	}
	second := strings.ReplaceAll(strings.ReplaceAll(string(fixture), "demo-v1", "demo-v2"), "revision: 1", "revision: 2")
	second = strings.ReplaceAll(second, "point_radius_meters: 20", "point_radius_meters: 40")
	second = strings.TrimPrefix(second, "schema_version: 1\npolicies:\n")
	newCatalog, err := policycatalog.Parse(append(fixture, []byte(second)...))
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := control.New(tracker, reader{route: syntheticRoute()}, newCatalog, reservation, secret[:], time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := restarted.Create(context.Background(), principal(), "catalog-key", request)
	if err != nil || !replay.Replayed || replay.Session.ID != created.Session.ID || reservation.saved.Snapshot.Policy.PointRadiusMeters != 20 {
		t.Fatalf("retry changed effective policy: %+v %v", replay, err)
	}
	newReservation := &uncertainReservation{failed: true}
	newTracker := &fakeTracker{}
	newService, err := control.New(newTracker, reader{route: syntheticRoute()}, newCatalog, newReservation, secret[:], time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.PolicyID = "demo-v2"
	newSession, err := newService.Create(context.Background(), principal(), "catalog-key-v2", request)
	if err != nil || newSession.Session.ID == created.Session.ID || newReservation.saved.Snapshot.Policy.PointRadiusMeters != 40 || newReservation.saved.Snapshot.PolicyReference.Revision != 2 {
		t.Fatalf("new policy version was not selected: %+v %v", newSession, err)
	}
}

func syntheticRoute() routes.Route {
	first, last, resolved, verified, version := "PE01", "PE02", "resolved", "verified", "test-version"
	return routes.Route{ID: "test-route", Name: "isolated test route", GeometryVersion: &version, AssociationStatus: &verified,
		Geometry:      geo.LineString{Coordinates: []geo.Coordinate{{Longitude: -74.1, Latitude: 4.6}, {Longitude: -74.101, Latitude: 4.6}}},
		MeetingPoints: []routes.MeetingPoint{{ID: first, Name: first, Kind: "meeting_point", Location: geo.Coordinate{Longitude: -74.1, Latitude: 4.6}}, {ID: last, Name: last, Kind: "meeting_point", Location: geo.Coordinate{Longitude: -74.101, Latitude: 4.6}}},
		Endpoints:     routes.Endpoints{Status: &resolved, FirstPointID: &first, Start: routes.Endpoint{PointID: &first}, End: routes.Endpoint{PointID: &last}}}
}

func principal() control.Principal {
	return control.Principal{Subject: "test-controller", Audience: control.Audience, ExpiresAt: time.Now().Add(time.Hour), Capabilities: map[string]bool{control.CreateCapability: true, control.ReadCapability: true, control.FinishCapability: true}, TripIDs: map[string]bool{"test-trip": true}}
}

func TestPrincipalScopeAndExpiry(t *testing.T) {
	p := principal()
	if !p.Allows(time.Now(), control.CreateCapability, "test-trip", "") || p.Allows(time.Now(), control.CreateCapability, "other-trip", "") {
		t.Fatal("trip scope")
	}
	if p.Allows(time.Now(), "tracking:observe", "test-trip", "") {
		t.Fatal("observation credential cannot control sessions")
	}
	sessionOnly := principal()
	sessionOnly.TripIDs = nil
	sessionOnly.TrackingIDs = map[string]bool{"tracking-test": true}
	if !sessionOnly.Allows(time.Now(), control.ReadCapability, "other-trip", "tracking-test") || sessionOnly.Allows(time.Now(), control.ReadCapability, "other-trip", "other-tracking") {
		t.Fatal("tracking scope")
	}
	p.ExpiresAt = time.Now().Add(-time.Second)
	if p.Allows(time.Now(), control.ReadCapability, "test-trip", "") {
		t.Fatal("expired credential")
	}
	p = principal()
	p.Audience = "other"
	if p.Allows(time.Now(), control.ReadCapability, "test-trip", "") {
		t.Fatal("audience")
	}
	p = principal()
	p.NotBefore = time.Now().Add(time.Minute)
	if p.Allows(time.Now(), control.ReadCapability, "test-trip", "") {
		t.Fatal("not-before")
	}
}

func TestUnavailableRedisDoesNotProvision(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 100 * time.Millisecond, MaxRetries: -1})
	defer client.Close()
	store, _ := redisstore.New(client, 24*time.Hour, 0)
	tracker, _ := tracking.NewService(store, 15*time.Minute, 30*time.Second, nil, nil)
	secret := sha256.Sum256([]byte("isolated unavailable Redis test secret"))
	svc, _ := control.New(tracker, reader{route: syntheticRoute()}, policies{}, store, secret[:], time.Minute, nil)
	_, err := svc.Create(context.Background(), principal(), "unavailable", control.CreateRequest{TripID: "test-trip", GuideID: "test-guide", GeoRouteID: "test-route", PolicyID: "test-policy"})
	if !errors.Is(err, tracking.ErrUnavailable) {
		t.Fatalf("Redis outage: %v", err)
	}
}

func TestControlWithIsolatedRedis(t *testing.T) {
	addr := os.Getenv("GPS_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("set GPS_TEST_REDIS_ADDR to an isolated Redis")
	}
	ctx := context.Background()
	client := redis.NewClient(&redis.Options{Addr: addr})
	defer client.Close()
	if err := client.Ping(ctx).Err(); err != nil {
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
	secret := sha256.Sum256([]byte("isolated test only, not a production service secret"))
	svc, err := control.New(tracker, reader{route: syntheticRoute()}, policies{}, store, secret[:], 10*time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	legacy := syntheticRoute()
	legacy.GeometryVersion = nil
	ineligible, _ := control.New(tracker, reader{route: legacy}, policies{}, store, secret[:], 10*time.Minute, nil)
	if _, err = ineligible.Create(ctx, principal(), "legacy-key", control.CreateRequest{TripID: "test-trip", GuideID: "test-guide", GeoRouteID: "test-route", PolicyID: "test-policy"}); !errors.Is(err, tracking.ErrRouteIneligible) {
		t.Fatalf("legacy route allowed: %v", err)
	}
	request := control.CreateRequest{TripID: "test-trip", GuideID: "test-guide", GeoRouteID: "test-route", PolicyID: "test-policy"}
	p := principal()
	if _, err = svc.Create(ctx, p, "missing-scope", control.CreateRequest{TripID: "other-trip", GuideID: "g", GeoRouteID: "test-route", PolicyID: "test-policy"}); !errors.Is(err, control.ErrForbidden) {
		t.Fatalf("scope: %v", err)
	}
	const workers = 12
	idempotencyKey := fmt.Sprintf("same-key-%d", time.Now().UnixNano())
	results := make([]control.Created, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = svc.Create(ctx, p, idempotencyKey, request)
		}(i)
	}
	wg.Wait()
	for i := range results {
		if errs[i] != nil || results[i].Session.ID != results[0].Session.ID || results[i].Credential != results[0].Credential || results[i].Credential == "" {
			t.Fatalf("concurrent create %d: %+v %v", i, results[i], errs[i])
		}
	}
	if _, err = tracker.Authenticate(ctx, results[0].Session.ID, results[0].Credential); err != nil {
		t.Fatalf("observation token: %v", err)
	}
	accepted, err := tracker.Receive(ctx, results[0].Session.ID, results[0].Credential, tracking.ObservationInput{Sequence: 1, Longitude: -74.1, Latitude: 4.6, AccuracyMeters: 5, CapturedAt: time.Now().UTC().Truncate(time.Millisecond)})
	if err != nil || accepted.ExpiresAt.Before(time.Now().Add(24*time.Hour)) {
		t.Fatalf("observation retention: %+v %v", accepted, err)
	}
	other := request
	other.GuideID = "different"
	if _, err = svc.Create(ctx, p, idempotencyKey, other); !errors.Is(err, tracking.ErrConflict) {
		t.Fatalf("body conflict: %v", err)
	}
	// A lost HTTP response can be recovered using the same reservation even
	// when the geographic service becomes unavailable.
	restarted, err := control.New(tracker, reader{err: routes.ErrUnavailable}, policies{}, store, secret[:], 10*time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := restarted.Create(ctx, p, idempotencyKey, request)
	if err != nil || replay.Session.ID != results[0].Session.ID || !replay.Replayed || replay.Credential != results[0].Credential {
		t.Fatalf("response recovery: %+v %v", replay, err)
	}
	expiredWindow, err := control.New(tracker, reader{err: routes.ErrUnavailable}, policies{}, store, secret[:], 10*time.Minute, func() time.Time { return time.Now().Add(11 * time.Minute) })
	if err != nil {
		t.Fatal(err)
	}
	late, err := expiredWindow.Create(ctx, p, idempotencyKey, request)
	if err != nil || late.Session.ID != replay.Session.ID || late.Credential != "" || late.CredentialStatus != "reissue_required" {
		t.Fatalf("late replay: %+v %v", late, err)
	}
	if _, err = svc.Status(ctx, control.Principal{Subject: p.Subject, Audience: p.Audience, ExpiresAt: p.ExpiresAt, Capabilities: p.Capabilities, TripIDs: map[string]bool{"other-trip": true}}, replay.Session.ID); !errors.Is(err, tracking.ErrNotFound) {
		t.Fatalf("read scope: %v", err)
	}
	finished, err := svc.Finish(ctx, p, replay.Session.ID)
	if err != nil || finished.State != tracking.Finished {
		t.Fatalf("finish: %+v %v", finished, err)
	}
	finished, err = svc.Finish(ctx, p, replay.Session.ID)
	if err != nil || finished.State != tracking.Finished {
		t.Fatalf("idempotent finish: %+v %v", finished, err)
	}
	if _, err = tracker.Authenticate(ctx, replay.Session.ID, replay.Credential); !errors.Is(err, tracking.ErrUnauthorized) {
		t.Fatalf("token not revoked: %v", err)
	}
	page, err := tracker.History(ctx, replay.Session.ID, 0, 10)
	if err != nil || len(page.Positions) != 1 || page.Positions[0].Sequence != 1 {
		t.Fatalf("history after finish: %+v %v", page, err)
	}
	postFinish, err := svc.Create(ctx, p, idempotencyKey, request)
	if err != nil || postFinish.Session.ID != replay.Session.ID || postFinish.Credential != "" || postFinish.CredentialStatus != "revoked" {
		t.Fatalf("post finish replay: %+v %v", postFinish, err)
	}
}

func TestControlRestartPhase(t *testing.T) {
	phase := os.Getenv("GPS_TEST_CONTROL_RESTART_PHASE")
	if phase == "" {
		t.Skip("isolated AOF restart test only")
	}
	addr := os.Getenv("GPS_TEST_REDIS_ADDR")
	if addr == "" {
		t.Fatal("isolated Redis address required")
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	defer client.Close()
	store, err := redisstore.New(client, 24*time.Hour, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	tracker, err := tracking.NewService(store, 15*time.Minute, 30*time.Second, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	secret := sha256.Sum256([]byte("isolated restart test secret, not production"))
	r := reader{route: syntheticRoute()}
	if phase == "read" {
		r = reader{err: routes.ErrUnavailable}
	}
	svc, err := control.New(tracker, r, policies{}, store, secret[:], 10*time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	created, err := svc.Create(context.Background(), principal(), "restart-phase-key", control.CreateRequest{TripID: "test-trip", GuideID: "test-guide", GeoRouteID: "test-route", PolicyID: "test-policy"})
	if err != nil || created.Session.ID == "" {
		t.Fatalf("restart phase %s: %+v %v", phase, created, err)
	}
	if phase == "read" && !created.Replayed {
		t.Fatal("reservation lost after Redis restart")
	}
	if created.Credential == "" {
		t.Fatal("credential could not be securely reissued within window")
	}
	if _, err = tracker.Authenticate(context.Background(), created.Session.ID, created.Credential); err != nil {
		t.Fatalf("credential after restart: %v", err)
	}
	t.Logf("restart phase %s recovered tracking ID %s", phase, created.Session.ID)
}
