package control

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/routes"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/tracking"
)

const Audience = "acb-gps-control"
const (
	CreateCapability = "tracking:create"
	ReadCapability   = "tracking:read"
	FinishCapability = "tracking:finish"
)

var ErrForbidden = errors.New("control capability or scope denied")
var ErrUnauthenticated = errors.New("control identity unverified")
var ErrNoObservations = errors.New("tracking session has no observations")
var ErrPositionExpired = errors.New("latest tracking position expired")

// A Verifier must establish identity from a cryptographically verified service
// credential. HTTP identity headers are never used as authority.
type Verifier interface {
	Verify(context.Context, string) (Principal, error)
}

type Principal struct {
	Subject      string
	Audience     string
	NotBefore    time.Time
	ExpiresAt    time.Time
	Capabilities map[string]bool
	TripIDs      map[string]bool
	TrackingIDs  map[string]bool
}

func (p Principal) Allows(now time.Time, capability, tripID, trackingID string) bool {
	if !tracking.ValidID(p.Subject) || p.Audience != Audience || now.Before(p.NotBefore) || !now.Before(p.ExpiresAt) || !p.Capabilities[capability] {
		return false
	}
	if trackingID != "" {
		return p.TrackingIDs[trackingID] || (tripID != "" && p.TripIDs[tripID])
	}
	if tripID != "" && !p.TripIDs[tripID] {
		return false
	}
	return true
}

type CreateRequest struct {
	TripID     string `json:"trip_id"`
	GuideID    string `json:"guide_id"`
	GeoRouteID string `json:"geo_route_id"`
	PolicyID   string `json:"policy_id"`
}

func (r CreateRequest) Validate() error {
	if !tracking.ValidID(r.TripID) || !tracking.ValidID(r.GuideID) || !tracking.ValidID(r.GeoRouteID) || !tracking.ValidID(r.PolicyID) {
		return tracking.ErrInvalid
	}
	return nil
}

type Policy struct {
	Geographic  tracking.GeographicPolicy
	Transitions *tracking.TransitionPolicy
	Reference   tracking.PolicyReference
}
type Policies interface {
	Resolve(context.Context, string) (Policy, error)
}

// Reservation contains no plaintext credential. The snapshot is frozen before
// the first session write, so a later Python edit cannot reinterpret a retry.
type Reservation struct {
	Fingerprint string                      `json:"fingerprint"`
	PolicyID    string                      `json:"policy_id"`
	Session     tracking.Session            `json:"session"`
	Snapshot    tracking.GeographicSnapshot `json:"snapshot"`
	IssuedUntil time.Time                   `json:"issued_until"`
}
type Reservations interface {
	Lookup(context.Context, string, string) (Reservation, bool, error)
	Reserve(context.Context, string, string, Reservation) (Reservation, bool, error)
}

type Tracking interface {
	ProvisionPrepared(context.Context, tracking.Session, []byte, *tracking.GeographicSnapshot) error
	Status(context.Context, string) (tracking.Session, error)
	Finish(context.Context, string) error
	Latest(context.Context, string) (tracking.Position, error)
	History(context.Context, string, int64, int64) (tracking.Page, error)
}

type Service struct {
	tracker      Tracking
	routes       routes.Reader
	policies     Policies
	reservations Reservations
	secret       []byte
	window       time.Duration
	now          func() time.Time
	entropy      io.Reader
}

func New(tracker Tracking, reader routes.Reader, policies Policies, reservations Reservations, secret []byte, window time.Duration, now func() time.Time) (*Service, error) {
	if tracker == nil || reader == nil || policies == nil || reservations == nil || len(secret) < 32 || window <= 0 || window > time.Hour {
		return nil, tracking.ErrInvalid
	}
	if now == nil {
		now = time.Now
	}
	return &Service{tracker: tracker, routes: reader, policies: policies, reservations: reservations, secret: append([]byte(nil), secret...), window: window, now: now, entropy: rand.Reader}, nil
}

type Created struct {
	Session          tracking.Session `json:"session"`
	Credential       string           `json:"observation_credential,omitempty"`
	CredentialStatus string           `json:"credential_status"`
	Replayed         bool             `json:"-"`
}

