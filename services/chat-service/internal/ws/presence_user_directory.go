package ws

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	valkey "github.com/valkey-io/valkey-go"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// The per-user half of the directory on Valkey (issue #798).
//
// Layout, one hash per person:
//
//	nchat:chat:ws:presence-user:{workspace}:{user}
//	  field r:{instance id}: state|unixNano|generation — that instance's reach
//	  field p:              availability|activity|seconds|nanos — the projection
//	  field v:              the facts revision (see UserPresenceStore)
//	  field w:{token}:      unix ms — a facts change in flight, until when;
//	                        dated, judged and, once lapsed, recovered by the
//	                        scripts, all on Valkey's own clock
//
// Same single-writer rule as the target hashes: an instance writes and deletes
// only its own r: field, and only through reachScript, which refuses an older
// lifecycle than the one stored. The r: fields of instances whose liveness key
// has lapsed are ignored and reaped, a bounded batch per read, by reapScript,
// which moves the revision when it removes anything. The projection is shared
// by every instance and changes only through projectionScript. The key
// carries a lease renewed by every write and by the heartbeat of each instance
// still serving the person, so a person nobody serves any more costs nothing
// after userPresenceTTL.

const (
	userPresencePrefix     = "nchat:chat:ws:presence-user:"
	userReachFieldPrefix   = "r:"
	userChangeFieldPrefix  = "w:"
	userProjectionField    = "p"
	userRevisionField      = "v"
	userPresenceTTL        = 24 * time.Hour
	userPresenceProjectSep = "|"
	// factsMarkReapLimit bounds how many lapsed facts-change marks one script
	// removes: marks are only ever added by a facts change, which reaps this
	// many first, so a hash never accumulates them however long it lives.
	factsMarkReapLimit = 32
)

// reachScript writes (ARGV[2] non-empty) or removes this instance's reach
// field, fenced by lifecycle generation (ARGV[4]): when the stored field
// belongs to a newer generation nothing happens and 0 is returned. The
// revision moves when the field is removed, or written with another state or
// another generation; re-asserting the same state of the same lifecycle with a
// fresher instant is not a new fact.
const reachScriptBody = `
local old = redis.call('HGET', KEYS[1], ARGV[1])
local oldState, oldGeneration
if old then
  oldState, oldGeneration = string.match(old, '^([^|]*)|%d+|(%d+)$')
  if not oldState then return redis.error_reply('presence reach malformed') end
  if tonumber(oldGeneration) > tonumber(ARGV[4]) then return 0 end
end
if ARGV[2] == '' then
  if old then
    redis.call('HDEL', KEYS[1], ARGV[1])
    redis.call('HINCRBY', KEYS[1], 'v', 1)
  end
  return 1
end
local newState = string.match(ARGV[2], '^([^|]*)|')
redis.call('HSET', KEYS[1], ARGV[1], ARGV[2])
if not old or oldState ~= newState or oldGeneration ~= ARGV[4] then
  redis.call('HINCRBY', KEYS[1], 'v', 1)
end
redis.call('EXPIRE', KEYS[1], ARGV[3])
return 1`

var reachScript = valkey.NewLuaScript(reachScriptBody)

// reapScript removes the reach fields of dead instances (ARGV) and moves the
// revision when it actually removed one.
const reapScriptBody = `
local removed = 0
for i = 1, #ARGV do removed = removed + redis.call('HDEL', KEYS[1], ARGV[i]) end
if removed > 0 then redis.call('HINCRBY', KEYS[1], 'v', 1) end
return removed`

var reapScript = valkey.NewLuaScript(reapScriptBody)

// authorityNowLua is the authority's clock, in unix ms: every deadline a
// script judges — a timed fact, a facts change's lease — is judged against it,
// at the moment the script runs, never against an instant a client captured
// earlier.
const authorityNowLua = `
local clock = redis.call('TIME')
local now = tonumber(clock[1]) * 1000 + math.floor(tonumber(clock[2]) / 1000)
`

