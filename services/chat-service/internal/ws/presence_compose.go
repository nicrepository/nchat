package ws

import (
	"context"
	"errors"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// Public presence (issue #798).
//
// The tracker and the directories answer one question — can this person be
// reached, and are they at the keyboard. What other people are shown is a
// different thing: that reach, combined with what the person chose (a manual
// state with an expiry) and what they are doing (a call), by
// domain.ResolvePresence. This file is where the two meet:
//
//   - the reach is the *cluster's*: every live instance's assertion under the
//     person's own key (UserPresenceStore), never whichever conversations one
//     session happened to subscribe to. This instance's own assertion is
//     written before it is read, so the answer includes it;
//   - the context is read from the database, one statement for however many
//     people are being answered for;
//   - the instant every event and every snapshot carries is the projection's
//     version: it moves when the effective presence moves and at no other
//     time, whichever instance or path observed the change;
//   - when any of those reads fails nothing is published and nothing is
//     concluded. The publication is kept as owed and the context sweep retries
//     it, so a failure costs latency and never a false "offline" or a last seen
//     nobody observed.

// presenceContextReadTimeout bounds the database read a publication or a roster
// waits on. A slow read costs one publication; it must not stall the fan-out.
const presenceContextReadTimeout = 2 * time.Second

// PresenceContextSource is what presence needs from the database: each user's
// live manual state and call participation, and somewhere to record when a user
// was last published as offline.
type PresenceContextSource interface {
	Contexts(ctx context.Context, workspaceID string, userIDs []string) (map[string]domain.PresenceContext, error)
	MarkLastSeen(ctx context.Context, workspaceID, userID string, at time.Time) error
}

// PresenceMetrics receives presence outcomes for observability. Labels are
// closed sets — an availability, an outcome — and never a user, a session or a
// workspace.
type PresenceMetrics interface {
	// PresenceTransition counts one published change of effective presence.
	PresenceTransition(availability string)
	// DisconnectGrace counts how a disconnect grace ended: "recovered" when a
	// connection came back inside it, "expired" when it did not.
	DisconnectGrace(outcome string)
	// PresenceDeferred counts a publication that could not be composed because
	// a source it needs did not answer; the context sweep retries it.
	PresenceDeferred(reason string)
}

// WithPresenceMetrics attaches presence observability. Optional.
func WithPresenceMetrics(metrics PresenceMetrics) HubOption {
	return func(h *Hub) { h.presenceMetrics = metrics }
}

type nopPresenceMetrics struct{}

func (nopPresenceMetrics) PresenceTransition(string) {}
func (nopPresenceMetrics) DisconnectGrace(string)    {}
func (nopPresenceMetrics) PresenceDeferred(string)   {}

func (h *Hub) metrics() PresenceMetrics {
	if h.presenceMetrics == nil {
		return nopPresenceMetrics{}
	}
	return h.presenceMetrics
}

// WithPresenceContext attaches the source of manual states and call activity.
// Without it presence is automatic only: nobody is busy, nobody is hidden.
func WithPresenceContext(source PresenceContextSource) HubOption {
	return func(h *Hub) { h.presenceContext = source }
}

// publishedPresence is this replica's memory about one person it serves.
//
// It is responsibility, not truth: the version the replica last published to
// its own rooms (so a version another replica wrote is still published here),
// the context it composed with (so the sweep can tell when the context moved),
// and any publication it still owes. It exists while the replica holds the
// person — a connection or a grace — or owes them a publication, and not a
// moment longer: another replica serving the same person answers for its own
// rooms.
type publishedPresence struct {
	at      time.Time
	context domain.PresenceContext
	// owed is set when a publication could not be composed; owedKeys is the
	// audience it still has to reach (a departed connection's rooms, which
	// nothing else remembers).
	owed     bool
	owedKeys []string
	// owedGeneration is the lifecycle an owed departure ended, which the
	// tracker no longer remembers and the fenced withdrawal needs.
	owedGeneration uint64
}

// reachFor maps the tracker's vocabulary onto the domain's.
func reachFor(status PresenceStatus) domain.PresenceReach {
	switch status {
	case PresenceOnline:
		return domain.PresenceReachActive
	case PresenceAway:
		return domain.PresenceReachIdle
	default:
		return domain.PresenceReachNone
	}
}

// legacyStateFor is the RF-58 state a client that predates issue #798 reads.
// Every availability maps onto the reachability it implies, so an older client
// keeps showing something true: busy and dnd people are online, brb and away
// people are away, hidden people are offline.
func legacyStateFor(availability domain.PresenceAvailability) PresenceStatus {
	switch availability {
	case domain.PresenceAvailable, domain.PresenceBusy, domain.PresenceDoNotDisturb:
		return PresenceOnline
	case domain.PresenceAway, domain.PresenceBeRightBack:
		return PresenceAway
	default:
		return PresenceOffline
	}
}

// presencePayloadFor renders an effective presence for the wire. Nothing else
// about the user — no reach, no override, no expiry, no session — is in it.
func presencePayloadFor(userID string, effective domain.EffectivePresence, at time.Time) PresencePayload {
	return PresencePayload{
		UserID:       userID,
		State:        string(legacyStateFor(effective.Availability)),
		Availability: string(effective.Availability),
		Activity:     string(effective.Activity),
		UpdatedAt:    formatPresenceTime(at),
	}
}

// presenceClock is the clock presence decisions are stamped with: the tracker's
// when there is one, so tests drive both from the same fake.
func (h *Hub) presenceClock() time.Time {
	if h.presence != nil {
		return h.presence.now()
	}
	return time.Now()
}

// localReach is what this instance's own connections say about a person,
// including a connection inside its disconnect grace.
func (h *Hub) localReach(pk presenceKey) PresenceStatus {
	if h.presence == nil {
		return PresenceOffline
	}
	status, _ := h.presence.StatusAt(pk.workspaceID, pk.userID)
	return status
}

// loadPresenceContexts reads the context of several users of one workspace.
// Without a source there is no context, which is a complete answer.
func (h *Hub) loadPresenceContexts(
	ctx context.Context, workspaceID string, userIDs []string,
) (map[string]domain.PresenceContext, error) {
	if h.presenceContext == nil || len(userIDs) == 0 {
		return map[string]domain.PresenceContext{}, nil
	}
	readCtx, cancel := context.WithTimeout(ctx, presenceContextReadTimeout)
	defer cancel()
	return h.presenceContext.Contexts(readCtx, workspaceID, userIDs)
}

// assertOwnReach writes this instance's reach for the subject of a change —
// the state the change carries, or nothing at all when it is a departure —
// fenced by the lifecycle the change belongs to. false: the shared authority
// already holds a newer lifecycle, and this change is no authority over it.
// Serialised per user with every other directory write this instance makes.
func (h *Hub) assertOwnReach(ctx context.Context, change presenceChange) (bool, error) {
	unlock := h.assertionLocks.lock(presenceKey{workspaceID: change.workspaceID, userID: change.userID})
	defer unlock()
	writeCtx, cancel := context.WithTimeout(ctx, directoryWriteTimeout)
	defer cancel()
	if change.status == PresenceOffline {
		return h.userPresence().WithdrawReach(writeCtx, change.workspaceID, change.userID, change.generation)
	}
	return h.userPresence().AssertReach(writeCtx, change.workspaceID, change.userID, change.status, change.at, change.generation)
}

// Reachability has one authority and one bridge (issue #798):
//
//   - modern: the person's own record (UserPresenceStore) — every live
//     instance's reach, this instance's own included as written: a lifecycle
//     this instance's tracker began is public only once its reach is in the
//     shared record, never before, so no replica — this one included — shows
//     a person the others cannot see. Target
//     rosters say who to deliver to in a conversation, never how reachable a
//     person is: a roster entry written by an instance that also keeps
//     per-user reach repeats, possibly stale, what the record already holds,
//     and it is not read here.
//   - legacy bridge: while an instance from before #798 may still serve
//     somebody (the rollout window, gate closed), its roster assertion is the
//     only evidence of that session. Only those entries — Legacy, from a live
//     instance whose liveness key lacks instanceCapability — are added, and
//     only to fill what is missing: they never stand for a modern session and
//     never remove anything. Off once the manual presence gate opens, which
//     requires no such instance to be left.

// clusterReach aggregates the person's reach — every live instance's,
// this one's included — and the legacy evidence the bridge found.
func clusterReach(record UserPresenceRecord, legacy []DirectoryEntry) PresenceStatus {
	status, _, ok := aggregatePresence(append(append([]DirectoryEntry{}, record.Reach...), legacy...))
	if !ok {
		return PresenceOffline
	}
	return status
}

// countedInstances are the other instances whose reach a composition counted:
// each must still be alive when it commits.
func (h *Hub) countedInstances(record UserPresenceRecord, legacy []DirectoryEntry) []string {
	seen := map[string]struct{}{h.presenceInstanceID: {}}
	var instances []string
	for _, entry := range append(append([]DirectoryEntry{}, record.Reach...), legacy...) {
		if _, dup := seen[entry.InstanceID]; dup {
			continue
		}
		seen[entry.InstanceID] = struct{}{}
		instances = append(instances, entry.InstanceID)
	}
	return instances
}

// factsValidUntil is when the earliest timed fact the composition used stops
// holding — the lease behind an activity it counted, the end of the manual
// state it counted — zero for none. Every fact used is bounded, however close
// its end, and none is dropped here for looking over on this process's clock:
// the read found it in force, and whether it still is when published is the
// store's to judge at the commit, on its own clock.
func factsValidUntil(presenceCtx domain.PresenceContext) time.Time {
	var until time.Time
	if presenceCtx.Activity != domain.PresenceActivityNone {
		until = earliest(until, presenceCtx.ActivityUntil)
	}
	if presenceCtx.Override.State != "" {
		until = earliest(until, presenceCtx.Override.ExpiresAt)
	}
	return until
}

// earliest is the earlier of two instants, a zero one meaning none.
func earliest(a, b time.Time) time.Time {
	if a.IsZero() || (!b.IsZero() && b.Before(a)) {
		return b
	}
	return a
}

// legacyEvidence keeps, of a roster's entries about one person, the ones only
// an instance from before #798 stands behind — and none while the bridge is
// off.
func (h *Hub) legacyEvidence(entries []DirectoryEntry, userID string) []DirectoryEntry {
	if h.legacyBridgeDisabled {
		return nil
	}
	var legacy []DirectoryEntry
	for _, entry := range entries {
		if entry.Legacy && entry.UserID == userID && entry.InstanceID != h.presenceInstanceID {
			legacy = append(legacy, entry)
		}
	}
	return legacy
}

// legacyReach is the bridge for a publication: the legacy evidence about the
// subject in one of their rosters. A failed or empty read adds nothing.
func (h *Hub) legacyReach(ctx context.Context, pk presenceKey, keys []string) []DirectoryEntry {
	if h.legacyBridgeDisabled || h.directory == nil || len(keys) == 0 {
		return nil
	}
	readCtx, cancel := context.WithTimeout(ctx, directoryReadTimeout)
	entries, err := h.directory.Present(readCtx, keys[0])
	cancel()
	if err != nil {
		return nil
	}
	return h.legacyEvidence(entries, pk.userID)
}

// transitionCurrent is whether the tracker still says what a change says. A
// change is a decision taken under the tracker's lock and acted on after it
// was released; a connection arriving in between makes it stale, and a stale
// departure is no authority to withdraw reach, publish offline or record a
// last seen. Whatever moved the tracker queued a change of its own, which
// carries the current answer.
func (h *Hub) transitionCurrent(change presenceChange) bool {
	return h.presence == nil || h.localReach(presenceKey{workspaceID: change.workspaceID, userID: change.userID}) == change.status
}

// currentPresenceChange brings a change taken from the queue up to the
// tracker's answer. Coalescing keeps the newest change per person, and a
// grace that expired just before a reconnection can still be queued after
// it; acting on its state would drop the reconnection's.
func (h *Hub) currentPresenceChange(change presenceChange) presenceChange {
	if h.presence == nil {
		return change
	}
	current := h.presence.Lifecycle(change.workspaceID, change.userID)
	if current.Status != change.status {
		change.status, change.at, change.derive = current.Status, current.At, true
	}
	// An offline's lifecycle is the one it ended, which the tracker has
	// already forgotten; anything else belongs to the current one.
	if current.Status != PresenceOffline {
		change.generation = current.Generation
	}
	return change
}

// projectionAttempts bounds how often a composition is redone because its
// facts moved before it could commit. Past it the publication is owed, and the
// next sweep composes it again; nothing stale is published meanwhile.
const projectionAttempts = 3

var (
	errPresenceSuperseded = errors.New("presence change superseded")
	errProjectionConflict = errors.New("presence facts kept moving")
)

// composed is one committed answer about a person.
type composed struct {
	effective domain.EffectivePresence
	context   domain.PresenceContext
	reach     PresenceStatus
	at        time.Time
	outcome   projectionOutcome
}

// retryable is whether a commit lost to newer or lapsed facts.
func (c composed) retryable() bool {
	return c.outcome == projectionConflict || c.outcome == projectionExpired
}

// commitComposition reads the person's facts at a revision, resolves them, and
// commits the result against that revision — again, a bounded number of
// times, when somebody else's facts landed first, a timed fact lapsed, or a
// facts change was in flight. It returns the reason to defer with when it
// fails, and errPresenceSuperseded when the change itself went stale.
func (h *Hub) commitComposition(
	ctx context.Context, change presenceChange, legacy []DirectoryEntry,
) (composed, string, error) {
	for range projectionAttempts {
		result, reason, err := h.composeOnce(ctx, change, legacy)
		if err != nil || !result.retryable() {
			return result, reason, err
		}
	}
	return composed{}, "projection_conflict", errProjectionConflict
}

// composeOnce is one attempt: the record (with its revision) before the
// database, the tracker checked last, then the commit with every bound the
// facts carried.
func (h *Hub) composeOnce(ctx context.Context, change presenceChange, legacy []DirectoryEntry) (composed, string, error) {
	pk := presenceKey{workspaceID: change.workspaceID, userID: change.userID}
	record, err := h.readUserRecord(ctx, pk)
	if err != nil {
		return composed{}, "reach_read", err
	}
	reach := clusterReach(record, legacy)
	effective, presenceCtx, err := h.resolveEffective(ctx, pk, reach)
	if err != nil {
		return composed{}, "context_read", err
	}
	if !h.transitionCurrent(change) {
		return composed{}, "", errPresenceSuperseded
	}
	at, outcome, err := h.userPresence().Project(ctx, pk.workspaceID, pk.userID, ProjectionCommit{
		Effective: effective, Expected: record.Revision, Now: h.presenceClock(),
		ValidUntil: factsValidUntil(presenceCtx), Instances: h.countedInstances(record, legacy),
	})
	if err != nil {
		return composed{}, "projection", err
	}
	return composed{effective: effective, context: presenceCtx, reach: reach, at: at, outcome: outcome}, "", nil
}

// readUserRecord reads one person's record.
func (h *Hub) readUserRecord(ctx context.Context, pk presenceKey) (UserPresenceRecord, error) {
	readCtx, cancel := context.WithTimeout(ctx, directoryReadTimeout)
	defer cancel()
	records, err := h.userPresence().ReadUsers(readCtx, pk.workspaceID, []string{pk.userID})
	return records[pk.userID], err
}

// publicPresence composes what observers should be told about the subject of a
// change, and whether this replica should tell its rooms.
//
// Every step that can fail defers instead of guessing: an unwritable or
// unreadable reach could turn a replica's last local disconnect into a false
// "offline", and an unreadable manual state could publish somebody who chose to
// appear offline. A change the tracker no longer agrees with does nothing at
// all — before its reach is written and again before anything is published.
func (h *Hub) publicPresence(ctx context.Context, change presenceChange, keys []string) (PresencePayload, bool) {
	pk := presenceKey{workspaceID: change.workspaceID, userID: change.userID}
	if !h.transitionCurrent(change) {
		return PresencePayload{}, false
	}
	written, err := h.assertOwnReach(ctx, change)
	if err != nil {
		return h.deferPresence(ctx, change, keys, "reach_write", err)
	}
	if !written {
		return PresencePayload{}, false
	}
	result, reason, err := h.commitComposition(ctx, change, h.legacyReach(ctx, pk, keys))
	if errors.Is(err, errPresenceSuperseded) {
		return PresencePayload{}, false
	}
	if err != nil {
		return h.deferPresence(ctx, change, keys, reason, err)
	}
	if !h.transitionCurrent(change) {
		return PresencePayload{}, false
	}
	publish := h.recordPublication(pk, result.effective, result.at, result.outcome == projectionApplied, result.context, change.announce)
	if departed(result.effective, result.at, result.reach) {
		h.markLastSeen(ctx, pk, result.at)
	}
	if !publish {
		return PresencePayload{}, false
	}
	h.metrics().PresenceTransition(string(result.effective.Availability))
	return presencePayloadFor(pk.userID, result.effective, result.at), true
}

// resolveEffective reads the person's context, when their reach makes it
// matter, and resolves their effective presence.
func (h *Hub) resolveEffective(
	ctx context.Context, pk presenceKey, reach PresenceStatus,
) (domain.EffectivePresence, domain.PresenceContext, error) {
	var presenceCtx domain.PresenceContext
	if reachFor(reach).Connected() {
		contexts, err := h.loadPresenceContexts(ctx, pk.workspaceID, []string{pk.userID})
		if err != nil {
			return domain.EffectivePresence{}, domain.PresenceContext{}, err
		}
		presenceCtx = contexts[pk.userID]
	}
	return domain.ResolvePresence(reachFor(reach), presenceCtx), presenceCtx, nil
}

// recordPublication updates this replica's memory and decides whether its
// rooms are told.
//
// They are told when the projection moved, when it carries a version this
// replica has not published yet (another replica moved it), or when a room
// has just been joined. They are not told about an unchanged offline this
// replica never published: that is a hidden person reconnecting, and even a
// repeat of their offline would tell observers something happened.
func (h *Hub) recordPublication(
	pk presenceKey, effective domain.EffectivePresence, at time.Time, changed bool,
	presenceCtx domain.PresenceContext, announce bool,
) bool {
	retain := h.localReach(pk) != PresenceOffline
	h.publishedMu.Lock()
	defer h.publishedMu.Unlock()

	previous, known := h.published[pk]
	visible := effective.Availability != domain.PresenceOffline
	publish := changed || (known && !previous.at.Equal(at)) || (!known && visible) || (announce && visible)
	if !retain {
		delete(h.published, pk)
		return publish
	}
	if h.published == nil {
		h.published = make(map[presenceKey]publishedPresence)
	}
	h.published[pk] = publishedPresence{at: at, context: presenceCtx}
	return publish
}

// deferPresence keeps a publication that could not be composed as owed, with
// the audience it still has to reach, for the context sweep to retry.
func (h *Hub) deferPresence(
	ctx context.Context, change presenceChange, keys []string, reason string, err error,
) (PresencePayload, bool) {
	h.logger.WarnContext(ctx, "ws: presence publication deferred", "reason", reason, "error", err)
	h.metrics().PresenceDeferred(reason)
	pk := presenceKey{workspaceID: change.workspaceID, userID: change.userID}
	h.publishedMu.Lock()
	defer h.publishedMu.Unlock()
	if h.published == nil {
		h.published = make(map[presenceKey]publishedPresence)
	}
	entry := h.published[pk]
	entry.owed = true
	entry.owedKeys = mergeKeys(entry.owedKeys, keys)
	entry.owedGeneration = max(entry.owedGeneration, change.generation)
	h.published[pk] = entry
	return PresencePayload{}, false
}

// mergeKeys is the union of two key lists, in first-seen order.
func mergeKeys(a, b []string) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	merged := make([]string, 0, len(a)+len(b))
	for _, key := range append(append([]string{}, a...), b...) {
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		merged = append(merged, key)
	}
	return merged
}

