package tracking

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func fixture() (Session, Position) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	s := Session{ID: "sesión 1", GuideID: "guide:opaque", RouteID: "route/a", TripID: "trip/1", State: Active, StartedAt: now}
	p := Position{SessionID: s.ID, Sequence: 1, CapturedAt: now, ReceivedAt: now.Add(time.Second), Longitude: -74.1, Latitude: 4.6}
	return s, p
}

func TestModelValidationAndSerialization(t *testing.T) {
	s, p := fixture()
	if err := s.ValidateNew(); err != nil {
		t.Fatal(err)
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, v := range []any{s, p} {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(b, &decoded); err != nil {
			t.Fatal(err)
		}
		if len(decoded) == 0 {
			t.Fatal("empty serialization")
		}
	}
	a, b, err := p.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	c, d, err := p.Fingerprint()
	if err != nil || a != c || string(b) != string(d) {
		t.Fatal("unstable fingerprint")
	}
	p.Sequence = 0
	if !errors.Is(p.Validate(), ErrInvalid) {
		t.Fatal("zero sequence accepted")
	}
	p.Sequence = 1 << 53
	if !errors.Is(p.Validate(), ErrInvalid) {
		t.Fatal("sequence beyond exact Redis/Lua integer range accepted")
	}
	p.Sequence = 1
	p.SessionID = " bad "
	if !errors.Is(p.Validate(), ErrInvalid) {
		t.Fatal("invalid identifier accepted")
	}
	p.SessionID = s.ID
	p.CapturedAt = p.CapturedAt.Add(time.Nanosecond)
	if !errors.Is(p.Validate(), ErrInvalid) {
		t.Fatal("submillisecond capture accepted")
	}
}

func TestExpiration(t *testing.T) {
	_, p := fixture()
	now := p.ReceivedAt.Add(time.Hour)
	expires, err := Expiration(p.CapturedAt, p.ReceivedAt, now, MinRetention, 5*time.Minute)
	if err != nil || !expires.Equal(now.Add(24*time.Hour+5*time.Minute)) {
		t.Fatalf("expiry %v %v", expires, err)
	}
	if _, err := Expiration(p.CapturedAt, p.ReceivedAt, now, 23*time.Hour, 0); !errors.Is(err, ErrInvalid) {
		t.Fatal("short retention allowed")
	}
	if _, err := Expiration(p.CapturedAt, p.ReceivedAt, now, MinRetention, -time.Second); !errors.Is(err, ErrInvalid) {
		t.Fatal("negative margin allowed")
	}
	if _, err := Expiration(p.ReceivedAt.Add(6*time.Minute), p.ReceivedAt, now, MinRetention, 0); !errors.Is(err, ErrInvalid) {
		t.Fatal("clock skew allowed")
	}
}

func TestArrivalReference(t *testing.T) {
	_, p := fixture()
	id := "PE1"
	distance := 1.0
	p.Geographic = &GeographicResult{MeetingPointID: &id, MeetingPointDistanceMeters: &distance}
	p.FirstArrival = &Arrival{MeetingPointID: id, CapturedAt: p.CapturedAt, Sequence: p.Sequence}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	p.FirstArrival.Sequence = 2
	if !errors.Is(p.Validate(), ErrInvalid) {
		t.Fatal("inconsistent arrival accepted")
	}
}
