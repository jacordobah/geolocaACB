package redisstore

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/tracking"
	"github.com/redis/go-redis/v9"
)

type Store struct {
	client    *redis.Client
	retention time.Duration
	margin    time.Duration
}

func New(client *redis.Client, retention, safetyMargin time.Duration) (*Store, error) {
	if client == nil || retention < tracking.MinRetention || safetyMargin < 0 || retention > 365*24*time.Hour || safetyMargin > 24*time.Hour {
		return nil, fmt.Errorf("%w: Redis store configuration", tracking.ErrInvalid)
	}
	return &Store{client: client, retention: retention, margin: safetyMargin}, nil
}

func keyID(id string) string      { return base64.RawURLEncoding.EncodeToString([]byte(id)) }
func sessionKey(id string) string { return "gps:session:" + keyID(id) }
func positionKey(id string, seq int64) string {
	return "gps:position:" + keyID(id) + ":" + strconv.FormatInt(seq, 10)
}
func historyKey(id string) string { return "gps:history:" + keyID(id) }

const activeKey = "gps:active:sessions"

func ms(t time.Time) string { return strconv.FormatInt(t.UTC().UnixMilli(), 10) }
func fromMS(raw string) (time.Time, error) {
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	return time.UnixMilli(v).UTC(), nil
}
func storeErr(err error) error { return fmt.Errorf("%w: %v", tracking.ErrUnavailable, err) }

func (s *Store) CreateSession(ctx context.Context, session tracking.Session) error {
	if err := session.ValidateNew(); err != nil {
		return err
	}
	result, err := createScript.Run(ctx, s.client, []string{sessionKey(session.ID), activeKey}, session.ID, session.GuideID, session.RouteID, session.TripID, ms(session.StartedAt), keyID(session.ID), "", "0", "0").Text()
	if err != nil {
		return fmt.Errorf("%w: create session: %v", tracking.ErrUncertain, err)
	}
	switch result {
	case "CREATED":
		return nil
	case "CONFLICT":
		return tracking.ErrConflict
	default:
		return fmt.Errorf("%w: create session %s", tracking.ErrUnavailable, result)
	}
}

// Provision stores only a SHA-256 credential digest in the session hash.
// The caller owns token generation and must return it only on confirmed creation.
func (s *Store) Provision(ctx context.Context, session tracking.Session, digest [32]byte) error {
	if err := session.ValidateNew(); err != nil {
		return err
	}
	if session.MaxObservationAge <= 0 || session.MaxObservationAge > 24*time.Hour || session.FutureSkew < 0 || session.FutureSkew > 5*time.Minute || session.MaxObservationAge%time.Millisecond != 0 || session.FutureSkew%time.Millisecond != 0 {
		return tracking.ErrInvalid
	}
	result, err := createScript.Run(ctx, s.client, []string{sessionKey(session.ID), activeKey}, session.ID, session.GuideID, session.RouteID, session.TripID, ms(session.StartedAt), keyID(session.ID), hex.EncodeToString(digest[:]), session.MaxObservationAge.Milliseconds(), session.FutureSkew.Milliseconds()).Text()
	if err != nil {
		return fmt.Errorf("%w: provision session: %v", tracking.ErrUncertain, err)
	}
	switch result {
	case "CREATED":
		return nil
	case "CONFLICT":
		return tracking.ErrConflict
	case "INVALID":
		return tracking.ErrInvalid
	default:
		return fmt.Errorf("%w: provision state %s", tracking.ErrUnavailable, result)
	}
}

// Credential loads the session policy and digest for an internal verifier.
// Missing, closed and legacy sessions deliberately have no usable credential.
func (s *Store) Credential(ctx context.Context, id string) (tracking.Session, [32]byte, error) {
	var digest [32]byte
	if !validID(id) {
		return tracking.Session{}, digest, tracking.ErrInvalid
	}
	fields, err := s.client.HGetAll(ctx, sessionKey(id)).Result()
	if err != nil {
		return tracking.Session{}, digest, storeErr(err)
	}
	if len(fields) == 0 {
		return tracking.Session{}, digest, tracking.ErrNotFound
	}
	session, err := decodeSession(fields)
	if err != nil {
		return tracking.Session{}, digest, err
	}
	if session.ID != id || session.State != tracking.Active || fields["credential_hash"] == "" {
		return tracking.Session{}, digest, tracking.ErrNotFound
	}
	if session.MaxObservationAge <= 0 || session.MaxObservationAge > 24*time.Hour || session.FutureSkew < 0 || session.FutureSkew > 5*time.Minute {
		return tracking.Session{}, digest, tracking.ErrUnavailable
	}
	b, err := hex.DecodeString(fields["credential_hash"])
	if err != nil || len(b) != 32 {
		return tracking.Session{}, digest, tracking.ErrUnavailable
	}
	copy(digest[:], b)
	return session, digest, nil
}

