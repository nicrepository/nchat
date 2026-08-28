package ws

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

// Reconciliation of active targets (RF-58).
//
// Everything else in this feature is event-driven, and events have one thing
// they structurally cannot do: arrive from a process that is gone. When a
// replica is killed, nothing runs on it to say its users left, so an observer
// on another replica keeps the last thing it was told — forever, since presence
// only changes when somebody sends a change. Expiring the dead replica's
// assertions is not enough on its own either: that only takes effect the next
// time somebody builds a snapshot, which for an already-connected observer may
// be never.
//
// So each instance periodically rebuilds the roster of the targets it actually
// has observers for, and sends a fresh snapshot when it differs from the last
// one it delivered. That single mechanism closes several holes at once:
//
//   - a replica that died without cleaning up;
//   - a transition that was published while the bus was briefly unavailable;
//   - a room the departing connection was not subscribed to and so was not told
//     about promptly;
//   - a subject whose access was removed while they were idle.
//
// It is bounded by what is actually being watched here, not by the workspace:
// one pass per target with local subscribers, one directory read each, and
// nothing at all is sent when nothing changed.
//
// The sweep itself (reconcilePresence) is a plain method, so tests drive it
// directly and assert on what it produced rather than on how long they waited.

// presenceReconcileInterval is how often active targets are rebuilt.
//
// It is deliberately shorter than instanceLivenessTTL: the whole point is to
// notice a replica whose liveness has expired, so a sweep has to happen inside
// the window in which that expiry becomes visible. Two sweeps per TTL means a
// dead instance's users converge in at most instanceLivenessTTL plus one
// interval, without making the sweep itself frequent enough to matter.
const presenceReconcileInterval = instanceLivenessTTL / 2

// runPresenceReconciler drives reconciliation from two sources.
//
// The periodic sweep only earns its keep where there is a shared directory to
// compare against: it exists to notice a replica that died without cleaning up,
// and without a directory there are no other replicas. So the ticker is created
// only in that configuration.
//
// The targeted requests are not optional in either configuration, and they are
// what makes an unsubscribe correct. Nothing else revisits a target when the
// last local coverage of one subject disappears: the user is still connected, so
// no presence transition happens, and single-node has no sweep. Draining a
// deduplicated set on a signal keeps that bounded — one pass per target however
// many times it was asked for, and no goroutine per unsubscribe.
func (h *Hub) runPresenceReconciler() {
	defer h.broadcastWG.Done()

	var sweep <-chan time.Time
	if h.directory != nil {
		ticker := time.NewTicker(presenceReconcileInterval)
		defer ticker.Stop()
		sweep = ticker.C
	}

	for {
		select {
		case <-sweep:
			h.reconcilePresence()
		case <-h.reconcileSignal:
			h.drainReconcileRequests()
		case <-h.quit:
			return
		}
	}
}

// requestPresenceReconcile asks for one target's roster to be rebuilt and
// redelivered, as soon as the reconciler gets to it.
//
// It never blocks its caller and never queues the same target twice: the set is
// the queue, and the signal — capacity 1 — only says that the set is non-empty.
// A signal dropped because one is already waiting cannot lose work, since the
// drain takes everything.
func (h *Hub) requestPresenceReconcile(key string) {
	if key == "" {
		return
	}
	h.reconcileMu.Lock()
	if h.pendingReconcile == nil {
		h.pendingReconcile = make(map[string]struct{}, 4)
	}
	h.pendingReconcile[key] = struct{}{}
	h.reconcileMu.Unlock()

	select {
	case h.reconcileSignal <- struct{}{}:
	default:
	}
}

// drainReconcileRequests rebuilds every target asked for since the last drain.
func (h *Hub) drainReconcileRequests() {
	h.reconcileMu.Lock()
	pending := h.pendingReconcile
	h.pendingReconcile = nil
	h.reconcileMu.Unlock()

	for key := range pending {
		select {
		case <-h.quit:
			return
		default:
		}
		h.reconcileTarget(key)
	}
}

