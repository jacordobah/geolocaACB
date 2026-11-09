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

const provisionIndexKey = "gps:provision:pending"

func provisionPlanKey(id string) string { return "gps:provision:plan:" + keyID(id) }

type provisionPlan struct {
	Session   tracking.Session             `json:"session"`
	DigestHex string                       `json:"credential_digest"`
	Snapshot  *tracking.GeographicSnapshot `json:"snapshot,omitempty"`
}

// The index is written first: a failed plan SET leaves only a harmless
// candidate, while a saved plan can always be discovered after process loss.
var prepareProvisionScript = redis.NewScript(`
if redis.call('TYPE',KEYS[1]).ok ~= 'none' and redis.call('TYPE',KEYS[1]).ok ~= 'zset' then return 'CORRUPT' end
if redis.call('TYPE',KEYS[2]).ok ~= 'none' and redis.call('TYPE',KEYS[2]).ok ~= 'string' then return 'CORRUPT' end
local old=redis.call('GET',KEYS[2])
if old and old ~= ARGV[3] then return 'CONFLICT' end
local clock=redis.call('TIME')
local now=tonumber(clock[1])*1000+math.floor(tonumber(clock[2])/1000)
redis.call('ZADD',KEYS[1],now,ARGV[1])
if not old then redis.call('SET',KEYS[2],ARGV[3],'NX') end
return 'READY'
`)

var cleanProvisionScript = redis.NewScript(`
if redis.call('GET',KEYS[2]) == ARGV[2] then redis.call('DEL',KEYS[2]) end
redis.call('ZREM',KEYS[1],ARGV[1])
return 'OK'
`)

func (s *Store) prepareProvision(ctx context.Context, session tracking.Session, digest [32]byte, snapshot *tracking.GeographicSnapshot) (string, error) {
	plan := provisionPlan{Session: session, DigestHex: hex.EncodeToString(digest[:]), Snapshot: snapshot}
	body, err := json.Marshal(plan)
	if err != nil {
		return "", tracking.ErrInvalid
	}
	result, err := prepareProvisionScript.Run(ctx, s.client, []string{provisionIndexKey, provisionPlanKey(session.ID)}, keyID(session.ID), ms(session.StartedAt), string(body)).Text()
	if err != nil {
		if ctx.Err() == nil {
			stored, checkErr := s.client.Get(ctx, provisionPlanKey(session.ID)).Result()
			if checkErr == nil && stored == string(body) {
				if _, indexErr := s.client.ZScore(ctx, provisionIndexKey, keyID(session.ID)).Result(); indexErr == nil {
					return string(body), nil
				}
			}
		}
		return "", fmt.Errorf("%w: prepare provision: %v", tracking.ErrUncertain, err)
	}
	if result == "CONFLICT" {
		return "", tracking.ErrConflict
	}
	if result != "READY" {
		return "", tracking.ErrUnavailable
	}
	return string(body), nil
}

func (s *Store) cleanProvision(ctx context.Context, id, body string) {
	// A failed cleanup leaves a discoverable, idempotent plan.
	_ = cleanProvisionScript.Run(ctx, s.client, []string{provisionIndexKey, provisionPlanKey(id)}, keyID(id), body).Err()
}

func (s *Store) PendingProvisions(ctx context.Context, limit int64) ([]string, error) {
	if limit < 1 || limit > 100 {
		return nil, tracking.ErrInvalid
	}
	clock, err := s.client.Time(ctx).Result()
	if err != nil {
		return nil, storeErr(err)
	}
	encoded, err := s.client.ZRangeByScore(ctx, provisionIndexKey, &redis.ZRangeBy{Min: "-inf", Max: strconv.FormatInt(clock.UnixMilli(), 10), Count: limit}).Result()
	if err != nil {
		return nil, storeErr(err)
	}
	ids := make([]string, 0, len(encoded))
	for _, member := range encoded {
		id, err := base64.RawURLEncoding.DecodeString(member)
		if err != nil {
			return nil, tracking.ErrUnavailable
		}
		ids = append(ids, string(id))
	}
	return ids, nil
}

func (s *Store) DeferProvision(ctx context.Context, id string, delay time.Duration) error {
	if !validID(id) || delay < time.Second || delay > time.Hour {
		return tracking.ErrInvalid
	}
	clock, err := s.client.Time(ctx).Result()
	if err != nil {
		return storeErr(err)
	}
	if err := s.client.ZAdd(ctx, provisionIndexKey, redis.Z{Score: float64(clock.Add(delay).UnixMilli()), Member: keyID(id)}).Err(); err != nil {
		return storeErr(err)
	}
	return nil
}

func (s *Store) RecoverProvision(ctx context.Context, id string) error {
	if !validID(id) {
		return tracking.ErrInvalid
	}
	raw, err := s.client.Get(ctx, provisionPlanKey(id)).Result()
	if errors.Is(err, redis.Nil) {
		// An index-first write can leave a candidate with no plan. It is safe
		// to remove only if no unusable provisioning session exists.
		state, stateErr := s.client.HGet(ctx, sessionKey(id), "state").Result()
		if stateErr != nil && !errors.Is(stateErr, redis.Nil) {
			return storeErr(stateErr)
		}
		if state == "provisioning" {
			return tracking.ErrPending
		}
		if err := s.client.ZRem(ctx, provisionIndexKey, keyID(id)).Err(); err != nil {
			return storeErr(err)
		}
		return nil
	}
	if err != nil {
		return storeErr(err)
	}
	var plan provisionPlan
	if json.Unmarshal([]byte(raw), &plan) != nil || plan.Session.ID != id || plan.Session.ValidateNew() != nil || len(plan.DigestHex) != 64 {
		return tracking.ErrUnavailable
	}
	state, stateErr := s.client.HMGet(ctx, sessionKey(id), "state", "activation_event_done").Result()
	if stateErr != nil {
		return storeErr(stateErr)
	}
	if state[0] == "finished" && state[1] == "1" {
		s.cleanProvision(ctx, id, raw)
		return nil
	}
	// A very old orphan must not be silently turned into a fresh active
	// session after the expected observation window has passed.
	if state[0] == nil && time.Since(plan.Session.StartedAt) > tracking.MinRetention {
		return tracking.ErrPending
	}
	digestBytes, err := hex.DecodeString(plan.DigestHex)
	if err != nil || len(digestBytes) != 32 {
		return tracking.ErrUnavailable
	}
	var digest [32]byte
	copy(digest[:], digestBytes)
	if plan.Snapshot != nil {
		if plan.Snapshot.OperationalReady() != nil {
			return tracking.ErrUnavailable
		}
	}
	return s.provision(ctx, plan.Session, digest, plan.Snapshot)
}

// A reserved idempotency key never expires automatically. The generic plan
// above is separate and can be removed once provisioning is fully verified.