func (s *Store) Session(ctx context.Context, id string) (tracking.Session, error) {
	if !validID(id) {
		return tracking.Session{}, tracking.ErrInvalid
	}
	fields, err := s.client.HGetAll(ctx, sessionKey(id)).Result()
	if err != nil {
		return tracking.Session{}, storeErr(err)
	}
	if len(fields) == 0 {
		return tracking.Session{}, tracking.ErrNotFound
	}
	result, err := decodeSession(fields)
	if err == nil && result.ID != id {
		return tracking.Session{}, fmt.Errorf("%w: session key mismatch", tracking.ErrUnavailable)
	}
	return result, err
}

func validID(id string) bool { return tracking.ValidID(id) }

func decodeSession(fields map[string]string) (tracking.Session, error) {
	var result tracking.Session
	result.ID, result.GuideID, result.RouteID, result.TripID = fields["id"], fields["guide_id"], fields["route_id"], fields["trip_id"]
	result.State = tracking.State(fields["state"])
	var err error
	result.StartedAt, err = fromMS(fields["started_ms"])
	if err != nil {
		return result, fmt.Errorf("%w: session started_at", tracking.ErrUnavailable)
	}
	result.LastSequence, err = strconv.ParseInt(fields["last_sequence"], 10, 64)
	if err != nil || result.ID == "" || (result.State != tracking.Active && result.State != tracking.Finished) {
		return result, fmt.Errorf("%w: session fields", tracking.ErrUnavailable)
	}
	if raw := fields["last_captured_ms"]; raw != "" {
		t, err := fromMS(raw)
		if err != nil {
			return result, storeErr(err)
		}
		result.LastCapturedAt = &t
	}
	if raw := fields["finished_ms"]; raw != "" {
		t, err := fromMS(raw)
		if err != nil {
			return result, storeErr(err)
		}
		result.FinishedAt = &t
	}
	if raw := fields["first_arrival"]; raw != "" {
		var arrival tracking.Arrival
		if err := json.Unmarshal([]byte(raw), &arrival); err != nil {
			return result, storeErr(err)
		}
		result.FirstArrival = &arrival
	}
	if raw := fields["max_age_ms"]; raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value <= 0 {
			return result, tracking.ErrUnavailable
		}
		result.MaxObservationAge = time.Duration(value) * time.Millisecond
	}
	if raw := fields["future_skew_ms"]; raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 0 {
			return result, tracking.ErrUnavailable
		}
		result.FutureSkew = time.Duration(value) * time.Millisecond
	}
	return result, nil
}

type Acceptance = tracking.Acceptance

func (s *Store) Accept(ctx context.Context, position tracking.Position) (Acceptance, error) {
	fingerprint, body, err := position.Fingerprint()
	if err != nil {
		return Acceptance{}, err
	}
	// Verify absolute-expiry arithmetic before allowing the script to write.
	if _, err := tracking.Expiration(position.CapturedAt, position.ReceivedAt, time.Now().UTC().Truncate(time.Millisecond), s.retention, s.margin); err != nil {
		return Acceptance{}, err
	}
	arrival := ""
	if position.FirstArrival != nil {
		b, err := json.Marshal(position.FirstArrival)
		if err != nil {
			return Acceptance{}, err
		}
		arrival = string(b)
	}
	keys := []string{sessionKey(position.SessionID), positionKey(position.SessionID, position.Sequence), historyKey(position.SessionID), activeKey}
	result, err := acceptScript.Run(ctx, s.client, keys, position.Sequence, ms(position.CapturedAt), ms(position.ReceivedAt), s.retention.Milliseconds(), s.margin.Milliseconds(), fingerprint, fingerprint+"|"+string(body), arrival).Text()
	if err != nil {
		// A broken connection may hide a committed write. Never report success
		// unless all three durable Redis structures agree and no repair is pending.
		if ctx.Err() == nil {
			confirmed, checkErr := s.confirm(ctx, position, fingerprint)
			if checkErr == nil && confirmed {
				stored, readErr := s.ReadPosition(ctx, position.SessionID, position.Sequence)
				if readErr == nil {
					expiry, expiryErr := s.PositionExpiry(ctx, position.SessionID, position.Sequence)
					if expiryErr == nil {
						return Acceptance{Position: stored, Duplicate: true, ExpiresAt: expiry}, nil
					}
				}
			}
		}
		return Acceptance{}, fmt.Errorf("%w: accept observation: %v", tracking.ErrUncertain, err)
	}
	switch result {
	case "ACCEPTED":
		expiry, err := s.PositionExpiry(ctx, position.SessionID, position.Sequence)
		if err != nil {
			return Acceptance{}, fmt.Errorf("%w: position expiry cannot be verified", tracking.ErrUncertain)
		}
		return Acceptance{Position: position, ExpiresAt: expiry}, nil
	case "DUPLICATE":
		stored, err := s.position(ctx, position.SessionID, position.Sequence)
		if err != nil {
			return Acceptance{}, err
		}
		expiry, err := s.PositionExpiry(ctx, position.SessionID, position.Sequence)
		if err != nil {
			return Acceptance{}, fmt.Errorf("%w: position expiry cannot be verified", tracking.ErrUncertain)
		}
		return Acceptance{Position: stored, Duplicate: true, ExpiresAt: expiry}, nil
	case "CONFLICT":
		return Acceptance{}, tracking.ErrConflict
	case "STALE":
		return Acceptance{}, tracking.ErrStale
	case "CLOSED":
		return Acceptance{}, tracking.ErrClosed
	case "PENDING":
		return Acceptance{}, tracking.ErrPending
	case "MISSING":
		return Acceptance{}, tracking.ErrNotFound
	case "INVALID":
		return Acceptance{}, tracking.ErrInvalid
	default:
		return Acceptance{}, fmt.Errorf("%w: acceptance state %s", tracking.ErrUnavailable, result)
	}
}

