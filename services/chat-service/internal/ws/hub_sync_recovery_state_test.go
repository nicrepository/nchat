package ws

// Issue #947: the parts of sync recovery a connection cannot observe — how much
// state a gap costs, when it is forgotten, how a re-check is classified, and
// what reaches the metrics. The behavioural contract is in
// hub_sync_recovery_test.go.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type recordingDeliveryMetrics struct {
	mu          sync.Mutex
	rechecks    []string
	recoveries  []string
	disconnects []string
}

func (m *recordingDeliveryMetrics) AuthRecheck(targetType, outcome string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rechecks = append(m.rechecks, targetType+"/"+outcome)
}

func (m *recordingDeliveryMetrics) SyncRecovery(outcome string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recoveries = append(m.recoveries, outcome)
}

func (m *recordingDeliveryMetrics) Disconnect(reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.disconnects = append(m.disconnects, reason)
}

func (m *recordingDeliveryMetrics) snapshot() (rechecks, recoveries, disconnects []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.rechecks), slices.Clone(m.recoveries), slices.Clone(m.disconnects)
}

func pendingGapCount(h *Hub) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	n := 0
	for _, keys := range h.pendingSync {
		n += len(keys)
	}
	return n
}

// B08: memory is bounded by subscriptions, never by lost events.
func TestHub_GapLedgerHoldsOneEntryPerSubscription(t *testing.T) {
	auth := &fakeAuthorizer{}
	h := newTestHub(auth)
	c := newClient("c1", "user-1", "ws-1", &fakeSender{})
	registerInHub(t, h, c)
	auth.setAccess("user-1", "ws-1", TargetTypeChannel, "ch-1", true)
	auth.setAccess("user-1", "ws-1", TargetTypeDM, "dm-1", true)
	mustSubscribe(t, h, c, TargetTypeChannel, "ch-1")
	mustSubscribe(t, h, c, TargetTypeDM, "dm-1")
	auth.setErr("user-1", "ws-1", TargetTypeChannel, "ch-1", errors.New("db unavailable"))
	auth.setErr("user-1", "ws-1", TargetTypeDM, "dm-1", errors.New("db unavailable"))

	for i := range 500 {
		broadcastMessage(h, TargetTypeChannel, "ch-1", fmt.Sprintf("c-%d", i))
		broadcastMessage(h, TargetTypeDM, "dm-1", fmt.Sprintf("d-%d", i))
	}

	if n := pendingGapCount(h); n != 2 {
		t.Fatalf("1000 skipped deliveries on 2 subscriptions must cost 2 entries, got %d", n)
	}
	if len(c.outbox) != 0 {
		t.Fatalf("nothing may be queued while access is unproven, got %d", len(c.outbox))
	}
}

// B07/I7: every way a subscription ends takes its gap with it.
func TestHub_GapIsForgottenWithItsSubscription(t *testing.T) {
	setup := func(t *testing.T) (*Hub, *Client, string) {
		auth := &fakeAuthorizer{}
		h := newTestHub(auth)
		c := newClient("c1", "user-1", "ws-1", &fakeSender{})
		registerInHub(t, h, c)
		auth.setAccess("user-1", "ws-1", TargetTypeChannel, "ch-1", true)
		mustSubscribe(t, h, c, TargetTypeChannel, "ch-1")
		auth.setErr("user-1", "ws-1", TargetTypeChannel, "ch-1", errors.New("db unavailable"))
		broadcastMessage(h, TargetTypeChannel, "ch-1", "msg-1")
		if pendingGapCount(h) != 1 {
			t.Fatal("setup: the skipped delivery must be recorded")
		}
		return h, c, targetKey{workspaceID: "ws-1", targetType: TargetTypeChannel, targetID: "ch-1"}.String()
	}

	t.Run("revocation", func(t *testing.T) {
		h, c, key := setup(t)
		h.revokeSubscription(c, key)
		if pendingGapCount(h) != 0 {
			t.Fatal("a revoked subscription must not keep its gap")
		}
	})
	t.Run("disconnect", func(t *testing.T) {
		h, c, _ := setup(t)
		h.removeClient(c)
		h.mu.RLock()
		_, leaked := h.pendingSync["c1"]
		h.mu.RUnlock()
		if leaked {
			t.Fatal("a departed connection must not keep its gaps")
		}
	})
	t.Run("shutdown", func(t *testing.T) {
		h, _, _ := setup(t)
		h.clearClients()
		if pendingGapCount(h) != 0 {
			t.Fatal("shutdown must drop every gap")
		}
	})
}

