package ws

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// The confirmed-assertion ledger (RF-58, CQ-1 and CQ-2).
//
// The ledger records what the directory *accepted*, never what was attempted.
// These tests are about the difference: a write that was overtaken by an
// unsubscribe, and a write that failed.

// ── a publish overtaken by an unsubscribe (CQ-1) ─────────────────────────────

// The interleaving the review found: a subscribe queues a publish, the
// subscription ends, and the queued publish then writes an assertion for a
// conversation nothing covers any more.
//
// Deterministic, with the directory suspended exactly at the write rather than
// with any timing assumption.
func TestLedger_PublishOvertakenByUnsubscribeLeavesNothing(t *testing.T) {
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()
	h, tracker := newAssertionTestNode(t, shared, clk)

	c := connectClient(t, h, tracker, "c-1", "user-1")
	key := subscribeInHubState(t, h, c, TargetTypeChannel, "chan-x")
	h.handleSubscribed(c, TargetTypeChannel, "chan-x", 0)

	// Hold the write open, then run the publish on its own goroutine so the
	// unsubscribe can land while it is in flight.
	release, blocked := shared.blockRecord()
	published := make(chan struct{})
	go func() {
		defer close(published)
		drainPresenceEvents(t, h)
	}()
	<-blocked // the write is parked

	// The subscription ends while the write is suspended. On its own goroutine:
	// the withdrawal it triggers now has a field to remove — the write is in
	// flight, so this instance is answerable for it — and it waits its turn on
	// the same sequencer rather than racing past the write.
	revoked := make(chan struct{})
	go func() {
		defer close(revoked)
		if !h.revokeSubscription(c, key) {
			t.Error("unsubscribe failed")
		}
	}()
	waitUntilUncovered(t, h, "ws-1", "user-1", key)

	release()
	<-published
	<-revoked

	if directoryTargetsFor(t, shared, "user-1", key)[key] {
		t.Fatal("a publish overtaken by an unsubscribe left an assertion behind")
	}
	if got := h.assertedTargetsList("ws-1", "user-1"); len(got) != 0 {
		t.Fatalf("the ledger kept a withdrawn conversation: %v", got)
	}

	// And a later heartbeat does not renew it back into existence.
	h.heartbeatDirectory()
	if directoryTargetsFor(t, shared, "user-1", key)[key] {
		t.Fatal("the heartbeat resurrected a withdrawn assertion")
	}
	for _, refreshed := range shared.refreshedKeys() {
		if refreshed == key {
			t.Fatal("a withdrawn conversation was renewed")
		}
	}
}

