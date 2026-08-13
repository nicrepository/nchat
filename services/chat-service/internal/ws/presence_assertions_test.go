package ws

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// Assertion lifecycle and leases (RF-58, CQ-1 and CQ-2).
//
// These look at the shared directory, not at the hub's subscription maps. An
// earlier round asserted on the hub's own state and so proved nothing about what
// another instance can actually see, which is the only thing that matters here.

// directoryTargetsFor asks the directory which of these targets currently hold
// the user, the way a remote instance would.
func directoryTargetsFor(t *testing.T, shared *fakeDirectory, userID string, keys ...string) map[string]bool {
	t.Helper()
	present := make(map[string]bool, len(keys))
	for _, key := range keys {
		entries, err := shared.view("reader").Present(context.Background(), key)
		if err != nil {
			t.Fatalf("directory read: %v", err)
		}
		for _, user := range aggregateRoster(entries) {
			if user.UserID == userID {
				present[key] = true
			}
		}
	}
	return present
}

// newAssertionTestNode is a hub wired to a shared directory, with a tracker.
func newAssertionTestNode(t *testing.T, shared *fakeDirectory, clk *fakeClock) (*Hub, *PresenceTracker) {
	t.Helper()
	tracker := newTestPresenceTracker(5*time.Minute, clk)
	return newClusterNode("node-a", shared.view("node-a"), tracker), tracker
}

// joinTarget subscribes a client and lets the announce reach the directory.
func joinTarget(t *testing.T, h *Hub, c *Client, targetID string) string {
	t.Helper()
	key := subscribeInHubState(t, h, c, TargetTypeChannel, targetID)
	h.handleSubscribed(c, TargetTypeChannel, targetID, 0)
	drainPresenceEvents(t, h)
	return key
}

func connectClient(t *testing.T, h *Hub, tracker *PresenceTracker, id, userID string) *Client {
	t.Helper()
	c := newClient(id, userID, "ws-1", &fakeSender{})
	registerInHub(t, h, c)
	tracker.Connect(c.workspaceID, c.userID, c.id)
	return c
}

// ── lifecycle (CQ-1) ─────────────────────────────────────────────────────────

// One connection, one conversation, then it unsubscribes.
func TestAssertionLifecycle_UnsubscribeWithdrawsTheAssertion(t *testing.T) {
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()
	h, tracker := newAssertionTestNode(t, shared, clk)

	c := connectClient(t, h, tracker, "c-1", "user-1")
	key := joinTarget(t, h, c, "chan-x")

	if !directoryTargetsFor(t, shared, "user-1", key)[key] {
		t.Fatal("expected the assertion to exist after the subscribe")
	}

	if !h.revokeSubscription(c, key) {
		t.Fatal("unsubscribe failed")
	}

	if directoryTargetsFor(t, shared, "user-1", key)[key] {
		t.Fatal("the assertion survived the unsubscribe")
	}
	// The person did not go anywhere.
	if got := tracker.Status("ws-1", "user-1"); got != PresenceOnline {
		t.Fatalf("expected the user to stay online, got %q", got)
	}
}

// Two connections covering the same conversation: the first unsubscribe changes
// nothing, the second withdraws.
func TestAssertionLifecycle_UnsubscribeKeepsTargetCoveredByAnotherConnection(t *testing.T) {
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()
	h, tracker := newAssertionTestNode(t, shared, clk)

	connA := connectClient(t, h, tracker, "c-a", "user-1")
	connB := connectClient(t, h, tracker, "c-b", "user-1")
	key := joinTarget(t, h, connA, "chan-x")
	joinTarget(t, h, connB, "chan-x")

	if !h.revokeSubscription(connA, key) {
		t.Fatal("unsubscribe A failed")
	}
	if !directoryTargetsFor(t, shared, "user-1", key)[key] {
		t.Fatal("the assertion was withdrawn while another connection still covered the target")
	}

	if !h.revokeSubscription(connB, key) {
		t.Fatal("unsubscribe B failed")
	}
	if directoryTargetsFor(t, shared, "user-1", key)[key] {
		t.Fatal("the assertion survived the last unsubscribe")
	}
}

