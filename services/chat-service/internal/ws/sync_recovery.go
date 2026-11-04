package ws

// Issue #947: a delivery skipped because access could not be proven is a gap,
// and a gap must be closed.
//
// The broadcast path re-checks every recipient's access before delivering
// (handleBroadcast). When that check fails operationally — a timeout, a pool
// error — the event is withheld, which is the only fail-secure answer. It used
// to be the end of the story: the subscription stayed, the event was gone, and
// nothing ever told the client. The persisted message only reappeared on F5.
//
// What this file adds is the smallest thing that restores convergence without
// weakening the check:
//
//   - the gap is recorded per connection and per target, never per event. A
//     burst of a thousand skipped events is one entry, and the ledger is bounded
//     by the subscriptions that already exist;
//   - once access can be proven again, the connection is sent one route-only
//     conversation.sync_required, and the client reads the target back through
//     the same authorized listing a page load uses. The skipped payload is never
//     replayed: it was never authorized, and the database has the truth anyway;
//   - access is re-proven either by the next delivery to that subscription or,
//     when nobody posts again, by a single background pass with backoff — so
//     recovery never depends on future traffic.
//
// What "resolved" means here is deliberately modest. Queuing the signal is not
// proof the client reconciled, and nothing pretends it is. The guarantee is the
// one the transport can actually give: the signal reaches the outbox of a live
// connection, or that connection dies — and every new connection reconciles on
// open anyway (the client's resubscription is its own resync point). A gap is
// therefore never silently forgotten, only handed to a mechanism that converges.
//
// The signal is connection-local. It never touches the bus, so instances of
// different versions sharing one Valkey channel see nothing new (Blue/Green).

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// SyncRequiredType is the wire value of syncRequiredResponse.Type.
const SyncRequiredType = "conversation.sync_required"

// syncRecoveryInitialDelay is how long the first background re-check waits
// after a gap is recorded. Re-asking the instant a check failed would mostly
// hit the same failure; a short pause lets a transient blip clear.
const syncRecoveryInitialDelay = 500 * time.Millisecond

// syncRecoveryMaxDelay caps the backoff while access stays unprovable, so a long
// outage costs one sequential pass every half minute, not a query storm.
const syncRecoveryMaxDelay = 30 * time.Second

// syncRequiredResponse tells one connection that it may have missed events for
// a target and should read it back.
//
// Route only: no message, no actor, no count, no reason. It is sent only after
// access to exactly this target was proven for exactly this connection, so it
// reveals nothing the subscription itself does not — and a client that is not
// allowed to read the target learns nothing from the listing it triggers either,
// because the listing re-authorizes.
type syncRequiredResponse struct {
	Type       string     `json:"type"`
	TargetType TargetType `json:"target_type"`
	TargetID   string     `json:"target_id"`
}

// recheckOutcome classifies one authorization re-check. It is a closed set, so
// it is safe as a metric label.
type recheckOutcome string

const (
	recheckAllowed recheckOutcome = "allowed"
	recheckDenied  recheckOutcome = "denied"
	recheckTimeout recheckOutcome = "timeout"
	recheckError   recheckOutcome = "error"
	// recheckClientClosed is a check that failed because the connection asking
	// was already going away: close() cancels its context before the hub has
	// unregistered it. It is neither a denial nor a gap — the connection has no
	// future to resynchronise, and its successor reconciles on open.
	recheckClientClosed recheckOutcome = "client_closed"
)

// classifyRecheck turns an authorizer answer into an outcome. A denial is only
// ever a nil error with false, exactly as SubscriptionAuthorizer documents; no
// error is ever read as a denial.
func classifyRecheck(c *Client, allowed bool, err error) recheckOutcome {
	switch {
	case err == nil && allowed:
		return recheckAllowed
	case err == nil:
		return recheckDenied
	case c.ctx.Err() != nil:
		return recheckClientClosed
	case errors.Is(err, context.DeadlineExceeded):
		return recheckTimeout
	default:
		return recheckError
	}
}

// Sync recovery outcomes, as the metric spells them.
const (
	syncGapRecorded = "recorded"
	syncSignalled   = "signalled"
	syncRevoked     = "revoked"
	syncDeferred    = "deferred"
)

