package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// Presence across instances (RF-58, CQ-2).
//
// The question these tests exist for: a person is already online, connected to
// one instance, and somebody joins the conversation through a different one.
// Nothing about that person is going to change, so no event is coming. Either
// the shared directory answers, or the newcomer never learns they are there.

// fakeDirectory is an in-memory PresenceDirectory shared by several hubs, which
// is what makes a two-node test possible without a Valkey. It keeps the same
// contract as the real one: per-target rosters, entries attributed to the
// instance that wrote them, and entries from an instance that stopped
// heartbeating are not returned.
type fakeDirectory struct {
	mu sync.Mutex
	// rosters is target key → assertion field (user id + instance id) → entry.
	// Keyed exactly like the real directory: one cell per (user, instance), so a
	// second node asserting about the same person cannot overwrite the first.
	rosters  map[string]map[string]DirectoryEntry
	live     map[string]bool
	failNext error
	reads    int
	// leases models the key TTL: a target whose lease is older than
	// directoryEntryTTL has expired, exactly as Valkey would drop the key.
	leases    map[string]time.Time
	refreshed []string
	clock     func() time.Time
	// Failure injection, so a test can prove the ledger never mistakes an
	// attempt for a confirmation.
	failRecord int
	failForget error
	// beforeRecord lets a test suspend a write exactly where the race lives, and
	// recordParked reports that a call has reached it. beforeForget/forgetParked
	// are the same for a withdrawal.
	beforeRecord chan struct{}
	recordParked chan struct{}
	beforeForget chan struct{}
	forgetParked chan struct{}
	// writes is every value the directory was asked to store, so a test can
	// assert on behaviour and not only on the state left behind.
	writes []directoryWrite
	// heartbeats models the real liveness key: an instance is alive while its
	// last heartbeat is within instanceLivenessTTL.
	heartbeats map[string]time.Time
	records    int
	forgets    int
	// reaped is every field a read physically removed because its writer was gone.
	reaped []string
	// mutations is every remote mutation in the order it took effect, so a test
	// can assert on the sequence rather than only on what was left behind.
	mutations []directoryMutation
}

// directoryMutation is one applied Record or Forget.
type directoryMutation struct {
	op   string // "record" or "forget"
	user string
	keys []string
}

func newFakeDirectory() *fakeDirectory {
	return &fakeDirectory{
		rosters:    make(map[string]map[string]DirectoryEntry),
		live:       make(map[string]bool),
		leases:     make(map[string]time.Time),
		clock:      time.Now,
		heartbeats: make(map[string]time.Time),
	}
}

// failNextRecord makes the next n Record calls fail.
func (d *fakeDirectory) failNextRecord(n int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.failRecord = n
}

// failForgetWith makes every Forget fail until cleared with nil.
func (d *fakeDirectory) failForgetWith(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.failForget = err
}

// blockRecord suspends the *next* Record until the returned release function is
// called, and lets every later one through.
//
// Only the next one, so a test can hold one publication open while a second runs
// to completion — which is the only way to order two generations against each
// other without a timing assumption.
// blocked is closed once a call has actually parked on the gate, so a test can
// order the two publications without guessing.
func (d *fakeDirectory) blockRecord() (release func(), blocked <-chan struct{}) {
	gate := make(chan struct{})
	parked := make(chan struct{})
	d.mu.Lock()
	d.beforeRecord = gate
	d.recordParked = parked
	d.mu.Unlock()
	return func() { close(gate) }, parked
}

func (d *fakeDirectory) forgetCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.forgets
}

// now reads the directory's clock, which a TTL test replaces with a fake one.
func (d *fakeDirectory) now() time.Time { return d.clock() }

// aliveLocked reports whether an instance still counts: killed instances never
// do, and a heartbeat older than instanceLivenessTTL has lapsed. Called with the
// lock held.
func (d *fakeDirectory) aliveLocked(instanceID string) bool {
	if !d.live[instanceID] {
		return false
	}
	beat, beaten := d.heartbeats[instanceID]
	if !beaten {
		return true // never heartbeat in this test; treated as alive
	}
	return d.now().Sub(beat) <= instanceLivenessTTL
}

// expired reports whether a target key's lease has run out. Called with the
// lock held.
func (d *fakeDirectory) expired(key string) bool {
	lease, ok := d.leases[key]
	return ok && d.now().Sub(lease) > directoryEntryTTL
}

// directoryWrite is one recorded Record call, flattened for assertions.
type directoryWrite struct {
	userID   string
	instance string
	state    string
	at       string
	keys     []string
}

// mutationsFor is the ordered list of operations that touched one target.
func (d *fakeDirectory) mutationsFor(key string) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	ops := make([]string, 0, len(d.mutations))
	for _, mutation := range d.mutations {
		for _, touched := range mutation.keys {
			if touched == key {
				ops = append(ops, mutation.op)
				break
			}
		}
	}
	return ops
}

func (d *fakeDirectory) recordedWrites() []directoryWrite {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]directoryWrite{}, d.writes...)
}

// blockForget suspends the next Forget until released, reporting when it parks.
func (d *fakeDirectory) blockForget() (release func(), blocked <-chan struct{}) {
	gate := make(chan struct{})
	parked := make(chan struct{})
	d.mu.Lock()
	d.beforeForget = gate
	d.forgetParked = parked
	d.mu.Unlock()
	return func() { close(gate) }, parked
}

func (d *fakeDirectory) refreshCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.refreshed)
}

func (d *fakeDirectory) refreshedKeys() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string{}, d.refreshed...)
}

// view returns a handle a single hub writes through, so entries carry that
// hub's instance ID exactly as the real directory does.
func (d *fakeDirectory) view(instanceID string) PresenceDirectory {
	d.mu.Lock()
	d.live[instanceID] = true
	d.mu.Unlock()
	return &fakeDirectoryView{shared: d, instanceID: instanceID}
}

func (d *fakeDirectory) killInstance(instanceID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.live[instanceID] = false
}

func (d *fakeDirectory) failReadsWith(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.failNext = err
}

func (d *fakeDirectory) readCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.reads
}

type fakeDirectoryView struct {
	shared     *fakeDirectory
	instanceID string
}

