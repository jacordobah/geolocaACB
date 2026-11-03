package redisstore

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"

	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/tracking"
	"github.com/redis/go-redis/v9"
)

// The definition registry is durable for the lifetime of the Redis data
// volume. It must not be expired or rewritten when the YAML file changes.
var registerPolicyScript = redis.NewScript(`
local kind = redis.call('TYPE', KEYS[1]).ok
if kind ~= 'none' and kind ~= 'string' then return 'WRONGTYPE' end
local old = redis.call('GET', KEYS[1])
if old then
  if old == ARGV[1] then return 'EXISTING' end
  return 'CONFLICT'
end
redis.call('SET', KEYS[1], ARGV[1])
return 'CREATED'
`)

func policyDefinitionKey(id string) string { return "gps:policy:definition:" + keyID(id) }

// RegisterPolicyDefinitions rejects reuse of an ID with different values,
// including after Go restarts. A partially registered catalog is safe to retry:
// already registered definitions remain immutable.
func (s *Store) RegisterPolicyDefinitions(ctx context.Context, refs []tracking.PolicyReference) error {
	ordered := append([]tracking.PolicyReference(nil), refs...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].PolicyID < ordered[j].PolicyID })
	for _, ref := range ordered {
		if !tracking.ValidID(ref.PolicyID) || ref.Revision < 1 || len(ref.DefinitionHash) != 64 {
			return tracking.ErrInvalid
		}
		if _, err := hex.DecodeString(ref.DefinitionHash); err != nil {
			return tracking.ErrInvalid
		}
		key := policyDefinitionKey(ref.PolicyID)
		result, err := registerPolicyScript.Run(ctx, s.client, []string{key}, ref.DefinitionHash).Text()
		if err != nil {
			if ctx.Err() == nil {
				confirmed, checkErr := s.client.Get(ctx, key).Result()
				if checkErr == nil {
					if confirmed == ref.DefinitionHash {
						continue
					}
					return fmt.Errorf("%w: policy_id %q has a different registered definition", tracking.ErrConflict, ref.PolicyID)
				}
				if errors.Is(checkErr, redis.Nil) {
					return fmt.Errorf("%w: policy registry write not confirmed", tracking.ErrUncertain)
				}
			}
			return fmt.Errorf("%w: policy registry write not confirmed: %v", tracking.ErrUncertain, err)
		}
		switch result {
		case "CREATED", "EXISTING":
		case "CONFLICT":
			return fmt.Errorf("%w: policy_id %q has a different registered definition", tracking.ErrConflict, ref.PolicyID)
		default:
			return fmt.Errorf("%w: policy registry returned %s", tracking.ErrUnavailable, result)
		}
	}
	return nil
}
