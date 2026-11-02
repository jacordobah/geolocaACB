package tracking

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/geo"
	"reflect"
	"strings"
	"time"
)

// TransitionPolicy has no production defaults. An authorized provisioning
// caller supplies it explicitly and the snapshot hash freezes its values.
type TransitionPolicy struct {
	ArrivalObservations   int           `json:"arrival_observations"`
	ArrivalDwell          time.Duration `json:"arrival_dwell"`
	DeviationObservations int           `json:"deviation_observations"`
	DeviationDwell        time.Duration `json:"deviation_dwell"`
	RecoveryObservations  int           `json:"recovery_observations"`
	RecoveryDwell         time.Duration `json:"recovery_dwell"`
	MaxSampleGap          time.Duration `json:"max_sample_gap"`
}

func (p TransitionPolicy) Validate() error {
	if p.ArrivalObservations < 1 || p.ArrivalObservations > 100 || p.DeviationObservations < 2 || p.DeviationObservations > 100 || p.RecoveryObservations < 2 || p.RecoveryObservations > 100 || p.ArrivalDwell < 0 || p.DeviationDwell < 0 || p.RecoveryDwell < 0 || p.ArrivalDwell > 24*time.Hour || p.DeviationDwell > 24*time.Hour || p.RecoveryDwell > 24*time.Hour || p.MaxSampleGap <= 0 || p.MaxSampleGap > time.Hour || p.ArrivalDwell%time.Millisecond != 0 || p.DeviationDwell%time.Millisecond != 0 || p.RecoveryDwell%time.Millisecond != 0 || p.MaxSampleGap%time.Millisecond != 0 {
		return fmt.Errorf("%w: temporal confirmation policy", ErrInvalid)
	}
	return nil
}

type Evidence struct {
	Since time.Time `json:"since"`
	Count int       `json:"count"`
}
type PointProgress struct {
	ID                string    `json:"id"`
	Candidate         *Evidence `json:"candidate,omitempty"`
	ConfirmedSequence int64     `json:"confirmed_sequence,omitempty"`
}
type DeviationProgress struct {
	Active               bool      `json:"active"`
	EpisodeStartSequence int64     `json:"episode_start_sequence,omitempty"`
	Candidate            *Evidence `json:"candidate,omitempty"`
}
type GeographicState struct {
	SnapshotHash   string            `json:"snapshot_hash"`
	LastSequence   int64             `json:"last_sequence"`
	LastCapturedAt time.Time         `json:"last_captured_at"`
	Points         []PointProgress   `json:"points"`
	Deviation      DeviationProgress `json:"deviation"`
}

func InitialGeographicState(snapshot GeographicSnapshot) GeographicState {
	state := GeographicState{SnapshotHash: snapshot.SnapshotHash, Points: make([]PointProgress, len(snapshot.Route.MeetingPoints))}
	for i, point := range snapshot.Route.MeetingPoints {
		state.Points[i].ID = point.ID
	}
	return state
}

type EventType string

const (
	FirstMeetingPointArrival EventType = "first_meeting_point_arrival"
	MeetingPointArrival      EventType = "meeting_point_arrival"
	RouteDeviationStarted    EventType = "route_deviation_started"
	RouteDeviationResolved   EventType = "route_deviation_resolved"
	TrackingSessionActivated EventType = "tracking_session_activated"
	TrackingSessionFinished  EventType = "tracking_session_finished"
)

// Event is self-contained: its future consumer need not read GPS history.
type Event struct {
	ID                         string    `json:"event_id"`
	Type                       EventType `json:"event_type"`
	TrackingID                 string    `json:"tracking_id"`
	TripID                     string    `json:"trip_id"`
	GuideID                    string    `json:"guide_id"`
	GeoRouteID                 string    `json:"geo_route_id"`
	MeetingPointID             string    `json:"meeting_point_id,omitempty"`
	MeetingPointOrder          int       `json:"meeting_point_order,omitempty"`
	Sequence                   int64     `json:"sequence"`
	OccurredAt                 time.Time `json:"occurred_at"`
	CapturedAt                 time.Time `json:"captured_at"`
	ObservedFrom               time.Time `json:"observed_from"`
	EvidenceCount              int       `json:"evidence_count"`
	GeometryVersion            *string   `json:"geometry_version,omitempty"`
	GeometryFingerprint        string    `json:"geometry_fingerprint"`
	SnapshotHash               string    `json:"snapshot_hash"`
	Longitude                  float64   `json:"longitude"`
	Latitude                   float64   `json:"latitude"`
	AccuracyMeters             float64   `json:"accuracy_meters"`
	RouteDistanceMeters        float64   `json:"route_distance_meters"`
	MeetingPointDistanceMeters *float64  `json:"meeting_point_distance_meters,omitempty"`
	EpisodeStartSequence       int64     `json:"episode_start_sequence,omitempty"`
}

