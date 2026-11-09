package tracking

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/geo"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/pathshttp"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/routes"
)

type fakeRepository struct {
	session           Session
	digest            [32]byte
	position          Position
	expiry            time.Time
	writes            int
	acceptErr         error
	provisionFailures int
	provisionCalls    int
	snapshot          GeographicSnapshot
}

func (f *fakeRepository) GeographicState(context.Context, string) (GeographicState, error) {
	return GeographicState{}, ErrNotFound
}
func (f *fakeRepository) AcceptTransition(context.Context, Position, TransitionPlan) (Acceptance, error) {
	return Acceptance{}, ErrUnavailable
}
func (f *fakeRepository) RecoverPending(context.Context, string) error { return ErrPending }

func (f *fakeRepository) ProvisionSnapshot(ctx context.Context, s Session, d [32]byte, snapshot GeographicSnapshot) error {
	f.snapshot = snapshot
	return f.Provision(ctx, s, d)
}
func (f *fakeRepository) Snapshot(_ context.Context, id string) (GeographicSnapshot, error) {
	if id != f.session.ID || f.snapshot.SnapshotHash == "" {
		return GeographicSnapshot{}, ErrNotFound
	}
	return f.snapshot, nil
}

func (f *fakeRepository) Provision(_ context.Context, s Session, d [32]byte) error {
	f.provisionCalls++
	f.session = s
	f.digest = d
	if f.provisionFailures > 0 {
		f.provisionFailures--
		return ErrUncertain
	}
	return nil
}

func TestProvisionRetriesSameCredentialAfterUncertainWrite(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	repo := &fakeRepository{provisionFailures: 1}
	service, _ := NewService(repo, 15*time.Minute, 30*time.Second, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{31}, 56)))
	session, token, err := service.Provision(context.Background(), "guide", "trip", "route")
	if err != nil || token == "" || repo.provisionCalls != 2 || session.ID != repo.session.ID {
		t.Fatalf("reconciled provision %+v %d %v", session, repo.provisionCalls, err)
	}
	other := &fakeRepository{provisionFailures: 2}
	service, _ = NewService(other, 15*time.Minute, 30*time.Second, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{32}, 56)))
	session, token, err = service.Provision(context.Background(), "guide", "trip", "route")
	if !errors.Is(err, ErrUncertain) || token != "" || session.ID != "" || other.provisionCalls != 2 {
		t.Fatalf("uncertain token leaked %+v %q %v", session, token, err)
	}
}
func (f *fakeRepository) Credential(_ context.Context, id string) (Session, [32]byte, error) {
	if id != f.session.ID || f.session.State != Active {
		return Session{}, [32]byte{}, ErrNotFound
	}
	return f.session, f.digest, nil
}
func (f *fakeRepository) Session(_ context.Context, id string) (Session, error) {
	if id != f.session.ID {
		return Session{}, ErrNotFound
	}
	return f.session, nil
}
func (f *fakeRepository) FinishSession(_ context.Context, id string, at time.Time) error {
	if id != f.session.ID {
		return ErrNotFound
	}
	f.session.State = Finished
	f.session.FinishedAt = &at
	return nil
}
func (f *fakeRepository) Accept(_ context.Context, p Position) (Acceptance, error) {
	if f.acceptErr != nil {
		return Acceptance{}, f.acceptErr
	}
	f.writes++
	f.position = p
	return Acceptance{Position: p, ExpiresAt: f.expiry}, nil
}
func (f *fakeRepository) ReadPosition(_ context.Context, id string, seq int64) (Position, error) {
	if f.position.SessionID == id && f.position.Sequence == seq {
		return f.position, nil
	}
	return Position{}, ErrNotFound
}
func (f *fakeRepository) PositionExpiry(context.Context, string, int64) (time.Time, error) {
	return f.expiry, nil
}
func (f *fakeRepository) Latest(context.Context, string) (Position, error) { return f.position, nil }
func (f *fakeRepository) History(context.Context, string, int64, int64) (Page, error) {
	return Page{Positions: []Position{f.position}}, nil
}

type syntheticReader struct {
	route routes.Route
	err   error
}

func (r syntheticReader) Get(_ context.Context, id string) (routes.Route, error) {
	if r.err != nil {
		return routes.Route{}, r.err
	}
	if id != r.route.ID {
		return routes.Route{}, routes.ErrNotFound
	}
	return r.route, nil
}