// The interleaving that survived the coverage fence: a publication captured
// before an unsubscribe finishing after the resubscribe, when the person's state
// has moved on in between.
//
// Generation 1 carries online/t0. While it is suspended the user goes away at
// t1, and generation 2 is published. Both write the same field — user|instance
// carries no generation — so whichever lands last decides what the cluster sees.
//
// Two things make that safe, and this test exercises both: the writes for one
// person are serialised, so "last" is a defined order rather than a race; and
// the state written is read inside that critical section, so a publication can
// never carry a stale payload back to the directory.
func TestLedger_StaleWorkCannotDowngradeANewerState(t *testing.T) {
	const awayTimeout = 5 * time.Minute
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()
	tracker := newTestPresenceTracker(awayTimeout, clk)
	h := newClusterNode("node-a", shared.view("node-a"), tracker)

	c := connectClient(t, h, tracker, "c-1", "user-1")

	// Generation 1: online at t0, suspended at the write.
	key := subscribeInHubState(t, h, c, TargetTypeChannel, "chan-x")
	h.handleSubscribed(c, TargetTypeChannel, "chan-x", 0)
	release, blocked := shared.blockRecord()
	gen1 := make(chan struct{})
	go func() {
		defer close(gen1)
		drainPresenceEvents(t, h)
	}()
	<-blocked // generation 1 is inside the write, holding this user's turn

	// The subscription ends and the person's state moves on: away at t1 > t0.
	// On its own goroutine — the withdrawal it triggers waits for generation 1 to
	// come out of the write, which is the sequencer doing its job.
	revoked := make(chan struct{})
	go func() {
		defer close(revoked)
		if !h.revokeSubscription(c, key) {
			t.Error("unsubscribe failed")
		}
	}()
	waitUntilUncovered(t, h, "ws-1", "user-1", key)
	clk.Advance(awayTimeout + time.Second)
	tracker.checkAway()
	if got := tracker.Status("ws-1", "user-1"); got != PresenceAway {
		t.Fatalf("expected the user to be away, got %q", got)
	}
	_, t1 := tracker.StatusAt("ws-1", "user-1")

	// Generation 2 subscribes and publishes. It queues behind generation 1,
	// which is the ordering the sequencer exists to impose.
	subscribeInHubState(t, h, c, TargetTypeChannel, "chan-x")
	h.handleSubscribed(c, TargetTypeChannel, "chan-x", 0)
	gen2 := make(chan struct{})
	go func() {
		defer close(gen2)
		drainPresenceEvents(t, h)
	}()

	release()
	<-gen1
	<-gen2
	<-revoked

	// Whatever order they ran in, the directory holds the *current* state.
	entries, err := shared.view("reader").Present(context.Background(), key)
	if err != nil {
		t.Fatalf("directory read: %v", err)
	}
	roster := aggregateRoster(entries)
	if len(roster) != 1 {
		t.Fatalf("expected exactly one assertion, got %+v", roster)
	}
	if roster[0].State != string(PresenceAway) {
		t.Fatalf("stale work downgraded the state to %q", roster[0].State)
	}
	if roster[0].UpdatedAt != formatPresenceTime(t1) {
		t.Fatalf("stale work downgraded the instant to %q, want %q",
			roster[0].UpdatedAt, formatPresenceTime(t1))
	}

	// No write ever carried the state generation 1 had captured.
	for _, write := range shared.recordedWrites() {
		if write.state == string(PresenceOnline) && write.at == formatPresenceTime(t1) {
			t.Fatal("a write mixed the old state with the new instant")
		}
	}

	// And the ledger is confirmed at the newest version, never rolled back.
	version, confirmed := h.confirmedAssertionVersion("ws-1", "user-1", key)
	if !confirmed {
		t.Fatal("expected the assertion to be confirmed")
	}
	if version != h.highestAssertionVersion() {
		t.Fatalf("ledger confirmed version %d, newest issued %d", version, h.highestAssertionVersion())
	}

	// A later sweep leaves the live assertion alone.
	h.heartbeatDirectory()
	if !directoryTargetsFor(t, shared, "user-1", key)[key] {
		t.Fatal("a later sweep removed the live assertion")
	}
}

// The same fence in the other direction: a withdrawal in flight while the
// conversation comes back must not leave the person missing from it.
//
// The withdrawal holds this user's turn while it runs, so the rejoin's write
// queues behind it and lands afterwards — which is the ordering that makes a
// blind HDEL safe here. Without it the two could cross and the removal would
// delete the assertion the rejoin had just created, since the field carries no
// generation of its own.
func TestLedger_StaleForgetCannotLeaveARejoinedTargetEmpty(t *testing.T) {
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()
	tracker := newTestPresenceTracker(5*time.Minute, clk)
	h := newClusterNode("node-a", shared.view("node-a"), tracker)

	// Every version this assertion is given, in order, so the test names the
	// interleaving instead of inferring it. Set before any goroutine reads it.
	versions := make(chan uint64, 8)
	h.intentOpened = func(opened map[string]uint64) {
		for _, version := range opened {
			versions <- version
		}
	}

	c := connectClient(t, h, tracker, "c-1", "user-1")
	key := joinTarget(t, h, c, "chan-x")
	v1 := <-versions
	if !directoryTargetsFor(t, shared, "user-1", key)[key] {
		t.Fatal("this test needs the first assertion to exist")
	}
	if version, confirmed := h.confirmedAssertionVersion("ws-1", "user-1", key); !confirmed || version != v1 {
		t.Fatalf("ledger holds (%d, %v), want v1 = %d", version, confirmed, v1)
	}

	// The unsubscribe's withdrawal parks inside the directory, holding this
	// user's turn.
	release, blocked := shared.blockForget()
	withdrawal := make(chan struct{})
	go func() {
		defer close(withdrawal)
		if !h.revokeSubscription(c, key) {
			t.Error("unsubscribe failed")
		}
	}()
	v2 := <-versions
	<-blocked

	// The conversation comes back through the real path, and its publication
	// queues behind the withdrawal on the same sequencer.
	subscribeInHubState(t, h, c, TargetTypeChannel, "chan-x")
	h.handleSubscribed(c, TargetTypeChannel, "chan-x", 0)
	rejoin := make(chan struct{})
	go func() {
		defer close(rejoin)
		drainPresenceEvents(t, h)
	}()
	v3 := <-versions

	// The barrier the whole test exists for. Generation 3's intention is on
	// record *while* generation 2 is still inside its HDEL — not soon, not
	// probably: the version it took has been observed.
	if v3 <= v2 || v2 <= v1 {
		t.Fatalf("versions must increase with each intention: v1=%d v2=%d v3=%d", v1, v2, v3)
	}
	current, known := h.currentAssertionEpoch("ws-1", "user-1", key)
	if !known || current != v3 {
		t.Fatalf("epoch is (%d, %v) before the withdrawal was released, want v3 = %d",
			current, known, v3)
	}
	if _, desired := h.desiredAssertions("ws-1", "user-1")[key]; !desired {
		t.Fatal("the rejoined conversation is not desired")
	}
	if shared.forgetCount() != 0 {
		t.Fatal("the withdrawal ran before the barrier")
	}

	release()
	<-withdrawal
	<-rejoin

	// The withdrawal completed and did not take generation 3 with it.
	if !directoryTargetsFor(t, shared, "user-1", key)[key] {
		t.Fatal("a withdrawal crossing a rejoin left the conversation empty")
	}
	if got := h.assertedTargetsList("ws-1", "user-1"); len(got) != 1 || got[0] != key {
		t.Fatalf("the ledger does not describe the live assertion: %v", got)
	}
	version, confirmed := h.confirmedAssertionVersion("ws-1", "user-1", key)
	if !confirmed || version != v3 {
		t.Fatalf("ledger confirmed %d (present=%v), want generation 3's %d",
			version, confirmed, v3)
	}

	// The order the directory actually saw, and where it ended.
	ops := shared.mutationsFor(key)
	want := []string{"record", "forget", "record"}
	if strings.Join(ops, ",") != strings.Join(want, ",") {
		t.Fatalf("mutations were %v, want %v", ops, want)
	}
	if ops[len(ops)-1] != "record" {
		t.Fatalf("the last mutation on the target was %q", ops[len(ops)-1])
	}
}