// settleLua is settle(now, limit) → inFlight, over KEYS[1]'s facts changes:
// whether one is still in flight at now, after physically removing at most
// limit marks whose lease has passed.
//
// A mark that lapsed was never ended: its writer died or its End failed, so
// the database may hold a change made under it — and a composition read while
// it was open carries the revision its Begin moved to, which nothing else
// would move again. Removing such marks is therefore a recovery: the revision
// moves, once however many are removed, so every read taken inside those
// changes conflicts and is redone against the database as it ended up.
const settleLua = `
local function settle(now, limit)
  local fields = redis.call('HGETALL', KEYS[1])
  local inFlight, removed = false, 0
  for i = 1, #fields, 2 do
    if string.sub(fields[i], 1, 2) == 'w:' then
      local untilMs = tonumber(fields[i + 1])
      if untilMs and untilMs > now then
        inFlight = true
      elseif removed < limit then
        redis.call('HDEL', KEYS[1], fields[i])
        removed = removed + 1
      end
    end
  end
  if removed > 0 then redis.call('HINCRBY', KEYS[1], 'v', 1) end
  return inFlight
end
`

// beginChangeScript marks a facts change in flight (field ARGV[1]) for ARGV[2]
// ms of the authority's own clock, and moves the revision, after recovering at
// most ARGV[4] lapsed marks (which may move it once more); endChangeScript
// clears the mark and moves the revision again. The lease travels as a
// duration so that the clock that judges the mark is also the one that dated
// it: a writer whose clock is behind cannot hand the authority a mark that is
// already over.
const beginChangeScriptBody = authorityNowLua + settleLua + `
settle(now, tonumber(ARGV[4]))
redis.call('HSET', KEYS[1], ARGV[1], string.format('%d', now + tonumber(ARGV[2])))
redis.call('HINCRBY', KEYS[1], 'v', 1)
redis.call('EXPIRE', KEYS[1], ARGV[3])
return 1`

var beginChangeScript = valkey.NewLuaScript(beginChangeScriptBody)

const endChangeScriptBody = `
redis.call('HDEL', KEYS[1], ARGV[1])
redis.call('HINCRBY', KEYS[1], 'v', 1)
redis.call('EXPIRE', KEYS[1], ARGV[2])
return 1`

var endChangeScript = valkey.NewLuaScript(endChangeScriptBody)

// projectionScript is the linearization point of a person's public presence.
// KEYS[1] is the person; KEYS[2..] the liveness keys of the other instances
// whose reach the composition counted. Time is the authority's own, read
// here. In order:
//
//   - a stored value that does not parse is an error, never silently replaced;
//   - the composition's timed facts ended (now ≥ ARGV[7] valid-until, when
//     set), or a counted instance is no longer alive: expired, nothing
//     written — checked first, so a fact that lapsed cannot even confirm an
//     identical projection;
//   - a facts change is in flight (a w: mark whose lease has not lapsed), or
//     the revision moved since the composer read its facts (ARGV[6]) —
//     including the move a recovery of lapsed marks made just before the
//     comparison: a conflict, nothing written — even when the composition
//     happens to equal the stored projection: facts that changed cannot
//     confirm anything;
//   - the stored projection already is this one, or this is an offline over no
//     projection at all: unchanged, nothing written;
//   - otherwise the projection is applied, at an instant later than the stored
//     one, and the revision moves.
//
// Up to ARGV[8] lapsed marks are recovered first (settleLua), whatever the
// outcome, and the revision is compared only after it.
// Seconds and nanoseconds travel apart: a nanosecond epoch does not fit the
// double Lua numbers are, and an instant rounded on its way through here would
// collide with the one it had to follow.
const projectionScriptBody = authorityNowLua + settleLua + `
local current = redis.call('HGET', KEYS[1], 'p')
local revision = redis.call('HGET', KEYS[1], 'v') or '0'
if not string.match(revision, '^%d+$') then return redis.error_reply('presence revision malformed') end
local projection, s, n
if current then
  projection, s, n = string.match(current, '^([^|]*|[^|]*)|(%d+)|(%d+)$')
  if not projection then return redis.error_reply('presence projection malformed') end
end
local inFlight = settle(now, tonumber(ARGV[8]))
revision = redis.call('HGET', KEYS[1], 'v') or '0'
if ARGV[7] ~= '0' and now >= tonumber(ARGV[7]) then return {'0', '0', 'expired'} end
for i = 2, #KEYS do
  if redis.call('EXISTS', KEYS[i]) == 0 then return {'0', '0', 'expired'} end
end
if inFlight or revision ~= ARGV[6] then return {'0', '0', 'conflict'} end
if not current and ARGV[1] == ARGV[5] then return {'0', '0', 'unchanged'} end
if projection == ARGV[1] then return {s, n, 'unchanged'} end
local seconds, nanos = tonumber(ARGV[2]), tonumber(ARGV[3])
if current then
  s, n = tonumber(s), tonumber(n)
  if seconds < s or (seconds == s and nanos <= n) then
    seconds, nanos = s, n + 1
    if nanos >= 1000000000 then seconds, nanos = s + 1, 0 end
  end
end
s, n = string.format('%d', seconds), string.format('%d', nanos)
redis.call('HSET', KEYS[1], 'p', ARGV[1] .. '|' .. s .. '|' .. n)
redis.call('HINCRBY', KEYS[1], 'v', 1)
redis.call('EXPIRE', KEYS[1], ARGV[4])
return {s, n, 'applied'}`

