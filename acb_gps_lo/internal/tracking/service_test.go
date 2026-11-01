package tracking

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"
)

type fakeRepository struct {
	session   Session
	digest    [32]byte
	position  Position
	expiry    time.Time
	writes    int
	acceptErr error
}

func (f *fakeRepository) Provision(_ context.Context, s Session, d [32]byte) error {
	f.session = s
	f.digest = d
	return nil
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