// Lifecycle envelopes contain no invented GPS observation, coordinate or
// sequence. Geographic envelopes retain their existing JSON representation.
func (event Event) MarshalJSON() ([]byte, error) {
	if event.Type == TrackingSessionActivated || event.Type == TrackingSessionFinished {
		return json.Marshal(struct {
			ID                  string    `json:"event_id"`
			Type                EventType `json:"event_type"`
			TrackingID          string    `json:"tracking_id"`
			TripID              string    `json:"trip_id"`
			GuideID             string    `json:"guide_id"`
			GeoRouteID          string    `json:"geo_route_id"`
			OccurredAt          time.Time `json:"occurred_at"`
			GeometryVersion     *string   `json:"geometry_version,omitempty"`
			GeometryFingerprint string    `json:"geometry_fingerprint,omitempty"`
			SnapshotHash        string    `json:"snapshot_hash,omitempty"`
		}{event.ID, event.Type, event.TrackingID, event.TripID, event.GuideID, event.GeoRouteID, event.OccurredAt, event.GeometryVersion, event.GeometryFingerprint, event.SnapshotHash})
	}
	type raw Event
	return json.Marshal(raw(event))
}

func NewLifecycleEvent(session Session, kind EventType, at time.Time) (Event, error) {
	if kind != TrackingSessionActivated && kind != TrackingSessionFinished {
		return Event{}, ErrInvalid
	}
	event := Event{ID: eventID(session.ID, kind, "", 0), Type: kind, TrackingID: session.ID, TripID: session.TripID, GuideID: session.GuideID, GeoRouteID: session.RouteID, OccurredAt: at.UTC().Truncate(time.Millisecond), SnapshotHash: session.SnapshotHash}
	return event, event.Validate()
}

func (event Event) Validate() error {
	if event.Type == TrackingSessionActivated || event.Type == TrackingSessionFinished {
		if !validHash(event.ID) || event.ID != eventID(event.TrackingID, event.Type, "", 0) || !validID(event.TrackingID) || !validID(event.TripID) || !validID(event.GuideID) || !validID(event.GeoRouteID) || !validTime(event.OccurredAt) || event.Sequence != 0 || !event.CapturedAt.IsZero() || !event.ObservedFrom.IsZero() || event.MeetingPointID != "" || event.MeetingPointOrder != 0 || event.EvidenceCount != 0 || event.EpisodeStartSequence != 0 || event.Longitude != 0 || event.Latitude != 0 || event.AccuracyMeters != 0 || event.RouteDistanceMeters != 0 || event.MeetingPointDistanceMeters != nil || event.SnapshotHash != "" && !validHash(event.SnapshotHash) || event.GeometryFingerprint != "" && !validHash(event.GeometryFingerprint) {
			return ErrInvalid
		}
		if event.GeometryVersion != nil && strings.TrimSpace(*event.GeometryVersion) == "" {
			return ErrInvalid
		}
		return nil
	}
	if !validHash(event.ID) || !validHash(event.SnapshotHash) || !validHash(event.GeometryFingerprint) || !validID(event.TrackingID) || !validID(event.TripID) || !validID(event.GuideID) || !validID(event.GeoRouteID) || event.Sequence < 1 || !validTime(event.OccurredAt) || !validTime(event.CapturedAt) || !validTime(event.ObservedFrom) || event.ObservedFrom.After(event.CapturedAt) || event.EvidenceCount < 1 || !finiteNonnegative(event.AccuracyMeters) || !finiteNonnegative(event.RouteDistanceMeters) || event.Longitude < -180 || event.Longitude > 180 || event.Latitude < -90 || event.Latitude > 90 || event.GeometryVersion != nil && strings.TrimSpace(*event.GeometryVersion) == "" {
		return ErrInvalid
	}
	if (geo.Coordinate{Longitude: event.Longitude, Latitude: event.Latitude}).Validate() != nil {
		return ErrInvalid
	}
	if event.ID != eventID(event.TrackingID, event.Type, event.MeetingPointID, event.Sequence) {
		return ErrInvalid
	}
	switch event.Type {
	case FirstMeetingPointArrival, MeetingPointArrival:
		if !validID(event.MeetingPointID) || event.MeetingPointOrder < 1 || event.MeetingPointDistanceMeters == nil || !finiteNonnegative(*event.MeetingPointDistanceMeters) || event.EpisodeStartSequence != 0 {
			return ErrInvalid
		}
	case RouteDeviationStarted, RouteDeviationResolved:
		if event.MeetingPointID != "" || event.MeetingPointOrder != 0 || event.MeetingPointDistanceMeters != nil || event.EpisodeStartSequence < 1 || event.EpisodeStartSequence > event.Sequence {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

func eventID(sessionID string, kind EventType, pointID string, sequence int64) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("gps-event-v1\x00%s\x00%s\x00%s\x00%d", sessionID, kind, pointID, sequence)))
	return hex.EncodeToString(sum[:])
}