// reconcileLostCoverage asks for a fresh roster on every target this user no
// longer has any local subscription to (SR-444-04).
//
// This is the hole an unsubscribe used to leave. Presence is aggregated per
// user, so a user who stops watching one conversation while staying connected
// to another moves nothing the tracker reports: no transition, no event, and
// therefore nothing that would ever correct the observers still watching the
// conversation they left. They stayed visible there indefinitely.
//
// The correction is target-scoped on purpose. The user has not gone offline —
// they are still connected — so no global state is published; the target's
// roster is simply rebuilt from the current authority and redelivered, and the
// subject drops out of it because they are no longer in it.
//
// Nothing is requested for a target nobody here is watching any more: there is
// no one to correct, and removeClient/revokeSubscription have already forgotten
// what was last delivered to it.
func (h *Hub) reconcileLostCoverage(workspaceID, userID string, keys []string) {
	if len(keys) == 0 {
		return
	}
	covered := h.coveredTargetsForUser(workspaceID, userID)
	for _, key := range keys {
		if _, still := covered[key]; still {
			continue
		}
		if !h.hasLocalSubscribers(key) {
			continue
		}
		h.requestPresenceReconcile(key)
	}
}

// hasLocalSubscribers reports whether anybody on this instance is still watching
// a target.
func (h *Hub) hasLocalSubscribers(key string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs[key]) > 0
}

// reconcilePresence rebuilds every locally observed target once.
func (h *Hub) reconcilePresence() {
	for _, key := range h.observedTargetKeys() {
		select {
		case <-h.quit:
			return
		default:
		}
		h.reconcileTarget(key)
	}
}

// observedTargetKeys returns the targets that have at least one subscriber on
// this instance. Nobody watching means nobody to correct.
func (h *Hub) observedTargetKeys() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()

	keys := make([]string, 0, len(h.subs))
	for key, clients := range h.subs {
		if len(clients) > 0 {
			keys = append(keys, key)
		}
	}
	return keys
}

// reconcileTarget re-reads one target's roster and, when it has moved since the
// last delivery, sends it to that target's subscribers.
//
// The comparison is what keeps this quiet. A roster that has not changed
// produces no frame, so an idle conversation costs one directory read per sweep
// and nothing on the wire — and, just as importantly, a client's presence
// timestamps do not churn every tick, which would make every avatar look like it
// was constantly changing state.
func (h *Hub) reconcileTarget(key string) {
	parsed, ok := parseTargetKey(key)
	if !ok || (parsed.targetType != TargetTypeChannel && parsed.targetType != TargetTypeDM) {
		return
	}
	// Neither authority is attached, so there is no roster to rebuild — and an
	// empty one would be a claim about a feature this hub is not running.
	if h.directory == nil && h.presence == nil {
		return
	}

	// Where the roster comes from is the same question sendPresenceSnapshot
	// answers, and it gets the same answer: the shared directory when there is
	// one, this instance's own connections when there is not. A deployment
	// without a directory is not a deployment without an authority — it is one
	// where this hub *is* the authority — and reading its own state is what lets
	// an unsubscribe be corrected here rather than never.
	var (
		users    []PresencePayload
		complete bool
	)
	if h.directory != nil {
		ctx, cancel := context.WithTimeout(context.Background(), directoryReadTimeout)
		entries, err := h.directory.Present(ctx, key)
		cancel()
		if err != nil {
			// Nothing is sent on a failed read: an empty roster would be indistinguishable
			// from "everybody left", and inventing that is worse than staying still.
			h.logger.WarnContext(context.Background(), "ws: presence reconciliation read failed",
				"target_type", string(parsed.targetType), "error", err)
			return
		}
		// The same authorization the initial snapshot applies: a correction is not a
		// reason to publish somebody the domain no longer places here (SR-444-01).
		users, complete = h.authorizedRoster(parsed.workspaceID, parsed, aggregateRoster(entries))
	} else {
		users, complete = h.localRoster(parsed, key)
	}
	if len(users) > presenceSnapshotMaxUsers {
		users = users[:presenceSnapshotMaxUsers]
		complete = false
	}
	if !h.presenceRosterChanged(key, users, complete) {
		return
	}

	data, err := json.Marshal(PresenceSnapshotResponse{
		Type: PresenceSnapshotType, TargetType: parsed.targetType, TargetID: parsed.targetID,
		Users: users, Complete: complete, TakenAt: formatPresenceTime(time.Now().UTC()),
	})
	if err != nil {
		h.logger.ErrorContext(context.Background(), "ws: marshal reconciled snapshot", "error", err)
		return
	}
	// Delivered through the ordinary broadcast path, so every recipient is
	// re-authorized exactly as they are for any other event. A correction is not
	// a reason to skip the check that decides who may see a roster.
	evt := Event{
		SchemaVersion: CurrentEventSchemaVersion, Type: EventTypePresenceUpdated,
		WorkspaceID: parsed.workspaceID, TargetType: parsed.targetType, TargetID: parsed.targetID,
		SourceInstanceID: h.presenceInstanceID, CreatedAt: time.Now().UTC(),
	}
	select {
	case h.bcast <- broadcastReq{event: evt, data: data}:
	case <-h.quit:
	}
}