// cancelAwareAuthorizer answers like a real store: a check whose context is
// already done fails with that context's error.
type cancelAwareAuthorizer struct{ fakeAuthorizer }

func (a *cancelAwareAuthorizer) CanAccess(ctx context.Context, userID, workspaceID string, tt TargetType, targetID string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return a.fakeAuthorizer.CanAccess(ctx, userID, workspaceID, tt, targetID)
}

// A connection that is already closing cancels its own context before the hub
// unregisters it. Those failed checks are not a gap — the connection has no
// future, and its successor reconciles on open — and they used to be logged as
// auth re-check errors in bursts, one per queued broadcast.
func TestHub_ClosingConnectionIsNotRecordedAsAGap(t *testing.T) {
	auth := &cancelAwareAuthorizer{}
	metrics := &recordingDeliveryMetrics{}
	h := newTestHub(auth)
	h.deliveryMetrics = metrics
	c := newClient("c1", "user-1", "ws-1", &fakeSender{})
	registerInHub(t, h, c)
	auth.setAccess("user-1", "ws-1", TargetTypeChannel, "ch-1", true)
	mustSubscribe(t, h, c, TargetTypeChannel, "ch-1")

	c.close()
	broadcastMessage(h, TargetTypeChannel, "ch-1", "msg-1")

	if len(c.outbox) != 0 || pendingGapCount(h) != 0 {
		t.Fatal("a closing connection must receive nothing and record no gap")
	}
	rechecks, recoveries, _ := metrics.snapshot()
	if !slices.Equal(rechecks, []string{"channel/client_closed"}) || len(recoveries) != 0 {
		t.Fatalf("got rechecks %v, recoveries %v", rechecks, recoveries)
	}
}

func TestClassifyRecheck(t *testing.T) {
	open := newClient("c1", "u", "ws", &fakeSender{})
	closing := newClient("c2", "u", "ws", &fakeSender{})
	closing.close()
	for _, tc := range []struct {
		name    string
		client  *Client
		allowed bool
		err     error
		want    recheckOutcome
	}{
		{"allowed", open, true, nil, recheckAllowed},
		{"denied", open, false, nil, recheckDenied},
		{"timeout", open, false, fmt.Errorf("query: %w", context.DeadlineExceeded), recheckTimeout},
		{"operational", open, false, errors.New("pool exhausted"), recheckError},
		// An error is never a denial, even with allowed=true.
		{"error with allowed", open, true, errors.New("partial row"), recheckError},
		{"client closing", closing, false, context.Canceled, recheckClientClosed},
		{"closing client, other error", closing, false, errors.New("pool exhausted"), recheckClientClosed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyRecheck(tc.client, tc.allowed, tc.err); got != tc.want {
				t.Fatalf("classifyRecheck = %q, want %q", got, tc.want)
			}
		})
	}
}

