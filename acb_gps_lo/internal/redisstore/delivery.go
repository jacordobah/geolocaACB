package redisstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/tracking"
	"github.com/redis/go-redis/v9"
)

const blockedKey = "gps:outbox:blocked"
const pendingEventsKey = "gps:pending:events"

func deliveryKey(id string) string { return "gps:delivery:" + id }

type ClaimedEvent struct {
	Event     tracking.Event
	Token     string
	Attempts  int
	ClaimedAt time.Time
}

// ClaimEvent uses Redis time, a bounded due scan, and a fencing token. A
// claim's due score is its deadline, so abandoned claims become eligible again.
func (s *Store) ClaimEvent(ctx context.Context, lease time.Duration, maxPerHour int, deliveredRetention time.Duration) (ClaimedEvent, bool, error) {
	if lease < time.Second || lease > time.Hour || maxPerHour < 1 || maxPerHour > 3600 || deliveredRetention < time.Hour || deliveredRetention > 365*24*time.Hour {
		return ClaimedEvent{}, false, tracking.ErrInvalid
	}
	var random [24]byte
	if _, err := rand.Read(random[:]); err != nil {
		return ClaimedEvent{}, false, err
	}
	token := hex.EncodeToString(random[:])
	values, err := claimEventScript.Run(ctx, s.client, []string{outboxKey, blockedKey, pendingEventsKey}, lease.Milliseconds(), token, maxPerHour, deliveredRetention.Milliseconds()).StringSlice()
	if err != nil {
		return ClaimedEvent{}, false, storeErr(err)
	}
	if len(values) == 1 && values[0] == "EMPTY" {
		return ClaimedEvent{}, false, nil
	}
	if len(values) != 5 || values[0] != "CLAIMED" {
		return ClaimedEvent{}, false, fmt.Errorf("%w: claim %v", tracking.ErrUnavailable, values)
	}
	var event tracking.Event
	if err := json.Unmarshal([]byte(values[2]), &event); err != nil || event.ID != values[1] || event.Validate() != nil {
		_ = s.SettleEvent(ctx, values[1], token, "blocked", 0, "invalid_envelope")
		return ClaimedEvent{}, false, tracking.ErrUnavailable
	}
	// Compatibility with plans written before the dedicated pending-event index.
	pending, err := s.client.HGet(ctx, sessionKey(event.TrackingID), "pending_sequence").Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return ClaimedEvent{}, false, storeErr(err)
	}
	if pending != "" {
		_ = s.SettleEvent(ctx, event.ID, token, "pending", int64(time.Second/time.Millisecond), "origin_pending")
		return ClaimedEvent{}, false, tracking.ErrPending
	}
	attempts, err := strconv.Atoi(values[3])
	if err != nil {
		return ClaimedEvent{}, false, tracking.ErrUnavailable
	}
	ms, err := strconv.ParseInt(values[4], 10, 64)
	if err != nil {
		return ClaimedEvent{}, false, tracking.ErrUnavailable
	}
	return ClaimedEvent{Event: event, Token: token, Attempts: attempts, ClaimedAt: time.UnixMilli(ms).UTC()}, true, nil
}

func (s *Store) SettleEvent(ctx context.Context, id, token, state string, value int64, reason string) error {
	if len(id) != 64 || len(token) != 48 || (state != "delivered" && state != "pending" && state != "blocked") {
		return tracking.ErrInvalid
	}
	result, err := settleEventScript.Run(ctx, s.client, []string{outboxKey, blockedKey, deliveryKey(id), eventKey(id)}, id, token, state, value, reason).Text()
	if err != nil {
		return fmt.Errorf("%w: settle event: %v", tracking.ErrUncertain, err)
	}
	switch result {
	case "OK", "DELIVERED":
		return nil
	case "STALE":
		return tracking.ErrConflict
	default:
		return fmt.Errorf("%w: settle event %s", tracking.ErrUnavailable, result)
	}
}

// DeliveryState allows reconciliation after a lost Redis response. Only a
// persisted delivered state counts as confirmation of local settlement.
func (s *Store) DeliveryState(ctx context.Context, id string) (string, error) {
	if len(id) != 64 {
		return "", tracking.ErrInvalid
	}
	state, err := s.client.HGet(ctx, deliveryKey(id), "state").Result()
	if errors.Is(err, redis.Nil) {
		return "pending", nil
	}
	if err != nil {
		return "", storeErr(err)
	}
	return state, nil
}

func (s *Store) OutboxStats(ctx context.Context) (pending, blocked int64, oldest time.Time, err error) {
	pending, err = s.client.ZCard(ctx, outboxKey).Result()
	if err != nil {
		err = storeErr(err)
		return
	}
	blocked, err = s.client.ZCard(ctx, blockedKey).Result()
	if err != nil {
		err = storeErr(err)
		return
	}
	first, e := s.client.ZRangeWithScores(ctx, outboxKey, 0, 0).Result()
	if e != nil {
		err = storeErr(e)
		return
	}
	if len(first) > 0 {
		oldest = time.UnixMilli(int64(first[0].Score)).UTC()
	}
	return
}