// presenceRosterChanged records the roster just computed for a target and
// reports whether it differs from the one last delivered.
//
// The fingerprint is the serialised roster itself, which is why aggregateRoster
// sorts: two reads describing the same world have to produce the same string, or
// every sweep would look like a change.
func (h *Hub) presenceRosterChanged(key string, users []PresencePayload, complete bool) bool {
	fingerprint, err := json.Marshal(struct {
		Users    []PresencePayload `json:"users"`
		Complete bool              `json:"complete"`
	}{Users: users, Complete: complete})
	if err != nil {
		return false
	}
	h.reconcileMu.Lock()
	defer h.reconcileMu.Unlock()

	if previous, seen := h.deliveredRosters[key]; seen && previous == string(fingerprint) {
		return false
	}
	h.deliveredRosters[key] = string(fingerprint)
	return true
}

// forgetReconciledTarget drops a target nobody is watching here any more, so the
// memory of what was delivered cannot outlive the subscriptions that justified
// it. Called when the last local subscriber of a target goes.
func (h *Hub) forgetReconciledTarget(key string) {
	h.reconcileMu.Lock()
	defer h.reconcileMu.Unlock()
	delete(h.deliveredRosters, key)
}

// reconcileAssertions brings this instance's directory assertions for one user
// into line with what its live connections actually cover (CQ-1, CQ-2).
//
// Three sets, and the whole design is in telling them apart:
//
//   - desired — the targets this instance's live connections for that user
//     currently subscribe to, minus any the subject may no longer be seen in.
//     Coverage, re-derived every time; never a history.
//   - confirmed — the targets the directory has *accepted* a write for. Not what
//     was attempted: a failed write confirms nothing.
//   - the delta — desired minus confirmed needs a Record, confirmed minus
//     desired needs a Forget.
//
// Only the delta is applied, and the ledger moves only where the directory
// answered successfully. A transient failure therefore leaves the two sets
// disagreeing, which is exactly the state that makes the next sweep retry it —
// no retry loop, no backoff, no goroutine per assertion.
//
// A subscription ending changes desired without touching confirmed, and it moves
// neither the user's aggregate presence nor PresenceChange.Changed, which is why
// this cannot be driven from a presence transition.
func (h *Hub) reconcileAssertions(workspaceID, userID string) {
	if h.directory == nil {
		return
	}
	desired := h.desiredAssertions(workspaceID, userID)
	confirmed := h.assertedTargetsFor(workspaceID, userID)
	// Unsettled writes are considered for removal too. A field written by a call
	// whose reply never came is not in the ledger, and if coverage has since
	// ended nothing else would ever go looking for it.
	held := h.heldAssertionKeys(workspaceID, userID)

	stale := make([]string, 0, len(held))
	for key := range held {
		if _, still := desired[key]; !still {
			stale = append(stale, key)
		}
	}
	missing := make([]string, 0, len(desired))
	for key := range desired {
		if _, already := confirmed[key]; !already {
			missing = append(missing, key)
		}
	}

	ctx := context.Background()
	if len(stale) > 0 {
		h.applyAssertionForget(ctx, h.openForgetIntent(workspaceID, userID, stale))
	}
	if len(missing) > 0 {
		h.reassertPresence(ctx, workspaceID, userID, missing)
	}
}

