package redisstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/control"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/tracking"
	"github.com/redis/go-redis/v9"
)

// The reservation is deliberately retained without a TTL. Expiring it while
// the session still exists would permit the same request key to create a new
// session. A lifecycle cleanup rule needs agreement with the control owner.
var reserveControlScript = redis.NewScript(`
if redis.call('TYPE', KEYS[1]).ok ~= 'none' and redis.call('TYPE', KEYS[1]).ok ~= 'string' then return redis.error_reply('WRONGTYPE control reservation') end
local old = redis.call('GET', KEYS[1])
if old then return {0, old} end
redis.call('SET', KEYS[1], ARGV[1], 'NX')
return {1, ARGV[1]}
`)

func controlKey(owner, key string) string {
	sum := sha256.Sum256([]byte(owner + "\x00" + key))
	return "gps:control:request:" + hex.EncodeToString(sum[:])
}

func decodeReservation(raw string) (control.Reservation, error) {
	var record control.Reservation
	if err := json.Unmarshal([]byte(raw), &record); err != nil || record.Fingerprint == "" || !tracking.ValidID(record.PolicyID) || record.Session.ID == "" || record.Snapshot.SnapshotHash != record.Session.SnapshotHash {
		return control.Reservation{}, tracking.ErrUncertain
	}
	return record, nil
}

func (s *Store) Lookup(ctx context.Context, owner, key string) (control.Reservation, bool, error) {
	if !tracking.ValidID(owner) || !tracking.ValidID(key) {
		return control.Reservation{}, false, tracking.ErrInvalid
	}
	raw, err := s.client.Get(ctx, controlKey(owner, key)).Result()
	if errors.Is(err, redis.Nil) {
		return control.Reservation{}, false, nil
	}
	if err != nil {
		return control.Reservation{}, false, storeErr(err)
	}
	record, err := decodeReservation(raw)
	return record, true, err
}

func (s *Store) Reserve(ctx context.Context, owner, key string, proposed control.Reservation) (control.Reservation, bool, error) {
	if !tracking.ValidID(owner) || !tracking.ValidID(key) || !tracking.ValidID(proposed.PolicyID) || proposed.Session.ValidateNew() != nil || proposed.Snapshot.OperationalReady() != nil || proposed.Session.SnapshotHash != proposed.Snapshot.SnapshotHash || proposed.Fingerprint == "" || proposed.IssuedUntil.IsZero() {
		return control.Reservation{}, false, tracking.ErrInvalid
	}
	body, err := json.Marshal(proposed)
	if err != nil {
		return control.Reservation{}, false, tracking.ErrInvalid
	}
	keyName := controlKey(owner, key)
	result, err := reserveControlScript.Run(ctx, s.client, []string{keyName}, string(body)).Result()
	if err != nil {
		return control.Reservation{}, false, fmt.Errorf("%w: reserve control request: %v", tracking.ErrUncertain, err)
	}
	values, ok := result.([]interface{})
	if !ok || len(values) != 2 {
		return control.Reservation{}, false, tracking.ErrUncertain
	}
	flag, ok := values[0].(int64)
	if !ok {
		return control.Reservation{}, false, tracking.ErrUncertain
	}
	raw, ok := values[1].(string)
	if !ok {
		return control.Reservation{}, false, tracking.ErrUncertain
	}
	stored, err := decodeReservation(raw)
	return stored, flag == 0, err
}