func (s *Store) ReadPosition(ctx context.Context, id string, seq int64) (tracking.Position, error) {
	if !validID(id) || seq < 1 {
		return tracking.Position{}, tracking.ErrInvalid
	}
	return s.position(ctx, id, seq)
}

func (s *Store) PositionExpiry(ctx context.Context, id string, seq int64) (time.Time, error) {
	if !validID(id) || seq < 1 {
		return time.Time{}, tracking.ErrInvalid
	}
	value, err := s.client.PExpireTime(ctx, positionKey(id, seq)).Result()
	if err != nil {
		return time.Time{}, storeErr(err)
	}
	if value <= 0 {
		return time.Time{}, tracking.ErrUnavailable
	}
	return time.UnixMilli(int64(value / time.Millisecond)).UTC(), nil
}

func (s *Store) confirm(ctx context.Context, p tracking.Position, fingerprint string) (bool, error) {
	pipe := s.client.Pipeline()
	value := pipe.Get(ctx, positionKey(p.SessionID, p.Sequence))
	fields := pipe.HMGet(ctx, sessionKey(p.SessionID), "last_sequence", "pending_sequence")
	score := pipe.ZScore(ctx, historyKey(p.SessionID), strconv.FormatInt(p.Sequence, 10))
	_, err := pipe.Exec(ctx)
	if err != nil && !errors.Is(err, redis.Nil) {
		return false, err
	}
	if value.Err() != nil || score.Err() != nil || fields.Err() != nil {
		return false, nil
	}
	v := value.Val()
	f := fields.Val()
	if len(v) < 65 || v[:64] != fingerprint || len(f) != 2 || f[1] != nil {
		return false, nil
	}
	last, err := strconv.ParseInt(fmt.Sprint(f[0]), 10, 64)
	return err == nil && last >= p.Sequence && int64(score.Val()) == p.Sequence, nil
}

func (s *Store) FinishSession(ctx context.Context, id string, at time.Time) error {
	if !validID(id) || at.IsZero() || at.Nanosecond()%int(time.Millisecond) != 0 {
		return tracking.ErrInvalid
	}
	result, err := finishScript.Run(ctx, s.client, []string{sessionKey(id), activeKey, historyKey(id)}, ms(at), keyID(id)).Text()
	if err != nil {
		return fmt.Errorf("%w: finish session: %v", tracking.ErrUncertain, err)
	}
	switch result {
	case "FINISHED":
		return nil
	case "MISSING":
		return tracking.ErrNotFound
	case "PENDING":
		return tracking.ErrPending
	default:
		return fmt.Errorf("%w: finish state %s", tracking.ErrUnavailable, result)
	}
}

func decodePosition(raw string) (tracking.Position, error) {
	if len(raw) < 66 || raw[64] != '|' {
		return tracking.Position{}, fmt.Errorf("%w: position encoding", tracking.ErrUnavailable)
	}
	var p tracking.Position
	if err := json.Unmarshal([]byte(raw[65:]), &p); err != nil {
		return p, storeErr(err)
	}
	fingerprint, _, err := p.Fingerprint()
	if err != nil {
		return p, err
	}
	if raw[:64] != fingerprint {
		return p, fmt.Errorf("%w: position checksum", tracking.ErrUnavailable)
	}
	return p, nil
}

