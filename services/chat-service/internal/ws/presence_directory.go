package ws

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	valkey "github.com/valkey-io/valkey-go"
)

// PresenceDirectory is the cluster-wide answer to "who is present in this
// target" (RF-58).
//
// It exists because a single chat-service instance cannot answer that question
// once there is more than one of them. Each instance knows only its own
// connections, so the roster it builds from them is complete for itself and
// silently partial for the cluster — and a partial roster presented as complete
// is what turns "I have not heard about this person" into "this person is
// offline".
//
// It holds the *current* state and nothing else: no history, no sessions, no
// devices. It is a cache of something that is already ephemeral, and every entry
// is disposable — losing it costs a snapshot, never a fact the system needed to
// keep.
type PresenceDirectory interface {
	// Record marks a user present in each of the given target keys.
	Record(ctx context.Context, entry DirectoryEntry, keys []string) error
	// Forget removes a user from each of the given target keys.
	Forget(ctx context.Context, workspaceID, userID string, keys []string) error
	// Present returns the users currently present in one target key.
	Present(ctx context.Context, key string) ([]DirectoryEntry, error)
	// Refresh renews the lease on target keys this instance still asserts into,
	// without rewriting the assertions themselves.
	Refresh(ctx context.Context, keys []string) error
	// Heartbeat renews this instance's liveness. Entries written by an instance
	// that has stopped renewing are ignored by Present.
	Heartbeat(ctx context.Context) error
	// Close releases resources.
	Close()
}

// DirectoryEntry is one *assertion* about one user, made by one instance.
//
// It is deliberately not "one user's presence": a person connected through two
// replicas is described by two assertions, and collapsing them into a single
// record is what made one node's disconnect erase the other node's live user.
// The aggregate is computed at read time, by aggregatePresence.
//
// Four fields, and the omissions are the contract: no session, no device, no
// address, no name. A directory that could answer "which devices is this person
// on" would be a different, and much more sensitive, thing than one that answers
// "can I reach this person".
type DirectoryEntry struct {
	UserID string
	State  PresenceStatus
	At     time.Time
	// InstanceID is the instance that asserted this, so a reader can tell
	// whether anybody still stands behind it — and so that two instances
	// asserting about the same person do not overwrite each other.
	InstanceID string
}

// aggregatePresence reduces every live assertion about one user to the state
// that person is actually in.
//
// The rule is semantic, not chronological: online beats away beats offline,
// because a user with any active connection *is* reachable, whatever some other
// replica last observed about a different tab. Taking the newest assertion
// instead would let an idle laptop going away five seconds ago override a phone
// that is being typed on.
//
// Timestamps still matter, but only inside a state: the instant reported is the
// one belonging to the assertion that won, so the client's ordering rule keeps
// working and a re-read that changes nothing reports the same instant.
func aggregatePresence(entries []DirectoryEntry) (PresenceStatus, time.Time, bool) {
	var best DirectoryEntry
	found := false
	for _, entry := range entries {
		switch {
		case !found:
		case presenceRank(entry.State) > presenceRank(best.State):
		case presenceRank(entry.State) == presenceRank(best.State) && entry.At.After(best.At):
		default:
			continue
		}
		best = entry
		found = true
	}
	if !found {
		return PresenceOffline, time.Time{}, false
	}
	return best.State, best.At, true
}

// presenceRank orders the states by how much reachability they assert.
func presenceRank(state PresenceStatus) int {
	switch state {
	case PresenceOnline:
		return 2
	case PresenceAway:
		return 1
	default:
		return 0
	}
}

// directoryEntryTTL is the lease on a target's roster.
//
// It is a lease and not a lifetime: Valkey expires a *key*, not a field, so this
// governs the whole target hash, and it is renewed both by every write and by
// the instance heartbeat for as long as this instance still asserts anything
// into that target. Without the heartbeat half, a conversation where everybody
// is connected and simply not changing state would quietly expire and its
// occupants would read as absent — presence is not traffic, and a user can be
// online for a working day without producing a single transition.
//
// Renewing the key does extend fields belonging to instances that have since
// died. Liveness, not expiry, is what decides whether an assertion counts, so
// such a field is ignored on every read no matter how long the key survives —
// but it is not left there: see reapDeadFields.
const directoryEntryTTL = 6 * time.Hour