func (s *Service) Create(ctx context.Context, p Principal, key string, request CreateRequest) (Created, error) {
	if err := request.Validate(); err != nil || !tracking.ValidID(key) {
		return Created{}, tracking.ErrInvalid
	}
	if !p.Allows(s.now(), CreateCapability, request.TripID, "") {
		return Created{}, ErrForbidden
	}
	body, _ := json.Marshal(request)
	fingerprint := sha256.Sum256(body)
	owner := sha256.Sum256([]byte(p.Audience + "\x00" + p.Subject))
	ownerKey := hex.EncodeToString(owner[:])
	reserved, replayed, err := s.reservations.Lookup(ctx, ownerKey, key)
	if err != nil {
		return Created{}, err
	}
	if !replayed {
		route, err := s.routes.Get(ctx, request.GeoRouteID)
		if err != nil {
			return Created{}, err
		}
		if route.ID != request.GeoRouteID {
			return Created{}, tracking.ErrRouteIneligible
		}
		policy, err := s.policies.Resolve(ctx, request.PolicyID)
		if err != nil {
			return Created{}, err
		}
		if policy.Transitions == nil || policy.Reference.PolicyID != request.PolicyID {
			return Created{}, tracking.ErrInvalid
		}
		snapshot, err := tracking.BuildSnapshotWithPolicy(route, policy.Geographic, *policy.Transitions, policy.Reference)
		if err != nil {
			return Created{}, err
		}
		if err = snapshot.OperationalReady(); err != nil {
			return Created{}, err
		}
		identifier := make([]byte, 24)
		if _, err = io.ReadFull(s.entropy, identifier); err != nil {
			return Created{}, err
		}
		now := s.now().UTC().Truncate(time.Millisecond)
		session := tracking.Session{ID: base64.RawURLEncoding.EncodeToString(identifier), TripID: request.TripID, GuideID: request.GuideID, RouteID: request.GeoRouteID, State: tracking.Active, StartedAt: now, SnapshotHash: snapshot.SnapshotHash}
		if limits, ok := s.tracker.(interface {
			ObservationLimits() (time.Duration, time.Duration)
		}); ok {
			session.MaxObservationAge, session.FutureSkew = limits.ObservationLimits()
		} else {
			return Created{}, tracking.ErrInvalid
		}
		initial := Reservation{Fingerprint: hex.EncodeToString(fingerprint[:]), PolicyID: request.PolicyID, Session: session, Snapshot: snapshot, IssuedUntil: now.Add(s.window)}
		reserved, replayed, err = s.reservations.Reserve(ctx, ownerKey, key, initial)
		if err != nil {
			return Created{}, err
		}
	}
	if reserved.Fingerprint != hex.EncodeToString(fingerprint[:]) || reserved.PolicyID != request.PolicyID || reserved.Session.TripID != request.TripID || reserved.Session.GuideID != request.GuideID || reserved.Session.RouteID != request.GeoRouteID {
		return Created{}, tracking.ErrConflict
	}
	if err := reserved.Snapshot.OperationalReady(); err != nil || reserved.Session.SnapshotHash != reserved.Snapshot.SnapshotHash {
		return Created{}, tracking.ErrUncertain
	}
	material := s.credential(reserved.Session.ID)
	status, err := s.tracker.Status(ctx, reserved.Session.ID)
	if err == nil && status.State == tracking.Finished {
		return Created{Session: status, CredentialStatus: "revoked", Replayed: true}, nil
	}
	if err != nil && !errors.Is(err, tracking.ErrNotFound) && !errors.Is(err, tracking.ErrUnavailable) {
		return Created{}, err
	}
	if err = s.tracker.ProvisionPrepared(ctx, reserved.Session, material[:], &reserved.Snapshot); err != nil {
		return Created{}, err
	}
	status, err = s.tracker.Status(ctx, reserved.Session.ID)
	if err != nil || status.ID != reserved.Session.ID || status.SnapshotHash != reserved.Snapshot.SnapshotHash || status.State != tracking.Active {
		return Created{}, tracking.ErrUncertain
	}
	result := Created{Session: status, CredentialStatus: "reissue_required", Replayed: replayed}
	if s.now().Before(reserved.IssuedUntil) {
		result.Credential = base64.RawURLEncoding.EncodeToString(material[:])
		result.CredentialStatus = "issued"
	}
	return result, nil
}

func (s *Service) credential(id string) [32]byte {
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write([]byte("gps-observation-v1\x00" + id))
	var out [32]byte
	copy(out[:], mac.Sum(nil))
	return out
}

func (s *Service) Status(ctx context.Context, p Principal, id string) (tracking.Session, error) {
	if !tracking.ValidID(id) {
		return tracking.Session{}, tracking.ErrInvalid
	}
	if !p.Allows(s.now(), ReadCapability, "", "") {
		return tracking.Session{}, ErrForbidden
	}
	session, err := s.tracker.Status(ctx, id)
	if err != nil {
		return tracking.Session{}, err
	}
	if !p.Allows(s.now(), ReadCapability, session.TripID, id) {
		return tracking.Session{}, tracking.ErrNotFound
	}
	return session, nil
}

// Latest authorizes against the session's persisted trip before reading GPS.
// An expected expiry differs from an unexpectedly missing live position.
func (s *Service) Latest(ctx context.Context, p Principal, id string) (tracking.Session, tracking.Position, error) {
	session, err := s.Status(ctx, p, id)
	if err != nil {
		return tracking.Session{}, tracking.Position{}, err
	}
	if session.LastSequence == 0 {
		return session, tracking.Position{}, ErrNoObservations
	}
	position, err := s.tracker.Latest(ctx, id)
	if errors.Is(err, tracking.ErrNotFound) {
		if session.LastPositionExpiresAt != nil && !s.now().Before(*session.LastPositionExpiresAt) {
			return session, tracking.Position{}, ErrPositionExpired
		}
		return session, tracking.Position{}, tracking.ErrUnavailable
	}
	if err != nil {
		return session, tracking.Position{}, err
	}
	return session, position, nil
}

func (s *Service) History(ctx context.Context, p Principal, id string, after, limit int64) (tracking.Session, tracking.Page, error) {
	if after < 0 || limit < 1 || limit > 100 {
		return tracking.Session{}, tracking.Page{}, tracking.ErrInvalid
	}
	session, err := s.Status(ctx, p, id)
	if err != nil {
		return tracking.Session{}, tracking.Page{}, err
	}
	page, err := s.tracker.History(ctx, id, after, limit)
	return session, page, err
}

func (s *Service) Finish(ctx context.Context, p Principal, id string) (tracking.Session, error) {
	if !tracking.ValidID(id) {
		return tracking.Session{}, tracking.ErrInvalid
	}
	if !p.Allows(s.now(), FinishCapability, "", "") {
		return tracking.Session{}, ErrForbidden
	}
	session, err := s.tracker.Status(ctx, id)
	if err != nil {
		return tracking.Session{}, err
	}
	if !p.Allows(s.now(), FinishCapability, session.TripID, id) {
		return tracking.Session{}, tracking.ErrNotFound
	}
	if err = s.tracker.Finish(ctx, id); err != nil {
		return tracking.Session{}, err
	}
	return s.tracker.Status(ctx, id)
}
