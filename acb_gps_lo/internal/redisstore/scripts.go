package redisstore

import "github.com/redis/go-redis/v9"

// A pending marker is written before the position. If a later command fails,
// only an identical retry may reconcile the same session. Lua is atomic with
// respect to other clients but does not roll back writes after runtime errors.
var acceptScript = redis.NewScript(`
local session, position, history, active, snapshot = KEYS[1], KEYS[2], KEYS[3], KEYS[4], KEYS[5]
local seq, captured, received, retention, margin = tonumber(ARGV[1]), tonumber(ARGV[2]), tonumber(ARGV[3]), tonumber(ARGV[4]), tonumber(ARGV[5])
local fingerprint, payload, arrival = ARGV[6], ARGV[7], ARGV[8]
if not seq or not captured or not received or not retention or not margin or seq < 1 or seq > 9007199254740991 or retention < 86400000 or margin < 0 or string.len(fingerprint) ~= 64 then return 'INVALID' end
if redis.call('TYPE', session).ok ~= 'hash' then return 'MISSING' end
if redis.call('TYPE', position).ok ~= 'none' and redis.call('TYPE', position).ok ~= 'string' then return 'CORRUPT' end
if redis.call('TYPE', history).ok ~= 'none' and redis.call('TYPE', history).ok ~= 'zset' then return 'CORRUPT' end
if redis.call('TYPE', active).ok ~= 'none' and redis.call('TYPE', active).ok ~= 'zset' then return 'CORRUPT' end
if redis.call('TYPE', snapshot).ok ~= 'none' and redis.call('TYPE', snapshot).ok ~= 'string' then return 'CORRUPT' end
local fields = redis.call('HMGET', session, 'state', 'last_sequence', 'last_captured_ms', 'pending_sequence', 'pending_fingerprint', 'first_arrival', 'max_expiry_ms', 'started_ms')
local state, last, lastCaptured, pending, pendingHash, first, maxExpiry, started = fields[1], tonumber(fields[2]), tonumber(fields[3]), tonumber(fields[4]), fields[5], fields[6], tonumber(fields[7]), tonumber(fields[8])
if not last or last < 0 or (last > 0 and not lastCaptured) or (pending and not pendingHash) then return 'CORRUPT' end
local existing = redis.call('GET', position)
if existing and string.sub(existing, 1, 64) ~= fingerprint then return 'CONFLICT' end
if existing and not pending and seq <= last then return 'DUPLICATE' end
if pending and (pending ~= seq or pendingHash ~= fingerprint) then return 'PENDING' end
if state ~= 'active' then return 'CLOSED' end
local snapshotHash = redis.call('HGET', session, 'snapshot_hash')
if snapshotHash and snapshotHash ~= '' then
  if snapshotHash ~= ARGV[10] then return 'CONFLICT' end
  local snapshotPrefix = redis.call('GETRANGE', snapshot, 0, 64)
  if snapshotPrefix ~= snapshotHash .. '|' then return 'PENDING' end
elseif ARGV[10] ~= '' then return 'CONFLICT' end
local activeScore = redis.call('ZSCORE', active, ARGV[9])
if not started or not activeScore or tonumber(activeScore) ~= started then return 'PENDING' end
if not pending and (seq <= last or (lastCaptured and captured < lastCaptured)) then return 'STALE' end
if existing and not pending then return 'CORRUPT' end
local oldScore = redis.call('ZSCORE', history, ARGV[1])
if oldScore and tonumber(oldScore) ~= seq then return 'CORRUPT' end
local now = redis.call('TIME')
local redisMs = tonumber(now[1]) * 1000 + math.floor(tonumber(now[2]) / 1000)
if received > redisMs + 300000 or captured > received + 300000 then return 'INVALID' end
local base = math.max(captured, received, redisMs)
local expiry = base + retention + margin
if expiry > 9007199254740991 or expiry <= redisMs then return 'INVALID' end
if not pending then redis.call('HSET', session, 'pending_sequence', ARGV[1], 'pending_fingerprint', fingerprint) end
if not existing then
  local inserted = redis.call('SET', position, payload, 'NX', 'PXAT', string.format('%.0f', expiry))
  if not inserted then return 'PENDING' end
end
redis.call('ZADD', history, seq, ARGV[1])
if first == false and arrival ~= '' then redis.call('HSETNX', session, 'first_arrival', arrival) end
redis.call('HSET', session, 'last_sequence', ARGV[1], 'last_captured_ms', ARGV[2], 'max_expiry_ms', string.format('%.0f', math.max(maxExpiry or 0, expiry)))
redis.call('HDEL', session, 'pending_sequence', 'pending_fingerprint')
return 'ACCEPTED'
`)