func (v *fakeDirectoryView) Record(_ context.Context, entry DirectoryEntry, keys []string) error {
	v.shared.mu.Lock()
	gate := v.shared.beforeRecord
	parked := v.shared.recordParked
	v.shared.beforeRecord = nil // this call claims the gate; later ones pass
	v.shared.recordParked = nil
	v.shared.mu.Unlock()
	if gate != nil {
		if parked != nil {
			close(parked)
		}
		<-gate // suspended exactly where the interleaving lives
	}

	v.shared.mu.Lock()
	defer v.shared.mu.Unlock()
	v.shared.records++
	v.shared.mutations = append(v.shared.mutations, directoryMutation{
		op: "record", user: entry.UserID, keys: append([]string{}, keys...),
	})
	v.shared.writes = append(v.shared.writes, directoryWrite{
		userID: entry.UserID, instance: v.instanceID,
		state: string(entry.State), at: formatPresenceTime(entry.At),
		keys: append([]string{}, keys...),
	})
	if v.shared.failRecord > 0 {
		v.shared.failRecord--
		return errors.New("valkey record failed")
	}
	for _, key := range keys {
		roster := v.shared.rosters[key]
		if roster == nil {
			roster = make(map[string]DirectoryEntry)
			v.shared.rosters[key] = roster
		}
		stored := entry
		stored.InstanceID = v.instanceID
		roster[directoryField(entry.UserID, v.instanceID)] = stored
		v.shared.leases[key] = v.shared.now()
	}
	return nil
}

func (v *fakeDirectoryView) Forget(_ context.Context, _, userID string, keys []string) error {
	v.shared.mu.Lock()
	gate := v.shared.beforeForget
	parked := v.shared.forgetParked
	v.shared.beforeForget = nil
	v.shared.forgetParked = nil
	v.shared.mu.Unlock()
	if gate != nil {
		if parked != nil {
			close(parked)
		}
		<-gate
	}

	v.shared.mu.Lock()
	defer v.shared.mu.Unlock()
	v.shared.forgets++
	if v.shared.failForget != nil {
		return v.shared.failForget
	}
	v.shared.mutations = append(v.shared.mutations, directoryMutation{
		op: "forget", user: userID, keys: append([]string{}, keys...),
	})
	for _, key := range keys {
		// Only this instance's own assertion, never another's live one.
		delete(v.shared.rosters[key], directoryField(userID, v.instanceID))
	}
	return nil
}

func (v *fakeDirectoryView) Present(_ context.Context, key string) ([]DirectoryEntry, error) {
	v.shared.mu.Lock()
	defer v.shared.mu.Unlock()
	v.shared.reads++
	if v.shared.failNext != nil {
		return nil, v.shared.failNext
	}
	if v.shared.expired(key) {
		// The key is gone, and with it every assertion it held.
		delete(v.shared.rosters, key)
		delete(v.shared.leases, key)
		return nil, nil
	}
	entries := make([]DirectoryEntry, 0, len(v.shared.rosters[key]))
	live := make(map[string]struct{}, 4)
	for _, entry := range v.shared.rosters[key] {
		entries = append(entries, entry)
		if v.shared.aliveLocked(entry.InstanceID) {
			live[entry.InstanceID] = struct{}{}
		}
	}
	// The same partition the real directory makes, from the same helper, so the
	// rule that decides a field is dead is not written twice.
	kept, dead := partitionByLiveness(entries, live, v.instanceID)
	for _, field := range dead {
		delete(v.shared.rosters[key], field)
	}
	v.shared.reaped = append(v.shared.reaped, dead...)
	return kept, nil
}

// fieldsIn reports the raw fields a target holds, dead ones included, which is
// the only way to tell "excluded from the roster" from "actually removed".
func (d *fakeDirectory) fieldsIn(key string) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	fields := make([]string, 0, len(d.rosters[key]))
	for field := range d.rosters[key] {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	return fields
}

// Refresh renews the lease exactly as the real directory does: it extends the
// target key and rewrites nothing, so a key nobody refreshes eventually expires.
func (v *fakeDirectoryView) Refresh(_ context.Context, keys []string) error {
	v.shared.mu.Lock()
	defer v.shared.mu.Unlock()
	v.shared.refreshed = append(v.shared.refreshed, keys...)
	for _, key := range keys {
		if _, exists := v.shared.rosters[key]; exists {
			v.shared.leases[key] = v.shared.now()
		}
	}
	return nil
}

// Heartbeat renews this instance's liveness, and liveness is temporal: an
// instance stays alive for instanceLivenessTTL after its last beat, exactly as
// the real key's TTL behaves.
func (v *fakeDirectoryView) Heartbeat(context.Context) error {
	v.shared.mu.Lock()
	defer v.shared.mu.Unlock()
	v.shared.heartbeats[v.instanceID] = v.shared.now()
	v.shared.live[v.instanceID] = true
	return nil
}
func (v *fakeDirectoryView) Close() {}

// newClusterNode builds a hub that behaves like one instance among several: it
// has a bus (so it knows it is not alone) and a share of the directory.
func newClusterNode(instanceID string, directory PresenceDirectory, tracker *PresenceTracker) *Hub {
	h := newPresenceTestHub(allowAllAuthorizer{}, tracker)
	h.instanceID = instanceID
	// The directory view and the hub must agree on the physical identity, exactly
	// as app startup makes them agree on one generated value.
	h.presenceInstanceID = instanceID
	h.distributed = true
	h.directory = directory
	return h
}

func snapshotFor(t *testing.T, h *Hub, c *Client, targetType TargetType, targetID string) PresenceSnapshotResponse {
	t.Helper()
	key := targetKey{workspaceID: c.workspaceID, targetType: targetType, targetID: targetID}.String()
	h.sendPresenceSnapshot(c, targetType, targetID, key)
	snapshots := takeSnapshots(t, c)
	if len(snapshots) != 1 {
		t.Fatalf("expected one snapshot, got %d", len(snapshots))
	}
	return snapshots[0]
}