// publishedState is the memory for one user, if any.
func (h *Hub) publishedState(pk presenceKey) (publishedPresence, bool) {
	h.publishedMu.Lock()
	defer h.publishedMu.Unlock()
	entry, ok := h.published[pk]
	return entry, ok
}

// departed is the authoritative evidence for last seen: the public presence is
// offline and no live instance serves a session of the person. A person who
// appeared offline first was last seen publicly when they hid — at is then the
// unchanged version of that projection, so the record and the event agree, and
// nobody can tell from them that the person stayed connected. A zero at is a
// person who was never shown, and has no public last seen to record.
func departed(effective domain.EffectivePresence, at time.Time, reach PresenceStatus) bool {
	return effective.Availability == domain.PresenceOffline && !at.IsZero() && !reachFor(reach).Connected()
}

// markLastSeen records the instant a person was last seen publicly, once no
// session is left anywhere. It is the server's instant, written by the server,
// and never on a departure only one replica observed.
func (h *Hub) markLastSeen(ctx context.Context, pk presenceKey, at time.Time) {
	if h.presenceContext == nil {
		return
	}
	writeCtx, cancel := context.WithTimeout(ctx, presenceContextReadTimeout)
	defer cancel()
	if err := h.presenceContext.MarkLastSeen(writeCtx, pk.workspaceID, pk.userID, at); err != nil {
		h.logger.WarnContext(ctx, "ws: recording last seen failed", "error", err)
	}
}