// reassertPresence re-writes assertions the directory never accepted, using the
// user's current state rather than the one the failed publish carried.
//
// Nothing is written for a user this instance no longer holds present: they have
// no state to assert, and their pending removals are handled by the Forget side.
func (h *Hub) reassertPresence(ctx context.Context, workspaceID, userID string, keys []string) {
	if h.presence == nil {
		return
	}
	status, at := h.presence.StatusAt(workspaceID, userID)
	if status == PresenceOffline {
		return
	}
	h.applyAssertionRecord(ctx, h.openRecordIntent(workspaceID, userID, keys, status, at))
}

// reconcileAllAssertions revisits every user this instance either covers or
// still has confirmed assertions for.
//
// The second half is what makes retry work without any bookkeeping of its own: a
// user whose last connection closed has no coverage left, but a Forget that
// failed leaves them in the ledger, and that is enough for the next sweep to
// find them and try again. Once the removal lands, the entry goes and they stop
// being visited.
func (h *Hub) reconcileAllAssertions() {
	if h.directory == nil {
		return
	}
	seen := make(map[presenceKey]struct{}, 16)
	for _, pk := range h.connectedUsers() {
		seen[pk] = struct{}{}
	}
	for _, pk := range h.confirmedAssertionUsers() {
		seen[pk] = struct{}{}
	}
	for pk := range seen {
		h.reconcileAssertions(pk.workspaceID, pk.userID)
	}
}

// connectedUsers lists the distinct users with a live connection here.
func (h *Hub) connectedUsers() []presenceKey {
	h.mu.RLock()
	defer h.mu.RUnlock()

	seen := make(map[presenceKey]struct{}, len(h.clients))
	users := make([]presenceKey, 0, len(h.clients))
	for _, c := range h.clients {
		pk := presenceKey{workspaceID: c.workspaceID, userID: c.userID}
		if _, dup := seen[pk]; dup {
			continue
		}
		seen[pk] = struct{}{}
		users = append(users, pk)
	}
	return users
}

// desiredAssertions is coverage narrowed by what the subject may still be seen
// in.
//
// Authorization belongs here and not only at publish time: a person who loses
// access while idle produces no transition, so nothing else would ever revisit
// them. Being denied makes the target undesired, which the delta above turns
// into a Forget — the assertion is withdrawn rather than merely hidden.
//
// A transient authorization error excludes the target too. It is not treated as
// permission, and it is not treated as a removal either: the target simply is
// not desired this time round, so nothing is written for it and the next sweep
// asks again.
func (h *Hub) desiredAssertions(workspaceID, userID string) map[string]struct{} {
	covered := h.coveredTargetsForUser(workspaceID, userID)
	if len(covered) == 0 {
		return covered
	}
	desired := make(map[string]struct{}, len(covered))
	for key := range covered {
		parsed, ok := parseTargetKey(key)
		if !ok || parsed.workspaceID != workspaceID {
			continue
		}
		if h.subjectMayAppear(workspaceID, userID, parsed) {
			desired[key] = struct{}{}
		}
	}
	return desired
}

