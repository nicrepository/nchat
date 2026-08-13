package ws

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Single writer and intent fencing (RF-58).
//
// Two separate claims are proved here, and the second only holds because of the
// first:
//
//   - the field a mutation writes has exactly one writer, even when two
//     processes are configured identically;
//   - within that writer, work is applied in the order it was *decided*, not in
//     the order it happens to finish.

// ── one writer per field ─────────────────────────────────────────────────────

// Two processes handed the same WS_INSTANCE_ID.
//
// This is not hypothetical: a Deployment that sets the variable to a fixed
// string gives every replica the same value, and nothing in the configuration
// path can detect it. If the directory field were named after it, both processes
// would write user|same-id — each overwriting the other's state and each
// deleting the other's assertion on disconnect — and every ordering guarantee
// this feature rests on would be a guarantee about half the writers.
func TestRuntimeIdentity_ProcessesSharingAConfiguredIDDoNotShareAField(t *testing.T) {
	const configured = "same-id"
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()

	trackerA := newTestPresenceTracker(5*time.Minute, clk)
	nodeA := newClusterNode("runtime-a", shared.view("runtime-a"), trackerA)
	nodeA.instanceID = configured

	trackerB := newTestPresenceTracker(5*time.Minute, clk)
	nodeB := newClusterNode("runtime-b", shared.view("runtime-b"), trackerB)
	nodeB.instanceID = configured

	if nodeA.presenceInstanceID == nodeB.presenceInstanceID {
		t.Fatal("two processes ended up with the same physical presence identity")
	}
	if nodeA.presenceInstanceID == configured || nodeB.presenceInstanceID == configured {
		t.Fatal("the configured id is being used as the physical identity of a field")
	}

	// The same person, the same workspace, the same conversation — one tab on
	// each process. A says online; B has gone idle and says away.
	clientA := connectClient(t, nodeA, trackerA, "c-a", "user-1")
	key := joinTarget(t, nodeA, clientA, "chan-x")

	clientB := connectClient(t, nodeB, trackerB, "c-b", "user-1")
	subscribeInHubState(t, nodeB, clientB, TargetTypeChannel, "chan-x")
	nodeB.handleSubscribed(clientB, TargetTypeChannel, "chan-x", 0)
	drainPresenceEvents(t, nodeB)
	nodeB.applyAssertionRecord(context.Background(),
		nodeB.openRecordIntent("ws-1", "user-1", []string{key}, PresenceAway, clk.Now()))

	fields := shared.fieldsIn(key)
	if len(fields) != 2 {
		t.Fatalf("expected one field per process, got %v", fields)
	}
	for _, field := range fields {
		if strings.HasSuffix(field, "|"+configured) {
			t.Fatalf("a field was named after the configured id: %q", field)
		}
	}

	// Neither overwrote the other, and the aggregate is still meaningful:
	// somewhere they are online, so they are online.
	entries, err := shared.view("reader").Present(context.Background(), key)
	if err != nil {
		t.Fatalf("directory read: %v", err)
	}
	roster := aggregateRoster(entries)
	if len(roster) != 1 || roster[0].State != string(PresenceOnline) {
		t.Fatalf("expected one aggregated online user, got %+v", roster)
	}

	// A's withdrawal takes A's field and leaves B's standing.
	nodeA.applyAssertionForget(context.Background(),
		nodeA.openWithdrawalIntent("ws-1", "user-1", []string{key}))

	remaining := shared.fieldsIn(key)
	if len(remaining) != 1 {
		t.Fatalf("a withdrawal removed more than its own field: %v", remaining)
	}
	if !strings.HasSuffix(remaining[0], "|runtime-b") {
		t.Fatalf("the surviving field is not the other process's: %q", remaining[0])
	}
	entries, err = shared.view("reader").Present(context.Background(), key)
	if err != nil {
		t.Fatalf("directory read: %v", err)
	}
	if roster := aggregateRoster(entries); len(roster) != 1 || roster[0].State != string(PresenceAway) {
		t.Fatalf("expected the other process's away assertion to remain, got %+v", roster)
	}
}

// ── old work arriving late ───────────────────────────────────────────────────