func syntheticRoute() routes.Route {
	first, last, resolved, verified, version := "PE01", "PE02", "resolved", "verified", "synthetic-v1"
	return routes.Route{
		ID: "synthetic-route", Name: "Synthetic KMZ-like school route", GeometryVersion: &version, AssociationStatus: &verified,
		Geometry:      geo.LineString{Coordinates: []geo.Coordinate{{Longitude: -74.1, Latitude: 4.6}, {Longitude: -74.1005, Latitude: 4.6}, {Longitude: -74.101, Latitude: 4.6}}},
		MeetingPoints: []routes.MeetingPoint{{ID: first, Name: first, Kind: "meeting_point", Location: geo.Coordinate{Longitude: -74.1, Latitude: 4.6}}, {ID: last, Name: last, Kind: "meeting_point", Location: geo.Coordinate{Longitude: -74.101, Latitude: 4.6}}},
		Endpoints:     routes.Endpoints{Status: &resolved, FirstPointID: &first, Start: routes.Endpoint{PointID: &first}, End: routes.Endpoint{PointID: &last}},
	}
}

func TestGeographicSnapshotAndReceipt(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	policy := GeographicPolicy{CorridorRadiusMeters: 30, PointRadiusMeters: 20, MaxAccuracyMeters: 25} // test-only
	route := syntheticRoute()
	snapshot, err := BuildSnapshot(route, policy)
	if err != nil || snapshot.OperationalReady() != nil || snapshot.GeometryFingerprint == snapshot.SnapshotHash {
		t.Fatalf("snapshot: %+v %v", snapshot, err)
	}
	route.Geometry.Coordinates[0].Latitude = 30
	if snapshot.Route.Geometry.Coordinates[0].Latitude != 4.6 {
		t.Fatal("snapshot retained caller's mutable coordinates")
	}
	repo := &fakeRepository{expiry: now.Add(25 * time.Hour)}
	service, _ := NewService(repo, 15*time.Minute, 30*time.Second, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{9}, 56)))
	session, token, err := service.ProvisionGeographic(context.Background(), syntheticReader{route: syntheticRoute()}, "guide", "trip", "synthetic-route", policy)
	if err != nil || session.SnapshotHash == "" || token == "" {
		t.Fatalf("geographic provision: %+v %v", session, err)
	}
	input := ObservationInput{Sequence: 1, Latitude: 4.6, Longitude: -74.1, AccuracyMeters: 5, CapturedAt: now}
	accepted, err := service.Receive(context.Background(), session.ID, token, input)
	if err != nil || accepted.Position.Geographic == nil || accepted.Position.Geographic.RouteStatus != "inside" || len(accepted.Position.Geographic.MeetingPoints) != 2 || accepted.Position.Geographic.MeetingPoints[0].Status != "inside" || accepted.Position.Geographic.MeetingPoints[1].Status != "outside" || accepted.Position.FirstArrival != nil {
		t.Fatalf("inside evaluation: %+v %v", accepted, err)
	}
	retry, err := service.Receive(context.Background(), session.ID, token, input)
	if err != nil || !retry.Duplicate || repo.writes != 1 || retry.Position.Geographic.SnapshotHash != accepted.Position.Geographic.SnapshotHash {
		t.Fatalf("replay: %+v %v", retry, err)
	}
	input.Latitude = 4.61
	if _, err := service.Receive(context.Background(), session.ID, token, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("same sequence changed: %v", err)
	}
	input.Sequence = 2
	outside, err := service.Receive(context.Background(), session.ID, token, input)
	if err != nil || outside.Position.Geographic.RouteStatus != "outside" {
		t.Fatalf("outside: %+v %v", outside, err)
	}
	input.Sequence = 3
	input.Latitude = 4.6
	input.AccuracyMeters = 100
	uncertain, err := service.Receive(context.Background(), session.ID, token, input)
	if err != nil || uncertain.Position.Geographic.RouteStatus != "indeterminate" || uncertain.Position.Geographic.MeetingPoints[0].Status != "indeterminate" {
		t.Fatalf("indeterminate: %+v %v", uncertain, err)
	}
	if err := service.Finish(context.Background(), session.ID); err != nil {
		t.Fatal(err)
	}
	input.Sequence = 4
	if _, err := service.Receive(context.Background(), session.ID, token, input); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("closed session: %v", err)
	}
}