// PublicLastSeen is the instant observers were told a person went offline —
// the version of their offline projection — and whether they are offline at
// all. It is what a profile may say about when somebody was last seen, and it
// is the same instant the realtime event carried, whether the person left or
// chose to appear offline.
func (h *Hub) PublicLastSeen(ctx context.Context, workspaceID, userID string) (time.Time, bool, error) {
	readCtx, cancel := context.WithTimeout(ctx, directoryReadTimeout)
	defer cancel()
	records, err := h.userPresence().ReadUsers(readCtx, workspaceID, []string{userID})
	if err != nil {
		return time.Time{}, false, err
	}
	projection := records[userID].Projection
	if projection == nil || projection.Effective.Availability != domain.PresenceOffline {
		return time.Time{}, false, nil
	}
	return projection.At, true, nil
}

// composeRoster turns a target roster into the roster observers are shown,
// dropping everybody whose public state is offline.
//
// Membership comes from the target; reach, context and version come from each
// person, exactly as for an event, so a snapshot says what an event about the
// same person says. Each person's projection is settled — committed when the
// snapshot is the first to see it move — before deciding whether they are
// shown: somebody hidden is left out of the roster only after their public
// presence says offline. One record read and one context read for the whole
// roster, the records first so a context written after them conflicts. A
// failed read fails the roster: it could include somebody who chose to appear
// offline, and publishing them because a store was slow would be exactly the
// leak the choice exists to prevent.
func (h *Hub) composeRoster(
	ctx context.Context, workspaceID string, users []PresencePayload, entries []DirectoryEntry,
) ([]PresencePayload, error) {
	ids := make([]string, len(users))
	for i, user := range users {
		ids[i] = user.UserID
	}
	records, err := h.userPresence().ReadUsers(ctx, workspaceID, ids)
	if err != nil {
		return nil, err
	}
	contexts, err := h.loadPresenceContexts(ctx, workspaceID, ids)
	if err != nil {
		return nil, err
	}
	shown := make([]PresencePayload, 0, len(users))
	for _, id := range ids {
		pk := presenceKey{workspaceID: workspaceID, userID: id}
		member := rosterMember{record: records[id], context: contexts[id], legacy: h.legacyEvidence(entries, id)}
		projection, err := h.settleRosterMember(ctx, pk, member)
		if err != nil {
			return nil, err
		}
		if projection != nil && projection.Effective.Availability != domain.PresenceOffline {
			shown = append(shown, presencePayloadFor(id, projection.Effective, projection.At))
		}
	}
	return shown, nil
}