// The interleaving a coverage check cannot catch: generation 1 does not merely
// finish late, it *starts* late — it reaches the sequencer after generation 2
// has already been written and confirmed.
//
// Nothing about the world at that moment says the work is old. The conversation
// is covered, the user is present, the field is writable. The only thing that
// distinguishes it is the version it was stamped with when the decision to write
// it was made, which is why the version is issued there and not here.
func TestFencing_WorkArrivingAfterANewerGenerationIsRejectedBeforeTheWrite(t *testing.T) {
	const awayTimeout = 5 * time.Minute
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()
	tracker := newTestPresenceTracker(awayTimeout, clk)
	h := newClusterNode("runtime-a", shared.view("runtime-a"), tracker)

	c := connectClient(t, h, tracker, "c-1", "user-1")
	key := subscribeInHubState(t, h, c, TargetTypeChannel, "chan-x")
	_, t0 := tracker.StatusAt("ws-1", "user-1")

	// Generation 1 is decided — online at t0 — and then simply not executed.
	gen1 := h.openRecordIntent("ws-1", "user-1", []string{key}, PresenceOnline, t0)

	// The subscription ends, the person goes away at t1 > t0, and they come back.
	if !h.revokeSubscription(c, key) {
		t.Fatal("unsubscribe failed")
	}
	clk.Advance(awayTimeout + time.Second)
	tracker.checkAway()
	_, t1 := tracker.StatusAt("ws-1", "user-1")
	if !t1.After(t0) {
		t.Fatalf("the test needs a later instant: t0=%v t1=%v", t0, t1)
	}

	subscribeInHubState(t, h, c, TargetTypeChannel, "chan-x")
	gen2 := h.openRecordIntent("ws-1", "user-1", []string{key}, PresenceAway, t1)
	if gen2.versions[key] <= gen1.versions[key] {
		t.Fatalf("generation 2 must carry a newer version: %d vs %d",
			gen2.versions[key], gen1.versions[key])
	}
	h.applyAssertionRecord(context.Background(), gen2)

	writesAfterGen2 := len(shared.recordedWrites())
	if writesAfterGen2 != 1 {
		t.Fatalf("expected generation 2 to be the only write so far, got %d", writesAfterGen2)
	}

	// Now the old work runs, into a world that would otherwise accept it.
	h.applyAssertionRecord(context.Background(), gen1)

	// It never reached the directory. This is the assertion the whole fence is
	// for: not that the damage was repaired, but that it was never done.
	writes := shared.recordedWrites()
	if len(writes) != writesAfterGen2 {
		t.Fatalf("stale work reached the directory: %+v", writes[writesAfterGen2:])
	}
	for _, write := range writes {
		if write.state == string(PresenceOnline) && write.at == formatPresenceTime(t0) {
			t.Fatalf("the state generation 1 captured was written: %+v", write)
		}
	}

	entries, err := shared.view("reader").Present(context.Background(), key)
	if err != nil {
		t.Fatalf("directory read: %v", err)
	}
	roster := aggregateRoster(entries)
	if len(roster) != 1 {
		t.Fatalf("expected one assertion, got %+v", roster)
	}
	if roster[0].State != string(PresenceAway) || roster[0].UpdatedAt != formatPresenceTime(t1) {
		t.Fatalf("directory holds %s/%s, want away/%s",
			roster[0].State, roster[0].UpdatedAt, formatPresenceTime(t1))
	}

	version, confirmed := h.confirmedAssertionVersion("ws-1", "user-1", key)
	if !confirmed {
		t.Fatal("expected generation 2 to be confirmed")
	}
	if version != gen2.versions[key] {
		t.Fatalf("ledger confirmed version %d, want generation 2's %d", version, gen2.versions[key])
	}
}