// Partial disconnect over different conversations. The aggregate does not move,
// which is exactly why the old code — gated on PresenceChange.Changed — did no
// directory work here at all.
func TestAssertionLifecycle_PartialDisconnectWithdrawsOnlyUncoveredTargets(t *testing.T) {
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()
	h, tracker := newAssertionTestNode(t, shared, clk)

	connA := connectClient(t, h, tracker, "c-a", "user-1")
	connB := connectClient(t, h, tracker, "c-b", "user-1")
	keyX := joinTarget(t, h, connA, "chan-x")
	keyY := joinTarget(t, h, connB, "chan-y")

	h.dropClient(connA)
	drainPresenceEvents(t, h)

	if got := tracker.Status("ws-1", "user-1"); got != PresenceOnline {
		t.Fatalf("expected the user to stay online, got %q", got)
	}
	present := directoryTargetsFor(t, shared, "user-1", keyX, keyY)
	if present[keyX] {
		t.Fatal("a conversation nothing covers any more still asserts the user")
	}
	if !present[keyY] {
		t.Fatal("a conversation the surviving connection covers lost its assertion")
	}
}

// Partial disconnect over a shared conversation: the survivor keeps it.
func TestAssertionLifecycle_PartialDisconnectKeepsSharedTarget(t *testing.T) {
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()
	h, tracker := newAssertionTestNode(t, shared, clk)

	connA := connectClient(t, h, tracker, "c-a", "user-1")
	connB := connectClient(t, h, tracker, "c-b", "user-1")
	key := joinTarget(t, h, connA, "chan-x")
	joinTarget(t, h, connB, "chan-x")

	h.dropClient(connA)
	drainPresenceEvents(t, h)
	if !directoryTargetsFor(t, shared, "user-1", key)[key] {
		t.Fatal("a shared conversation lost its assertion when one connection closed")
	}

	h.dropClient(connB)
	drainPresenceEvents(t, h)
	if directoryTargetsFor(t, shared, "user-1", key)[key] {
		t.Fatal("the last disconnect left the assertion behind")
	}
	if got := tracker.Status("ws-1", "user-1"); got != PresenceOffline {
		t.Fatalf("expected offline after the last connection, got %q", got)
	}
}

// Opening and closing many conversations leaves nothing behind — checked in the
// directory, which is where accumulation would actually matter.
func TestAssertionLifecycle_NoAccumulationInTheDirectory(t *testing.T) {
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()
	h, tracker := newAssertionTestNode(t, shared, clk)

	c := connectClient(t, h, tracker, "c-1", "user-1")

	keys := make([]string, 0, 30)
	for i := range 30 {
		key := joinTarget(t, h, c, fmt.Sprintf("chan-%02d", i))
		keys = append(keys, key)
		if !h.revokeSubscription(c, key) {
			t.Fatalf("unsubscribe %d failed", i)
		}
	}

	if present := directoryTargetsFor(t, shared, "user-1", keys...); len(present) != 0 {
		t.Fatalf("the directory accumulated %d abandoned conversation(s)", len(present))
	}
	if got := h.renewableAssertionKeys(); len(got) != 0 {
		t.Fatalf("the ledger accumulated %d target(s)", len(got))
	}
}

// One instance withdrawing must never touch another's assertion.
func TestAssertionLifecycle_WithdrawalIsScopedToTheInstance(t *testing.T) {
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()

	trackerA := newTestPresenceTracker(5*time.Minute, clk)
	nodeA := newClusterNode("node-a", shared.view("node-a"), trackerA)
	trackerB := newTestPresenceTracker(5*time.Minute, clk)
	nodeB := newClusterNode("node-b", shared.view("node-b"), trackerB)

	ca := connectClient(t, nodeA, trackerA, "c-a", "user-1")
	key := joinTarget(t, nodeA, ca, "chan-x")
	cb := connectClient(t, nodeB, trackerB, "c-b", "user-1")
	joinTarget(t, nodeB, cb, "chan-x")

	if !nodeA.revokeSubscription(ca, key) {
		t.Fatal("unsubscribe failed")
	}
	if !directoryTargetsFor(t, shared, "user-1", key)[key] {
		t.Fatal("one instance's withdrawal removed another instance's live assertion")
	}
}