// Disconnect reasons, as the metric spells them.
const (
	disconnectSendError  = "send_error"
	disconnectHeartbeat  = "heartbeat"
	disconnectSlowClient = "slow_client"
)

// DeliveryMetrics receives realtime delivery outcomes for observability. Every
// label is a closed set — a target type, an outcome, a reason — and never a
// user, a connection, a target or a message.
//
// It only covers what this server can see. Whether a browser actually
// reconciled after a signal is the client's knowledge, not the hub's.
type DeliveryMetrics interface {
	// AuthRecheck counts one per-recipient authorization re-check.
	AuthRecheck(targetType, outcome string)
	// SyncRecovery counts a gap recorded and how each recovery attempt ended.
	SyncRecovery(outcome string)
	// Disconnect counts a connection the server dropped, by reason.
	Disconnect(reason string)
}

// WithDeliveryMetrics attaches delivery observability. Optional.
func WithDeliveryMetrics(metrics DeliveryMetrics) HubOption {
	return func(h *Hub) { h.deliveryMetrics = metrics }
}

type nopDeliveryMetrics struct{}

func (nopDeliveryMetrics) AuthRecheck(string, string) {}
func (nopDeliveryMetrics) SyncRecovery(string)        {}
func (nopDeliveryMetrics) Disconnect(string)          {}

func (h *Hub) delivery() DeliveryMetrics {
	if h.deliveryMetrics == nil {
		return nopDeliveryMetrics{}
	}
	return h.deliveryMetrics
}

// pendingGap is one connection's unresolved gap on one target, as a recovery
// pass sees it.
type pendingGap struct {
	subscription broadcastSubscription
	key          string
}

// recordGap remembers that subscription missed a delivery on key. Repeated
// calls for the same subscription coalesce into one entry.
//
// Only a subscription that is still current may hold a gap: one that was
// released or replaced in the meantime has nothing left to resynchronise.
func (h *Hub) recordGap(subscription broadcastSubscription, key string) {
	h.mu.Lock()
	recorded := h.subscriptionIsCurrentLocked(subscription, key)
	if recorded {
		if h.pendingSync == nil {
			h.pendingSync = make(map[string]map[string]uint64)
		}
		gaps := h.pendingSync[subscription.client.id]
		if gaps == nil {
			gaps = make(map[string]uint64)
			h.pendingSync[subscription.client.id] = gaps
		}
		gaps[key] = subscription.generation
	}
	h.mu.Unlock()
	if !recorded {
		return
	}
	h.delivery().SyncRecovery(syncGapRecorded)
	select {
	case h.syncSignal <- struct{}{}:
	default:
	}
}

// forgetGapLocked drops the gap a subscription held. Called wherever the
// subscription itself goes away. Caller holds h.mu.
func (h *Hub) forgetGapLocked(clientID, key string) {
	gaps, ok := h.pendingSync[clientID]
	if !ok {
		return
	}
	delete(gaps, key)
	if len(gaps) == 0 {
		delete(h.pendingSync, clientID)
	}
}

// gapIsPendingLocked reports whether this exact subscription generation still
// holds a gap on key. Caller holds h.mu.
func (h *Hub) gapIsPendingLocked(subscription broadcastSubscription, key string) bool {
	generation, ok := h.pendingSync[subscription.client.id][key]
	return ok && generation == subscription.generation && h.subscriptionIsCurrentLocked(subscription, key)
}

// signalGapLocked queues the sync signal for a gap this subscription holds and
// clears it. It reports false only when a gap existed and the outbox was full,
// which the caller treats like any other slow client. Caller holds h.mu and has
// just proven access for this subscription.
func (h *Hub) signalGapLocked(subscription broadcastSubscription, key string) bool {
	if !h.gapIsPendingLocked(subscription, key) {
		return true
	}
	h.forgetGapLocked(subscription.client.id, key)
	target, ok := parseTargetKey(key)
	if !ok {
		return true
	}
	data, err := json.Marshal(syncRequiredResponse{
		Type: SyncRequiredType, TargetType: target.targetType, TargetID: target.targetID,
	})
	if err != nil {
		return true
	}
	if !subscription.client.enqueue(data) {
		return false
	}
	h.delivery().SyncRecovery(syncSignalled)
	return true
}

