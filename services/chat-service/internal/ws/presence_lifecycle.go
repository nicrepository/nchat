package ws

import (
	"context"
	"encoding/json"
	"math"
	"time"

	"github.com/google/uuid"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// Presence lifecycle beyond connect and disconnect (issue #798): the
// disconnect grace, the context that changes without any socket moving, and
// the hint that tells a user's own sessions their settings changed.

// presenceContextSweepInterval is how often the context of the users this
// instance serves is re-read. It bounds how late an expired manual state, a
// call lease that ran out, or a change made through another replica can be
// noticed. One batched read per workspace per sweep, whatever the user count.
const presenceContextSweepInterval = 30 * time.Second

// presenceResumeWindow is how long a reconnected user's previous rooms stay
// covered while the new connection subscribes again. It only bridges the gap
// between the socket opening and its first subscribe.
const presenceResumeWindow = 30 * time.Second

// EventTypePresenceSettingsChanged tells a user's own sessions that their
// presence settings changed (issue #798). It carries no payload — the client
// re-reads its settings over HTTP — and is delivered only to that user.
const EventTypePresenceSettingsChanged EventType = "presence.settings_changed"

// lingeringCover is what a departed connection still covers: its rooms, which
// are also where its offline is owed if the grace runs out.
//
// Two kinds, and they are owned differently:
//
//   - a grace cover belongs to a pending grace. Nothing may discard it but the
//     grace itself: the tracker's expiry consumes it to address the offline
//     (takeLingering), or a reconnect turns it into a resume cover. It has no
//     deadline of its own — its deadline is the grace's.
//   - a resume cover is what is left after a reconnect: the old rooms stay
//     covered for presenceResumeWindow while the new connection subscribes
//     again, and then it is residue the sweep drops.
type lingeringCover struct {
	keys    map[string]struct{}
	resumed bool
	until   time.Time
}

// valid reports whether a cover still covers anything at now.
func (c lingeringCover) valid(now time.Time) bool {
	return !c.resumed || now.Before(c.until)
}

// holdLingering keeps a departed connection's rooms covered for its grace. The
// directory keeps the user's assertions in them, so a snapshot read meanwhile
// on any replica still shows the person.
func (h *Hub) holdLingering(pk presenceKey, keys []string) {
	h.lingerMu.Lock()
	defer h.lingerMu.Unlock()
	cover := h.lingering[pk]
	if cover.keys == nil {
		cover.keys = make(map[string]struct{}, len(keys))
	}
	for _, key := range keys {
		cover.keys[key] = struct{}{}
	}
	cover.resumed = false
	cover.until = time.Time{}
	if h.lingering == nil {
		h.lingering = make(map[presenceKey]lingeringCover)
	}
	h.lingering[pk] = cover
}

// takeLingering removes and returns a user's lingering rooms. Called when the
// grace ends, so the offline reaches every room the person was in.
func (h *Hub) takeLingering(pk presenceKey) []string {
	h.lingerMu.Lock()
	defer h.lingerMu.Unlock()
	cover, ok := h.lingering[pk]
	if !ok {
		return nil
	}
	delete(h.lingering, pk)
	return snapshotKeys(cover.keys)
}

// resumeLingering turns a user's grace cover into a resume cover and reports
// whether there was a grace to end — a grace that ended in recovery.
func (h *Hub) resumeLingering(pk presenceKey) bool {
	h.lingerMu.Lock()
	defer h.lingerMu.Unlock()
	cover, ok := h.lingering[pk]
	if !ok || cover.resumed {
		return false
	}
	cover.resumed = true
	cover.until = h.presenceClock().Add(presenceResumeWindow)
	h.lingering[pk] = cover
	return true
}

// lingeringKeysFor is a user's still-valid lingering rooms.
func (h *Hub) lingeringKeysFor(pk presenceKey) []string {
	now := h.presenceClock()
	h.lingerMu.Lock()
	defer h.lingerMu.Unlock()
	cover, ok := h.lingering[pk]
	if !ok || !cover.valid(now) {
		return nil
	}
	return snapshotKeys(cover.keys)
}

// lingeringUsersOn is every user whose valid lingering cover includes key.
func (h *Hub) lingeringUsersOn(key string) []presenceKey {
	now := h.presenceClock()
	h.lingerMu.Lock()
	defer h.lingerMu.Unlock()
	var users []presenceKey
	for pk, cover := range h.lingering {
		if _, covers := cover.keys[key]; covers && cover.valid(now) {
			users = append(users, pk)
		}
	}
	return users
}

// pruneLingering drops resume covers whose window has passed. A grace cover
// is never pruned here, whatever the clock says: its grace owns it, and the
// offline it has to address may still be on its way (issue #798, HIGH-1).
func (h *Hub) pruneLingering() {
	now := h.presenceClock()
	h.lingerMu.Lock()
	defer h.lingerMu.Unlock()
	for pk, cover := range h.lingering {
		if cover.resumed && !cover.valid(now) {
			delete(h.lingering, pk)
		}
	}
}

// presenceAudience is every target a change is addressed to: what it names,
// plus — when it asks to be derived — the user's live subscriptions and the
// rooms a lingering connection still covers.
func (h *Hub) presenceAudience(change presenceChange) []string {
	keys := append([]string{}, change.keys...)
	if !change.derive {
		return keys
	}
	keys = append(keys, h.subscribedTargetKeys(change.workspaceID, change.userID)...)
	pk := presenceKey{workspaceID: change.workspaceID, userID: change.userID}
	return append(keys, h.lingeringKeysFor(pk)...)
}

// RefreshPresence re-composes and, if it changed, republishes a user this
// instance holds presence for. It is how a change that moves no socket — a
// manual state, a call joined or left — reaches observers. A user with no
// presence here is left alone: the instance that holds them answers for them.
func (h *Hub) RefreshPresence(workspaceID, userID string) {
	if h.presence == nil || workspaceID == "" || userID == "" {
		return
	}
	status, _ := h.presence.StatusAt(workspaceID, userID)
	if status == PresenceOffline {
		return
	}
	h.enqueuePresenceChange(presenceChange{
		workspaceID: workspaceID, userID: userID, status: status, at: h.presenceClock(), derive: true,
	})
}

// sweepPresenceContexts re-reads the context of every user this instance
// serves and republishes the ones whose context moved since it was last
// published: an expired manual state, a call lease that lapsed, a change made
// through another replica whose hint was lost. It also retries every
// publication a failed read left owed.
func (h *Hub) sweepPresenceContexts() {
	h.pruneLingering()
	h.retryOwedPresence()
	if h.presenceContext == nil {
		return
	}
	for workspaceID, userIDs := range h.presenceSweepUsers() {
		contexts, err := h.loadPresenceContexts(context.Background(), workspaceID, userIDs)
		if err != nil {
			h.logger.WarnContext(context.Background(), "ws: presence context sweep read failed", "error", err)
			continue
		}
		for _, userID := range userIDs {
			h.applySweptContext(presenceKey{workspaceID: workspaceID, userID: userID}, contexts[userID])
		}
	}
}

// applySweptContext republishes a user whose context moved, and — when what
// moved is their own manual state, as an expiry does — tells their sessions
// here to re-read their settings, exactly as a write through the API would.
func (h *Hub) applySweptContext(pk presenceKey, current domain.PresenceContext) {
	published, ok := h.publishedState(pk)
	if ok && published.context == current {
		return
	}
	if ok && published.context.Override != current.Override {
		h.deliverPresenceSettingsChanged(pk.workspaceID, pk.userID)
	}
	// Remembered now rather than when the refresh lands, so a sweep that runs
	// before the fan-out catches up does not report the same change twice.
	h.rememberContext(pk, current)
	h.RefreshPresence(pk.workspaceID, pk.userID)
}

// rememberContext records the context a person is about to be republished
// with, for somebody this replica already remembers.
func (h *Hub) rememberContext(pk presenceKey, current domain.PresenceContext) {
	h.publishedMu.Lock()
	defer h.publishedMu.Unlock()
	if entry, ok := h.published[pk]; ok {
		entry.context = current
		h.published[pk] = entry
	}
}

// retryOwedPresence re-enqueues every publication a failed read left owed:
// for somebody still served here, a refresh with the owed audience added; for
// somebody who has left, the departure itself, to the rooms it was owed to.
func (h *Hub) retryOwedPresence() {
	for pk, owed := range h.owedPresence() {
		change := presenceChange{
			workspaceID: pk.workspaceID, userID: pk.userID,
			status: h.localReach(pk), at: h.presenceClock(), keys: owed.owedKeys, generation: owed.owedGeneration,
		}
		change.derive = change.status != PresenceOffline
		h.enqueuePresenceChange(change)
	}
}

// owedPresence lists the owed publications and their audiences.
func (h *Hub) owedPresence() map[presenceKey]publishedPresence {
	h.publishedMu.Lock()
	defer h.publishedMu.Unlock()
	owed := make(map[presenceKey]publishedPresence)
	for pk, entry := range h.published {
		if entry.owed {
			owed[pk] = entry
		}
	}
	return owed
}

// presenceSweepUsers is everyone this instance currently serves, by workspace:
// its connections and its graces. Nobody it merely remembers — memory is
// released with responsibility (publishedPresence), so this never grows with
// history.
func (h *Hub) presenceSweepUsers() map[string][]string {
	seen := make(map[presenceKey]struct{}, 16)
	for _, pk := range h.connectedUsers() {
		seen[pk] = struct{}{}
	}
	for _, pk := range h.lingeringUsers() {
		seen[pk] = struct{}{}
	}
	byWorkspace := make(map[string][]string, 1)
	for pk := range seen {
		byWorkspace[pk.workspaceID] = append(byWorkspace[pk.workspaceID], pk.userID)
	}
	return byWorkspace
}

// lingeringUsers is everyone with a valid lingering cover.
func (h *Hub) lingeringUsers() []presenceKey {
	now := h.presenceClock()
	h.lingerMu.Lock()
	defer h.lingerMu.Unlock()
	users := make([]presenceKey, 0, len(h.lingering))
	for pk, cover := range h.lingering {
		if cover.valid(now) {
			users = append(users, pk)
		}
	}
	return users
}

// PublishPresenceSettingsChanged tells every session of a user, on every
// replica, that their presence settings changed, and has every replica that
// holds them republish their presence.
func (h *Hub) PublishPresenceSettingsChanged(ctx context.Context, workspaceID, userID string) {
	if workspaceID == "" || userID == "" {
		return
	}
	evt, data, ok := h.presenceSettingsEvent(workspaceID, userID)
	if !ok {
		return
	}
	h.applyPresenceSettingsChanged(evt, data)
	if err := h.bus.Publish(ctx, evt); err != nil {
		h.logger.WarnContext(ctx, "ws: presence.settings_changed bus publish failed", "error", err)
	}
}

// applyPresenceSettingsChanged is what every replica does with the hint, its
// own or one from the bus.
func (h *Hub) applyPresenceSettingsChanged(evt Event, data []byte) {
	h.deliverToLocalUserSessions(evt, data)
	h.RefreshPresence(evt.WorkspaceID, evt.RecipientUserID)
}

// deliverPresenceSettingsChanged tells a user's sessions on this instance to
// re-read their settings. Local only: every replica serving the person sweeps
// their context itself and notices the same expiry for its own sessions.
func (h *Hub) deliverPresenceSettingsChanged(workspaceID, userID string) {
	evt, data, ok := h.presenceSettingsEvent(workspaceID, userID)
	if !ok {
		return
	}
	h.deliverToLocalUserSessions(evt, data)
}

// presenceSettingsEvent builds the hint for one user.
func (h *Hub) presenceSettingsEvent(workspaceID, userID string) (Event, []byte, bool) {
	evt := Event{
		SchemaVersion: CurrentEventSchemaVersion, Type: EventTypePresenceSettingsChanged,
		WorkspaceID: workspaceID, TargetType: TargetTypeUser, TargetID: userID, RecipientUserID: userID,
		EventID: uuid.NewString(), SourceInstanceID: h.presenceInstanceID, CreatedAt: time.Now().UTC(),
	}
	data, err := json.Marshal(evt)
	if err != nil {
		h.logger.ErrorContext(context.Background(), "ws: marshal presence.settings_changed event", "error", err)
		return Event{}, nil, false
	}
	return evt, data, true
}

// canonicalizePresenceSettingsEvent admits the hint only as a user telling
// themselves: the recipient must be the target, and the target must be a user.
func canonicalizePresenceSettingsEvent(evt Event) (Event, bool) {
	if evt.TargetType != TargetTypeUser || evt.RecipientUserID != evt.TargetID {
		return Event{}, false
	}
	return evt, true
}

// refreshCallParticipants republishes the presence of the people a call
// lifecycle event is about, where they are served by this instance.
func (h *Hub) refreshCallParticipants(call domain.Call) {
	for _, userID := range []string{call.CallerID, call.CalleeID} {
		if userID != "" {
			h.RefreshPresence(call.WorkspaceID, userID)
		}
	}
}

// refreshUserPresenceLeases renews the per-user records of everybody this
// instance serves, one pipeline per workspace on the heartbeat that already
// runs. A person served all day without a transition must not lapse.
func (h *Hub) refreshUserPresenceLeases() {
	for workspaceID, userIDs := range h.presenceSweepUsers() {
		ctx, cancel := context.WithTimeout(context.Background(), directoryWriteTimeout)
		if err := h.userPresence().RefreshUsers(ctx, workspaceID, userIDs); err != nil {
			h.logger.WarnContext(ctx, "ws: refreshing per-user presence leases failed", "error", err)
		}
		cancel()
	}
}

// withdrawUserReach removes this instance's per-user reach for everybody it
// serves, at graceful shutdown, so other replicas stop counting it at once
// rather than after its liveness lapses.
func (h *Hub) withdrawUserReach() {
	for workspaceID, userIDs := range h.presenceSweepUsers() {
		for _, userID := range userIDs {
			ctx, cancel := context.WithTimeout(context.Background(), directoryWriteTimeout)
			unlock := h.assertionLocks.lock(presenceKey{workspaceID: workspaceID, userID: userID})
			// The process is ending: every lifecycle it held ends with it.
			if _, err := h.userPresence().WithdrawReach(ctx, workspaceID, userID, math.MaxUint64); err != nil {
				h.logger.WarnContext(ctx, "ws: withdrawing per-user presence failed", "error", err)
			}
			unlock()
			cancel()
		}
	}
}