// rosterMember is what a snapshot read about one person.
type rosterMember struct {
	record  UserPresenceRecord
	context domain.PresenceContext
	legacy  []DirectoryEntry
}

// settleRosterMember resolves one person and commits their projection the way
// a publication does. When their facts keep moving under it — or a change is in
// flight — the snapshot reports what is committed, the answer somebody else
// published, never its own stale composition. nil is a person with no public
// presence.
func (h *Hub) settleRosterMember(ctx context.Context, pk presenceKey, member rosterMember) (*PresenceProjection, error) {
	for range projectionAttempts {
		projection, settled, err := h.commitRosterMember(ctx, pk, member)
		if err != nil || settled {
			return projection, err
		}
		if member, err = h.rereadRosterMember(ctx, pk, member); err != nil {
			return nil, err
		}
	}
	return member.record.Projection, nil
}

// commitRosterMember is one attempt; settled is false when it must be redone.
func (h *Hub) commitRosterMember(ctx context.Context, pk presenceKey, member rosterMember) (*PresenceProjection, bool, error) {
	reach := clusterReach(member.record, member.legacy)
	effective := domain.ResolvePresence(reachFor(reach), member.context)
	at, outcome, err := h.userPresence().Project(ctx, pk.workspaceID, pk.userID, ProjectionCommit{
		Effective: effective, Expected: member.record.Revision, Now: h.presenceClock(),
		ValidUntil: factsValidUntil(member.context), Instances: h.countedInstances(member.record, member.legacy),
	})
	if err != nil || (composed{outcome: outcome}).retryable() {
		return nil, false, err
	}
	return &PresenceProjection{Effective: effective, At: at}, true, nil
}