// The compare-and-delete on its own, without any concurrency to reproduce.
//
// The rule it encodes is one line and the failure it prevents took an
// interleaving to find, which is exactly the shape of thing that quietly
// regresses.
func TestAssertionIntent_RetiredOnlyWhenItIsStillTheCurrentOne(t *testing.T) {
	const key = "ws-1:channel:chan-x"

	t.Run("current version is retired", func(t *testing.T) {
		h := newTestHub(allowAllAuthorizer{})
		v2 := h.openIntent("ws-1", "user-1", []string{key}).versions[key]

		h.retireAssertionIntents("ws-1", "user-1", map[string]uint64{key: v2})

		if version, known := h.currentAssertionEpoch("ws-1", "user-1", key); known {
			t.Fatalf("the completed intention survived as %d", version)
		}
	})

	t.Run("a newer version is preserved", func(t *testing.T) {
		h := newTestHub(allowAllAuthorizer{})
		v2 := h.openIntent("ws-1", "user-1", []string{key}).versions[key]
		v3 := h.openIntent("ws-1", "user-1", []string{key}).versions[key]

		h.retireAssertionIntents("ws-1", "user-1", map[string]uint64{key: v2})

		version, known := h.currentAssertionEpoch("ws-1", "user-1", key)
		if !known || version != v3 {
			t.Fatalf("epoch is (%d, %v), want v3 = %d preserved", version, known, v3)
		}
	})

	t.Run("an absent epoch is a no-op", func(t *testing.T) {
		h := newTestHub(allowAllAuthorizer{})
		h.retireAssertionIntents("ws-1", "user-1", map[string]uint64{key: 42})

		if _, known := h.currentAssertionEpoch("ws-1", "user-1", key); known {
			t.Fatal("retiring an unknown assertion invented an epoch")
		}
	})
}

// A withdrawal that finds the conversation covered again by the time it runs is
// dropped rather than executed: nothing is removed and no call is made.
func TestLedger_WithdrawalIsDroppedWhenCoverageReturnsFirst(t *testing.T) {
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()
	tracker := newTestPresenceTracker(5*time.Minute, clk)
	h := newClusterNode("node-a", shared.view("node-a"), tracker)

	c := connectClient(t, h, tracker, "c-1", "user-1")
	key := joinTarget(t, h, c, "chan-x")

	// Coverage is back before the withdrawal is asked to run.
	subscribeInHubState(t, h, c, TargetTypeChannel, "chan-x")

	forgets := shared.forgetCount()
	h.applyAssertionForget(context.Background(), h.openForgetIntent("ws-1", "user-1", []string{key}))

	if shared.forgetCount() != forgets {
		t.Fatal("a withdrawal ran against coverage that had come back")
	}
	if !directoryTargetsFor(t, shared, "user-1", key)[key] {
		t.Fatal("the assertion was removed despite live coverage")
	}
}