// The mirror image: a withdrawal decided before a resubscribe, executed after
// the resubscribe's assertion is already in the store.
//
// A blind HDEL here deletes a live assertion, and nothing would put it back —
// the person is present, covered and authorized, so no later transition is
// coming to correct it. They would simply vanish from the conversation.
func TestFencing_ForgetArrivingAfterANewerRecordIsRejectedBeforeTheDelete(t *testing.T) {
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()
	tracker := newTestPresenceTracker(5*time.Minute, clk)
	h := newClusterNode("runtime-a", shared.view("runtime-a"), tracker)

	c := connectClient(t, h, tracker, "c-1", "user-1")
	key := joinTarget(t, h, c, "chan-x")
	if !directoryTargetsFor(t, shared, "user-1", key)[key] {
		t.Fatal("this test needs an assertion to exist first")
	}

	// The unsubscribe's withdrawal is decided and left unexecuted.
	if !h.revokeSubscription(c, key) {
		t.Fatal("unsubscribe failed")
	}
	forget := h.openForgetIntent("ws-1", "user-1", []string{key})

	// They come back, and that assertion is written and confirmed.
	subscribeInHubState(t, h, c, TargetTypeChannel, "chan-x")
	status, at := tracker.StatusAt("ws-1", "user-1")
	record := h.openRecordIntent("ws-1", "user-1", []string{key}, status, at)
	if record.versions[key] <= forget.versions[key] {
		t.Fatalf("the resubscribe must carry a newer version: %d vs %d",
			record.versions[key], forget.versions[key])
	}
	h.applyAssertionRecord(context.Background(), record)

	deletesBefore := shared.forgetCount()

	// The stale withdrawal runs.
	h.applyAssertionForget(context.Background(), forget)

	if got := shared.forgetCount(); got != deletesBefore {
		t.Fatalf("a stale withdrawal reached the directory: %d deletes, want %d", got, deletesBefore)
	}
	if !directoryTargetsFor(t, shared, "user-1", key)[key] {
		t.Fatal("a stale withdrawal removed a live assertion")
	}
	version, confirmed := h.confirmedAssertionVersion("ws-1", "user-1", key)
	if !confirmed || version != record.versions[key] {
		t.Fatalf("ledger holds (%d, %v), want the resubscribe's version %d",
			version, confirmed, record.versions[key])
	}
}

// ── every lifecycle goes through the one sequencer ───────────────────────────

// A subject who loses access is withdrawn through the shared sequencer, not by
// a delete of its own.
//
// The revocation path used to call the directory directly, which meant it could
// delete a field between another mutation's decision and its write. Here a write
// is held open while the revocation is issued: if it had its own path it would
// go first, and the write would then leave an assertion for somebody who is no
// longer allowed to be seen.
func TestSequencer_SubjectRevocationWaitsItsTurn(t *testing.T) {
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()
	tracker := newTestPresenceTracker(5*time.Minute, clk)
	auth := newRevocableAuthorizer()

	h := newPresenceTestHub(auth, tracker)
	h.instanceID = "node-a"
	h.presenceInstanceID = "runtime-a"
	h.distributed = true
	h.directory = shared.view("runtime-a")

	c := connectClient(t, h, tracker, "c-1", "user-1")
	key := subscribeInHubState(t, h, c, TargetTypeChannel, "chan-x")
	h.handleSubscribed(c, TargetTypeChannel, "chan-x", 0)

	// A write parks inside the directory, holding this user's turn.
	release, blocked := shared.blockRecord()
	writing := make(chan struct{})
	go func() {
		defer close(writing)
		drainPresenceEvents(t, h)
	}()
	<-blocked

	// Access is revoked and the withdrawal is issued while the write is in flight.
	auth.revoke("user-1", "chan-x")
	revoked := make(chan struct{})
	issued := make(chan struct{})
	go func() {
		defer close(revoked)
		close(issued) // the withdrawal is now on its way into the sequencer
		h.forgetSubject(context.Background(),
			presenceChange{workspaceID: "ws-1", userID: "user-1", status: PresenceOnline, at: clk.Now()}, key)
	}()

	// It cannot have run yet: the turn is taken.
	<-issued
	if got := shared.forgetCount(); got != 0 {
		t.Fatalf("a revocation deleted a field while a write held the sequencer (%d deletes)", got)
	}

	release()
	<-writing
	<-revoked

	// And the order the directory actually saw is the proof: the withdrawal was
	// issued while the write was parked, so a path of its own would have deleted
	// the field first. It went second because it waited.
	if ops := shared.mutationsFor(key); len(ops) != 2 || ops[0] != "record" || ops[1] != "forget" {
		t.Fatalf("the directory saw %v, want the write before the withdrawal", ops)
	}

	if directoryTargetsFor(t, shared, "user-1", key)[key] {
		t.Fatal("a revoked subject kept an assertion")
	}
	if got := h.assertedTargetsList("ws-1", "user-1"); len(got) != 0 {
		t.Fatalf("the ledger kept a revoked assertion: %v", got)
	}
}

