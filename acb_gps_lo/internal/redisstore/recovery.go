package redisstore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"time"

	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/tracking"
	"github.com/redis/go-redis/v9"
)

// CleanPendingEvents removes only stale claim guards. It never removes an
// event envelope or its delivery due entry.
func (s *Store) CleanPendingEvents(ctx context.Context, limit int64) (int, error) {
	if limit < 1 || limit > 100 {
		return 0, tracking.ErrInvalid
	}
	ids, err := s.client.ZRange(ctx, pendingEventsKey, 0, limit-1).Result()
	if err != nil {
		return 0, storeErr(err)
	}
	cleaned := 0
	for _, id := range ids {
		raw, err := s.client.Get(ctx, eventKey(id)).Result()
		if err != nil && err != redis.Nil {
			return cleaned, storeErr(err)
		}
		if err == redis.Nil {
			if e := s.client.ZRem(ctx, pendingEventsKey, id).Err(); e != nil {
				return cleaned, storeErr(e)
			}
			cleaned++
			continue
		}
		var event tracking.Event
		if json.Unmarshal([]byte(raw), &event) != nil || event.ID != id {
			if e := s.client.ZRem(ctx, pendingEventsKey, id).Err(); e != nil {
				return cleaned, storeErr(e)
			}
			cleaned++
			continue
		}
		field := "pending_sequence"
		switch event.Type {
		case tracking.TrackingSessionActivated:
			field = "activation_event_done"
		case tracking.TrackingSessionFinished:
			field = "finish_event_done"
		}
		value, err := s.client.HGet(ctx, sessionKey(event.TrackingID), field).Result()
		if err != nil && err != redis.Nil {
			return cleaned, storeErr(err)
		}
		clear := field == "pending_sequence" && value == "" || field != "pending_sequence" && value == "1"
		if clear {
			if err := s.client.ZRem(ctx, pendingEventsKey, id).Err(); err != nil {
				return cleaned, storeErr(err)
			}
			cleaned++
		}
	}
	return cleaned, nil
}

// DiscoverPending incrementally visits the active index. This also discovers
// plans left by versions that predate gps:pending:sessions.
func (s *Store) DiscoverPending(ctx context.Context, cursor uint64, count int64) (uint64, error) {
	if count < 1 || count > 100 {
		return cursor, tracking.ErrInvalid
	}
	items, next, err := s.client.ZScan(ctx, activeKey, cursor, "", count).Result()
	if err != nil {
		return cursor, storeErr(err)
	}
	clock, err := s.client.Time(ctx).Result()
	if err != nil {
		return cursor, storeErr(err)
	}
	for i := 0; i+1 < len(items); i += 2 {
		encoded := items[i]
		idBytes, err := base64.RawURLEncoding.DecodeString(encoded)
		if err != nil {
			continue
		}
		has, err := s.client.HExists(ctx, sessionKey(string(idBytes)), "pending_sequence").Result()
		if err != nil {
			return next, storeErr(err)
		}
		if has {
			if err := s.client.ZAddNX(ctx, pendingKey, redis.Z{Score: float64(clock.UnixMilli()), Member: encoded}).Err(); err != nil {
				return next, storeErr(err)
			}
		}
	}
	return next, nil
}

func (s *Store) PendingSessions(ctx context.Context, limit int64) ([]string, error) {
	if limit < 1 || limit > 100 {
		return nil, tracking.ErrInvalid
	}
	// Redis time avoids depending on the worker clock for due selection.
	clock, err := s.client.Time(ctx).Result()
	if err != nil {
		return nil, storeErr(err)
	}
	encoded, err := s.client.ZRangeByScore(ctx, pendingKey, &redis.ZRangeBy{Min: "-inf", Max: strconv.FormatInt(clock.UnixMilli(), 10), Offset: 0, Count: limit}).Result()
	if err != nil {
		return nil, storeErr(err)
	}
	ids := make([]string, 0, len(encoded))
	for _, value := range encoded {
		decoded, e := base64.RawURLEncoding.DecodeString(value)
		if e != nil {
			return nil, tracking.ErrUnavailable
		}
		ids = append(ids, string(decoded))
	}
	return ids, nil
}

func (s *Store) DeferRecovery(ctx context.Context, id string, minDelay, maxDelay time.Duration) (int64, error) {
	if !validID(id) || minDelay < time.Second || maxDelay < minDelay {
		return 0, tracking.ErrInvalid
	}
	count, err := s.client.HIncrBy(ctx, sessionKey(id), "recovery_failures", 1).Result()
	if err != nil {
		return 0, storeErr(err)
	}
	clock, err := s.client.Time(ctx).Result()
	if err != nil {
		return count, storeErr(err)
	}
	if count >= 10 {
		_ = s.client.HSet(ctx, sessionKey(id), "recovery_status", "needs_intervention").Err()
	}
	delay := minDelay
	for i := int64(1); i < count && delay < maxDelay; i++ {
		if delay > maxDelay/2 {
			delay = maxDelay
			break
		}
		delay *= 2
	}
	if err := s.client.ZAdd(ctx, pendingKey, redis.Z{Score: float64(clock.Add(delay).UnixMilli()), Member: keyID(id)}).Err(); err != nil {
		return count, storeErr(err)
	}
	return count, nil
}
