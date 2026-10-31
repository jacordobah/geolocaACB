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
	ErrInvalid     = errors.New("invalid tracking data")
	ErrNotFound    = errors.New("tracking session not found")
	ErrConflict    = errors.New("observation sequence conflict")
	ErrStale       = errors.New("old or out-of-order observation")
	ErrClosed      = errors.New("tracking session closed")
	ErrPending     = errors.New("tracking write requires reconciliation")
	ErrUnavailable = errors.New("tracking store unavailable")
	ErrUncertain   = errors.New("tracking write outcome uncertain")
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
	FirstArrival   *Arrival   `json:"first_arrival,omitempty"`
}

type Arrival struct {
	MeetingPointID string    `json:"meeting_point_id"`
	CapturedAt     time.Time `json:"captured_at"`
	Sequence       int64     `json:"sequence"`
}

type GeographicResult struct {
	// Values are supplied by a future geographic use case, never inferred here.
	RouteDistanceMeters        *float64 `json:"route_distance_meters,omitempty"`
	MeetingPointID             *string  `json:"meeting_point_id,omitempty"`
	MeetingPointDistanceMeters *float64 `json:"meeting_point_distance_meters,omitempty"`
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
	if !validID(s.ID) || !validID(s.GuideID) || !validID(s.RouteID) || !validID(s.TripID) || !validTime(s.StartedAt) || s.State != Active || s.LastSequence != 0 || s.LastCapturedAt != nil || s.FinishedAt != nil || s.FirstArrival != nil {
		return fmt.Errorf("%w: new session", ErrInvalid)
	}
	return nil
}

func (p Position) Validate() error {
	if !validID(p.SessionID) || p.Sequence < 1 || p.Sequence > 1<<53 || !validTime(p.CapturedAt) || !validTime(p.ReceivedAt) || p.CapturedAt.After(p.ReceivedAt.Add(5*time.Minute)) || math.IsNaN(p.Longitude) || math.IsInf(p.Longitude, 0) || math.IsNaN(p.Latitude) || math.IsInf(p.Latitude, 0) || p.Longitude < -180 || p.Longitude > 180 || p.Latitude < -90 || p.Latitude > 90 {
		return fmt.Errorf("%w: position", ErrInvalid)
	}
	if p.AccuracyMeters != nil && (!finiteNonnegative(*p.AccuracyMeters)) {
		return fmt.Errorf("%w: accuracy", ErrInvalid)
	}
	if p.Geographic != nil {
		g := p.Geographic
		if g.RouteDistanceMeters != nil && !finiteNonnegative(*g.RouteDistanceMeters) || g.MeetingPointDistanceMeters != nil && !finiteNonnegative(*g.MeetingPointDistanceMeters) || g.MeetingPointID != nil && !validID(*g.MeetingPointID) || (g.MeetingPointDistanceMeters != nil) != (g.MeetingPointID != nil) {
			return fmt.Errorf("%w: geographic result", ErrInvalid)
		}
	}
	if p.FirstArrival != nil && (!validID(p.FirstArrival.MeetingPointID) || p.FirstArrival.Sequence != p.Sequence || !p.FirstArrival.CapturedAt.Equal(p.CapturedAt) || p.Geographic == nil || p.Geographic.MeetingPointID == nil || *p.Geographic.MeetingPointID != p.FirstArrival.MeetingPointID) {
		return fmt.Errorf("%w: first arrival", ErrInvalid)
	}
	return nil
}

func finiteNonnegative(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 }

// Fingerprint hashes canonical JSON after validation. ReceivedAt is included:
// a retry must replay the original observation, not silently change its data.
func (p Position) Fingerprint() (string, []byte, error) {
	if err := p.Validate(); err != nil {
		return "", nil, err
	}
	b, err := json.Marshal(p)
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), b, nil
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