// Even when the state does not change, the newer instant must win: fencing is
// about ordering mutations, not about noticing that a value differs.
func TestLedger_SameStateNewerInstantWins(t *testing.T) {
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()
	tracker := newTestPresenceTracker(5*time.Minute, clk)
	h := newClusterNode("node-a", shared.view("node-a"), tracker)

	c := connectClient(t, h, tracker, "c-1", "user-1")
	key := subscribeInHubState(t, h, c, TargetTypeChannel, "chan-x")
	h.handleSubscribed(c, TargetTypeChannel, "chan-x", 0)

	release, blocked := shared.blockRecord()
	first := make(chan struct{})
	go func() {
		defer close(first)
		drainPresenceEvents(t, h)
	}()
	<-blocked

	// Still online, but the tracker's instant moves forward.
	clk.Advance(time.Minute)
	second := connectClient(t, h, tracker, "c-2", "user-1")
	tracker.Connect(second.workspaceID, second.userID, second.id)
	subscribeInHubState(t, h, second, TargetTypeChannel, "chan-x")
	h.handleSubscribed(second, TargetTypeChannel, "chan-x", 0)
	later := make(chan struct{})
	go func() {
		defer close(later)
		drainPresenceEvents(t, h)
	}()

	release()
	<-first
	<-later

	_, at := tracker.StatusAt("ws-1", "user-1")
	entries, err := shared.view("reader").Present(context.Background(), key)
	if err != nil {
		t.Fatalf("directory read: %v", err)
	}
	roster := aggregateRoster(entries)
	if len(roster) != 1 || roster[0].UpdatedAt != formatPresenceTime(at) {
		t.Fatalf("expected the current instant %q, got %+v", formatPresenceTime(at), roster)
	}
}

// ── failed writes (CQ-2) ─────────────────────────────────────────────────────

// A failed Record confirms nothing, and the next sweep tries again.
func TestLedger_RecordFailureIsNotConfirmedAndIsRetried(t *testing.T) {
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()
	h, tracker := newAssertionTestNode(t, shared, clk)

	shared.failNextRecord(1)

	c := connectClient(t, h, tracker, "c-1", "user-1")
	key := joinTarget(t, h, c, "chan-x")

	if got := h.assertedTargetsList("ws-1", "user-1"); len(got) != 0 {
		t.Fatalf("a failed write was recorded as confirmed: %v", got)
	}
	if directoryTargetsFor(t, shared, "user-1", key)[key] {
		t.Fatal("the directory somehow holds an assertion the write rejected")
	}

	// The heartbeat is where the retry happens — no loop of its own.
	h.heartbeatDirectory()

	if got := h.assertedTargetsList("ws-1", "user-1"); len(got) != 1 {
		t.Fatalf("the retry did not confirm the assertion: %v", got)
	}
	if !directoryTargetsFor(t, shared, "user-1", key)[key] {
		t.Fatal("the retry did not reach the directory")
	}
}

// A failed Forget keeps the entry confirmed — it is the record that something of
// ours is still out there — and the assertion is not renewed while it waits.
func TestLedger_ForgetFailureKeepsRetryStateAndSkipsRenewal(t *testing.T) {
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()
	h, tracker := newAssertionTestNode(t, shared, clk)

	c := connectClient(t, h, tracker, "c-1", "user-1")
	key := joinTarget(t, h, c, "chan-x")

	shared.failForgetWith(errors.New("valkey unavailable"))
	if !h.revokeSubscription(c, key) {
		t.Fatal("unsubscribe failed")
	}

	if got := h.assertedTargetsList("ws-1", "user-1"); len(got) != 1 {
		t.Fatalf("a failed removal dropped the retry state: %v", got)
	}
	// Confirmed but no longer desired, so it must not be renewed.
	for _, renewable := range h.renewableAssertionKeys() {
		if renewable == key {
			t.Fatal("an assertion awaiting removal was still eligible for renewal")
		}
	}

	// The production path, not the helper: the heartbeat is what the ticker runs,
	// and it must not renew a target that is waiting to be removed.
	before := len(shared.refreshedKeys())
	h.heartbeatDirectory()
	for _, refreshed := range shared.refreshedKeys()[before:] {
		if refreshed == key {
			t.Fatal("heartbeatDirectory renewed an assertion awaiting removal")
		}
	}
	if got := h.assertedTargetsList("ws-1", "user-1"); len(got) != 1 {
		t.Fatalf("a still-failing removal must stay pending: %v", got)
	}

	shared.failForgetWith(nil)
	h.heartbeatDirectory()

	if got := h.assertedTargetsList("ws-1", "user-1"); len(got) != 0 {
		t.Fatalf("the retry did not clear the ledger: %v", got)
	}
	if directoryTargetsFor(t, shared, "user-1", key)[key] {
		t.Fatal("the retry did not reach the directory")
	}

	// And nothing renews it afterwards either.
	after := len(shared.refreshedKeys())
	h.heartbeatDirectory()
	for _, refreshed := range shared.refreshedKeys()[after:] {
		if refreshed == key {
			t.Fatal("a removed assertion was renewed by a later heartbeat")
		}
	}
}