func (s *Store) position(ctx context.Context, id string, seq int64) (tracking.Position, error) {
	raw, err := s.client.Get(ctx, positionKey(id, seq)).Result()
	if errors.Is(err, redis.Nil) {
		return tracking.Position{}, tracking.ErrNotFound
	}
	if err != nil {
		return tracking.Position{}, storeErr(err)
	}
	p, err := decodePosition(raw)
	if err == nil && (p.SessionID != id || p.Sequence != seq) {
		return tracking.Position{}, fmt.Errorf("%w: position key mismatch", tracking.ErrUnavailable)
	}
	return p, err
}

func (s *Store) Latest(ctx context.Context, id string) (tracking.Position, error) {
	session, err := s.Session(ctx, id)
	if err != nil {
		return tracking.Position{}, err
	}
	if session.LastSequence == 0 {
		return tracking.Position{}, tracking.ErrNotFound
	}
	// The index may contain expired references; scan a small tail only.
	seqs, err := s.client.ZRevRange(ctx, historyKey(id), 0, 127).Result()
	if err != nil {
		return tracking.Position{}, storeErr(err)
	}
	for _, raw := range seqs {
		seq, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return tracking.Position{}, storeErr(err)
		}
		p, err := s.position(ctx, id, seq)
		if errors.Is(err, tracking.ErrNotFound) {
			_ = s.client.ZRem(ctx, historyKey(id), raw).Err()
			continue
		}
		return p, err
	}
	if len(seqs) == 128 {
		return tracking.Position{}, tracking.ErrPending
	}
	return tracking.Position{}, tracking.ErrNotFound
}

type Page = tracking.Page

// History scans at most four index entries per requested result. The caller
// resumes after NextSequence; missing positions are pruned only as encountered.
func (s *Store) History(ctx context.Context, id string, after int64, limit int64) (Page, error) {
	if !validID(id) || after < 0 || limit < 1 || limit > 100 {
		return Page{}, tracking.ErrInvalid
	}
	if _, err := s.Session(ctx, id); err != nil {
		return Page{}, err
	}
	page := Page{Positions: make([]tracking.Position, 0, limit), NextSequence: after}
	maxScan := limit * 4
	for scanned := int64(0); scanned < maxScan && int64(len(page.Positions)) < limit; {
		batch := maxScan - scanned
		if batch > 100 {
			batch = 100
		}
		seqs, err := s.client.ZRangeByScore(ctx, historyKey(id), &redis.ZRangeBy{Min: "(" + strconv.FormatInt(page.NextSequence, 10), Max: "+inf", Offset: 0, Count: batch}).Result()
		if err != nil {
			return Page{}, storeErr(err)
		}
		if len(seqs) == 0 {
			return page, nil
		}
		for _, raw := range seqs {
			seq, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				return Page{}, storeErr(err)
			}
			page.NextSequence = seq
			scanned++
			p, err := s.position(ctx, id, seq)
			if errors.Is(err, tracking.ErrNotFound) {
				if err := s.client.ZRem(ctx, historyKey(id), raw).Err(); err != nil {
					return Page{}, storeErr(err)
				}
				continue
			}
			if err != nil {
				return Page{}, err
			}
			page.Positions = append(page.Positions, p)
			if int64(len(page.Positions)) == limit {
				break
			}
		}
		if int64(len(seqs)) < batch && int64(len(page.Positions)) < limit {
			return page, nil
		}
	}
	count, err := s.client.ZCount(ctx, historyKey(id), "("+strconv.FormatInt(page.NextSequence, 10), "+inf").Result()
	if err != nil {
		return Page{}, storeErr(err)
	}
	page.HasMore = count > 0
	return page, nil
}

func (s *Store) ActiveSessions(ctx context.Context, after int64, limit int64) ([]string, error) {
	if after < 0 || limit < 1 || limit > 100 {
		return nil, tracking.ErrInvalid
	}
	ids, err := s.client.ZRange(ctx, activeKey, after, after+limit-1).Result()
	if err != nil {
		return nil, storeErr(err)
	}
	result := make([]string, 0, len(ids))
	for _, id := range ids {
		b, err := base64.RawURLEncoding.DecodeString(id)
		if err != nil {
			return nil, storeErr(err)
		}
		result = append(result, string(b))
	}
	return result, nil
}