func TestGeographicEligibilityAndGeometry(t *testing.T) {
	policy := GeographicPolicy{CorridorRadiusMeters: 30, PointRadiusMeters: 20, MaxAccuracyMeters: 25}
	route := syntheticRoute()
	route.GeometryVersion = nil
	route.AssociationStatus = nil
	route.Unavailable = []string{"geometry_version", "association_status"}
	snapshot, err := BuildSnapshot(route, policy)
	if err != nil || !errors.Is(snapshot.OperationalReady(), ErrRouteIneligible) {
		t.Fatalf("current Python contract: %v", err)
	}
	route = syntheticRoute()
	route.MeetingPoints[0], route.MeetingPoints[1] = route.MeetingPoints[1], route.MeetingPoints[0]
	if _, err := BuildSnapshot(route, policy); !errors.Is(err, ErrRouteIneligible) {
		t.Fatalf("wrong PE order: %v", err)
	}
	route = syntheticRoute()
	route.Geometry.Coordinates[1].Latitude = 85
	if _, err := BuildSnapshot(route, policy); !errors.Is(err, ErrInvalidGeometry) {
		t.Fatalf("unsupported geometry: %v", err)
	}
	// Python's synthetic test_kmz_reader.py fixture has a roughly 15.7 km
	// single segment; this engine intentionally rejects it.
	route = syntheticRoute()
	route.Geometry.Coordinates = []geo.Coordinate{{Longitude: -74.1, Latitude: 4.6}, {Longitude: -74.2, Latitude: 4.7}}
	if _, err := BuildSnapshot(route, policy); !errors.Is(err, ErrInvalidGeometry) {
		t.Fatalf("representative KMZ long segment: %v", err)
	}
	route = syntheticRoute()
	route.MeetingPoints[1].ID = route.MeetingPoints[0].ID
	if _, err := BuildSnapshot(route, policy); !errors.Is(err, ErrRouteIneligible) {
		t.Fatalf("duplicate association: %v", err)
	}
	if _, err := BuildSnapshot(syntheticRoute(), GeographicPolicy{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing policy: %v", err)
	}
}

func TestCurrentPythonContractCannotProvisionGeographicSession(t *testing.T) {
	const response = `{"_id":"synthetic-route","name":"school route","geometry":{"type":"LineString","coordinates":[[-74.1,4.6],[-74.1005,4.6]]},"endpoint_status":"resolved","first_meeting_point_id":"PE01","start_meeting_point_id":"PE01","end_meeting_point_id":"PE02","meeting_points":[{"_id":"PE01","name":"PE01","kind":"meeting_point","location":{"type":"Point","coordinates":[-74.1,4.6]}},{"_id":"PE02","name":"PE02","kind":"meeting_point","location":{"type":"Point","coordinates":[-74.1005,4.6]}}]}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/routes/synthetic-route" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(response))
	}))
	defer upstream.Close()
	reader, err := pathshttp.New(upstream.URL, "/api/v1/routes/{route_id}", "", time.Second, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	route, err := reader.Get(context.Background(), "synthetic-route")
	if err != nil {
		t.Fatal(err)
	}
	policy := GeographicPolicy{CorridorRadiusMeters: 30, PointRadiusMeters: 20, MaxAccuracyMeters: 25}
	snapshot, err := BuildSnapshot(route, policy)
	if err != nil || snapshot.GeometryFingerprint == "" {
		t.Fatalf("queryable route cannot be prepared: %v", err)
	}
	repo := &fakeRepository{}
	service, _ := NewService(repo, time.Minute, time.Second, nil, nil)
	_, token, err := service.ProvisionGeographic(context.Background(), reader, "guide", "trip", "synthetic-route", policy)
	if !errors.Is(err, ErrRouteIneligible) || token != "" || repo.provisionCalls != 0 {
		t.Fatalf("unsafe provision: token=%q calls=%d err=%v", token, repo.provisionCalls, err)
	}
}

func TestConcurrentSnapshotEvaluation(t *testing.T) {
	snapshot, err := BuildSnapshot(syntheticRoute(), GeographicPolicy{CorridorRadiusMeters: 30, PointRadiusMeters: 20, MaxAccuracyMeters: 25})
	if err != nil {
		t.Fatal(err)
	}
	accuracy := 5.0
	position := Position{Longitude: -74.1005, Latitude: 4.6, AccuracyMeters: &accuracy}
	var wg sync.WaitGroup
	problems := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := snapshot.Evaluate(position)
			if err != nil {
				problems <- err
				return
			}
			if result.RouteStatus != "inside" || len(result.MeetingPoints) != 2 {
				problems <- ErrInvalid
			}
		}()
	}
	wg.Wait()
	close(problems)
	for err := range problems {
		t.Fatal(err)
	}
}

func TestServiceProvisionAuthenticationAndFinish(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	repo := &fakeRepository{expiry: now.Add(24 * time.Hour)}
	service, err := NewService(repo, 15*time.Minute, 30*time.Second, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{42}, 56)))
	if err != nil {
		t.Fatal(err)
	}
	session, token, err := service.Provision(context.Background(), "guide opaque", "trip opaque", "route opaque")
	if err != nil || token == "" || session.ID == token || repo.session.MaxObservationAge != 15*time.Minute {
		t.Fatalf("provision %+v %v", session, err)
	}
	if digest := sha256.Sum256(bytes.Repeat([]byte{42}, 32)); digest != repo.digest {
		t.Fatal("digest not stored")
	}
	if _, err := service.Authenticate(context.Background(), session.ID, token); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RequireActive(context.Background(), session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(context.Background(), session.ID, "invalid"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("invalid token %v", err)
	}
	if _, err := service.Authenticate(context.Background(), "another-session", token); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("cross session token %v", err)
	}
	if err := service.Finish(context.Background(), session.ID); err != nil {
		t.Fatal(err)
	}
	status, err := service.Status(context.Background(), session.ID)
	if err != nil || status.State != Finished {
		t.Fatalf("status %+v %v", status, err)
	}
	if _, err := service.Authenticate(context.Background(), session.ID, token); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("revocation %v", err)
	}
	if _, err := service.RequireActive(context.Background(), session.ID); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed active check %v", err)
	}
}

func TestServiceReceiveTimePolicyAndRetry(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	repo := &fakeRepository{expiry: now.Add(24 * time.Hour)}
	service, _ := NewService(repo, 15*time.Minute, 30*time.Second, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{7}, 56)))
	session, token, err := service.Provision(context.Background(), "g", "t", "r")
	if err != nil {
		t.Fatal(err)
	}
	input := ObservationInput{Sequence: 1, Latitude: 4.6, Longitude: -74.1, AccuracyMeters: 5, CapturedAt: now}
	accepted, err := service.Receive(context.Background(), session.ID, token, input)
	if err != nil || accepted.Position.Geographic != nil || accepted.Position.FirstArrival != nil || accepted.Position.IdempotencyVersion != 1 {
		t.Fatalf("accept %+v %v", accepted, err)
	}
	now = now.Add(20 * time.Minute)
	replay, err := service.Receive(context.Background(), session.ID, token, input)
	if err != nil || !replay.Duplicate || repo.writes != 1 || !replay.Position.ReceivedAt.Equal(accepted.Position.ReceivedAt) {
		t.Fatalf("old replay %+v %v", replay, err)
	}
	now = now.Add(-20 * time.Minute)
	input.CapturedAt = now.Add(-20 * time.Minute)
	if _, err := service.Receive(context.Background(), session.ID, token, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed old sequence %v", err)
	}
	input.Sequence = 2
	if _, err := service.Receive(context.Background(), session.ID, token, input); !errors.Is(err, ErrStale) || repo.writes != 1 {
		t.Fatalf("stale %v writes=%d", err, repo.writes)
	}
	input.CapturedAt = now.Add(time.Minute)
	if _, err := service.Receive(context.Background(), session.ID, token, input); !errors.Is(err, ErrStale) || repo.writes != 1 {
		t.Fatalf("future %v writes=%d", err, repo.writes)
	}
	input.CapturedAt = now
	input.Sequence = 0
	if _, err := service.Receive(context.Background(), session.ID, token, input); !errors.Is(err, ErrInvalid) || repo.writes != 1 {
		t.Fatalf("invalid sequence %v", err)
	}
	input.Sequence = 2
	repo.acceptErr = ErrUncertain
	if _, err := service.Receive(context.Background(), session.ID, token, input); !errors.Is(err, ErrUncertain) {
		t.Fatalf("uncertain %v", err)
	}
}

func TestStableFingerprintPreservesLegacy(t *testing.T) {
	_, p := fixture()
	p.IdempotencyVersion = 1
	a, _, err := p.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	p.ReceivedAt = p.ReceivedAt.Add(time.Second)
	b, _, err := p.Fingerprint()
	if err != nil || a != b {
		t.Fatal("server receipt changed request fingerprint")
	}
	p.IdempotencyVersion = 0
	c, _, err := p.Fingerprint()
	if err != nil || c == b {
		t.Fatal("legacy fingerprint unexpectedly changed")
	}
}