// A user connected to node A, and an observer joining the same conversation on
// node B. No transition happens in between.
func TestPresenceCluster_LateSubscriberSeesUserOnAnotherNode(t *testing.T) {
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()

	trackerA := newTestPresenceTracker(5*time.Minute, clk)
	nodeA := newClusterNode("node-a", shared.view("node-a"), trackerA)

	// U is online on node A and subscribed to the channel.
	subject := newClient("c-subject", "user-subject", "ws-1", &fakeSender{})
	registerInHub(t, nodeA, subject)
	trackerA.Connect(subject.workspaceID, subject.userID, subject.id)
	subscribeInHubState(t, nodeA, subject, TargetTypeChannel, "chan-1")
	nodeA.handleSubscribed(subject, TargetTypeChannel, "chan-1", 0)
	drainPresenceEvents(t, nodeA) // the announce, which also writes the directory

	// The observer arrives on node B, which has never heard of U.
	trackerB := newTestPresenceTracker(5*time.Minute, clk)
	nodeB := newClusterNode("node-b", shared.view("node-b"), trackerB)
	observer := newClient("c-observer", "user-observer", "ws-1", &fakeSender{})
	registerInHub(t, nodeB, observer)
	trackerB.Connect(observer.workspaceID, observer.userID, observer.id)
	subscribeInHubState(t, nodeB, observer, TargetTypeChannel, "chan-1")

	if got := trackerB.Status("ws-1", "user-subject"); got != PresenceOffline {
		t.Fatalf("node B must not know U locally, got %q", got)
	}

	snapshot := snapshotFor(t, nodeB, observer, TargetTypeChannel, "chan-1")

	if !snapshot.Complete {
		t.Fatal("a snapshot answered from the shared directory is complete")
	}
	var found *PresencePayload
	for index, user := range snapshot.Users {
		if user.UserID == "user-subject" {
			found = &snapshot.Users[index]
		}
	}
	if found == nil {
		t.Fatalf("the late subscriber did not learn about a user on another node: %+v", snapshot.Users)
	}
	if found.State != string(PresenceOnline) {
		t.Fatalf("expected online, got %q", found.State)
	}
}

// Away travels the same way, and is not flattened into online.
func TestPresenceCluster_AwayIsVisibleAcrossNodes(t *testing.T) {
	const awayTimeout = 5 * time.Minute
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()

	trackerA := newTestPresenceTracker(awayTimeout, clk)
	nodeA := newClusterNode("node-a", shared.view("node-a"), trackerA)

	subject := newClient("c-subject", "user-subject", "ws-1", &fakeSender{})
	registerInHub(t, nodeA, subject)
	trackerA.Connect(subject.workspaceID, subject.userID, subject.id)
	subscribeInHubState(t, nodeA, subject, TargetTypeChannel, "chan-1")
	nodeA.handleSubscribed(subject, TargetTypeChannel, "chan-1", 0)
	drainPresenceEvents(t, nodeA)

	clk.Advance(awayTimeout + time.Second)
	trackerA.checkAway()
	drainPresenceEvents(t, nodeA)

	nodeB := newClusterNode("node-b", shared.view("node-b"), newTestPresenceTracker(awayTimeout, clk))
	observer := newClient("c-observer", "user-observer", "ws-1", &fakeSender{})
	registerInHub(t, nodeB, observer)

	snapshot := snapshotFor(t, nodeB, observer, TargetTypeChannel, "chan-1")
	if len(snapshot.Users) != 1 || snapshot.Users[0].State != string(PresenceAway) {
		t.Fatalf("expected away across nodes, got %+v", snapshot.Users)
	}
}

// Someone who left before the observer arrived is simply absent, and the
// snapshot is complete, so the client reads them as offline.
func TestPresenceCluster_OfflineBeforeSubscribeIsAbsent(t *testing.T) {
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()

	trackerA := newTestPresenceTracker(5*time.Minute, clk)
	nodeA := newClusterNode("node-a", shared.view("node-a"), trackerA)

	subject := newClient("c-subject", "user-subject", "ws-1", &fakeSender{})
	registerInHub(t, nodeA, subject)
	trackerA.Connect(subject.workspaceID, subject.userID, subject.id)
	subscribeInHubState(t, nodeA, subject, TargetTypeChannel, "chan-1")
	nodeA.handleSubscribed(subject, TargetTypeChannel, "chan-1", 0)
	drainPresenceEvents(t, nodeA)

	nodeA.dropClient(subject)
	drainPresenceEvents(t, nodeA)

	nodeB := newClusterNode("node-b", shared.view("node-b"), newTestPresenceTracker(5*time.Minute, clk))
	observer := newClient("c-observer", "user-observer", "ws-1", &fakeSender{})
	registerInHub(t, nodeB, observer)

	snapshot := snapshotFor(t, nodeB, observer, TargetTypeChannel, "chan-1")
	if !snapshot.Complete {
		t.Fatal("expected a complete snapshot")
	}
	for _, user := range snapshot.Users {
		if user.UserID == "user-subject" {
			t.Fatalf("a user who left is still in the roster: %+v", snapshot.Users)
		}
	}
}

// An instance that dies takes its assertions with it. Nothing removes them, so
// the reader must stop believing them — otherwise a killed pod leaves its users
// online forever.
func TestPresenceCluster_DeadInstanceEntriesAreNotBelieved(t *testing.T) {
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()

	trackerA := newTestPresenceTracker(5*time.Minute, clk)
	nodeA := newClusterNode("node-a", shared.view("node-a"), trackerA)

	subject := newClient("c-subject", "user-subject", "ws-1", &fakeSender{})
	registerInHub(t, nodeA, subject)
	trackerA.Connect(subject.workspaceID, subject.userID, subject.id)
	subscribeInHubState(t, nodeA, subject, TargetTypeChannel, "chan-1")
	nodeA.handleSubscribed(subject, TargetTypeChannel, "chan-1", 0)
	drainPresenceEvents(t, nodeA)

	// Node A is killed: no clean disconnect, no Forget, no heartbeat.
	shared.killInstance("node-a")

	nodeB := newClusterNode("node-b", shared.view("node-b"), newTestPresenceTracker(5*time.Minute, clk))
	observer := newClient("c-observer", "user-observer", "ws-1", &fakeSender{})
	registerInHub(t, nodeB, observer)

	snapshot := snapshotFor(t, nodeB, observer, TargetTypeChannel, "chan-1")
	for _, user := range snapshot.Users {
		if user.UserID == "user-subject" {
			t.Fatalf("a dead instance's user is still reported present: %+v", snapshot.Users)
		}
	}
}