// Resubscribing before the retry lands means the removal must not happen at all:
// the target is desired again, so the stale withdrawal would delete live
// coverage.
func TestLedger_ResubscribeDuringFailedForgetKeepsTheAssertion(t *testing.T) {
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()
	h, tracker := newAssertionTestNode(t, shared, clk)

	c := connectClient(t, h, tracker, "c-1", "user-1")
	key := joinTarget(t, h, c, "chan-x")

	shared.failForgetWith(errors.New("valkey unavailable"))
	if !h.revokeSubscription(c, key) {
		t.Fatal("unsubscribe failed")
	}
	shared.failForgetWith(nil)

	// Back before the retry runs: desired and confirmed agree again.
	subscribeInHubState(t, h, c, TargetTypeChannel, "chan-x")

	forgetsBefore := shared.forgetCount()
	h.heartbeatDirectory()
	if shared.forgetCount() != forgetsBefore {
		t.Fatal("a stale removal ran against coverage that had come back")
	}
	if !directoryTargetsFor(t, shared, "user-1", key)[key] {
		t.Fatal("the assertion was removed despite live coverage")
	}
	// And it is renewable again.
	renewed := false
	for _, refreshed := range shared.refreshedKeys() {
		if refreshed == key {
			renewed = true
		}
	}
	if !renewed {
		t.Fatal("a target that is desired and confirmed was not renewed")
	}
}

// The last connection closes and the removal fails: enough state has to survive
// for the next sweep to finish the job.
func TestLedger_FinalDisconnectWithFailedForgetRetries(t *testing.T) {
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()
	h, tracker := newAssertionTestNode(t, shared, clk)

	c := connectClient(t, h, tracker, "c-1", "user-1")
	key := joinTarget(t, h, c, "chan-x")

	shared.failForgetWith(errors.New("valkey unavailable"))
	h.dropClient(c)
	drainPresenceEvents(t, h)

	if got := tracker.Status("ws-1", "user-1"); got != PresenceOffline {
		t.Fatalf("expected the user offline, got %q", got)
	}
	if got := h.assertedTargetsList("ws-1", "user-1"); len(got) != 1 {
		t.Fatalf("the retry state was destroyed before the removal succeeded: %v", got)
	}
	if !directoryTargetsFor(t, shared, "user-1", key)[key] {
		t.Fatal("expected the assertion to still be in the directory")
	}

	shared.failForgetWith(nil)
	h.heartbeatDirectory()

	if got := h.assertedTargetsList("ws-1", "user-1"); len(got) != 0 {
		t.Fatalf("the ledger was not cleaned up after the retry: %v", got)
	}
	if directoryTargetsFor(t, shared, "user-1", key)[key] {
		t.Fatal("the assertion survived the retry")
	}
}

// ── the loop, checked every iteration (CQ-3) ─────────────────────────────────