// coveredTargetsForUser is the desired coverage: what this instance's live
// connections for that user are subscribed to right now.
func (h *Hub) coveredTargetsForUser(workspaceID, userID string) map[string]struct{} {
	h.mu.RLock()
	defer h.mu.RUnlock()

	covered := make(map[string]struct{}, 8)
	for _, c := range h.clients {
		if c.workspaceID != workspaceID || c.userID != userID {
			continue
		}
		for key := range h.clientSubs[c.id] {
			covered[key] = struct{}{}
		}
	}
	return covered
}

// assertedTargetsList is assertedTargetsFor as a slice.
func (h *Hub) assertedTargetsList(workspaceID, userID string) []string {
	return snapshotKeys(h.assertedTargetsFor(workspaceID, userID))
}

// assertedTargetsFor is the ownership side: what this instance has *confirmed*
// it published about that user.
func (h *Hub) assertedTargetsFor(workspaceID, userID string) map[string]struct{} {
	pk := presenceKey{workspaceID: workspaceID, userID: userID}
	h.assertedMu.Lock()
	defer h.assertedMu.Unlock()

	owned := make(map[string]struct{}, len(h.asserted[pk]))
	for key := range h.asserted[pk] {
		owned[key] = struct{}{}
	}
	return owned
}

// renewableAssertionKeys is desired ∩ confirmed, across every user,
// de-duplicated: the exact set whose lease has to stay alive and nothing beyond
// it.
func (h *Hub) renewableAssertionKeys() []string {
	seen := make(map[string]struct{}, 16)
	keys := make([]string, 0, 16)
	for _, pk := range h.confirmedAssertionUsers() {
		desired := h.desiredAssertions(pk.workspaceID, pk.userID)
		for key := range h.assertedTargetsFor(pk.workspaceID, pk.userID) {
			if _, wanted := desired[key]; !wanted {
				continue
			}
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			keys = append(keys, key)
		}
	}
	return keys
}

// refreshAssertionLeases renews the lease on the targets that are still both
// wanted and written (CQ-2, INV-6).
//
// The eligible set is desired ∩ confirmed, never the ledger alone. A target
// awaiting a Forget that failed is confirmed and *not* desired, and renewing it
// would keep an assertion alive that this instance is actively trying to remove
// — which, because the target key's lease is also held up by everyone else in
// the conversation, would otherwise never expire on its own.
//
// It renews rather than rewrites, and rides the heartbeat that already exists —
// no timer per user, per connection or per target.
func (h *Hub) refreshAssertionLeases() {
	if h.directory == nil {
		return
	}
	keys := h.renewableAssertionKeys()
	if len(keys) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), directoryWriteTimeout)
	defer cancel()
	if err := h.directory.Refresh(ctx, keys); err != nil {
		h.logger.WarnContext(ctx, "ws: refreshing presence assertion leases failed", "error", err)
	}
}

// withdrawLocalPresence removes this instance's assertions before it stops
// serving (CQ-2, graceful shutdown).
//
// A replica that shuts down cleanly knows something a crashed one cannot say:
// that its connections are ending now. Saying it costs one pipeline per user and
// saves everyone else from waiting out the liveness TTL to discover it. What it
// deliberately does not do is announce offline on behalf of those users — it has
// no idea whether they are also connected somewhere else, and the instances that
// do know will reconcile from a directory that no longer contains these
// assertions.
//
// It runs before the hub's state is torn down, because the assertions are keyed
// by the subscriptions that are about to be discarded.
func (h *Hub) withdrawLocalPresence() {
	if h.directory == nil {
		return
	}
	type withdrawal struct {
		workspaceID string
		userID      string
		keys        []string
	}

	h.mu.RLock()
	byUser := make(map[presenceKey]map[string]struct{}, len(h.clients))
	for _, c := range h.clients {
		pk := presenceKey{workspaceID: c.workspaceID, userID: c.userID}
		keys := byUser[pk]
		if keys == nil {
			keys = make(map[string]struct{}, 8)
			byUser[pk] = keys
		}
		for key := range h.clientSubs[c.id] {
			keys[key] = struct{}{}
		}
	}
	h.mu.RUnlock()

	withdrawals := make([]withdrawal, 0, len(byUser))
	for pk, keys := range byUser {
		withdrawals = append(withdrawals, withdrawal{
			workspaceID: pk.workspaceID, userID: pk.userID, keys: snapshotKeys(keys),
		})
	}

	ctx := context.Background()
	for _, w := range withdrawals {
		if len(w.keys) == 0 {
			continue
		}
		// Shutdown is not a special case and must not have a special writer: it
		// takes the same turn as everything else, so a write still in flight for
		// that user finishes before its assertion is withdrawn.
		h.applyAssertionForget(ctx, h.openWithdrawalIntent(w.workspaceID, w.userID, w.keys))
	}
}

