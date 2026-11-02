package tracking

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/geo"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/routes"
)

// Repository is the smallest persistence contract used by tracking cases.
// Administrative callers will be wired after their authorization contract exists.
type Repository interface {
	Provision(context.Context, Session, [32]byte) error
	ProvisionSnapshot(context.Context, Session, [32]byte, GeographicSnapshot) error
	Snapshot(context.Context, string) (GeographicSnapshot, error)
	Credential(context.Context, string) (Session, [32]byte, error)
	Session(context.Context, string) (Session, error)
	FinishSession(context.Context, string, time.Time) error
	Accept(context.Context, Position) (Acceptance, error)
	ReadPosition(context.Context, string, int64) (Position, error)
	PositionExpiry(context.Context, string, int64) (time.Time, error)
	Latest(context.Context, string) (Position, error)
	History(context.Context, string, int64, int64) (Page, error)
}

type ObservationInput struct {
	Sequence       int64
	Latitude       float64
	Longitude      float64
	AccuracyMeters float64
	CapturedAt     time.Time
}

type Service struct {
	repo       Repository
	maxAge     time.Duration
	futureSkew time.Duration
	now        func() time.Time
	entropy    io.Reader
	prepared   sync.Map // tracking ID -> immutable snapshot and prepared route
}

type preparedSnapshot struct {
	snapshot GeographicSnapshot
	route    geo.PreparedRoute
}

func NewService(repo Repository, maxAge, futureSkew time.Duration, now func() time.Time, entropy io.Reader) (*Service, error) {
	if repo == nil || maxAge <= 0 || maxAge > 24*time.Hour || futureSkew < 0 || futureSkew > 5*time.Minute || maxAge%time.Millisecond != 0 || futureSkew%time.Millisecond != 0 {
		return nil, ErrInvalid
	}
	if now == nil {
		now = time.Now
	}
	if entropy == nil {
		entropy = rand.Reader
	}
	return &Service{repo: repo, maxAge: maxAge, futureSkew: futureSkew, now: now, entropy: entropy}, nil
}

// Provision is internal only. It creates a technical session, not a finding
// that the referenced route is geographically eligible for monitoring.
// The plaintext credential is returned only after confirmed persistence.
func (s *Service) Provision(ctx context.Context, guideID, tripID, routeID string) (Session, string, error) {
	return s.issue(ctx, guideID, tripID, routeID, nil)
}

// ProvisionGeographic is internal only. Python's present response can be
// queried and assessed, but cannot yet prove operational eligibility.
func (s *Service) ProvisionGeographic(ctx context.Context, reader routes.Reader, guideID, tripID, routeID string, policy GeographicPolicy) (Session, string, error) {
	if reader == nil || !ValidID(routeID) {
		return Session{}, "", ErrInvalid
	}
	route, err := reader.Get(ctx, routeID)
	if err != nil {
		return Session{}, "", err
	}
	if route.ID != routeID {
		return Session{}, "", ErrRouteIneligible
	}
	snapshot, err := BuildSnapshot(route, policy)
	if err != nil {
		return Session{}, "", err
	}
	if err := snapshot.OperationalReady(); err != nil {
		return Session{}, "", err
	}
	return s.issue(ctx, guideID, tripID, routeID, &snapshot)
}

func (s *Service) issue(ctx context.Context, guideID, tripID, routeID string, snapshot *GeographicSnapshot) (Session, string, error) {
	identifier := make([]byte, 24)
	credential := make([]byte, 32)
	if _, err := io.ReadFull(s.entropy, identifier); err != nil {
		return Session{}, "", fmt.Errorf("generate session id: %w", err)
	}
	if _, err := io.ReadFull(s.entropy, credential); err != nil {
		return Session{}, "", fmt.Errorf("generate credential: %w", err)
	}
	session := Session{ID: base64.RawURLEncoding.EncodeToString(identifier), GuideID: guideID, TripID: tripID, RouteID: routeID, State: Active, StartedAt: s.now().UTC().Truncate(time.Millisecond), MaxObservationAge: s.maxAge, FutureSkew: s.futureSkew}
	if snapshot != nil {
		session.SnapshotHash = snapshot.SnapshotHash
	}
	if err := session.ValidateNew(); err != nil {
		return Session{}, "", err
	}
	digest := sha256.Sum256(credential)
	provision := func() error {
		if snapshot != nil {
			return s.repo.ProvisionSnapshot(ctx, session, digest, *snapshot)
		}
		return s.repo.Provision(ctx, session, digest)
	}
	err := provision()
	if errors.Is(err, ErrUncertain) && ctx.Err() == nil {
		// Reconcile once with the same ID and digest. Repository creation is
		// idempotent; a second confirmed result can safely release the token.
		err = provision()
	}
	if err != nil {
		return Session{}, "", err
	}
	return session, base64.RawURLEncoding.EncodeToString(credential), nil
}