// The directory is per target, so a conversation never answers for another.
func TestPresenceCluster_TargetIsolation(t *testing.T) {
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()

	trackerA := newTestPresenceTracker(5*time.Minute, clk)
	nodeA := newClusterNode("node-a", shared.view("node-a"), trackerA)

	subject := newClient("c-subject", "user-subject", "ws-1", &fakeSender{})
	registerInHub(t, nodeA, subject)
	trackerA.Connect(subject.workspaceID, subject.userID, subject.id)
	subscribeInHubState(t, nodeA, subject, TargetTypeChannel, "chan-private")
	nodeA.handleSubscribed(subject, TargetTypeChannel, "chan-private", 0)
	drainPresenceEvents(t, nodeA)

	nodeB := newClusterNode("node-b", shared.view("node-b"), newTestPresenceTracker(5*time.Minute, clk))
	observer := newClient("c-observer", "user-observer", "ws-1", &fakeSender{})
	registerInHub(t, nodeB, observer)

	snapshot := snapshotFor(t, nodeB, observer, TargetTypeChannel, "chan-other")
	if len(snapshot.Users) != 0 {
		t.Fatalf("an unrelated target exposed a user: %+v", snapshot.Users)
	}
}

// The same target name in two workspaces is two different rosters: the key
// carries the workspace, so one can never answer for the other.
func TestPresenceCluster_WorkspaceIsolation(t *testing.T) {
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()

	trackerA := newTestPresenceTracker(5*time.Minute, clk)
	nodeA := newClusterNode("node-a", shared.view("node-a"), trackerA)

	foreign := newClient("c-foreign", "user-foreign", "ws-2", &fakeSender{})
	registerInHub(t, nodeA, foreign)
	trackerA.Connect(foreign.workspaceID, foreign.userID, foreign.id)
	subscribeInHubState(t, nodeA, foreign, TargetTypeChannel, "chan-1")
	nodeA.handleSubscribed(foreign, TargetTypeChannel, "chan-1", 0)
	drainPresenceEvents(t, nodeA)

	nodeB := newClusterNode("node-b", shared.view("node-b"), newTestPresenceTracker(5*time.Minute, clk))
	local := newClient("c-local", "user-local", "ws-1", &fakeSender{})
	registerInHub(t, nodeB, local)

	snapshot := snapshotFor(t, nodeB, local, TargetTypeChannel, "chan-1")
	for _, user := range snapshot.Users {
		if user.UserID == "user-foreign" {
			t.Fatalf("presence crossed a workspace boundary: %+v", snapshot.Users)
		}
	}
}

// A directory that is unreachable degrades to the local answer, and says so
// rather than presenting it as the whole truth.
func TestPresenceCluster_DirectoryFailureFallsBackAndAdmitsIt(t *testing.T) {
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()
	shared.failReadsWith(errors.New("valkey unreachable"))

	tracker := newTestPresenceTracker(5*time.Minute, clk)
	node := newClusterNode("node-a", shared.view("node-a"), tracker)

	local := newClient("c-local", "user-local", "ws-1", &fakeSender{})
	registerInHub(t, node, local)
	tracker.Connect(local.workspaceID, local.userID, local.id)
	subscribeInHubState(t, node, local, TargetTypeChannel, "chan-1")

	snapshot := snapshotFor(t, node, local, TargetTypeChannel, "chan-1")

	if snapshot.Complete {
		t.Fatal("a local-only answer must not be presented as complete")
	}
	if len(snapshot.Users) != 1 || snapshot.Users[0].UserID != "user-local" {
		t.Fatalf("expected the local roster as a fallback, got %+v", snapshot.Users)
	}
	if shared.readCount() == 0 {
		t.Fatal("expected the directory to have been consulted")
	}
}

// Without a directory, an instance that shares a bus can only speak for itself.
func TestPresenceCluster_NoDirectoryMeansIncomplete(t *testing.T) {
	clk := newFakeClock(time.Now())
	tracker := newTestPresenceTracker(5*time.Minute, clk)
	node := newPresenceTestHub(allowAllAuthorizer{}, tracker)
	node.distributed = true

	c := newClient("c-1", "user-1", "ws-1", &fakeSender{})
	registerInHub(t, node, c)
	tracker.Connect(c.workspaceID, c.userID, c.id)
	subscribeInHubState(t, node, c, TargetTypeChannel, "chan-1")

	if snapshot := snapshotFor(t, node, c, TargetTypeChannel, "chan-1"); snapshot.Complete {
		t.Fatal("an instance sharing a bus is not the whole cluster")
	}
}

// A single instance with no bus is the whole cluster, and its own connections
// are the complete answer. This is the deployed configuration.
func TestPresenceCluster_SingleNodeIsComplete(t *testing.T) {
	clk := newFakeClock(time.Now())
	tracker := newTestPresenceTracker(5*time.Minute, clk)
	node := newPresenceTestHub(allowAllAuthorizer{}, tracker)

	c := newClient("c-1", "user-1", "ws-1", &fakeSender{})
	registerInHub(t, node, c)
	tracker.Connect(c.workspaceID, c.userID, c.id)
	subscribeInHubState(t, node, c, TargetTypeChannel, "chan-1")

	snapshot := snapshotFor(t, node, c, TargetTypeChannel, "chan-1")
	if !snapshot.Complete {
		t.Fatal("a single instance answers completely for itself")
	}
	if len(snapshot.Users) != 1 {
		t.Fatalf("expected the local roster, got %+v", snapshot.Users)
	}
}

// NewHub decides on its own whether it is alone, from the bus it was given.
func TestPresenceCluster_DistributedIsDerivedFromTheBus(t *testing.T) {
	local := NewHub(allowAllAuthorizer{}, newTestLogger(), NopBus{}, "solo")
	defer local.Shutdown()
	if local.distributed {
		t.Fatal("a hub with no bus is the whole cluster")
	}

	clustered := NewHub(allowAllAuthorizer{}, newTestLogger(), newValkeyBusWithAdapter(&fakePubSub{}, "clustered", newTestLogger()), "clustered")
	defer clustered.Shutdown()
	if !clustered.distributed {
		t.Fatal("a hub with a bus shares its clients with other instances")
	}
}

// ── the stored value (CQ-2 security) ─────────────────────────────────────────

// The directory holds a state, an instant and the instance that vouched for it.
// Nothing else has any business being there.
func TestPresenceDirectory_ValueCarriesOnlyPresence(t *testing.T) {
	at := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	field := directoryField("user-1", "node-a")
	value := encodeDirectoryValue(PresenceAway, at)

	for _, forbidden := range []string{"@", "token", "session", "Bearer"} {
		if strings.Contains(value, forbidden) || strings.Contains(field, forbidden) {
			t.Fatalf("stored assertion carries %q: %q %q", forbidden, field, value)
		}
	}

	entry, ok := decodeDirectoryEntry(field, value)
	if !ok {
		t.Fatalf("round trip failed for %q/%q", field, value)
	}
	if entry.State != PresenceAway || !entry.At.Equal(at) || entry.InstanceID != "node-a" {
		t.Fatalf("round trip lost information: %+v", entry)
	}
	if entry.UserID != "user-1" {
		t.Fatalf("round trip lost the subject: %+v", entry)
	}
}