// presenceDeadFieldReapLimit caps how many dead assertions one read removes.
//
// A busy conversation that outlived several deployments can hold more dead
// fields than anyone wants in a single pipeline, and there is no hurry: the
// entries are already excluded from the roster, and each subsequent read removes
// another batch until none are left. The bound is on the cleanup, never on the
// roster, which is always computed from everything the read returned.
const presenceDeadFieldReapLimit = 64

// instanceLivenessTTL and instanceHeartbeatInterval decide how quickly a dead
// instance stops being believed.
//
// The heartbeat is the whole staleness mechanism, and it is one key per
// instance rather than one per user or per target: an instance that dies takes
// all of its assertions with it in one step, at a cost that does not grow with
// how many people it was serving. The TTL is three heartbeats, so a single
// missed renewal — a slow round trip, a brief Valkey blip — does not evict a
// healthy instance's users.
const (
	instanceLivenessTTL       = 90 * time.Second
	instanceHeartbeatInterval = 30 * time.Second
)

const (
	directoryKeyPrefix  = "nchat:chat:ws:presence:"
	directoryLivePrefix = "nchat:chat:ws:instance:"
)

// ValkeyPresenceDirectory implements PresenceDirectory on Valkey.
//
// Layout, one hash per target:
//
//	nchat:chat:ws:presence:{workspace}:{targetType}:{targetID}
//	  field: {user id}|{instance id}
//	  value: state|unixNano
//
// The field is the *assertion*, not the person, and that is the whole
// correction: with a bare user id, an instance writing about someone connected
// to it overwrote whatever another instance had said about the same person, and
// its HDEL on disconnect deleted a live connection somebody else was still
// serving. Keyed by (user, instance) the two coexist, each instance owns exactly
// its own field, and the person's actual state is the aggregate of what is left
// (see aggregatePresence).
//
// A hash rather than a set because an assertion is replaced, not accumulated: a
// second write from the same instance about the same person overwrites the
// first. The key is namespaced like every other Valkey key this service owns,
// and its variable parts are canonical UUIDs validated before they get here.
type ValkeyPresenceDirectory struct {
	client     valkey.Client
	instanceID string
}

// NewValkeyPresenceDirectory connects to Valkey for shared presence state.
func NewValkeyPresenceDirectory(valkeyURL, instanceID string) (*ValkeyPresenceDirectory, error) {
	if valkeyURL == "" {
		return nil, errors.New("ws: presence directory requires VALKEY_URL")
	}
	if instanceID == "" {
		return nil, errors.New("ws: presence directory requires an instance ID")
	}
	option, err := valkey.ParseURL(valkeyURL)
	if err != nil {
		return nil, fmt.Errorf("ws: parse presence directory valkey URL: %w", err)
	}
	client, err := valkey.NewClient(option)
	if err != nil {
		return nil, fmt.Errorf("ws: create presence directory client: %w", err)
	}
	return &ValkeyPresenceDirectory{client: client, instanceID: instanceID}, nil
}

func (d *ValkeyPresenceDirectory) Close() { d.client.Close() }

// Record writes one user into every target's roster, in a single pipeline.
//
// One round trip for all of a user's rooms, not one per room: presence changes
// arrive in bursts — a mass reconnect is every user announcing into every
// conversation they have — and a per-room call would turn that into a storm.
func (d *ValkeyPresenceDirectory) Record(ctx context.Context, entry DirectoryEntry, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	field := directoryField(entry.UserID, d.instanceID)
	value := encodeDirectoryValue(entry.State, entry.At)
	commands := make(valkey.Commands, 0, len(keys)*2)
	for _, key := range keys {
		redisKey := directoryKeyPrefix + key
		commands = append(commands,
			d.client.B().Hset().Key(redisKey).FieldValue().FieldValue(field, value).Build(),
			d.client.B().Expire().Key(redisKey).Seconds(int64(directoryEntryTTL.Seconds())).Build(),
		)
	}
	for _, resp := range d.client.DoMulti(ctx, commands...) {
		if err := resp.Error(); err != nil {
			return fmt.Errorf("ws: presence directory record: %w", err)
		}
	}
	return nil
}