// pendingGaps snapshots every recorded gap for a recovery pass.
func (h *Hub) pendingGaps() []pendingGap {
	h.mu.RLock()
	defer h.mu.RUnlock()
	var gaps []pendingGap
	for clientID, keys := range h.pendingSync {
		c := h.clients[clientID]
		if c == nil {
			continue
		}
		for key, generation := range keys {
			gaps = append(gaps, pendingGap{
				subscription: broadcastSubscription{client: c, generation: generation},
				key:          key,
			})
		}
	}
	return gaps
}

// runSyncRecovery closes gaps that no later delivery closed.
//
// One goroutine for the whole hub, re-checking sequentially: at most one
// authorization query is in flight for recovery, whatever the number of gaps,
// so a database that is already struggling is not handed a fan-out of retries.
// It sleeps until a gap is recorded, then makes passes with exponential backoff
// until none is left unresolved.
func (h *Hub) runSyncRecovery() {
	defer h.broadcastWG.Done()
	for {
		select {
		case <-h.quit:
			return
		case <-h.syncSignal:
		}
		if !h.drainGaps() {
			return
		}
	}
}

// drainGaps runs recovery passes until every gap is resolved. It returns false
// when the hub is shutting down.
func (h *Hub) drainGaps() bool {
	delay := syncRecoveryInitialDelay
	for {
		timer := time.NewTimer(delay)
		select {
		case <-h.quit:
			timer.Stop()
			return false
		case <-timer.C:
		}
		if h.recoverGaps() {
			return true
		}
		delay = min(delay*2, syncRecoveryMaxDelay)
	}
}

// recoverGaps makes one pass and reports whether every gap was resolved.
func (h *Hub) recoverGaps() bool {
	resolved := true
	for _, gap := range h.pendingGaps() {
		if !h.recoverGap(gap) {
			resolved = false
		}
	}
	return resolved
}

// recoverGap re-proves access for one gap and acts on the answer exactly as the
// broadcast path would. It reports false while access is still unprovable.
func (h *Hub) recoverGap(gap pendingGap) bool {
	c := gap.subscription.client
	h.mu.RLock()
	pending := h.gapIsPendingLocked(gap.subscription, gap.key)
	h.mu.RUnlock()
	target, ok := parseTargetKey(gap.key)
	if !pending || !ok {
		return true
	}

	ctx, cancel := context.WithTimeout(c.ctx, broadcastAuthTimeout)
	allowed, err := h.authorizer.CanAccess(ctx, c.userID, c.workspaceID, target.targetType, target.targetID)
	cancel()
	outcome := classifyRecheck(c, allowed, err)
	h.delivery().AuthRecheck(string(target.targetType), string(outcome))

	switch outcome {
	case recheckAllowed:
		h.signalRecoveredGap(gap)
	case recheckDenied:
		// The same revocation the broadcast path applies, scoped to the
		// generation that was asked about; it also drops the gap. A denial about
		// a subscription replaced meanwhile changes nothing (CQ947-06).
		if h.revokeDeniedSubscription(context.Background(), gap.subscription, gap.key) == nil {
			h.delivery().SyncRecovery(syncRevoked)
		}
	case recheckClientClosed:
		// Unregistering the connection drops the gap with it.
	default:
		h.delivery().SyncRecovery(syncDeferred)
		h.logger.WarnContext(context.Background(), "ws: sync recovery re-check failed; will retry",
			"target_type", string(target.targetType),
			"error_class", string(outcome),
			"client_id", c.id,
			"error", err,
		)
		return false
	}
	return true
}

func (h *Hub) signalRecoveredGap(gap pendingGap) {
	h.mu.Lock()
	queued := h.signalGapLocked(gap.subscription, gap.key)
	h.mu.Unlock()
	if queued {
		return
	}
	h.dropSlowClient(gap.subscription.client)
}

// dropSlowClient is the outbox-full policy every enqueue path shares: a client
// that cannot keep up is disconnected, and reconciles when it comes back.
func (h *Hub) dropSlowClient(c *Client) {
	h.logger.WarnContext(context.Background(), "ws: dropping slow client",
		"user_id", c.userID,
		"client_id", c.id,
	)
	h.delivery().Disconnect(disconnectSlowClient)
	h.Unregister(c)
	c.close()
}
