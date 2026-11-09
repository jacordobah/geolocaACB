package redisstore

import (
	"context"
	"encoding/base64"
	"strconv"
	"time"

	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/tracking"
	"github.com/redis/go-redis/v9"
)

const finishPendingKey = "gps:finish:pending"

// PendingFinishes reads only due recovery candidates, never scans the keyspace.
func (s *Store) PendingFinishes(ctx context.Context, limit int64) ([]string, error) {
	if limit < 1 || limit > 100 {
		return nil, tracking.ErrInvalid
	}
	clock, err := s.client.Time(ctx).Result()
	if err != nil {
		return nil, storeErr(err)
	}
	encoded, err := s.client.ZRangeByScore(ctx, finishPendingKey, &redis.ZRangeBy{Min: "-inf", Max: strconv.FormatInt(clock.UnixMilli(), 10), Count: limit}).Result()
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

func (s *Store) RecoverFinish(ctx context.Context, id string) error {
	if !validID(id) {
		return tracking.ErrInvalid
	}
	fields, err := s.client.HMGet(ctx, sessionKey(id), "state", "finished_ms", "finish_event_done").Result()
	if err != nil {
		return storeErr(err)
	}
	if fields[0] == nil {
		return tracking.ErrUnavailable
	}
	if fields[0] == "finished" && fields[2] == "1" {
		if err := s.client.ZRem(ctx, finishPendingKey, keyID(id)).Err(); err != nil {
			return storeErr(err)
		}
		return nil
	}
	if fields[0] == "active" && fields[1] == nil {
		// Index-first preparation failed before any session transition.
		return storeErrIf(s.client.ZRem(ctx, finishPendingKey, keyID(id)).Err())
	}
	if fields[0] != "finishing" || fields[1] == nil {
		return tracking.ErrPending
	}
	at, err := fromMS(fields[1].(string))
	if err != nil {
		return tracking.ErrUnavailable
	}
	return s.FinishSession(ctx, id, at)
}

func (s *Store) DeferFinish(ctx context.Context, id string, delay time.Duration) error {
	if !validID(id) || delay < time.Second || delay > time.Hour {
		return tracking.ErrInvalid
	}
	clock, err := s.client.Time(ctx).Result()
	if err != nil {
		return storeErr(err)
	}
	return storeErrIf(s.client.ZAdd(ctx, finishPendingKey, redis.Z{Score: float64(clock.Add(delay).UnixMilli()), Member: keyID(id)}).Err())
}

func storeErrIf(err error) error {
	if err != nil {
		return storeErr(err)
	}
	return nil
}