func TestPresenceDirectory_RejectsMalformedAssertions(t *testing.T) {
	for _, value := range []string{"", "online", "online|notanumber", "busy|1", "offline|1"} {
		if _, ok := decodeDirectoryEntry("user-1|node-a", value); ok {
			t.Fatalf("expected value %q to be rejected", value)
		}
	}
	for _, field := range []string{"", "user-1", "|node-a", "user-1|"} {
		if _, ok := decodeDirectoryEntry(field, "online|1"); ok {
			t.Fatalf("expected field %q to be rejected", field)
		}
	}
}

// ── reconciliation helpers ───────────────────────────────────────────────────

// drainBroadcasts empties the hub's broadcast queue without inspecting it.
func drainBroadcasts(h *Hub) {
	for {
		select {
		case <-h.bcast:
		default:
			return
		}
	}
}

// reconciledRosters returns the snapshots a reconciliation sweep produced,
// keyed by the target they correct.
func reconciledRosters(t *testing.T, h *Hub) map[string]PresenceSnapshotResponse {
	t.Helper()
	rosters := make(map[string]PresenceSnapshotResponse, 4)
	for {
		select {
		case req := <-h.bcast:
			var snapshot PresenceSnapshotResponse
			if err := json.Unmarshal(req.data, &snapshot); err != nil {
				continue
			}
			if snapshot.Type != PresenceSnapshotType {
				continue
			}
			rosters[snapshot.TargetID] = snapshot
		default:
			return rosters
		}
	}
}

// ── aggregation across instances (CQ-1) ──────────────────────────────────────

// Reachability, not recency. A person with an active connection anywhere is
// online, whatever a different replica last observed about a different tab.
func TestPresenceAggregate_SemanticPriorityBeatsRecency(t *testing.T) {
	early := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	late := early.Add(time.Minute)

	for name, tc := range map[string]struct {
		entries []DirectoryEntry
		want    PresenceStatus
		wantAt  time.Time
	}{
		"online and away": {
			entries: []DirectoryEntry{
				{UserID: "u", State: PresenceOnline, At: early, InstanceID: "a"},
				{UserID: "u", State: PresenceAway, At: late, InstanceID: "b"},
			},
			want: PresenceOnline, wantAt: early,
		},
		"away and online": {
			entries: []DirectoryEntry{
				{UserID: "u", State: PresenceAway, At: late, InstanceID: "a"},
				{UserID: "u", State: PresenceOnline, At: early, InstanceID: "b"},
			},
			want: PresenceOnline, wantAt: early,
		},
		"away everywhere": {
			entries: []DirectoryEntry{
				{UserID: "u", State: PresenceAway, At: early, InstanceID: "a"},
				{UserID: "u", State: PresenceAway, At: late, InstanceID: "b"},
			},
			want: PresenceAway, wantAt: late,
		},
		"one instance": {
			entries: []DirectoryEntry{{UserID: "u", State: PresenceOnline, At: late, InstanceID: "a"}},
			want:    PresenceOnline, wantAt: late,
		},
	} {
		t.Run(name, func(t *testing.T) {
			state, at, ok := aggregatePresence(tc.entries)
			if !ok || state != tc.want {
				t.Fatalf("expected %q, got %q (ok=%v)", tc.want, state, ok)
			}
			if !at.Equal(tc.wantAt) {
				t.Fatalf("expected the winning assertion's instant %v, got %v", tc.wantAt, at)
			}
		})
	}

	if _, _, ok := aggregatePresence(nil); ok {
		t.Fatal("no assertions means nothing is asserted")
	}
}

// The literal cluster scenario: the same person on two replicas, closed one at
// a time. The first close must not erase the second replica's live connection.
func TestPresenceCluster_SameUserOnTwoNodes(t *testing.T) {
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()

	trackerA := newTestPresenceTracker(5*time.Minute, clk)
	nodeA := newClusterNode("node-a", shared.view("node-a"), trackerA)
	trackerC := newTestPresenceTracker(5*time.Minute, clk)
	nodeC := newClusterNode("node-c", shared.view("node-c"), trackerC)

	key := targetKey{workspaceID: "ws-1", targetType: TargetTypeChannel, targetID: "chan-1"}.String()
	rosterNow := func() []PresencePayload {
		entries, err := shared.view("reader").Present(context.Background(), key)
		if err != nil {
			t.Fatalf("directory read: %v", err)
		}
		return aggregateRoster(entries)
	}

	ua := newClient("c-ua", "user-1", "ws-1", &fakeSender{})
	registerInHub(t, nodeA, ua)
	trackerA.Connect(ua.workspaceID, ua.userID, ua.id)
	subscribeInHubState(t, nodeA, ua, TargetTypeChannel, "chan-1")
	nodeA.handleSubscribed(ua, TargetTypeChannel, "chan-1", 0)
	drainPresenceEvents(t, nodeA)

	uc := newClient("c-uc", "user-1", "ws-1", &fakeSender{})
	registerInHub(t, nodeC, uc)
	trackerC.Connect(uc.workspaceID, uc.userID, uc.id)
	subscribeInHubState(t, nodeC, uc, TargetTypeChannel, "chan-1")
	nodeC.handleSubscribed(uc, TargetTypeChannel, "chan-1", 0)
	drainPresenceEvents(t, nodeC)

	if roster := rosterNow(); len(roster) != 1 || roster[0].State != string(PresenceOnline) {
		t.Fatalf("expected one aggregated online user, got %+v", roster)
	}

	// Node A's connection closes. Node C's is untouched.
	nodeA.dropClient(ua)
	drainPresenceEvents(t, nodeA)
	if roster := rosterNow(); len(roster) != 1 || roster[0].State != string(PresenceOnline) {
		t.Fatalf("one node's disconnect erased another node's live user: %+v", roster)
	}

	// Node C's connection closes. Now nobody asserts them.
	nodeC.dropClient(uc)
	drainPresenceEvents(t, nodeC)
	if roster := rosterNow(); len(roster) != 0 {
		t.Fatalf("expected the user to be gone, got %+v", roster)
	}
}