type TransitionPlan struct {
	ExpectedSequence int64           `json:"expected_sequence"`
	State            GeographicState `json:"state"`
	Events           []Event         `json:"events"`
	FirstArrival     *Arrival        `json:"first_arrival,omitempty"`
}

func (plan TransitionPlan) Validate(position Position) error {
	if position.Geographic == nil || plan.ExpectedSequence < 0 || plan.State.LastSequence != position.Sequence || plan.State.SnapshotHash != position.Geographic.SnapshotHash || len(plan.Events) > 2 || !plan.State.LastCapturedAt.Equal(position.CapturedAt) {
		return ErrInvalid
	}
	if !reflect.DeepEqual(plan.FirstArrival, position.FirstArrival) {
		return ErrInvalid
	}
	for _, event := range plan.Events {
		if err := event.Validate(); err != nil {
			return err
		}
		if event.ID != eventID(position.SessionID, event.Type, event.MeetingPointID, position.Sequence) || event.TrackingID != position.SessionID || event.Sequence != position.Sequence || event.SnapshotHash != plan.State.SnapshotHash || !event.CapturedAt.Equal(position.CapturedAt) || !event.OccurredAt.Equal(position.ReceivedAt) {
			return ErrInvalid
		}
	}
	return nil
}

func advanceEvidence(old *Evidence, at time.Time, contiguous, fresh bool) *Evidence {
	if !contiguous || old == nil {
		return &Evidence{Since: at, Count: 1}
	}
	if !fresh {
		return old
	}
	return &Evidence{Since: old.Since, Count: old.Count + 1}
}