// Shutdown withdraws through the same path as everything else.
//
// A direct Forget here would race the writes still finishing on other
// goroutines, and the last one to land would decide whether this process left an
// assertion behind for the rest of the cluster to believe in.
func TestSequencer_ShutdownWithdrawalWaitsItsTurn(t *testing.T) {
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()
	tracker := newTestPresenceTracker(5*time.Minute, clk)
	h := newClusterNode("runtime-a", shared.view("runtime-a"), tracker)

	c := connectClient(t, h, tracker, "c-1", "user-1")
	key := subscribeInHubState(t, h, c, TargetTypeChannel, "chan-x")
	h.handleSubscribed(c, TargetTypeChannel, "chan-x", 0)

	release, blocked := shared.blockRecord()
	writing := make(chan struct{})
	go func() {
		defer close(writing)
		drainPresenceEvents(t, h)
	}()
	<-blocked

	withdrawn := make(chan struct{})
	issued := make(chan struct{})
	go func() {
		defer close(withdrawn)
		close(issued) // the withdrawal is now on its way into the sequencer
		h.withdrawLocalPresence()
	}()

	<-issued
	if got := shared.forgetCount(); got != 0 {
		t.Fatalf("shutdown deleted a field while a write held the sequencer (%d deletes)", got)
	}

	release()
	<-writing
	<-withdrawn

	// Same proof as above: issued while the write was parked, executed after it.
	if ops := shared.mutationsFor(key); len(ops) != 2 || ops[0] != "record" || ops[1] != "forget" {
		t.Fatalf("the directory saw %v, want the write before the withdrawal", ops)
	}

	// It withdraws despite the connections still being registered — that is what
	// separates a shutdown from an unsubscribe — and the store is left clean.
	if directoryTargetsFor(t, shared, "user-1", key)[key] {
		t.Fatal("a graceful shutdown left an assertion behind")
	}
	if got := shared.forgetCount(); got == 0 {
		t.Fatal("shutdown withdrew nothing at all")
	}
}

// ── dead fields are removed, not merely ignored ──────────────────────────────

// A process dies. Its liveness key expires, so its assertions stop counting —
// but the field itself stays, and the target hash is kept alive indefinitely by
// everyone still in the conversation. One field per process that ever served a
// member, forever.
//
// Removing it is safe precisely because of the identity above: the field belongs
// to one execution of one process, and no restart and no second pod can ever
// come back claiming it.
func TestDirectory_DeadRuntimeFieldsAreRemovedByARead(t *testing.T) {
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()
	shared.clock = clk.Now

	tracker := newTestPresenceTracker(5*time.Minute, clk)
	survivor := newClusterNode("runtime-live", shared.view("runtime-live"), tracker)
	c := connectClient(t, survivor, tracker, "c-live", "user-live")
	key := joinTarget(t, survivor, c, "chan-x")

	// Three processes wrote here and are now gone.
	dead := []string{"runtime-dead-1", "runtime-dead-2", "runtime-dead-3"}
	for index, id := range dead {
		view := shared.view(id)
		userID := "user-dead-" + string(rune('a'+index))
		if err := view.Record(context.Background(), DirectoryEntry{
			UserID: userID, State: PresenceOnline, At: clk.Now(),
		}, []string{key}); err != nil {
			t.Fatalf("seeding a dead process's assertion: %v", err)
		}
		if err := view.Heartbeat(context.Background()); err != nil {
			t.Fatalf("heartbeat: %v", err)
		}
	}
	if got := len(shared.fieldsIn(key)); got != 1+len(dead) {
		t.Fatalf("expected %d fields before the deaths, got %d", 1+len(dead), got)
	}

	// Their liveness lapses. The survivor keeps heartbeating and keeps the key.
	clk.Advance(instanceLivenessTTL + time.Second)
	if err := shared.view("runtime-live").Heartbeat(context.Background()); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}

	entries, err := shared.view("reader").Present(context.Background(), key)
	if err != nil {
		t.Fatalf("directory read: %v", err)
	}
	roster := aggregateRoster(entries)
	if len(roster) != 1 || roster[0].UserID != "user-live" {
		t.Fatalf("a dead process's assertion was returned: %+v", roster)
	}

	// Excluded is not enough: gone.
	fields := shared.fieldsIn(key)
	if len(fields) != 1 {
		t.Fatalf("dead fields survived the read: %v", fields)
	}
	if !strings.HasSuffix(fields[0], "|runtime-live") {
		t.Fatalf("the read removed the live process's field, leaving %q", fields[0])
	}

	// And a second read has nothing left to do.
	before := len(shared.reaped)
	if _, err := shared.view("reader").Present(context.Background(), key); err != nil {
		t.Fatalf("directory read: %v", err)
	}
	if len(shared.reaped) != before {
		t.Fatal("a second read kept re-reaping already removed fields")
	}
}