// Away on one node and online on another aggregates to online, end to end.
func TestPresenceCluster_AwayOnOneNodeOnlineOnAnother(t *testing.T) {
	const awayTimeout = 5 * time.Minute
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()

	trackerA := newTestPresenceTracker(awayTimeout, clk)
	nodeA := newClusterNode("node-a", shared.view("node-a"), trackerA)
	trackerB := newTestPresenceTracker(awayTimeout, clk)
	nodeB := newClusterNode("node-b", shared.view("node-b"), trackerB)

	for _, node := range []struct {
		hub     *Hub
		tracker *PresenceTracker
		id      string
	}{{nodeA, trackerA, "c-a"}, {nodeB, trackerB, "c-b"}} {
		c := newClient(node.id, "user-1", "ws-1", &fakeSender{})
		registerInHub(t, node.hub, c)
		node.tracker.Connect(c.workspaceID, c.userID, c.id)
		subscribeInHubState(t, node.hub, c, TargetTypeChannel, "chan-1")
		node.hub.handleSubscribed(c, TargetTypeChannel, "chan-1", 0)
		drainPresenceEvents(t, node.hub)
	}

	// Node A's connection goes idle.
	clk.Advance(awayTimeout + time.Second)
	trackerA.checkAway()
	drainPresenceEvents(t, nodeA)

	key := targetKey{workspaceID: "ws-1", targetType: TargetTypeChannel, targetID: "chan-1"}.String()
	entries, err := shared.view("reader").Present(context.Background(), key)
	if err != nil {
		t.Fatalf("directory read: %v", err)
	}
	roster := aggregateRoster(entries)
	if len(roster) != 1 || roster[0].State != string(PresenceOnline) {
		t.Fatalf("an idle tab must not hide an active one: %+v", roster)
	}

	// The other node goes idle too. Only now is the person away.
	clk.Advance(awayTimeout + time.Second)
	trackerB.checkAway()
	drainPresenceEvents(t, nodeB)

	entries, _ = shared.view("reader").Present(context.Background(), key)
	if roster := aggregateRoster(entries); len(roster) != 1 || roster[0].State != string(PresenceAway) {
		t.Fatalf("expected away once every node is idle, got %+v", roster)
	}
}

// A dead node while another is away: away, never offline.
func TestPresenceCluster_DeadNodeWhileAnotherIsAway(t *testing.T) {
	const awayTimeout = 5 * time.Minute
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()

	trackerA := newTestPresenceTracker(awayTimeout, clk)
	nodeA := newClusterNode("node-a", shared.view("node-a"), trackerA)
	trackerB := newTestPresenceTracker(awayTimeout, clk)
	nodeB := newClusterNode("node-b", shared.view("node-b"), trackerB)

	for _, node := range []struct {
		hub     *Hub
		tracker *PresenceTracker
		id      string
	}{{nodeA, trackerA, "c-a"}, {nodeB, trackerB, "c-b"}} {
		c := newClient(node.id, "user-1", "ws-1", &fakeSender{})
		registerInHub(t, node.hub, c)
		node.tracker.Connect(c.workspaceID, c.userID, c.id)
		subscribeInHubState(t, node.hub, c, TargetTypeChannel, "chan-1")
		node.hub.handleSubscribed(c, TargetTypeChannel, "chan-1", 0)
		drainPresenceEvents(t, node.hub)
	}

	clk.Advance(awayTimeout + time.Second)
	trackerB.checkAway()
	drainPresenceEvents(t, nodeB)

	// Node A is killed while node B holds an idle connection.
	shared.killInstance("node-a")

	key := targetKey{workspaceID: "ws-1", targetType: TargetTypeChannel, targetID: "chan-1"}.String()
	entries, err := shared.view("node-b").Present(context.Background(), key)
	if err != nil {
		t.Fatalf("directory read: %v", err)
	}
	if roster := aggregateRoster(entries); len(roster) != 1 || roster[0].State != string(PresenceAway) {
		t.Fatalf("expected away, not offline, got %+v", roster)
	}

	// And when B goes too, nobody is left asserting anything.
	shared.killInstance("node-b")
	entries, _ = shared.view("reader").Present(context.Background(), key)
	if roster := aggregateRoster(entries); len(roster) != 0 {
		t.Fatalf("expected nobody present, got %+v", roster)
	}
}

// ── convergence for observers already connected (CQ-2) ───────────────────────

// The crash case: node A stops renewing its liveness without running any
// cleanup, and an observer already connected to node B — who never reconnects —
// must still stop seeing the user.
func TestPresenceCluster_AbruptNodeLossConvergesForConnectedObserver(t *testing.T) {
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()

	trackerA := newTestPresenceTracker(5*time.Minute, clk)
	nodeA := newClusterNode("node-a", shared.view("node-a"), trackerA)

	subject := newClient("c-subject", "user-subject", "ws-1", &fakeSender{})
	registerInHub(t, nodeA, subject)
	trackerA.Connect(subject.workspaceID, subject.userID, subject.id)
	subscribeInHubState(t, nodeA, subject, TargetTypeChannel, "chan-1")
	nodeA.handleSubscribed(subject, TargetTypeChannel, "chan-1", 0)
	drainPresenceEvents(t, nodeA)

	trackerB := newTestPresenceTracker(5*time.Minute, clk)
	nodeB := newClusterNode("node-b", shared.view("node-b"), trackerB)
	observer := newClient("c-observer", "user-observer", "ws-1", &fakeSender{})
	registerInHub(t, nodeB, observer)
	trackerB.Connect(observer.workspaceID, observer.userID, observer.id)
	subscribeInHubState(t, nodeB, observer, TargetTypeChannel, "chan-1")

	// The observer's first sweep establishes what it has been told.
	nodeB.reconcilePresence()
	first := reconciledRosters(t, nodeB)
	if roster, ok := first["chan-1"]; !ok || len(roster.Users) != 1 {
		t.Fatalf("expected the observer to be told about the user, got %+v", first)
	}

	// A sweep that finds nothing new says nothing.
	nodeB.reconcilePresence()
	if quiet := reconciledRosters(t, nodeB); len(quiet) != 0 {
		t.Fatalf("an unchanged roster must produce no frame, got %+v", quiet)
	}

	// Node A dies. No cleanup runs, no event is published, the observer's socket
	// is never reconnected.
	shared.killInstance("node-a")

	nodeB.reconcilePresence()
	corrected := reconciledRosters(t, nodeB)
	roster, ok := corrected["chan-1"]
	if !ok {
		t.Fatal("the connected observer was never corrected after the node died")
	}
	if len(roster.Users) != 0 {
		t.Fatalf("expected the dead node's user to be gone, got %+v", roster.Users)
	}
	if !roster.Complete {
		t.Fatal("a directory-backed correction is complete")
	}
}