// ── leases (CQ-2) ────────────────────────────────────────────────────────────

// A user online all day, producing no transitions at all, must not expire out of
// the directory underneath themselves.
func TestAssertionLease_SurvivesBeyondTTLWithoutTransitions(t *testing.T) {
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()
	shared.clock = clk.Now
	h, tracker := newAssertionTestNode(t, shared, clk)

	c := connectClient(t, h, tracker, "c-1", "user-1")
	key := joinTarget(t, h, c, "chan-x")

	// Well past the lease, with the instance alive and the user connected. Only
	// the heartbeat's renewal can keep this alive: no transition is produced.
	for elapsed := time.Duration(0); elapsed < directoryEntryTTL*2; elapsed += instanceHeartbeatInterval {
		clk.Advance(instanceHeartbeatInterval)
		h.heartbeatDirectory()
	}

	if !directoryTargetsFor(t, shared, "user-1", key)[key] {
		t.Fatal("an active assertion expired while its user was connected")
	}
	if shared.refreshCount() == 0 {
		t.Fatal("expected the heartbeat to have renewed the lease")
	}
}

// Without renewal the lease is what it says it is.
func TestAssertionLease_ExpiresWithoutRenewal(t *testing.T) {
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()
	shared.clock = clk.Now
	h, tracker := newAssertionTestNode(t, shared, clk)

	c := connectClient(t, h, tracker, "c-1", "user-1")
	key := joinTarget(t, h, c, "chan-x")

	clk.Advance(directoryEntryTTL + time.Minute)

	if directoryTargetsFor(t, shared, "user-1", key)[key] {
		t.Fatal("an unrenewed lease did not expire")
	}
}

// Renewal follows the ledger, so a conversation that lost its cover is not kept
// alive by the heartbeat.
func TestAssertionLease_DoesNotRenewWithdrawnTargets(t *testing.T) {
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()
	shared.clock = clk.Now
	h, tracker := newAssertionTestNode(t, shared, clk)

	c := connectClient(t, h, tracker, "c-1", "user-1")
	kept := joinTarget(t, h, c, "chan-kept")
	dropped := joinTarget(t, h, c, "chan-dropped")

	if !h.revokeSubscription(c, dropped) {
		t.Fatal("unsubscribe failed")
	}

	h.heartbeatDirectory()
	for _, key := range shared.refreshedKeys() {
		if key == dropped {
			t.Fatal("the heartbeat renewed a conversation that had lost its cover")
		}
	}
	if !directoryTargetsFor(t, shared, "user-1", kept)[kept] {
		t.Fatal("the covered conversation lost its assertion")
	}
}

// Renewing a shared key must not resurrect a dead instance's assertions:
// liveness, not expiry, is what decides whether an assertion counts.
func TestAssertionLease_DeadInstanceStopsCountingDespiteLiveKey(t *testing.T) {
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()
	shared.clock = clk.Now

	trackerA := newTestPresenceTracker(5*time.Minute, clk)
	nodeA := newClusterNode("node-a", shared.view("node-a"), trackerA)
	trackerB := newTestPresenceTracker(5*time.Minute, clk)
	nodeB := newClusterNode("node-b", shared.view("node-b"), trackerB)

	ca := connectClient(t, nodeA, trackerA, "c-a", "user-dead")
	key := joinTarget(t, nodeA, ca, "chan-x")
	cb := connectClient(t, nodeB, trackerB, "c-b", "user-live")
	joinTarget(t, nodeB, cb, "chan-x")

	// Node A dies; node B keeps the shared key alive with its own renewals.
	shared.killInstance("node-a")
	clk.Advance(directoryEntryTTL / 2)
	nodeB.heartbeatDirectory()

	if directoryTargetsFor(t, shared, "user-dead", key)[key] {
		t.Fatal("a renewed key kept a dead instance's assertion alive")
	}
	if !directoryTargetsFor(t, shared, "user-live", key)[key] {
		t.Fatal("the live instance's own assertion was lost")
	}
}