var projectionScript = valkey.NewLuaScript(projectionScriptBody)

func userPresenceKey(workspaceID, userID string) string {
	return userPresencePrefix + workspaceID + ":" + userID
}

func encodeProjection(effective domain.EffectivePresence) string {
	return string(effective.Availability) + userPresenceProjectSep + string(effective.Activity)
}

// decodeProjection reads a stored projection; anything malformed reads as
// absent here, and the projection script refuses to overwrite it.
func decodeProjection(value string) (*PresenceProjection, bool) {
	parts := strings.Split(value, userPresenceProjectSep)
	if len(parts) != 4 {
		return nil, false
	}
	seconds, errSeconds := strconv.ParseInt(parts[2], 10, 64)
	nanos, errNanos := strconv.ParseInt(parts[3], 10, 64)
	if errSeconds != nil || errNanos != nil || nanos < 0 || nanos >= int64(time.Second) {
		return nil, false
	}
	effective := domain.EffectivePresence{
		Availability: domain.PresenceAvailability(parts[0]), Activity: domain.PresenceActivity(parts[1]),
	}
	return &PresenceProjection{Effective: effective, At: time.Unix(seconds, nanos).UTC()}, true
}

func encodeUserReach(state PresenceStatus, at time.Time, generation uint64) string {
	return encodeDirectoryValue(state, at) + "|" + strconv.FormatUint(generation, 10)
}

// decodeUserReach reads one r: field: a directory value and its generation.
func decodeUserReach(userID, instanceID, value string) (DirectoryEntry, bool) {
	index := strings.LastIndex(value, "|")
	if index < 0 {
		return DirectoryEntry{}, false
	}
	generation, err := strconv.ParseUint(value[index+1:], 10, 64)
	if err != nil {
		return DirectoryEntry{}, false
	}
	entry, ok := decodeDirectoryEntry(directoryField(userID, instanceID), value[:index])
	entry.Generation = generation
	return entry, ok
}

func unixMillis(at time.Time) string {
	if at.IsZero() {
		return "0"
	}
	return strconv.FormatInt(at.UnixMilli(), 10)
}

var (
	userPresenceTTLArg    = strconv.FormatInt(int64(userPresenceTTL.Seconds()), 10)
	factsMarkReapLimitArg = strconv.Itoa(factsMarkReapLimit)
)

// AssertReach writes this instance's reach for one person in a lifecycle.
func (d *ValkeyPresenceDirectory) AssertReach(
	ctx context.Context, workspaceID, userID string, state PresenceStatus, at time.Time, generation uint64,
) (bool, error) {
	return d.writeReach(ctx, workspaceID, userID, encodeUserReach(state, at, generation), generation, "user reach assert")
}

// WithdrawReach removes this instance's reach for one person, and nobody
// else's, when it belongs to generation or an older lifecycle.
func (d *ValkeyPresenceDirectory) WithdrawReach(ctx context.Context, workspaceID, userID string, generation uint64) (bool, error) {
	return d.writeReach(ctx, workspaceID, userID, "", generation, "user reach withdraw")
}

func (d *ValkeyPresenceDirectory) writeReach(
	ctx context.Context, workspaceID, userID, value string, generation uint64, operation string,
) (bool, error) {
	applied, err := reachScript.Exec(ctx, d.client, []string{userPresenceKey(workspaceID, userID)}, []string{
		userReachFieldPrefix + d.instanceID, value, userPresenceTTLArg, strconv.FormatUint(generation, 10),
	}).AsInt64()
	if err != nil {
		return false, fmt.Errorf("ws: presence %s: %w", operation, err)
	}
	return applied == 1, nil
}