// The graceful case: a shutting-down instance withdraws its own assertions
// before it stops, so the other instances converge without waiting out the TTL.
func TestPresenceCluster_GracefulShutdownWithdrawsAssertions(t *testing.T) {
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()

	trackerA := newTestPresenceTracker(5*time.Minute, clk)
	nodeA := newClusterNode("node-a", shared.view("node-a"), trackerA)

	subject := newClient("c-subject", "user-subject", "ws-1", &fakeSender{})
	registerInHub(t, nodeA, subject)
	trackerA.Connect(subject.workspaceID, subject.userID, subject.id)
	subscribeInHubState(t, nodeA, subject, TargetTypeChannel, "chan-1")
	nodeA.handleSubscribed(subject, TargetTypeChannel, "chan-1", 0)
	drainPresenceEvents(t, nodeA)

	trackerB := newTestPresenceTracker(5*time.Minute, clk)
	nodeB := newClusterNode("node-b", shared.view("node-b"), trackerB)
	observer := newClient("c-observer", "user-observer", "ws-1", &fakeSender{})
	registerInHub(t, nodeB, observer)
	subscribeInHubState(t, nodeB, observer, TargetTypeChannel, "chan-1")
	nodeB.reconcilePresence()
	drainBroadcasts(nodeB)

	// Node A shuts down cleanly. Its liveness is still valid — nothing has
	// expired — so only the withdrawal can explain the correction.
	nodeA.withdrawLocalPresence()

	nodeB.reconcilePresence()
	corrected := reconciledRosters(t, nodeB)
	roster, ok := corrected["chan-1"]
	if !ok {
		t.Fatal("the observer was not corrected after a graceful shutdown")
	}
	if len(roster.Users) != 0 {
		t.Fatalf("expected the departed instance's user to be gone, got %+v", roster.Users)
	}
}

// A shutdown must not withdraw a user another instance still holds.
func TestPresenceCluster_GracefulShutdownKeepsOtherNodesUsers(t *testing.T) {
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()

	trackerA := newTestPresenceTracker(5*time.Minute, clk)
	nodeA := newClusterNode("node-a", shared.view("node-a"), trackerA)
	trackerB := newTestPresenceTracker(5*time.Minute, clk)
	nodeB := newClusterNode("node-b", shared.view("node-b"), trackerB)

	for _, node := range []struct {
		hub     *Hub
		tracker *PresenceTracker
		id      string
	}{{nodeA, trackerA, "c-a"}, {nodeB, trackerB, "c-b"}} {
		c := newClient(node.id, "user-1", "ws-1", &fakeSender{})
		registerInHub(t, node.hub, c)
		node.tracker.Connect(c.workspaceID, c.userID, c.id)
		subscribeInHubState(t, node.hub, c, TargetTypeChannel, "chan-1")
		node.hub.handleSubscribed(c, TargetTypeChannel, "chan-1", 0)
		drainPresenceEvents(t, node.hub)
	}

	nodeA.withdrawLocalPresence()

	key := targetKey{workspaceID: "ws-1", targetType: TargetTypeChannel, targetID: "chan-1"}.String()
	entries, err := shared.view("reader").Present(context.Background(), key)
	if err != nil {
		t.Fatalf("directory read: %v", err)
	}
	if roster := aggregateRoster(entries); len(roster) != 1 || roster[0].State != string(PresenceOnline) {
		t.Fatalf("a graceful shutdown removed a user another node still holds: %+v", roster)
	}
}

// Reconciliation only touches targets somebody here is actually watching.
func TestPresenceCluster_ReconciliationOnlyTouchesObservedTargets(t *testing.T) {
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()
	tracker := newTestPresenceTracker(5*time.Minute, clk)
	node := newClusterNode("node-a", shared.view("node-a"), tracker)

	c := newClient("c-1", "user-1", "ws-1", &fakeSender{})
	registerInHub(t, node, c)
	tracker.Connect(c.workspaceID, c.userID, c.id)
	key := subscribeInHubState(t, node, c, TargetTypeChannel, "chan-1")
	node.handleSubscribed(c, TargetTypeChannel, "chan-1", 0)
	drainPresenceEvents(t, node)

	before := shared.readCount()
	node.reconcilePresence()
	if got := shared.readCount() - before; got != 1 {
		t.Fatalf("expected one read for the one observed target, got %d", got)
	}

	// Nobody watching it any more: it stops being swept, and the memory of what
	// it was told goes with it.
	if !node.revokeSubscription(c, key) {
		t.Fatal("unsubscribe failed")
	}
	before = shared.readCount()
	node.reconcilePresence()
	if got := shared.readCount() - before; got != 0 {
		t.Fatalf("an unobserved target was still swept %d time(s)", got)
	}
	node.reconcileMu.Lock()
	remembered := len(node.deliveredRosters)
	node.reconcileMu.Unlock()
	if remembered != 0 {
		t.Fatalf("reconciliation state outlived the subscription: %d entries", remembered)
	}
}

// ── presence after access is revoked (SR-1) ──────────────────────────────────

// revocableAuthorizer allows everything until a target is closed to a user.
type revocableAuthorizer struct {
	mu      sync.Mutex
	revoked map[string]bool // "userID|targetID"
}

func newRevocableAuthorizer() *revocableAuthorizer {
	return &revocableAuthorizer{revoked: make(map[string]bool)}
}

func (a *revocableAuthorizer) revoke(userID, targetID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.revoked[userID+"|"+targetID] = true
}

func (a *revocableAuthorizer) CanAccess(_ context.Context, userID, _ string, _ TargetType, targetID string) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return !a.revoked[userID+"|"+targetID], nil
}