// assertionSequencer serialises this instance's directory mutations per user.
//
// The field a mutation writes — user|instance — has exactly one writer while
// this process lives, so ordering its writes locally is enough to order them
// absolutely. Without that, two publications for the same person can be in
// flight at once and the one that happens to finish last wins, which is how a
// state captured before an unsubscribe could land on top of the state written
// after the resubscribe.
//
// Per user rather than per target, for two reasons: a user's rooms are written
// in one pipelined call, so a finer lock would buy nothing and cost the batch;
// and independent users must never wait on each other. Locks are refcounted and
// deleted when the last holder leaves, so the map is bounded by concurrent
// mutations rather than by everyone who has ever been present.
type assertionSequencer struct {
	mu    sync.Mutex
	locks map[presenceKey]*assertionLock
}

type assertionLock struct {
	mu   sync.Mutex
	refs int
}

func newAssertionSequencer() *assertionSequencer {
	return &assertionSequencer{locks: make(map[presenceKey]*assertionLock)}
}

// lock serialises mutations for one user and returns the release function.
func (s *assertionSequencer) lock(pk presenceKey) (unlock func()) {
	s.mu.Lock()
	entry := s.locks[pk]
	if entry == nil {
		entry = &assertionLock{}
		s.locks[pk] = entry
	}
	entry.refs++
	s.mu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		s.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(s.locks, pk)
		}
		s.mu.Unlock()
	}
}

// confirmedAssertionVersion reports the version the ledger holds for one
// assertion. Test seam for the fencing invariants; production reads the ledger
// through the delta above.
func (h *Hub) confirmedAssertionVersion(workspaceID, userID, key string) (uint64, bool) {
	pk := presenceKey{workspaceID: workspaceID, userID: userID}
	h.assertedMu.Lock()
	defer h.assertedMu.Unlock()
	version, ok := h.asserted[pk][key]
	return version, ok
}

// highestAssertionVersion is the newest version this hub has issued.
func (h *Hub) highestAssertionVersion() uint64 {
	h.assertedMu.Lock()
	defer h.assertedMu.Unlock()
	return h.assertionSeq
}

// assertionIntent is one decided mutation of this instance's assertions about
// one user, carrying the version each target had when the decision was made.
//
// The version travels with the work rather than being taken when the work runs,
// and that is the entire point of the type. Two publications for the same person
// write the same field; if the second one to arrive is allowed to look newer
// simply because it started later, a state captured before an unsubscribe lands
// on top of the state written after the resubscribe. Carrying the version makes
// "old" a property of the decision instead of a property of the schedule.
type assertionIntent struct {
	workspaceID string
	userID      string
	// state and at are what a Record intends to write. A Forget uses neither.
	state PresenceStatus
	at    time.Time
	// versions is target key → the epoch that target had when this was decided.
	versions map[string]uint64
	// unconditional marks a withdrawal that does not depend on coverage having
	// ended: at shutdown the connections are still registered and still cover
	// their targets, and waiting for that to stop being true would mean never
	// withdrawing at all. The epoch check still applies.
	unconditional bool
}

// versionsFor narrows an intent to some of its keys, for confirming exactly the
// subset a write settled.
func (i assertionIntent) versionsFor(keys []string) map[string]uint64 {
	narrowed := make(map[string]uint64, len(keys))
	for _, key := range keys {
		if version, ok := i.versions[key]; ok {
			narrowed[key] = version
		}
	}
	return narrowed
}