// Forget removes *this instance's* assertion about a user from every target's
// roster, and never anybody else's.
//
// That restriction is the point. When one replica loses a user's connection it
// has learned something about its own connection and nothing at all about the
// other replicas', so deleting a field it does not own would report a person
// offline while they are still connected somewhere else.
//
// It is the immediate half of convergence: a clean disconnect deletes the
// assertion at once rather than waiting for anything to expire. The liveness
// key is the other half, for the disconnects that are never clean.
func (d *ValkeyPresenceDirectory) Forget(ctx context.Context, _, userID string, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	field := directoryField(userID, d.instanceID)
	commands := make(valkey.Commands, 0, len(keys))
	for _, key := range keys {
		commands = append(commands, d.client.B().Hdel().Key(directoryKeyPrefix+key).Field(field).Build())
	}
	for _, resp := range d.client.DoMulti(ctx, commands...) {
		if err := resp.Error(); err != nil {
			return fmt.Errorf("ws: presence directory forget: %w", err)
		}
	}
	return nil
}

// Refresh renews the lease on the given target keys.
//
// One pipelined EXPIRE per distinct target — not per user and not per assertion
// — so the cost of keeping a busy instance's rosters alive is the number of
// conversations it serves, once per heartbeat. Nothing is rewritten: the
// assertions already say what they say, and re-HSETting them would be write
// amplification for no new information.
func (d *ValkeyPresenceDirectory) Refresh(ctx context.Context, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	commands := make(valkey.Commands, 0, len(keys))
	for _, key := range keys {
		commands = append(commands, d.client.B().Expire().
			Key(directoryKeyPrefix+key).
			Seconds(int64(directoryEntryTTL.Seconds())).
			Build())
	}
	for _, resp := range d.client.DoMulti(ctx, commands...) {
		if err := resp.Error(); err != nil {
			return fmt.Errorf("ws: presence directory refresh: %w", err)
		}
	}
	return nil
}

// Present reads one target's roster and drops whatever no live instance stands
// behind.
//
// Two round trips, both bounded: the roster of one target, then the liveness of
// the handful of instances named in it. No scan, no per-user lookup, and nothing
// proportional to the size of the workspace.
func (d *ValkeyPresenceDirectory) Present(ctx context.Context, key string) ([]DirectoryEntry, error) {
	raw, err := d.client.Do(ctx, d.client.B().Hgetall().Key(directoryKeyPrefix+key).Build()).AsStrMap()
	if err != nil {
		return nil, fmt.Errorf("ws: presence directory read: %w", err)
	}
	if len(raw) == 0 {
		return nil, nil
	}

	entries := make([]DirectoryEntry, 0, len(raw))
	instances := make(map[string]struct{}, 4)
	for field, value := range raw {
		entry, ok := decodeDirectoryEntry(field, value)
		if !ok {
			continue
		}
		entries = append(entries, entry)
		instances[entry.InstanceID] = struct{}{}
	}

	live, err := d.liveInstances(ctx, instances)
	if err != nil {
		return nil, err
	}
	kept, dead := partitionByLiveness(entries, live, d.instanceID)
	if len(dead) > 0 {
		d.reapDeadFields(ctx, key, dead)
	}
	return kept, nil
}

// reapDeadFields deletes the assertions of instances that are gone.
//
// The read above already had to fetch the whole hash and resolve the liveness of
// every instance named in it, so which fields are dead is knowledge this call
// holds anyway; removing them costs one pipelined HDEL of names already in hand.
// Nothing is scanned and no key is enumerated.
//
// Without it those fields are correct but immortal: they are excluded from every
// roster, and the target key's lease is renewed by everyone still in the
// conversation, so the hash grows by one field per process that ever served a
// member and never shrinks. That is safe only because a field belongs to one
// process incarnation, which no restart and no second pod can reuse — the
// instance whose fields these are cannot come back to find them missing.
//
// It is an optimization, and failing it is not failing the read: the roster was
// already computed and is already correct. The next Present tries again.
func (d *ValkeyPresenceDirectory) reapDeadFields(ctx context.Context, key string, fields []string) {
	commands := make(valkey.Commands, 0, len(fields))
	for _, field := range fields {
		commands = append(commands, d.client.B().Hdel().Key(directoryKeyPrefix+key).Field(field).Build())
	}
	for _, resp := range d.client.DoMulti(ctx, commands...) {
		if resp.Error() != nil {
			return
		}
	}
}