// B15: every label is drawn from a closed set and none names a user, a
// connection, a target or a message.
func TestHub_DeliveryMetricsUseClosedLabels(t *testing.T) {
	auth := &fakeAuthorizer{}
	metrics := &recordingDeliveryMetrics{}
	h := NewHub(auth, newTestLogger(), NopBus{}, "metrics-test", WithDeliveryMetrics(metrics))
	t.Cleanup(h.Shutdown)
	allowed := subscribedRunningClient(t, h, auth, "c-allowed", "user-1", channelOne)
	_ = subscribedRunningClient(t, h, auth, "c-denied", "user-2", channelOne)
	failing := subscribedRunningClient(t, h, auth, "c-failing", "user-3", channelOne)

	auth.setAccess("user-2", "ws-1", TargetTypeChannel, "ch-1", false)
	auth.setErr("user-3", "ws-1", TargetTypeChannel, "ch-1", context.DeadlineExceeded)
	broadcastMessage(h, TargetTypeChannel, "ch-1", "msg-1")
	auth.setErr("user-3", "ws-1", TargetTypeChannel, "ch-1", nil)
	awaitFrame(t, failing, syncRequiredFrameType, syncRecoveryWait)
	drainOutboxFrames(t, allowed)

	rechecks, recoveries, _ := metrics.snapshot()
	for _, want := range []string{"channel/allowed", "channel/denied", "channel/timeout"} {
		if !slices.Contains(rechecks, want) {
			t.Fatalf("missing recheck %q in %v", want, rechecks)
		}
	}
	if !slices.Equal(recoveries, []string{syncGapRecorded, syncSignalled}) {
		t.Fatalf("recoveries = %v", recoveries)
	}
	closed := []string{"allowed", "denied", "timeout", "error", "client_closed",
		syncGapRecorded, syncSignalled, syncRevoked, syncDeferred}
	for _, label := range append(rechecks, recoveries...) {
		value := label[strings.LastIndex(label, "/")+1:]
		if !slices.Contains(closed, value) {
			t.Fatalf("label %q is not from the closed set", label)
		}
	}
}

// B14: the two transport failures that drop a connection are classified.
func TestHub_DisconnectReasonsAreClassified(t *testing.T) {
	metrics := &recordingDeliveryMetrics{}
	h := NewHub(&fakeAuthorizer{}, newTestLogger(), NopBus{}, "disconnect-test", WithDeliveryMetrics(metrics))
	t.Cleanup(h.Shutdown)

	sendFails := &controllableSender{}
	writer := newClient("c-send", "u1", "ws1", sendFails)
	registerInRunningHub(t, h, writer)
	sendFails.Close()
	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		writer.writePump(context.Background(), h, nil)
	}()
	writer.enqueue([]byte(`{"type":"test"}`))
	awaitGoroutineExit(t, pumpDone, "writePump")

	pingFails := &controllableSender{}
	pinger := newClient("c-ping", "u2", "ws1", pingFails)
	registerInRunningHub(t, h, pinger)
	pingFails.setNextPingError(errors.New("ping timeout"))
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		startHeartbeat(context.Background(), pinger, h, nil, time.Millisecond, 5*time.Millisecond)
	}()
	awaitGoroutineExit(t, heartbeatDone, "heartbeat")

	_, _, disconnects := metrics.snapshot()
	slices.Sort(disconnects)
	if !slices.Equal(disconnects, []string{disconnectHeartbeat, disconnectSendError}) {
		t.Fatalf("disconnects = %v", disconnects)
	}
}

// A sync signal that cannot be queued is a slow client like any other: it is
// dropped, and the reconnect reconciles.
func TestHub_FullOutboxOnSyncSignalDropsTheClient(t *testing.T) {
	auth := &fakeAuthorizer{}
	h := newRunningTestHub(t, auth)
	snd := &fakeSender{}
	c := newClient("c1", "user-1", "ws-1", snd)
	registerInRunningHub(t, h, c)
	auth.setAccess("user-1", "ws-1", TargetTypeChannel, "ch-1", true)
	if err := h.Subscribe(context.Background(), c, TargetTypeChannel, "ch-1"); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	auth.setErr("user-1", "ws-1", TargetTypeChannel, "ch-1", errors.New("db unavailable"))
	broadcastMessage(h, TargetTypeChannel, "ch-1", "msg-1")
	for c.enqueue([]byte(`{}`)) {
	}
	auth.setErr("user-1", "ws-1", TargetTypeChannel, "ch-1", nil)

	eventually(t, func() bool { return snd.isClosed() && !hubHasClient(h, "c1") }, syncRecoveryWait,
		"a client that cannot take the sync signal must be dropped")
}