// Growth that appears and disappears within one cycle is invisible to a test
// that only looks at the end, so this one asserts on every iteration.
func TestLedger_NoGrowthAtAnyPointInTheLoop(t *testing.T) {
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()
	h, tracker := newAssertionTestNode(t, shared, clk)

	c := connectClient(t, h, tracker, "c-1", "user-1")

	for i := range 30 {
		targetID := fmt.Sprintf("chan-%02d", i)
		key := joinTarget(t, h, c, targetID)

		// While subscribed: exactly this one conversation, here and in the store.
		if got := h.assertedTargetsList("ws-1", "user-1"); len(got) != 1 || got[0] != key {
			t.Fatalf("iteration %d: expected exactly the open conversation, got %v", i, got)
		}
		if !directoryTargetsFor(t, shared, "user-1", key)[key] {
			t.Fatalf("iteration %d: the open conversation is not in the directory", i)
		}

		if !h.revokeSubscription(c, key) {
			t.Fatalf("iteration %d: unsubscribe failed", i)
		}

		// After closing it: nothing, in either place.
		if got := h.assertedTargetsList("ws-1", "user-1"); len(got) != 0 {
			t.Fatalf("iteration %d: the ledger kept %v", i, got)
		}
		if directoryTargetsFor(t, shared, "user-1", key)[key] {
			t.Fatalf("iteration %d: the directory kept the closed conversation", i)
		}
	}
}

// ── liveness is temporal (CQ-3) ──────────────────────────────────────────────

func TestLedger_InstanceLivenessLapsesWithTime(t *testing.T) {
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()
	shared.clock = clk.Now

	trackerA := newTestPresenceTracker(5*time.Minute, clk)
	nodeA := newClusterNode("node-a", shared.view("node-a"), trackerA)
	ca := connectClient(t, nodeA, trackerA, "c-a", "user-1")
	key := joinTarget(t, nodeA, ca, "chan-x")
	nodeA.heartbeatDirectory()

	// Inside the liveness window, with no further beat: still believed.
	clk.Advance(instanceLivenessTTL / 2)
	if !directoryTargetsFor(t, shared, "user-1", key)[key] {
		t.Fatal("an instance was written off inside its liveness window")
	}

	// Past it, still with no beat: no longer believed.
	clk.Advance(instanceLivenessTTL)
	if directoryTargetsFor(t, shared, "user-1", key)[key] {
		t.Fatal("an instance that stopped beating was still believed")
	}
}

// The lease survives past the TTL through the production heartbeat, which is
// what the ticker calls — not a helper reachable only from a test.
func TestLedger_LeaseSurvivesThroughTheProductionHeartbeat(t *testing.T) {
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()
	shared.clock = clk.Now
	h, tracker := newAssertionTestNode(t, shared, clk)

	c := connectClient(t, h, tracker, "c-1", "user-1")
	key := joinTarget(t, h, c, "chan-x")

	for elapsed := time.Duration(0); elapsed < directoryEntryTTL*2; elapsed += instanceHeartbeatInterval {
		clk.Advance(instanceHeartbeatInterval)
		h.heartbeatDirectory() // exactly what runDirectoryHeartbeat's ticker calls
	}

	if !directoryTargetsFor(t, shared, "user-1", key)[key] {
		t.Fatal("an active assertion expired while its user was connected")
	}

	// And a late subscriber on another instance still sees them.
	trackerB := newTestPresenceTracker(5*time.Minute, clk)
	nodeB := newClusterNode("node-b", shared.view("node-b"), trackerB)
	observer := connectClient(t, nodeB, trackerB, "c-observer", "user-observer")
	subscribeInHubState(t, nodeB, observer, TargetTypeChannel, "chan-x")

	snapshot := snapshotFor(t, nodeB, observer, TargetTypeChannel, "chan-x")
	found := false
	for _, user := range snapshot.Users {
		if user.UserID == "user-1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("a late subscriber did not see the still-connected user: %+v", snapshot.Users)
	}
}

// The heartbeat goroutine stops with the hub, and running it leaves nothing
// behind.
func TestLedger_HeartbeatStopsWithTheHub(t *testing.T) {
	tracker := NewPresenceTracker(time.Hour)
	defer tracker.Stop()

	shared := newFakeDirectory()
	hub := NewHub(allowAllAuthorizer{}, newTestLogger(), NopBus{}, "heartbeat-shutdown",
		WithPresence(tracker), WithPresenceDirectory(shared.view("heartbeat-shutdown")))
	hub.heartbeatDirectory()
	hub.Shutdown()

	if err := context.Background().Err(); err != nil {
		t.Fatalf("unexpected context error: %v", err)
	}
}

// waitUntilUncovered blocks until a target has left a user's coverage, so a test
// can order itself against an unsubscribe running on another goroutine without
// guessing how long it takes.
func waitUntilUncovered(t *testing.T, h *Hub, workspaceID, userID, key string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, covered := h.coveredTargetsForUser(workspaceID, userID)[key]; !covered {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the conversation never left coverage")
}