// partitionByLiveness splits a target's assertions into the ones that still
// count and the field names of the ones whose writer is gone.
//
// Shared by the real directory and its test double so the rule that decides a
// field is dead — and the cap that keeps one cleanup bounded — is written once
// and exercised, rather than being duplicated into a fake that could agree with
// a mistake.
func partitionByLiveness(
	entries []DirectoryEntry, live map[string]struct{}, self string,
) (kept []DirectoryEntry, dead []string) {
	kept = entries[:0]
	for _, entry := range entries {
		// This instance's own assertions need no liveness check — it is running.
		if entry.InstanceID == self {
			kept = append(kept, entry)
			continue
		}
		if _, alive := live[entry.InstanceID]; alive {
			kept = append(kept, entry)
			continue
		}
		if len(dead) < presenceDeadFieldReapLimit {
			dead = append(dead, directoryField(entry.UserID, entry.InstanceID))
		}
	}
	return kept, dead
}

func (d *ValkeyPresenceDirectory) liveInstances(ctx context.Context, instances map[string]struct{}) (map[string]struct{}, error) {
	ids := make([]string, 0, len(instances))
	for id := range instances {
		if id != "" && id != d.instanceID {
			ids = append(ids, directoryLivePrefix+id)
		}
	}
	if len(ids) == 0 {
		return map[string]struct{}{}, nil
	}
	values, err := d.client.Do(ctx, d.client.B().Mget().Key(ids...).Build()).ToArray()
	if err != nil {
		return nil, fmt.Errorf("ws: presence directory liveness: %w", err)
	}
	live := make(map[string]struct{}, len(ids))
	for index, value := range values {
		if value.IsNil() {
			continue
		}
		live[strings.TrimPrefix(ids[index], directoryLivePrefix)] = struct{}{}
	}
	return live, nil
}

// Heartbeat renews this instance's liveness key.
func (d *ValkeyPresenceDirectory) Heartbeat(ctx context.Context) error {
	err := d.client.Do(ctx, d.client.B().Set().
		Key(directoryLivePrefix+d.instanceID).
		Value("1").
		ExSeconds(int64(instanceLivenessTTL.Seconds())).
		Build()).Error()
	if err != nil {
		return fmt.Errorf("ws: presence directory heartbeat: %w", err)
	}
	return nil
}

// directoryField names one instance's assertion about one user. The separator
// cannot occur in either part: a user id is a UUID and an instance id is
// validated against a conservative character set before the process starts.
func directoryField(userID, instanceID string) string {
	return userID + "|" + instanceID
}

func encodeDirectoryValue(state PresenceStatus, at time.Time) string {
	return string(state) + "|" + strconv.FormatInt(at.UnixNano(), 10)
}

// decodeDirectoryEntry parses one stored assertion, rejecting anything that is
// not the shape this service writes. A malformed entry is dropped rather than
// guessed at: a roster is easier to be missing from than to be wrong in.
func decodeDirectoryEntry(field, value string) (DirectoryEntry, bool) {
	userID, instanceID, ok := strings.Cut(field, "|")
	if !ok || userID == "" || instanceID == "" {
		return DirectoryEntry{}, false
	}
	parts := strings.SplitN(value, "|", 2)
	if len(parts) != 2 {
		return DirectoryEntry{}, false
	}
	state := PresenceStatus(parts[0])
	if _, known := presenceStateValues[string(state)]; !known || state == PresenceOffline {
		return DirectoryEntry{}, false
	}
	nanos, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return DirectoryEntry{}, false
	}
	return DirectoryEntry{
		UserID:     userID,
		State:      state,
		At:         time.Unix(0, nanos).UTC(),
		InstanceID: instanceID,
	}, true
}