// One cleanup is bounded, and repeated reads converge: a conversation that
// outlived many deployments must not turn a single snapshot into an unbounded
// pipeline.
func TestDirectory_DeadFieldCleanupIsBoundedAndConverges(t *testing.T) {
	entries := make([]DirectoryEntry, 0, presenceDeadFieldReapLimit*2)
	for index := 0; index < presenceDeadFieldReapLimit*2; index++ {
		entries = append(entries, DirectoryEntry{
			UserID: "user-x", State: PresenceOnline, InstanceID: "runtime-gone-" + string(rune('a'+index%26)) + string(rune('a'+index/26)),
		})
	}
	live := map[string]struct{}{}

	kept, dead := partitionByLiveness(entries, live, "runtime-self")
	if len(kept) != 0 {
		t.Fatalf("no instance is alive, so nothing should be kept: %d", len(kept))
	}
	if len(dead) != presenceDeadFieldReapLimit {
		t.Fatalf("one cleanup removed %d fields, want the cap of %d", len(dead), presenceDeadFieldReapLimit)
	}

	// The roster is never the thing that gets truncated — only the cleanup is.
	alive := map[string]struct{}{entries[0].InstanceID: {}}
	kept, _ = partitionByLiveness(append([]DirectoryEntry{}, entries...), alive, "runtime-self")
	if len(kept) != 1 {
		t.Fatalf("the cap leaked into the roster: kept %d", len(kept))
	}
}

// The identity a hub is built with is generated, not configured, and the option
// exists only to keep it in step with the directory's.
func TestRuntimeIdentity_IsGeneratedPerHubAndNeverEmpty(t *testing.T) {
	first := NewHub(allowAllAuthorizer{}, newTestLogger(), NopBus{}, "same-id")
	defer first.Shutdown()
	second := NewHub(allowAllAuthorizer{}, newTestLogger(), NopBus{}, "same-id")
	defer second.Shutdown()

	if first.presenceInstanceID == "" || second.presenceInstanceID == "" {
		t.Fatal("a hub started without a physical presence identity")
	}
	if first.presenceInstanceID == second.presenceInstanceID {
		t.Fatal("two hubs in one process share a presence identity")
	}
	if first.presenceInstanceID == "same-id" {
		t.Fatal("the configured instance id leaked into the physical identity")
	}

	// The option carries the value the directory was built with, and an empty one
	// never replaces a generated identity with nothing.
	third := NewHub(allowAllAuthorizer{}, newTestLogger(), NopBus{}, "same-id",
		WithPresenceInstanceID("runtime-x"))
	defer third.Shutdown()
	if third.presenceInstanceID != "runtime-x" {
		t.Fatalf("presence identity = %q, want runtime-x", third.presenceInstanceID)
	}

	fourth := NewHub(allowAllAuthorizer{}, newTestLogger(), NopBus{}, "same-id",
		WithPresenceInstanceID(""))
	defer fourth.Shutdown()
	if fourth.presenceInstanceID == "" {
		t.Fatal("an empty option erased the generated identity")
	}
}