func (s *Service) Status(ctx context.Context, id string) (Session, error) {
	return s.repo.Session(ctx, id)
}
func (s *Service) RequireActive(ctx context.Context, id string) (Session, error) {
	session, err := s.repo.Session(ctx, id)
	if err != nil {
		return Session{}, err
	}
	if session.State != Active {
		return Session{}, ErrClosed
	}
	if session.SnapshotHash != "" {
		snapshot, err := s.repo.Snapshot(ctx, id)
		if err != nil {
			return Session{}, err
		}
		if snapshot.SnapshotHash != session.SnapshotHash {
			return Session{}, ErrUncertain
		}
	}
	return session, nil
}
func (s *Service) Finish(ctx context.Context, id string) error {
	err := s.repo.FinishSession(ctx, id, s.now().UTC().Truncate(time.Millisecond))
	if err == nil {
		s.prepared.Delete(id)
	}
	return err
}
func (s *Service) Latest(ctx context.Context, id string) (Position, error) {
	return s.repo.Latest(ctx, id)
}
func (s *Service) History(ctx context.Context, id string, after, limit int64) (Page, error) {
	return s.repo.History(ctx, id, after, limit)
}

func (s *Service) Authenticate(ctx context.Context, id, token string) (Session, error) {
	if !ValidID(id) {
		return Session{}, ErrUnauthorized
	}
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(decoded) != 32 {
		return Session{}, ErrUnauthorized
	}
	session, digest, err := s.repo.Credential(ctx, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrClosed) || errors.Is(err, ErrInvalid) {
			return Session{}, ErrUnauthorized
		}
		return Session{}, err
	}
	provided := sha256.Sum256(decoded)
	if subtle.ConstantTimeCompare(provided[:], digest[:]) != 1 {
		return Session{}, ErrUnauthorized
	}
	return session, nil
}

func (s *Service) Receive(ctx context.Context, id, token string, input ObservationInput) (Acceptance, error) {
	session, err := s.Authenticate(ctx, id, token)
	if err != nil {
		return Acceptance{}, err
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	accuracy := input.AccuracyMeters
	position := Position{SessionID: id, Sequence: input.Sequence, CapturedAt: input.CapturedAt.UTC(), ReceivedAt: now, Longitude: input.Longitude, Latitude: input.Latitude, AccuracyMeters: &accuracy, IdempotencyVersion: 1}
	if session.SnapshotHash != "" {
		position.IdempotencyVersion = 2
	}
	// Validate the client-supplied fields before loading or evaluating a route.
	if position.IdempotencyVersion == 2 {
		probe := position
		probe.IdempotencyVersion = 1
		if err := probe.Validate(); err != nil {
			return Acceptance{}, err
		}
	} else if err := position.Validate(); err != nil {
		return Acceptance{}, err
	}
	// A geographic replay always returns its stored evaluation. This also
	// prevents a later change in implementation from rewriting past results.
	if session.SnapshotHash != "" {
		stored, err := s.repo.ReadPosition(ctx, id, input.Sequence)
		if err == nil {
			originalHash, hashErr := stored.InputFingerprint()
			retryHash, retryErr := position.InputFingerprint()
			if hashErr != nil || retryErr != nil {
				return Acceptance{}, ErrUncertain
			}
			if originalHash != retryHash {
				return Acceptance{}, ErrConflict
			}
			expiry, err := s.repo.PositionExpiry(ctx, id, input.Sequence)
			if err != nil {
				return Acceptance{}, ErrUncertain
			}
			return Acceptance{Position: stored, Duplicate: true, ExpiresAt: expiry}, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return Acceptance{}, err
		}
	}
	if position.CapturedAt.Before(now.Add(-session.MaxObservationAge)) || position.CapturedAt.After(now.Add(session.FutureSkew)) {
		// An already accepted observation may be replayed after it becomes old.
		stored, err := s.repo.ReadPosition(ctx, id, input.Sequence)
		if err == nil {
			originalHash, a := stored.InputFingerprint()
			retryHash, b := position.InputFingerprint()
			if a == nil && b == nil && originalHash == retryHash {
				expiry, expiryErr := s.repo.PositionExpiry(ctx, id, input.Sequence)
				if expiryErr != nil {
					return Acceptance{}, fmt.Errorf("%w: replay expiry unavailable", ErrUncertain)
				}
				return Acceptance{Position: stored, Duplicate: true, ExpiresAt: expiry}, nil
			}
			return Acceptance{}, ErrConflict
		}
		if !errors.Is(err, ErrNotFound) {
			return Acceptance{}, err
		}
		return Acceptance{}, ErrStale
	}
	if session.SnapshotHash != "" {
		cached, ok := s.prepared.Load(id)
		if !ok {
			snapshot, err := s.repo.Snapshot(ctx, id)
			if err != nil {
				return Acceptance{}, err
			}
			if snapshot.SnapshotHash != session.SnapshotHash {
				return Acceptance{}, ErrUncertain
			}
			prepared, err := geo.PrepareRoute(snapshot.Route.Geometry)
			if err != nil {
				return Acceptance{}, fmt.Errorf("%w: %v", ErrInvalidGeometry, err)
			}
			cached, _ = s.prepared.LoadOrStore(id, preparedSnapshot{snapshot: snapshot, route: prepared})
		}
		value := cached.(preparedSnapshot)
		if value.snapshot.SnapshotHash != session.SnapshotHash {
			return Acceptance{}, ErrUncertain
		}
		position.Geographic, err = value.snapshot.evaluatePrepared(position, value.route)
		if err != nil {
			return Acceptance{}, err
		}
	}
	return s.repo.Accept(ctx, position)
}