// Advance is pure. Redis compares ExpectedSequence and persists the result
// with the observation; callers must recompute if another writer advanced.
func Advance(snapshot GeographicSnapshot, state GeographicState, session Session, position Position) (TransitionPlan, error) {
	if err := position.Validate(); err != nil {
		return TransitionPlan{}, err
	}
	if snapshot.TransitionPolicy == nil || position.Geographic == nil || position.Geographic.SnapshotHash != snapshot.SnapshotHash || session.SnapshotHash != snapshot.SnapshotHash || len(position.Geographic.MeetingPoints) != len(snapshot.Route.MeetingPoints) || state.SnapshotHash != snapshot.SnapshotHash || len(state.Points) != len(snapshot.Route.MeetingPoints) {
		return TransitionPlan{}, ErrInvalid
	}
	if position.Sequence <= state.LastSequence || (!state.LastCapturedAt.IsZero() && position.CapturedAt.Before(state.LastCapturedAt)) {
		return TransitionPlan{}, ErrStale
	}
	policy := *snapshot.TransitionPolicy
	if err := policy.Validate(); err != nil {
		return TransitionPlan{}, err
	}
	if position.AccuracyMeters == nil {
		return TransitionPlan{}, ErrInvalid
	}
	gapOK := state.LastCapturedAt.IsZero() || position.CapturedAt.Sub(state.LastCapturedAt) <= policy.MaxSampleGap
	freshTime := state.LastCapturedAt.IsZero() || position.CapturedAt.After(state.LastCapturedAt)
	next := state
	next.Points = append([]PointProgress(nil), state.Points...)
	next.LastSequence, next.LastCapturedAt = position.Sequence, position.CapturedAt
	plan := TransitionPlan{ExpectedSequence: state.LastSequence, State: next}
	makeEvent := func(kind EventType, pointID string, order int, distance *float64, evidence *Evidence, episode int64) Event {
		return Event{ID: eventID(position.SessionID, kind, pointID, position.Sequence), Type: kind, TrackingID: position.SessionID, TripID: session.TripID, GuideID: session.GuideID, GeoRouteID: session.RouteID, MeetingPointID: pointID, MeetingPointOrder: order, Sequence: position.Sequence, OccurredAt: position.ReceivedAt, CapturedAt: position.CapturedAt, ObservedFrom: evidence.Since, EvidenceCount: evidence.Count, GeometryVersion: snapshot.Route.GeometryVersion, GeometryFingerprint: snapshot.GeometryFingerprint, SnapshotHash: snapshot.SnapshotHash, Longitude: position.Longitude, Latitude: position.Latitude, AccuracyMeters: *position.AccuracyMeters, RouteDistanceMeters: *position.Geographic.RouteDistanceMeters, MeetingPointDistanceMeters: distance, EpisodeStartSequence: episode}
	}
	insideCount, insideIndex := 0, -1
	for i, point := range position.Geographic.MeetingPoints {
		if point.ID != snapshot.Route.MeetingPoints[i].ID || state.Points[i].ID != point.ID {
			return TransitionPlan{}, ErrInvalid
		}
		if point.Status == "inside" {
			insideCount++
			insideIndex = i
		}
	}
	for i, point := range position.Geographic.MeetingPoints {
		progress := &plan.State.Points[i]
		if progress.ConfirmedSequence != 0 {
			continue
		}
		if point.Status != "inside" || insideCount != 1 || insideIndex != i {
			progress.Candidate = nil
			continue
		}
		progress.Candidate = advanceEvidence(progress.Candidate, position.CapturedAt, gapOK, freshTime)
		if progress.Candidate.Count < policy.ArrivalObservations || position.CapturedAt.Sub(progress.Candidate.Since) < policy.ArrivalDwell {
			continue
		}
		progress.ConfirmedSequence = position.Sequence
		distance := point.DistanceMeters
		kind := MeetingPointArrival
		if i == 0 {
			kind = FirstMeetingPointArrival
			plan.FirstArrival = &Arrival{MeetingPointID: point.ID, CapturedAt: position.CapturedAt, Sequence: position.Sequence, ObservedFrom: &progress.Candidate.Since, DistanceMeters: &distance, AccuracyMeters: position.AccuracyMeters}
		}
		plan.Events = append(plan.Events, makeEvent(kind, point.ID, i+1, &distance, progress.Candidate, 0))
		progress.Candidate = nil
	}
	dev := &plan.State.Deviation
	if !gapOK {
		dev.Candidate = nil
	}
	switch position.Geographic.RouteStatus {
	case "outside":
		if dev.Active {
			dev.Candidate = nil
			break
		}
		dev.Candidate = advanceEvidence(dev.Candidate, position.CapturedAt, gapOK, freshTime)
		if dev.Candidate.Count >= policy.DeviationObservations && position.CapturedAt.Sub(dev.Candidate.Since) >= policy.DeviationDwell {
			dev.Active, dev.EpisodeStartSequence = true, position.Sequence
			plan.Events = append(plan.Events, makeEvent(RouteDeviationStarted, "", 0, nil, dev.Candidate, position.Sequence))
			dev.Candidate = nil
		}
	case "inside":
		if !dev.Active {
			dev.Candidate = nil
			break
		}
		dev.Candidate = advanceEvidence(dev.Candidate, position.CapturedAt, gapOK, freshTime)
		if dev.Candidate.Count >= policy.RecoveryObservations && position.CapturedAt.Sub(dev.Candidate.Since) >= policy.RecoveryDwell {
			plan.Events = append(plan.Events, makeEvent(RouteDeviationResolved, "", 0, nil, dev.Candidate, dev.EpisodeStartSequence))
			dev.Active, dev.EpisodeStartSequence, dev.Candidate = false, 0, nil
		}
	case "indeterminate":
		dev.Candidate = nil
	default:
		return TransitionPlan{}, ErrInvalid
	}
	return plan, nil
}