var createScript = redis.NewScript(`
local session, active, snapshot = KEYS[1], KEYS[2], KEYS[3]
local kind = redis.call('TYPE', session).ok
if kind ~= 'none' and kind ~= 'hash' then return 'CORRUPT' end
local indexKind = redis.call('TYPE', active).ok
if indexKind ~= 'none' and indexKind ~= 'zset' then return 'CORRUPT' end
local snapshotKind = redis.call('TYPE', snapshot).ok
if snapshotKind ~= 'none' and snapshotKind ~= 'string' then return 'CORRUPT' end
if ARGV[10] ~= '' and (string.len(ARGV[10]) ~= 64 or string.sub(ARGV[11], 1, 65) ~= ARGV[10] .. '|') then return 'INVALID' end
local oldSnapshot = redis.call('GET', snapshot)
if ARGV[10] == '' and oldSnapshot then return 'CONFLICT' end
if oldSnapshot and oldSnapshot ~= ARGV[11] then return 'CONFLICT' end
if ARGV[7] ~= '' and (string.len(ARGV[7]) ~= 64 or not tonumber(ARGV[8]) or tonumber(ARGV[8]) < 1 or not tonumber(ARGV[9]) or tonumber(ARGV[9]) < 0) then return 'INVALID' end
if kind == 'none' and redis.call('ZSCORE', active, ARGV[6]) then return 'CORRUPT' end
if kind == 'none' and ARGV[10] ~= '' and not oldSnapshot then redis.call('SET', snapshot, ARGV[11], 'NX') end
if kind == 'none' then
  -- A failed later write leaves this record unusable until an identical retry.
  if ARGV[7] ~= '' then
    redis.call('HSET', session, 'id', ARGV[1], 'guide_id', ARGV[2], 'route_id', ARGV[3], 'trip_id', ARGV[4], 'state', 'provisioning', 'started_ms', ARGV[5], 'last_sequence', '0', 'credential_hash', ARGV[7], 'max_age_ms', ARGV[8], 'future_skew_ms', ARGV[9], 'provision_complete', '0', 'snapshot_hash', ARGV[10])
  else
    redis.call('HSET', session, 'id', ARGV[1], 'guide_id', ARGV[2], 'route_id', ARGV[3], 'trip_id', ARGV[4], 'state', 'provisioning', 'started_ms', ARGV[5], 'last_sequence', '0', 'provision_complete', '0', 'snapshot_hash', ARGV[10])
  end
else
  local fields = redis.call('HMGET', session, 'id', 'guide_id', 'route_id', 'trip_id', 'started_ms', 'credential_hash', 'max_age_ms', 'future_skew_ms', 'state', 'last_sequence', 'provision_complete', 'snapshot_hash')
  if fields[1] ~= ARGV[1] or fields[2] ~= ARGV[2] or fields[3] ~= ARGV[3] or fields[4] ~= ARGV[4] or fields[5] ~= ARGV[5] or (fields[6] or '') ~= ARGV[7] then return 'CONFLICT' end
  if ARGV[7] ~= '' and (fields[7] ~= ARGV[8] or fields[8] ~= ARGV[9]) then return 'CONFLICT' end
  if (fields[12] or '') ~= ARGV[10] then return 'CONFLICT' end
  if ARGV[10] ~= '' and not oldSnapshot then
    if fields[9] ~= 'provisioning' then return 'PENDING' end
    redis.call('SET', snapshot, ARGV[11], 'NX')
  end
  if fields[9] ~= 'provisioning' and fields[9] ~= 'active' then return 'CONFLICT' end
  if fields[9] == 'provisioning' and fields[10] ~= '0' then return 'CORRUPT' end
  local score = redis.call('ZSCORE', active, ARGV[6])
  if fields[9] == 'active' and score and tonumber(score) == tonumber(ARGV[5]) then
    if fields[11] == '1' then return 'EXISTING' end
    -- Existing pre-upgrade sessions had no completion marker.
    if fields[11] == false then return 'EXISTING' end
  end
  if fields[9] == 'active' then redis.call('HSET', session, 'state', 'provisioning', 'provision_complete', '0') end
end
redis.call('ZADD', active, tonumber(ARGV[5]), ARGV[6])
redis.call('HSET', session, 'state', 'active', 'provision_complete', '1')
if kind == 'none' then return 'CREATED' end
return 'REPAIRED'
`)

var finishScript = redis.NewScript(`
if redis.call('TYPE', KEYS[1]).ok ~= 'hash' then return 'MISSING' end
if redis.call('TYPE', KEYS[2]).ok ~= 'none' and redis.call('TYPE', KEYS[2]).ok ~= 'zset' then return 'CORRUPT' end
if redis.call('TYPE', KEYS[4]).ok ~= 'none' and redis.call('TYPE', KEYS[4]).ok ~= 'string' then return 'CORRUPT' end
local fields = redis.call('HMGET', KEYS[1], 'state', 'pending_sequence', 'finished_ms', 'max_expiry_ms')
if fields[2] then return 'PENDING' end
if fields[1] ~= 'active' and fields[1] ~= 'finished' then return 'CORRUPT' end
if fields[4] and not tonumber(fields[4]) then return 'CORRUPT' end
local now = redis.call('TIME')
local redisMs = tonumber(now[1]) * 1000 + math.floor(tonumber(now[2]) / 1000)
local expiry = math.max(tonumber(fields[4]) or 0, redisMs + 86400000)
if fields[1] == 'active' then redis.call('HSET', KEYS[1], 'state', 'finished', 'finished_ms', ARGV[1]) end
redis.call('HDEL', KEYS[1], 'credential_hash')
redis.call('ZREM', KEYS[2], ARGV[2])
redis.call('PEXPIREAT', KEYS[1], string.format('%.0f', expiry))
redis.call('PEXPIREAT', KEYS[3], string.format('%.0f', expiry))
if redis.call('EXISTS', KEYS[4]) == 1 then redis.call('PEXPIREAT', KEYS[4], string.format('%.0f', expiry)) end
return 'FINISHED'
`)