// BeginFactsChange marks a facts change in flight for each person.
func (d *ValkeyPresenceDirectory) BeginFactsChange(
	ctx context.Context, workspaceID string, userIDs []string, change FactsChange,
) error {
	return d.eachUser(ctx, workspaceID, userIDs, "facts change begin", beginChangeScript,
		userChangeFieldPrefix+change.Token, strconv.FormatInt(change.Lease.Milliseconds(), 10), userPresenceTTLArg, factsMarkReapLimitArg)
}

// EndFactsChange clears the mark BeginFactsChange left, moving the revision.
func (d *ValkeyPresenceDirectory) EndFactsChange(
	ctx context.Context, workspaceID string, userIDs []string, change FactsChange,
) error {
	return d.eachUser(ctx, workspaceID, userIDs, "facts change end", endChangeScript,
		userChangeFieldPrefix+change.Token, userPresenceTTLArg)
}

func (d *ValkeyPresenceDirectory) eachUser(
	ctx context.Context, workspaceID string, userIDs []string, operation string, script *valkey.Lua, args ...string,
) error {
	for _, userID := range userIDs {
		if err := script.Exec(ctx, d.client, []string{userPresenceKey(workspaceID, userID)}, args).Error(); err != nil {
			return fmt.Errorf("ws: presence %s: %w", operation, err)
		}
	}
	return nil
}

// ReadUsers reads several people in two round trips: their hashes in one
// pipeline, then the liveness of every instance named in them.
func (d *ValkeyPresenceDirectory) ReadUsers(
	ctx context.Context, workspaceID string, userIDs []string,
) (map[string]UserPresenceRecord, error) {
	if len(userIDs) == 0 {
		return map[string]UserPresenceRecord{}, nil
	}
	commands := make(valkey.Commands, 0, len(userIDs))
	for _, userID := range userIDs {
		commands = append(commands, d.client.B().Hgetall().Key(userPresenceKey(workspaceID, userID)).Build())
	}
	responses := d.client.DoMulti(ctx, commands...)
	if len(responses) != len(userIDs) {
		return nil, fmt.Errorf("ws: presence user read: %d replies for %d users", len(responses), len(userIDs))
	}
	raw := make(map[string]map[string]string, len(userIDs))
	for i, userID := range userIDs {
		fields, err := responses[i].AsStrMap()
		if err != nil {
			return nil, fmt.Errorf("ws: presence user read: %w", err)
		}
		raw[userID] = fields
	}
	return d.liveUserRecords(ctx, workspaceID, raw)
}

// liveUserRecords decodes the hashes read and keeps only reach that a live
// instance stands behind, reaping a bounded batch of the rest.
func (d *ValkeyPresenceDirectory) liveUserRecords(
	ctx context.Context, workspaceID string, raw map[string]map[string]string,
) (map[string]UserPresenceRecord, error) {
	instances := make(map[string]struct{}, 4)
	decoded := make(map[string]UserPresenceRecord, len(raw))
	for userID, fields := range raw {
		record := decodeUserRecord(userID, fields)
		for _, entry := range record.Reach {
			instances[entry.InstanceID] = struct{}{}
		}
		decoded[userID] = record
	}
	live, _, err := d.liveInstances(ctx, instances)
	if err != nil {
		return nil, err
	}
	reaped := 0
	for userID, record := range decoded {
		kept, dead := partitionByLiveness(record.Reach, live, d.instanceID)
		record.Reach = kept
		tried, removed := d.reapDeadReach(ctx, workspaceID, userID, dead, presenceDeadFieldReapLimit-reaped)
		reaped += tried
		if removed {
			// The reap moved the revision past the one read, for fields this
			// record already excludes. One past is never more than the
			// stored revision: any other change in between still conflicts.
			record.Revision++
		}
		decoded[userID] = record
	}
	return decoded, nil
}

// decodeUserRecord parses one person's hash. Malformed fields are skipped.
func decodeUserRecord(userID string, fields map[string]string) UserPresenceRecord {
	var record UserPresenceRecord
	for field, value := range fields {
		decodeUserField(&record, userID, field, value)
	}
	return record
}

func decodeUserField(record *UserPresenceRecord, userID, field, value string) {
	switch {
	case field == userProjectionField:
		record.Projection, _ = decodeProjection(value)
	case field == userRevisionField:
		record.Revision, _ = strconv.ParseUint(value, 10, 64)
	case strings.HasPrefix(field, userReachFieldPrefix):
		if entry, ok := decodeUserReach(userID, strings.TrimPrefix(field, userReachFieldPrefix), value); ok {
			record.Reach = append(record.Reach, entry)
		}
	}
}

