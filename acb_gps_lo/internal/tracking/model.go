package tracking

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

var (
	ErrInvalid      = errors.New("invalid tracking data")
	ErrNotFound     = errors.New("tracking session not found")
	ErrConflict     = errors.New("observation sequence conflict")
	ErrStale        = errors.New("old or out-of-order observation")
	ErrClosed       = errors.New("tracking session closed")
	ErrPending      = errors.New("tracking write requires reconciliation")
	ErrUnavailable  = errors.New("tracking store unavailable")
	ErrUncertain    = errors.New("tracking write outcome uncertain")
	ErrUnauthorized = errors.New("tracking credential not authorized")
	ErrStateChanged = errors.New("geographic state advanced concurrently")
)

const MinRetention = 24 * time.Hour

type State string

const (
	Active   State = "active"
	Finished State = "finished"
)

// External identifiers are opaque to GPS. They are encoded only in Redis keys.
type Session struct {
	ID             string     `json:"id"`
	GuideID        string     `json:"guide_id"`
	RouteID        string     `json:"route_id"`
	TripID         string     `json:"trip_id"`
	State          State      `json:"state"`
	StartedAt      time.Time  `json:"started_at"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
	LastSequence   int64      `json:"last_sequence"`
	LastCapturedAt *time.Time `json:"last_captured_at,omitempty"`
	// LastPositionExpiresAt is internal evidence for read availability. It is
	// never included in the public session JSON.
	LastPositionExpiresAt *time.Time    `json:"-"`
	FirstArrival          *Arrival      `json:"first_arrival,omitempty"`
	MaxObservationAge     time.Duration `json:"max_observation_age"`
	FutureSkew            time.Duration `json:"future_skew"`
	SnapshotHash          string        `json:"snapshot_hash,omitempty"`
}

type Arrival struct {
	MeetingPointID string     `json:"meeting_point_id"`
	CapturedAt     time.Time  `json:"captured_at"`
	Sequence       int64      `json:"sequence"`
	ObservedFrom   *time.Time `json:"observed_from,omitempty"`
	DistanceMeters *float64   `json:"distance_meters,omitempty"`
	AccuracyMeters *float64   `json:"accuracy_meters,omitempty"`
}

func (a Arrival) Validate() error {
	if !validID(a.MeetingPointID) || a.Sequence < 1 || !validTime(a.CapturedAt) || a.ObservedFrom != nil && (!validTime(*a.ObservedFrom) || a.ObservedFrom.After(a.CapturedAt)) || a.DistanceMeters != nil && !finiteNonnegative(*a.DistanceMeters) || a.AccuracyMeters != nil && !finiteNonnegative(*a.AccuracyMeters) {
		return fmt.Errorf("%w: first arrival evidence", ErrInvalid)
	}
	return nil
}

type GeographicResult struct {
	SnapshotHash string `json:"snapshot_hash,omitempty"`
	RouteStatus  string `json:"route_status,omitempty"`
	RouteReason  string `json:"route_reason,omitempty"`
	// Values are supplied by a future geographic use case, never inferred here.
	RouteDistanceMeters        *float64          `json:"route_distance_meters,omitempty"`
	MeetingPoints              []PointEvaluation `json:"meeting_points,omitempty"`
	MeetingPointID             *string           `json:"meeting_point_id,omitempty"`
	MeetingPointDistanceMeters *float64          `json:"meeting_point_distance_meters,omitempty"`
}

type PointEvaluation struct {
	ID             string  `json:"id"`
	Status         string  `json:"status"`
	Reason         string  `json:"reason"`
	DistanceMeters float64 `json:"distance_meters"`
}

type Position struct {
	SessionID      string            `json:"session_id"`
	Sequence       int64             `json:"sequence"`
	CapturedAt     time.Time         `json:"captured_at"`
	ReceivedAt     time.Time         `json:"received_at"`
	Longitude      float64           `json:"longitude"`
	Latitude       float64           `json:"latitude"`
	AccuracyMeters *float64          `json:"accuracy_meters,omitempty"`
	Geographic     *GeographicResult `json:"geographic,omitempty"`
	FirstArrival   *Arrival          `json:"first_arrival,omitempty"`
	// Version 1 omits server ReceivedAt from the fingerprint, so HTTP retries
	// remain idempotent. Version 0 preserves older stored fingerprints.
	IdempotencyVersion int `json:"idempotency_version,omitempty"`
}

type Acceptance struct {
	Position  Position
	Duplicate bool
	ExpiresAt time.Time
}

type Page struct {
	Positions    []Position
	NextSequence int64
	HasMore      bool
}

func validID(id string) bool {
	if strings.TrimSpace(id) != id || id == "" || len(id) > 256 {
		return false
	}
	for _, r := range id {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}

func ValidID(id string) bool { return validID(id) }

func validTime(t time.Time) bool {
	return !t.IsZero() && t.Year() >= 2000 && t.Year() <= 9999 && t.Nanosecond()%int(time.Millisecond) == 0
}

func (s Session) ValidateNew() error {
	if !validID(s.ID) || !validID(s.GuideID) || !validID(s.RouteID) || !validID(s.TripID) || !validTime(s.StartedAt) || s.State != Active || s.LastSequence != 0 || s.LastCapturedAt != nil || s.LastPositionExpiresAt != nil || s.FinishedAt != nil || s.FirstArrival != nil {
		return fmt.Errorf("%w: new session", ErrInvalid)
	}
	if s.SnapshotHash != "" && !validHash(s.SnapshotHash) {
		return ErrInvalid
	}
	return nil
}

func validHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func (p Position) Validate() error {
	if !validID(p.SessionID) || p.Sequence < 1 || p.Sequence > (1<<53)-1 || !validTime(p.CapturedAt) || !validTime(p.ReceivedAt) || p.CapturedAt.After(p.ReceivedAt.Add(5*time.Minute)) || p.IdempotencyVersion < 0 || p.IdempotencyVersion > 2 || math.IsNaN(p.Longitude) || math.IsInf(p.Longitude, 0) || math.IsNaN(p.Latitude) || math.IsInf(p.Latitude, 0) || p.Longitude < -180 || p.Longitude > 180 || p.Latitude < -90 || p.Latitude > 90 {
		return fmt.Errorf("%w: position", ErrInvalid)
	}
	if p.AccuracyMeters != nil && (!finiteNonnegative(*p.AccuracyMeters)) {
		return fmt.Errorf("%w: accuracy", ErrInvalid)
	}
	if p.Geographic != nil {
		g := p.Geographic
		if p.IdempotencyVersion == 2 && (len(g.SnapshotHash) != 64 || (g.RouteStatus != "inside" && g.RouteStatus != "outside" && g.RouteStatus != "indeterminate") || g.RouteDistanceMeters == nil) {
			return fmt.Errorf("%w: geographic evaluation", ErrInvalid)
		}
		for _, point := range g.MeetingPoints {
			if !validID(point.ID) || (point.Status != "inside" && point.Status != "outside" && point.Status != "indeterminate") || !finiteNonnegative(point.DistanceMeters) {
				return fmt.Errorf("%w: meeting point evaluation", ErrInvalid)
			}
		}
		if g.RouteDistanceMeters != nil && !finiteNonnegative(*g.RouteDistanceMeters) || g.MeetingPointDistanceMeters != nil && !finiteNonnegative(*g.MeetingPointDistanceMeters) || g.MeetingPointID != nil && !validID(*g.MeetingPointID) || (g.MeetingPointDistanceMeters != nil) != (g.MeetingPointID != nil) {
			return fmt.Errorf("%w: geographic result", ErrInvalid)
		}
	}
	if p.IdempotencyVersion == 2 && p.Geographic == nil {
		return fmt.Errorf("%w: missing geographic result", ErrInvalid)
	}
	if p.FirstArrival != nil {
		a := p.FirstArrival
		if a.Validate() != nil || a.Sequence != p.Sequence || !a.CapturedAt.Equal(p.CapturedAt) || p.Geographic == nil {
			return fmt.Errorf("%w: first arrival", ErrInvalid)
		}
		if p.IdempotencyVersion == 2 {
			found := false
			for _, point := range p.Geographic.MeetingPoints {
				if point.ID == a.MeetingPointID && point.Status == "inside" {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("%w: first arrival evidence", ErrInvalid)
			}
		} else if p.Geographic.MeetingPointID == nil || *p.Geographic.MeetingPointID != a.MeetingPointID {
			return fmt.Errorf("%w: first arrival", ErrInvalid)
		}
	}
	return nil
}

// InputFingerprint excludes the server receipt time and derived geography.
// It permits a replay to retrieve the original result without recalculation.
func (p Position) InputFingerprint() (string, error) {
	if p.IdempotencyVersion != 2 {
		hash, _, err := p.Fingerprint()
		return hash, err
	}
	probe := p
	probe.IdempotencyVersion = 1
	probe.Geographic = nil
	probe.FirstArrival = nil
	if err := probe.Validate(); err != nil {
		return "", err
	}
	probe.IdempotencyVersion = 2
	probe.ReceivedAt = time.Time{}
	b, err := json.Marshal(probe)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func finiteNonnegative(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 }

// Fingerprint hashes canonical JSON after validation. Version 0 requires
// exact replay; version 1 excludes the server-generated ReceivedAt.
func (p Position) Fingerprint() (string, []byte, error) {
	if err := p.Validate(); err != nil {
		return "", nil, err
	}
	b, err := json.Marshal(p)
	if err != nil {
		return "", nil, err
	}
	if p.IdempotencyVersion == 1 || p.IdempotencyVersion == 2 {
		stable := p
		stable.ReceivedAt = time.Time{}
		if p.IdempotencyVersion == 2 {
			stable.Geographic = nil
			stable.FirstArrival = nil
		}
		b, err = json.Marshal(stable)
		if err != nil {
			return "", nil, err
		}
	}
	sum := sha256.Sum256(b)
	body, err := json.Marshal(p)
	return hex.EncodeToString(sum[:]), body, err
}

func Expiration(captured, received, redisNow time.Time, retention, margin time.Duration) (time.Time, error) {
	if !validTime(captured) || !validTime(received) || !validTime(redisNow) || captured.After(received.Add(5*time.Minute)) || retention < MinRetention || margin < 0 {
		return time.Time{}, fmt.Errorf("%w: retention or timestamps", ErrInvalid)
	}
	base := captured
	if received.After(base) {
		base = received
	}
	if redisNow.After(base) {
		base = redisNow
	}
	expiry := base.Add(retention).Add(margin)
	if expiry.Year() > 9999 || !expiry.After(base) {
		return time.Time{}, fmt.Errorf("%w: expiry overflow", ErrInvalid)
	}
	return expiry.UTC(), nil
}