// rereadRosterMember reads one person again after a conflict.
func (h *Hub) rereadRosterMember(ctx context.Context, pk presenceKey, member rosterMember) (rosterMember, error) {
	record, err := h.readUserRecord(ctx, pk)
	if err != nil {
		return member, err
	}
	contexts, err := h.loadPresenceContexts(ctx, pk.workspaceID, []string{pk.userID})
	if err != nil {
		return member, err
	}
	member.record, member.context = record, contexts[pk.userID]
	return member, nil
}

// presenceAvailabilityValues and presenceActivityValues are the closed sets a
// remote presence event may carry. Empty is allowed for both: a producer that
// predates issue #798 sends neither.
var presenceAvailabilityValues = map[string]struct{}{
	"": {}, string(domain.PresenceAvailable): {}, string(domain.PresenceBusy): {},
	string(domain.PresenceDoNotDisturb): {}, string(domain.PresenceBeRightBack): {},
	string(domain.PresenceAway): {}, string(domain.PresenceOffline): {},
}

var presenceActivityValues = map[string]struct{}{
	"": {}, string(domain.PresenceActivityInCall): {},
	string(domain.PresenceActivityInMeeting): {}, string(domain.PresenceActivityPresenting): {},
}

// validPresenceComposition checks the issue #798 fields of a remote presence:
// each in its closed set, the availability agreeing with the legacy state it
// travels with, and an activity only where it may be public. A producer cannot
// make an offline person look busy, nor attach a call to somebody hidden.
func validPresenceComposition(payload PresencePayload) bool {
	if _, ok := presenceAvailabilityValues[payload.Availability]; !ok {
		return false
	}
	if _, ok := presenceActivityValues[payload.Activity]; !ok {
		return false
	}
	if payload.Availability == "" {
		return payload.Activity == ""
	}
	availability := domain.PresenceAvailability(payload.Availability)
	if string(legacyStateFor(availability)) != payload.State {
		return false
	}
	return payload.Activity == "" ||
		availability == domain.PresenceBusy || availability == domain.PresenceDoNotDisturb
}