// reapDeadReach deletes the reach of instances that are gone, at most budget of
// them, and reports how many it tried and whether it removed any. Best effort,
// like reapDeadFields: the record was already computed without them, and a
// composition that did count one cannot commit once its instance is dead
// (projectionScript).
func (d *ValkeyPresenceDirectory) reapDeadReach(
	ctx context.Context, workspaceID, userID string, dead []string, budget int,
) (int, bool) {
	if len(dead) == 0 || budget <= 0 {
		return 0, false
	}
	if len(dead) > budget {
		dead = dead[:budget]
	}
	fields := make([]string, 0, len(dead))
	for _, field := range dead {
		_, instanceID, _ := strings.Cut(field, "|")
		fields = append(fields, userReachFieldPrefix+instanceID)
	}
	removed, err := reapScript.Exec(ctx, d.client, []string{userPresenceKey(workspaceID, userID)}, fields).AsInt64()
	return len(dead), err == nil && removed > 0
}

// Project commits a composition against the revision, the timed facts and
// the instances it was read with.
func (d *ValkeyPresenceDirectory) Project(
	ctx context.Context, workspaceID, userID string, commit ProjectionCommit,
) (time.Time, projectionOutcome, error) {
	now := commit.Now.UTC() // the version instant only; validity is judged by the authority's clock
	keys := make([]string, 0, 1+len(commit.Instances))
	keys = append(keys, userPresenceKey(workspaceID, userID))
	for _, instanceID := range commit.Instances {
		keys = append(keys, directoryLivePrefix+instanceID)
	}
	reply, err := projectionScript.Exec(ctx, d.client, keys, []string{
		encodeProjection(commit.Effective),
		strconv.FormatInt(now.Unix(), 10),
		strconv.FormatInt(int64(now.Nanosecond()), 10),
		userPresenceTTLArg,
		encodeProjection(domain.EffectivePresence{Availability: domain.PresenceOffline}),
		strconv.FormatUint(commit.Expected, 10),
		unixMillis(commit.ValidUntil),
		factsMarkReapLimitArg,
	}).AsStrSlice()
	if err != nil {
		return time.Time{}, projectionUnchanged, fmt.Errorf("ws: presence projection: %w", err)
	}
	return parseProjectionReply(reply)
}

var projectionOutcomes = map[string]projectionOutcome{
	"unchanged": projectionUnchanged, "applied": projectionApplied,
	"conflict": projectionConflict, "expired": projectionExpired,
}

func parseProjectionReply(reply []string) (time.Time, projectionOutcome, error) {
	if len(reply) != 3 {
		return time.Time{}, projectionUnchanged, fmt.Errorf("ws: presence projection: unexpected reply of %d values", len(reply))
	}
	outcome, known := projectionOutcomes[reply[2]]
	seconds, errSeconds := strconv.ParseInt(reply[0], 10, 64)
	nanos, errNanos := strconv.ParseInt(reply[1], 10, 64)
	if !known || errSeconds != nil || errNanos != nil {
		return time.Time{}, projectionUnchanged, fmt.Errorf("ws: presence projection: malformed reply")
	}
	if seconds == 0 && nanos == 0 {
		// A conflict, an expiry, or an offline over nothing: no instant is held.
		return time.Time{}, outcome, nil
	}
	return time.Unix(seconds, nanos).UTC(), outcome, nil
}

// RefreshUsers renews the lease of the people this instance still serves.
func (d *ValkeyPresenceDirectory) RefreshUsers(ctx context.Context, workspaceID string, userIDs []string) error {
	if len(userIDs) == 0 {
		return nil
	}
	commands := make(valkey.Commands, 0, len(userIDs))
	for _, userID := range userIDs {
		commands = append(commands, d.client.B().Expire().Key(userPresenceKey(workspaceID, userID)).
			Seconds(int64(userPresenceTTL.Seconds())).Build())
	}
	return firstError("user lease refresh", d.client.DoMulti(ctx, commands...))
}

func firstError(operation string, responses []valkey.ValkeyResult) error {
	for _, resp := range responses {
		if err := resp.Error(); err != nil {
			return fmt.Errorf("ws: presence %s: %w", operation, err)
		}
	}
	return nil
}