// openRecordIntent declares that these targets should hold this state, and
// stamps each with the version that declaration has.
func (h *Hub) openRecordIntent(
	workspaceID, userID string, keys []string, state PresenceStatus, at time.Time,
) assertionIntent {
	intent := h.openIntent(workspaceID, userID, keys)
	intent.state = state
	intent.at = at
	return intent
}

// openForgetIntent declares that these targets should hold nothing.
func (h *Hub) openForgetIntent(workspaceID, userID string, keys []string) assertionIntent {
	return h.openIntent(workspaceID, userID, keys)
}

// openWithdrawalIntent is openForgetIntent for a process that is stopping.
func (h *Hub) openWithdrawalIntent(workspaceID, userID string, keys []string) assertionIntent {
	intent := h.openIntent(workspaceID, userID, keys)
	intent.unconditional = true
	return intent
}

// openIntent advances the epoch of each target and returns the intent holding
// those versions.
//
// Advancing here — where the intention changes — is what the fencing rests on. A
// subscribe that creates coverage, an unsubscribe that removes it, a state that
// has to be published and a withdrawal all pass through this, and each leaves
// every earlier intent for the same target permanently behind. Nothing advances
// on a heartbeat or a lease renewal: those change no intention.
func (h *Hub) openIntent(workspaceID, userID string, keys []string) assertionIntent {
	pk := presenceKey{workspaceID: workspaceID, userID: userID}
	h.assertedMu.Lock()
	epochs := h.assertionEpoch[pk]
	if epochs == nil {
		epochs = make(map[string]uint64, len(keys))
		h.assertionEpoch[pk] = epochs
	}
	versions := make(map[string]uint64, len(keys))
	for _, key := range keys {
		if _, dup := versions[key]; dup {
			continue
		}
		h.assertionSeq++
		epochs[key] = h.assertionSeq
		versions[key] = h.assertionSeq
	}
	h.assertedMu.Unlock()

	if h.intentOpened != nil {
		h.intentOpened(versions)
	}
	return assertionIntent{workspaceID: workspaceID, userID: userID, versions: versions}
}

// currentAssertionEpoch reports the version of the newest intention for one
// assertion. Test seam for the fencing invariants; production compares through
// currentIntentKeys.
func (h *Hub) currentAssertionEpoch(workspaceID, userID, key string) (uint64, bool) {
	pk := presenceKey{workspaceID: workspaceID, userID: userID}
	h.assertedMu.Lock()
	defer h.assertedMu.Unlock()
	version, ok := h.assertionEpoch[pk][key]
	return version, ok
}

// currentIntentKeys returns the targets of an intent that are still the newest
// decision about their field. Everything else is work that the world moved past
// while it waited.
func (h *Hub) currentIntentKeys(intent assertionIntent) []string {
	pk := presenceKey{workspaceID: intent.workspaceID, userID: intent.userID}
	h.assertedMu.Lock()
	defer h.assertedMu.Unlock()

	epochs := h.assertionEpoch[pk]
	current := make([]string, 0, len(intent.versions))
	for key, version := range intent.versions {
		if epochs[key] == version {
			current = append(current, key)
		}
	}
	return current
}

// heldAssertionKeys is everything this instance is answerable for about a user:
// what the directory confirmed, plus what it may hold from a write that never
// reported back.
func (h *Hub) heldAssertionKeys(workspaceID, userID string) map[string]struct{} {
	pk := presenceKey{workspaceID: workspaceID, userID: userID}
	h.assertedMu.Lock()
	defer h.assertedMu.Unlock()

	held := make(map[string]struct{}, len(h.asserted[pk])+len(h.pendingAssertions[pk]))
	for key := range h.asserted[pk] {
		held[key] = struct{}{}
	}
	for key := range h.pendingAssertions[pk] {
		held[key] = struct{}{}
	}
	return held
}
