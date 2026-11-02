package tracking

import (
	"testing"
	"time"
)

func testTransitionPolicy() *TransitionPolicy {
	// Explicit fixture values; no operational policy has been agreed.
	return &TransitionPolicy{ArrivalObservations: 2, ArrivalDwell: time.Second, DeviationObservations: 2, DeviationDwell: time.Second, RecoveryObservations: 2, RecoveryDwell: time.Second, MaxSampleGap: 5 * time.Second}
}

func transitionFixture(t *testing.T) (GeographicSnapshot, GeographicState, Session) {
	t.Helper()
	snapshot, err := BuildSnapshotWithTransitions(syntheticRoute(), GeographicPolicy{CorridorRadiusMeters: 30, PointRadiusMeters: 20, MaxAccuracyMeters: 25}, testTransitionPolicy())
	if err != nil || snapshot.OperationalReady() != nil {
		t.Fatalf("fixture: %v", err)
	}
	session := Session{ID: "synthetic-session", GuideID: "synthetic-guide", TripID: "synthetic-trip", RouteID: snapshot.Route.ID, SnapshotHash: snapshot.SnapshotHash, State: Active}
	return snapshot, InitialGeographicState(snapshot), session
}

func advanceFixture(t *testing.T, snapshot GeographicSnapshot, state GeographicState, session Session, seq int64, at time.Time, longitude, latitude, accuracy float64) (TransitionPlan, Position) {
	t.Helper()
	p := Position{SessionID: session.ID, Sequence: seq, CapturedAt: at, ReceivedAt: at, Longitude: longitude, Latitude: latitude, AccuracyMeters: &accuracy, IdempotencyVersion: 2}
	var err error
	p.Geographic, err = snapshot.Evaluate(p)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Advance(snapshot, state, session, p)
	if err != nil {
		t.Fatal(err)
	}
	p.FirstArrival = plan.FirstArrival
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := plan.Validate(p); err != nil {
		t.Fatal(err)
	}
	return plan, p
}

func TestArrivalConfirmationAndSkippedPoints(t *testing.T) {
	snapshot, state, session := transitionFixture(t)
	start := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	plan, _ := advanceFixture(t, snapshot, state, session, 1, start, -74.101, 4.6, 5)
	if len(plan.Events) != 0 {
		t.Fatal("single reading confirmed PE02")
	}
	state = plan.State
	plan, _ = advanceFixture(t, snapshot, state, session, 2, start.Add(time.Second), -74.101, 4.6, 5)
	if len(plan.Events) != 1 || plan.Events[0].Type != MeetingPointArrival || plan.Events[0].MeetingPointOrder != 2 || plan.FirstArrival != nil {
		t.Fatalf("skipped PE01: %+v", plan)
	}
	state = plan.State
	plan, _ = advanceFixture(t, snapshot, state, session, 3, start.Add(2*time.Second), -74.1, 4.6, 5)
	state = plan.State
	plan, position := advanceFixture(t, snapshot, state, session, 4, start.Add(3*time.Second), -74.1, 4.6, 5)
	if len(plan.Events) != 1 || plan.Events[0].Type != FirstMeetingPointArrival || plan.FirstArrival == nil || plan.FirstArrival.Sequence != 4 || position.FirstArrival == nil {
		t.Fatalf("first arrival: %+v", plan)
	}
	firstID := plan.Events[0].ID
	state = plan.State
	plan, _ = advanceFixture(t, snapshot, state, session, 5, start.Add(4*time.Second), -74.1, 4.6, 5)
	if len(plan.Events) != 0 || plan.FirstArrival != nil || state.Points[0].ConfirmedSequence != 4 || firstID == "" {
		t.Fatal("arrival repeated")
	}
}

func TestAmbiguousAndImpreciseArrival(t *testing.T) {
	snapshot, state, session := transitionFixture(t)
	start := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	plan, _ := advanceFixture(t, snapshot, state, session, 1, start, -74.1, 4.6, 100)
	state = plan.State
	plan, _ = advanceFixture(t, snapshot, state, session, 2, start.Add(time.Second), -74.1, 4.60016, 5)
	if plan.State.Points[0].Candidate != nil {
		t.Fatal("boundary uncertainty counted as arrival evidence")
	}
	state = plan.State
	plan, _ = advanceFixture(t, snapshot, state, session, 3, start.Add(2*time.Second), -74.1, 4.6, 5)
	if len(plan.Events) != 0 {
		t.Fatal("indeterminate or boundary evidence counted")
	}
	closeRoute := syntheticRoute()
	closeRoute.MeetingPoints[1].Location.Longitude = -74.10002
	snapshot, err := BuildSnapshotWithTransitions(closeRoute, snapshot.Policy, testTransitionPolicy())
	if err != nil {
		t.Fatal(err)
	}
	session.SnapshotHash = snapshot.SnapshotHash
	state = InitialGeographicState(snapshot)
	for i := int64(1); i <= 3; i++ {
		plan, _ = advanceFixture(t, snapshot, state, session, i, start.Add(time.Duration(i)*time.Second), -74.1, 4.6, 5)
		if len(plan.Events) != 0 {
			t.Fatal("overlapping geofences confirmed a point")
		}
		state = plan.State
	}
}

