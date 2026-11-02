package redisstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/tracking"
	"github.com/redis/go-redis/v9"
)

type pendingEvent struct {
	ID      string `json:"id"`
	Payload string `json:"payload"`
	Due     int64  `json:"due"`
}
type pendingPlan struct {
	State   string         `json:"state"`
	Events  []pendingEvent `json:"events"`
	Arrival string         `json:"arrival"`
}

func encodePlan(plan tracking.TransitionPlan) (string, error) {
	state, err := json.Marshal(plan.State)
	if err != nil {
		return "", tracking.ErrInvalid
	}
	pending := pendingPlan{State: string(state), Events: make([]pendingEvent, 0, len(plan.Events))}
	if plan.FirstArrival != nil {
		b, err := json.Marshal(plan.FirstArrival)
		if err != nil {
			return "", tracking.ErrInvalid
		}
		pending.Arrival = string(b)
	}
	for _, event := range plan.Events {
		b, err := json.Marshal(event)
		if err != nil {
			return "", tracking.ErrInvalid
		}
		pending.Events = append(pending.Events, pendingEvent{ID: event.ID, Payload: string(b), Due: event.OccurredAt.UnixMilli()})
	}
	b, err := json.Marshal(pending)
	if err != nil {
		return "", tracking.ErrInvalid
	}
	return string(b), nil
}

func (s *Store) AcceptTransition(ctx context.Context, position tracking.Position, plan tracking.TransitionPlan) (tracking.Acceptance, error) {
	if position.Geographic == nil || position.IdempotencyVersion != 2 {
		return tracking.Acceptance{}, tracking.ErrInvalid
	}
	if err := position.Validate(); err != nil {
		return tracking.Acceptance{}, err
	}
	if err := plan.Validate(position); err != nil {
		return tracking.Acceptance{}, err
	}
	return s.accept(ctx, position, &plan, false)
}

// GeographicState refuses to expose a partially committed transition.
func (s *Store) GeographicState(ctx context.Context, id string) (tracking.GeographicState, error) {
	if !validID(id) {
		return tracking.GeographicState{}, tracking.ErrInvalid
	}
	f, err := s.client.HMGet(ctx, sessionKey(id), "snapshot_hash", "transition_enabled", "last_sequence", "pending_sequence", "geo_state").Result()
	if err != nil {
		return tracking.GeographicState{}, storeErr(err)
	}
	if len(f) != 5 || f[0] == nil {
		return tracking.GeographicState{}, tracking.ErrNotFound
	}
	if f[1] != "1" {
		return tracking.GeographicState{}, tracking.ErrInvalid
	}
	if f[3] != nil {
		return tracking.GeographicState{}, tracking.ErrPending
	}
	last, err := strconv.ParseInt(fmt.Sprint(f[2]), 10, 64)
	if err != nil {
		return tracking.GeographicState{}, tracking.ErrUnavailable
	}
	if f[4] == nil {
		if last != 0 {
			return tracking.GeographicState{}, tracking.ErrUnavailable
		}
		snapshot, err := s.Snapshot(ctx, id)
		if err != nil {
			return tracking.GeographicState{}, err
		}
		return tracking.InitialGeographicState(snapshot), nil
	}
	var state tracking.GeographicState
	if err := json.Unmarshal([]byte(fmt.Sprint(f[4])), &state); err != nil || state.SnapshotHash != f[0] || state.LastSequence != last {
		return tracking.GeographicState{}, tracking.ErrUnavailable
	}
	return state, nil
}

// RecoverPending replays the saved plan, including events, after a failed
// write or process restart. A different GPS input cannot rewrite that plan.
func (s *Store) RecoverPending(ctx context.Context, id string) error {
	if !validID(id) {
		return tracking.ErrInvalid
	}
	f, err := s.client.HMGet(ctx, sessionKey(id), "pending_sequence", "pending_payload", "pending_plan").Result()
	if err != nil {
		return storeErr(err)
	}
	if f[0] == nil {
		return nil
	}
	if f[1] == nil || f[2] == nil {
		return tracking.ErrPending
	}
	position, err := decodePosition(fmt.Sprint(f[1]))
	if err != nil || position.SessionID != id {
		return tracking.ErrUnavailable
	}
	accepted, err := s.accept(ctx, position, nil, true)
	if err != nil {
		return err
	}
	if accepted.Position.Sequence != position.Sequence {
		return tracking.ErrUncertain
	}
	return nil
}

func (s *Store) confirmTransition(ctx context.Context, position tracking.Position, plan tracking.TransitionPlan) (bool, error) {
	f, err := s.client.HMGet(ctx, sessionKey(position.SessionID), "geo_state", "first_arrival").Result()
	if err != nil {
		return false, err
	}
	if f[0] == nil {
		return false, nil
	}
	var state tracking.GeographicState
	if json.Unmarshal([]byte(fmt.Sprint(f[0])), &state) != nil || state.LastSequence < position.Sequence || state.SnapshotHash != plan.State.SnapshotHash {
		return false, nil
	}
	if plan.FirstArrival != nil {
		b, _ := json.Marshal(plan.FirstArrival)
		if f[1] != string(b) {
			return false, nil
		}
	}
	for _, event := range plan.Events {
		body, err := json.Marshal(event)
		if err != nil {
			return false, err
		}
		stored, err := s.client.Get(ctx, eventKey(event.ID)).Result()
		if err != nil || stored != string(body) {
			return false, err
		}
		score, err := s.client.ZScore(ctx, outboxKey, event.ID).Result()
		if err != nil || score != float64(event.OccurredAt.UnixMilli()) {
			return false, err
		}
	}
	return true, nil
}

// PendingEvents is internal and bounded. Event envelopes have no TTL; a
// missing envelope is pruned only when its index entry is encountered.
func (s *Store) PendingEvents(ctx context.Context, offset, limit int64) ([]tracking.Event, error) {
	if offset < 0 || limit < 1 || limit > 100 {
		return nil, tracking.ErrInvalid
	}
	ids, err := s.client.ZRange(ctx, outboxKey, offset, offset+limit-1).Result()
	if err != nil {
		return nil, storeErr(err)
	}
	events := make([]tracking.Event, 0, len(ids))
	for _, id := range ids {
		raw, err := s.client.Get(ctx, eventKey(id)).Result()
		if errors.Is(err, redis.Nil) {
			if err := s.client.ZRem(ctx, outboxKey, id).Err(); err != nil {
				return nil, storeErr(err)
			}
			continue
		}
		if err != nil {
			return nil, storeErr(err)
		}
		var event tracking.Event
		if json.Unmarshal([]byte(raw), &event) != nil || event.ID != id || event.Validate() != nil {
			return nil, tracking.ErrUnavailable
		}
		pending, err := s.client.HGet(ctx, sessionKey(event.TrackingID), "pending_sequence").Result()
		if err != nil && !errors.Is(err, redis.Nil) {
			return nil, storeErr(err)
		}
		if pending != "" {
			return nil, tracking.ErrPending
		}
		events = append(events, event)
	}
	return events, nil
}