// B is removed from a private channel while still connected. Nothing B does
// afterwards may reach anybody still in that channel, and no later reader may
// find B in its roster.
func TestPresenceSecurity_RemovedSubjectStopsBeingPublished(t *testing.T) {
	const awayTimeout = 5 * time.Minute
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()
	tracker := newTestPresenceTracker(awayTimeout, clk)

	auth := newRevocableAuthorizer()
	h := newPresenceTestHub(auth, tracker)
	h.instanceID = "node-a"
	h.presenceInstanceID = "node-a"
	h.distributed = true
	h.directory = shared.view("node-a")

	subject := newClient("c-subject", "user-b", "ws-1", &fakeSender{})
	registerInHub(t, h, subject)
	tracker.Connect(subject.workspaceID, subject.userID, subject.id)
	key := subscribeInHubState(t, h, subject, TargetTypeChannel, "chan-private")
	h.handleSubscribed(subject, TargetTypeChannel, "chan-private", 0)

	watcher := newClient("c-watcher", "user-a", "ws-1", &fakeSender{})
	registerInHub(t, h, watcher)
	subscribeInHubState(t, h, watcher, TargetTypeChannel, "chan-private")
	drainPresenceEvents(t, h)

	// Everything is normal so far: B is in the roster.
	entries, err := shared.view("reader").Present(context.Background(), key)
	if err != nil {
		t.Fatalf("directory read: %v", err)
	}
	if roster := aggregateRoster(entries); len(roster) != 1 || roster[0].UserID != "user-b" {
		t.Fatalf("expected B present before the removal, got %+v", roster)
	}

	// B is removed from the channel. B's WebSocket stays open and B keeps using
	// the app, which is exactly the case the old model leaked through: the
	// subscription outlived the membership.
	auth.revoke("user-b", "chan-private")

	clk.Advance(awayTimeout + time.Second)
	tracker.checkAway()
	events := drainPresenceEvents(t, h)

	for _, evt := range events {
		if evt.Presence == nil || evt.Presence.UserID != "user-b" {
			continue
		}
		if evt.TargetID == "chan-private" && evt.Presence.State != string(PresenceOffline) {
			t.Fatalf("a removed member's presence was published into the channel: %+v", evt.Presence)
		}
	}

	// The shared roster no longer carries B either, so a later subscriber cannot
	// find them there.
	entries, err = shared.view("reader").Present(context.Background(), key)
	if err != nil {
		t.Fatalf("directory read: %v", err)
	}
	if roster := aggregateRoster(entries); len(roster) != 0 {
		t.Fatalf("the directory still shows a removed member: %+v", roster)
	}

	// And a fresh snapshot for that channel does not name them.
	snapshot := snapshotFor(t, h, watcher, TargetTypeChannel, "chan-private")
	for _, user := range snapshot.Users {
		if user.UserID == "user-b" {
			t.Fatalf("a new snapshot named a removed member: %+v", snapshot.Users)
		}
	}
}

// The same rule for a conversation the subject was removed from.
func TestPresenceSecurity_RemovedSubjectInDM(t *testing.T) {
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()
	tracker := newTestPresenceTracker(5*time.Minute, clk)

	auth := newRevocableAuthorizer()
	h := newPresenceTestHub(auth, tracker)
	h.instanceID = "node-a"
	h.presenceInstanceID = "node-a"
	h.distributed = true
	h.directory = shared.view("node-a")

	subject := newClient("c-subject", "user-b", "ws-1", &fakeSender{})
	registerInHub(t, h, subject)
	tracker.Connect(subject.workspaceID, subject.userID, subject.id)
	key := subscribeInHubState(t, h, subject, TargetTypeDM, "group-1")
	h.handleSubscribed(subject, TargetTypeDM, "group-1", 0)
	drainPresenceEvents(t, h)

	auth.revoke("user-b", "group-1")

	// Any later transition is withheld and withdraws the assertion.
	h.enqueuePresenceChange(presenceChange{
		workspaceID: "ws-1", userID: "user-b",
		status: PresenceOnline, at: clk.Now().Add(time.Minute), derive: true,
	})
	events := drainPresenceEvents(t, h)
	for _, evt := range events {
		if evt.Presence != nil && evt.Presence.UserID == "user-b" &&
			evt.Presence.State != string(PresenceOffline) {
			t.Fatalf("presence reached a conversation the subject left: %+v", evt)
		}
	}

	entries, err := shared.view("reader").Present(context.Background(), key)
	if err != nil {
		t.Fatalf("directory read: %v", err)
	}
	if roster := aggregateRoster(entries); len(roster) != 0 {
		t.Fatalf("the directory kept a removed participant: %+v", roster)
	}
}

// ── no unbounded per-user history (SR-2) ─────────────────────────────────────

// Visiting many conversations and leaving them must not accumulate anything.
// The property, not a magic number: what the hub holds tracks *active* state,
// and a later transition does not walk everywhere the user has ever been.
func TestPresenceSecurity_NoHistoricalTargetAccumulation(t *testing.T) {
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()
	tracker := newTestPresenceTracker(5*time.Minute, clk)

	auth := newRevocableAuthorizer()
	h := newPresenceTestHub(auth, tracker)
	h.instanceID = "node-a"
	h.presenceInstanceID = "node-a"
	h.directory = shared.view("node-a")

	c := newClient("c-1", "user-1", "ws-1", &fakeSender{})
	registerInHub(t, h, c)
	tracker.Connect(c.workspaceID, c.userID, c.id)

	// Fifty conversations, each opened and then closed.
	for i := range 50 {
		targetID := fmt.Sprintf("chan-%02d", i)
		key := subscribeInHubState(t, h, c, TargetTypeChannel, targetID)
		h.handleSubscribed(c, TargetTypeChannel, targetID, 0)
		drainPresenceEvents(t, h)
		if !h.revokeSubscription(c, key) {
			t.Fatalf("unsubscribe from %s failed", targetID)
		}
	}

	// Nothing accumulated in the hub: the subject's audience is derived from
	// live subscriptions, and there are none left.
	if got := h.subscribedTargetKeys("ws-1", "user-1"); len(got) != 0 {
		t.Fatalf("closed conversations are still counted as subscriptions: %d", len(got))
	}

	// The ownership ledger holds only what is genuinely still asserted, and a
	// later transition addresses that and nothing more.
	h.enqueuePresenceChange(presenceChange{
		workspaceID: "ws-1", userID: "user-1",
		status: PresenceAway, at: clk.Now().Add(time.Minute), derive: true,
	})
	events := drainPresenceEvents(t, h)
	if len(events) != 0 {
		t.Fatalf("a transition walked %d conversation(s) the user had left", len(events))
	}

	// Disconnecting clears the ledger completely.
	h.dropClient(c)
	drainPresenceEvents(t, h)
	h.assertedMu.Lock()
	remaining := len(h.asserted)
	h.assertedMu.Unlock()
	if remaining != 0 {
		t.Fatalf("the assertion ledger outlived the user: %d entries", remaining)
	}
}