func TestDeviationBoundaryOscillation(t *testing.T) {
	snapshot, state, session := transitionFixture(t)
	start := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	for i, step := range []struct {
		lat  float64
		want EventType
	}{
		{4.6004, ""}, {4.6002, ""}, {4.6004, ""}, {4.60027, ""},
		{4.6004, ""}, {4.6004, RouteDeviationStarted},
		{4.6002, ""}, {4.6004, ""}, {4.6002, ""}, {4.6002, RouteDeviationResolved},
	} {
		plan, _ := advanceFixture(t, snapshot, state, session, int64(i+1), start.Add(time.Duration(i)*time.Second), -74.1005, step.lat, 5)
		if step.want == "" && len(plan.Events) != 0 || step.want != "" && (len(plan.Events) != 1 || plan.Events[0].Type != step.want) {
			t.Fatalf("boundary step %d: %+v", i, plan.Events)
		}
		state = plan.State
	}
}

func TestDeviationEpisodeRecoveryGapsAndUncertainty(t *testing.T) {
	snapshot, state, session := transitionFixture(t)
	start := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	type sample struct {
		lat, accuracy float64
		seconds       int
		want          EventType
	}
	for i, sample := range []sample{
		{4.61, 5, 0, ""},
		{4.61, 100, 1, ""}, // low precision resets evidence
		{4.61, 5, 2, ""},
		{4.61, 5, 10, ""}, // gap resets evidence
		{4.61, 5, 11, RouteDeviationStarted},
		{4.61, 5, 12, ""}, // active episode does not repeat
		{4.6, 5, 13, ""},
		{4.6, 100, 14, ""}, // uncertain recovery resets evidence
		{4.6, 5, 15, ""},
		{4.6, 5, 16, RouteDeviationResolved},
	} {
		plan, _ := advanceFixture(t, snapshot, state, session, int64(i+1), start.Add(time.Duration(sample.seconds)*time.Second), -74.1005, sample.lat, sample.accuracy)
		if sample.want == "" && len(plan.Events) != 0 || sample.want != "" && (len(plan.Events) != 1 || plan.Events[0].Type != sample.want) {
			t.Fatalf("step %d: %+v", i, plan.Events)
		}
		state = plan.State
	}
	if state.Deviation.Active {
		t.Fatal("episode not resolved")
	}
}

func TestTransitionPolicyFrozenInSnapshot(t *testing.T) {
	snapshot, _, _ := transitionFixture(t)
	old, err := BuildSnapshot(syntheticRoute(), snapshot.Policy)
	if err != nil || old.TransitionPolicy != nil || old.SnapshotHash == snapshot.SnapshotHash {
		t.Fatalf("policy not bound: %v", err)
	}
	changed := *testTransitionPolicy()
	changed.ArrivalDwell = 2 * time.Second
	other, err := BuildSnapshotWithTransitions(syntheticRoute(), snapshot.Policy, &changed)
	if err != nil || other.SnapshotHash == snapshot.SnapshotHash {
		t.Fatalf("policy changed silently: %v", err)
	}
	if _, err := BuildSnapshotWithTransitions(syntheticRoute(), snapshot.Policy, &TransitionPolicy{}); err == nil {
		t.Fatal("empty temporal policy")
	}
}

func TestSameCaptureTimeIsNotIndependentEvidence(t *testing.T) {
	snapshot, state, session := transitionFixture(t)
	at := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	for seq := int64(1); seq <= 3; seq++ {
		plan, _ := advanceFixture(t, snapshot, state, session, seq, at, -74.1, 4.6, 5)
		if len(plan.Events) != 0 { t.Fatal("multiple sequences for one capture confirmed arrival") }
		state = plan.State
	}
	plan, _ := advanceFixture(t, snapshot, state, session, 4, at.Add(time.Second), -74.1, 4.6, 5)
	if len(plan.Events) != 1 || plan.Events[0].Type != FirstMeetingPointArrival { t.Fatalf("fresh evidence missing: %+v", plan.Events) }
}
