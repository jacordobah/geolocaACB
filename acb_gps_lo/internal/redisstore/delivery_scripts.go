package redisstore

import "github.com/redis/go-redis/v9"

// Due time doubles as the claim deadline. A claimant must present its random
// token before changing a delivery; an expired claimant cannot acknowledge it.
var claimEventScript = redis.NewScript(`
local due, blocked, pendingEvents = KEYS[1], KEYS[2], KEYS[3]
local alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_'
local function encodeID(value)
  local parts = {}
  for i=1,#value,3 do
    local a,b,c = string.byte(value,i,i+2)
    local n = a*65536+(b or 0)*256+(c or 0)
    parts[#parts+1] = string.sub(alphabet,math.floor(n/262144)%64+1,math.floor(n/262144)%64+1)
    parts[#parts+1] = string.sub(alphabet,math.floor(n/4096)%64+1,math.floor(n/4096)%64+1)
    if b then parts[#parts+1] = string.sub(alphabet,math.floor(n/64)%64+1,math.floor(n/64)%64+1) end
    if c then parts[#parts+1] = string.sub(alphabet,n%64+1,n%64+1) end
  end
  return table.concat(parts)
end
if redis.call('TYPE', due).ok ~= 'none' and redis.call('TYPE', due).ok ~= 'zset' then return {'CORRUPT'} end
if redis.call('TYPE', blocked).ok ~= 'none' and redis.call('TYPE', blocked).ok ~= 'zset' then return {'CORRUPT'} end
if redis.call('TYPE', pendingEvents).ok ~= 'none' and redis.call('TYPE', pendingEvents).ok ~= 'zset' then return {'CORRUPT'} end
local clock = redis.call('TIME')
local now = tonumber(clock[1])*1000 + math.floor(tonumber(clock[2])/1000)
local ids = redis.call('ZRANGEBYSCORE', due, '-inf', now, 'LIMIT', 0, 20)
for _, id in ipairs(ids) do
  local envelope, delivery = 'gps:event:'..id, 'gps:delivery:'..id
  local ek, dk = redis.call('TYPE', envelope).ok, redis.call('TYPE', delivery).ok
  if (ek == 'none' or ek == 'string') and (dk == 'none' or dk == 'hash') then
    local body = redis.call('GET', envelope)
    local state = redis.call('HGET', delivery, 'state')
    if state == 'delivered' then
      local deliveredAt = tonumber(redis.call('HGET', delivery, 'delivered_at_ms') or '0')
      if deliveredAt > 0 then
        local expiry = deliveredAt + tonumber(ARGV[4])
        if body and redis.call('PTTL', envelope) == -1 then redis.call('PEXPIREAT', envelope, expiry) end
        if redis.call('PTTL', delivery) == -1 then redis.call('PEXPIREAT', delivery, expiry) end
      end
      redis.call('ZREM', due, id)
    elseif not body then
      redis.call('ZADD', blocked, now, id)
      redis.call('HSET', delivery, 'state', 'blocked', 'reason', 'missing_envelope')
      redis.call('ZREM', due, id)
    elseif state == 'blocked' or redis.call('ZSCORE', blocked, id) then
      redis.call('ZADD', blocked, 'NX', now, id)
      redis.call('ZREM', due, id)
    elseif redis.call('ZSCORE', pendingEvents, id) then
      redis.call('ZADD', due, now+1000, id)
    else
      local ok, event = pcall(cjson.decode, body)
      if not ok or type(event) ~= 'table' or event.event_id ~= id or type(event.tracking_id) ~= 'string' then
        redis.call('ZADD', blocked, now, id)
        redis.call('HSET', delivery, 'state', 'blocked', 'reason', 'invalid_envelope')
        redis.call('ZREM', due, id)
      else
        local origin = 'gps:session:'..encodeID(event.tracking_id)
        if redis.call('TYPE', origin).ok == 'hash' and redis.call('HGET', origin, 'pending_sequence') then
          -- Compatibility with plans written before gps:pending:events existed.
          redis.call('ZADD', due, now+1000, id)
        else
        local window = tonumber(redis.call('HGET', delivery, 'window_start_ms') or '0')
        local count = tonumber(redis.call('HGET', delivery, 'window_count') or '0')
        if now-window >= 3600000 then window=now; count=0 end
        if count >= tonumber(ARGV[3]) then
          redis.call('ZADD', due, window+3600000, id)
        else
          local attempts = tonumber(redis.call('HGET', delivery, 'attempts') or '0') + 1
          local deadline = now + tonumber(ARGV[1])
          redis.call('ZADD', due, deadline, id)
          redis.call('HSET', delivery, 'state', 'in_flight', 'claim', ARGV[2], 'lease_until_ms', deadline, 'attempts', attempts, 'window_start_ms', window, 'window_count', count+1)
          return {'CLAIMED', id, body, tostring(attempts), tostring(now)}
        end
        end
      end
    end
  else
    return {'CORRUPT'}
  end
end
return {'EMPTY'}
`)

var settleEventScript = redis.NewScript(`
local due, blocked, delivery, envelope = KEYS[1], KEYS[2], KEYS[3], KEYS[4]
if (redis.call('TYPE', due).ok ~= 'none' and redis.call('TYPE', due).ok ~= 'zset') or (redis.call('TYPE', blocked).ok ~= 'none' and redis.call('TYPE', blocked).ok ~= 'zset') or redis.call('TYPE', delivery).ok ~= 'hash' or redis.call('TYPE', envelope).ok ~= 'string' then return 'CORRUPT' end
local clock=redis.call('TIME')
local now=tonumber(clock[1])*1000+math.floor(tonumber(clock[2])/1000)
local state=redis.call('HGET', delivery, 'state')
if state == 'delivered' and ARGV[3] == 'delivered' then return 'DELIVERED' end
if state ~= 'in_flight' or redis.call('HGET', delivery, 'claim') ~= ARGV[2] or tonumber(redis.call('HGET', delivery, 'lease_until_ms') or '0') < now then return 'STALE' end
if ARGV[3] == 'delivered' then
  redis.call('HSET', delivery, 'state', 'delivered', 'delivered_at_ms', now, 'reason', '')
  redis.call('HDEL', delivery, 'claim', 'lease_until_ms')
  redis.call('PEXPIRE', delivery, ARGV[4])
  redis.call('PEXPIRE', envelope, ARGV[4])
  redis.call('ZREM', due, ARGV[1])
elseif ARGV[3] == 'blocked' then
  redis.call('ZADD', blocked, now, ARGV[1])
  redis.call('HSET', delivery, 'state', 'blocked', 'reason', ARGV[5])
  redis.call('HDEL', delivery, 'claim', 'lease_until_ms')
  redis.call('ZREM', due, ARGV[1])
elseif ARGV[3] == 'pending' and tonumber(ARGV[4]) > 0 then
  redis.call('HSET', delivery, 'state', 'pending', 'reason', ARGV[5])
  redis.call('HDEL', delivery, 'claim', 'lease_until_ms')
  redis.call('ZADD', due, now+tonumber(ARGV[4]), ARGV[1])
else return 'INVALID' end
return 'OK'
`)
