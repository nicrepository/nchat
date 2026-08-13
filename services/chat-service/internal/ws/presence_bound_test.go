package ws

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// Bounding the cost of authorizing a roster (RF-58, SR-444-02).
//
// Every subject in a roster has to be re-checked before anybody sees it, and a
// conversation can hold far more present users than a snapshot will ever carry.
// Checking first and cutting afterwards spends reads on rows that are discarded
// a moment later — on every snapshot and every reconciliation sweep.

// countingAuthorizer answers for a list in one call and counts both how often it
// was asked and how many subjects each question carried.
type countingAuthorizer struct {
	mu sync.Mutex
	// denied is keyed "userID|targetID".
	denied     map[string]bool
	batchCalls int
	batchSizes []int
	singles    int
	batchErr   error
	noBatch    bool
}

func newCountingAuthorizer() *countingAuthorizer {
	return &countingAuthorizer{denied: make(map[string]bool)}
}

func (a *countingAuthorizer) deny(userID, targetID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.denied[userID+"|"+targetID] = true
}

func (a *countingAuthorizer) failBatch(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.batchErr = err
}

func (a *countingAuthorizer) withoutBatch() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.noBatch = true
}

func (a *countingAuthorizer) stats() (batchCalls int, sizes []int, singles int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.batchCalls, append([]int{}, a.batchSizes...), a.singles
}

func (a *countingAuthorizer) CanAccess(_ context.Context, userID, _ string, _ TargetType, targetID string) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.singles++
	return !a.denied[userID+"|"+targetID], nil
}

func (a *countingAuthorizer) AuthorizeSubjects(
	_ context.Context, _ string, _ TargetType, targetID string, userIDs []string,
) ([]string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.noBatch {
		return nil, ErrSubjectBatchUnsupported
	}
	a.batchCalls++
	a.batchSizes = append(a.batchSizes, len(userIDs))
	if a.batchErr != nil {
		return nil, a.batchErr
	}
	allowed := make([]string, 0, len(userIDs))
	for _, userID := range userIDs {
		if !a.denied[userID+"|"+targetID] {
			allowed = append(allowed, userID)
		}
	}
	return allowed, nil
}

// crowdedTarget builds a hub whose directory holds `population` present users in
// one channel, without opening that many connections.
func crowdedTarget(t *testing.T, auth SubscriptionAuthorizer, population int) (*Hub, *Client, string) {
	t.Helper()
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()
	tracker := newTestPresenceTracker(5*time.Minute, clk)

	h := newPresenceTestHub(auth, tracker)
	h.instanceID = "node-a"
	h.presenceInstanceID = "node-a"
	h.distributed = true
	directory := shared.view("node-a")
	h.directory = directory

	key := targetKey{workspaceID: "ws-1", targetType: TargetTypeChannel, targetID: "chan-big"}.String()
	for i := range population {
		err := directory.Record(context.Background(), DirectoryEntry{
			UserID: fmt.Sprintf("user-%05d", i), State: PresenceOnline, At: clk.Now(),
		}, []string{key})
		if err != nil {
			t.Fatalf("seed directory: %v", err)
		}
	}

	observer := newClient("c-observer", "user-observer", "ws-1", &fakeSender{})
	registerInHub(t, h, observer)
	tracker.Connect(observer.workspaceID, observer.userID, observer.id)
	subscribeInHubState(t, h, observer, TargetTypeChannel, "chan-big")
	return h, observer, key
}

// A — more candidates than the payload can carry.
func TestRosterBound_AuthorizationIsCappedBeforeItRuns(t *testing.T) {
	auth := newCountingAuthorizer()
	h, observer, _ := crowdedTarget(t, auth, presenceSnapshotMaxUsers+200)

	snapshot := snapshotFor(t, h, observer, TargetTypeChannel, "chan-big")

	batchCalls, sizes, singles := auth.stats()
	if batchCalls != 1 {
		t.Fatalf("expected exactly one batch authorization, got %d", batchCalls)
	}
	if sizes[0] > presenceSnapshotMaxUsers {
		t.Fatalf("authorized %d subjects for a payload capped at %d", sizes[0], presenceSnapshotMaxUsers)
	}
	if singles != 0 {
		t.Fatalf("expected no per-subject authorization, got %d", singles)
	}
	if len(snapshot.Users) > presenceSnapshotMaxUsers {
		t.Fatalf("payload of %d exceeds the bound", len(snapshot.Users))
	}
	if snapshot.Complete {
		t.Fatal("a truncated roster must not claim to be complete")
	}
}

// The same bound when there is no batch capability: at most the cap, never the
// whole population.
func TestRosterBound_FallbackIsAlsoCapped(t *testing.T) {
	auth := newCountingAuthorizer()
	auth.withoutBatch()
	h, observer, _ := crowdedTarget(t, auth, presenceSnapshotMaxUsers+200)

	snapshot := snapshotFor(t, h, observer, TargetTypeChannel, "chan-big")

	_, _, singles := auth.stats()
	if singles > presenceSnapshotMaxUsers {
		t.Fatalf("asked %d times for a payload capped at %d", singles, presenceSnapshotMaxUsers)
	}
	if snapshot.Complete {
		t.Fatal("a truncated roster must not claim to be complete")
	}
}

// B — inside the bound: everything is evaluated, denials are removed, and the
// answer is still complete.
func TestRosterBound_WithinTheBoundNothingChanges(t *testing.T) {
	auth := newCountingAuthorizer()
	auth.deny("user-00007", "chan-big")
	h, observer, _ := crowdedTarget(t, auth, 100)

	snapshot := snapshotFor(t, h, observer, TargetTypeChannel, "chan-big")

	if len(snapshot.Users) != 99 {
		t.Fatalf("expected the one denied subject removed, got %d users", len(snapshot.Users))
	}
	for _, user := range snapshot.Users {
		if user.UserID == "user-00007" {
			t.Fatal("a denied subject reached the roster")
		}
	}
	if !snapshot.Complete {
		t.Fatal("a roster inside the bound with every subject answered is complete")
	}
	batchCalls, sizes, _ := auth.stats()
	if batchCalls != 1 || sizes[0] != 100 {
		t.Fatalf("expected one batch of 100, got %d call(s) %v", batchCalls, sizes)
	}
}

// C — the authorization read fails: nobody is admitted and the answer says so.
func TestRosterBound_AuthorizationFailureAdmitsNobody(t *testing.T) {
	auth := newCountingAuthorizer()
	auth.failBatch(errors.New("database unavailable"))
	h, observer, _ := crowdedTarget(t, auth, 10)

	snapshot := snapshotFor(t, h, observer, TargetTypeChannel, "chan-big")

	if len(snapshot.Users) != 0 {
		t.Fatalf("a failed authorization admitted %d subject(s)", len(snapshot.Users))
	}
	if snapshot.Complete {
		t.Fatal("a roster built from an unanswered question must not be complete")
	}
	if _, _, singles := auth.stats(); singles != 0 {
		t.Fatalf("a batch failure must not fall back to per-subject reads, got %d", singles)
	}
}

// D — reconciliation uses the same bounded path.
func TestRosterBound_ReconciliationIsBoundedToo(t *testing.T) {
	auth := newCountingAuthorizer()
	h, _, _ := crowdedTarget(t, auth, presenceSnapshotMaxUsers+200)

	h.reconcilePresence()

	batchCalls, sizes, singles := auth.stats()
	if batchCalls != 1 {
		t.Fatalf("expected one batch authorization per swept target, got %d", batchCalls)
	}
	if sizes[0] > presenceSnapshotMaxUsers {
		t.Fatalf("reconciliation authorized %d subjects", sizes[0])
	}
	if singles != 0 {
		t.Fatalf("reconciliation fell back to per-subject reads: %d", singles)
	}

	rosters := reconciledRosters(t, h)
	roster, ok := rosters["chan-big"]
	if !ok {
		t.Fatal("expected the swept target to be corrected")
	}
	if len(roster.Users) > presenceSnapshotMaxUsers || roster.Complete {
		t.Fatalf("reconciled roster is unbounded or wrongly complete: %d users, complete=%v",
			len(roster.Users), roster.Complete)
	}
}

// E — one person connected through several instances takes one place in the
// limit, not one per instance.
func TestRosterBound_MultiInstanceSubjectCountsOnce(t *testing.T) {
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()
	auth := newCountingAuthorizer()
	tracker := newTestPresenceTracker(5*time.Minute, clk)

	h := newPresenceTestHub(auth, tracker)
	h.instanceID = "node-a"
	h.presenceInstanceID = "node-a"
	h.distributed = true
	h.directory = shared.view("node-a")

	key := targetKey{workspaceID: "ws-1", targetType: TargetTypeChannel, targetID: "chan-1"}.String()
	for _, instance := range []string{"node-a", "node-b", "node-c"} {
		err := shared.view(instance).Record(context.Background(), DirectoryEntry{
			UserID: "user-many", State: PresenceOnline, At: clk.Now(),
		}, []string{key})
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	observer := newClient("c-observer", "user-observer", "ws-1", &fakeSender{})
	registerInHub(t, h, observer)
	tracker.Connect(observer.workspaceID, observer.userID, observer.id)
	subscribeInHubState(t, h, observer, TargetTypeChannel, "chan-1")

	snapshot := snapshotFor(t, h, observer, TargetTypeChannel, "chan-1")

	if len(snapshot.Users) != 1 {
		t.Fatalf("expected one aggregated subject, got %+v", snapshot.Users)
	}
	_, sizes, _ := auth.stats()
	if len(sizes) != 1 || sizes[0] != 1 {
		t.Fatalf("expected one subject submitted for authorization, got %v", sizes)
	}
}

// G — a candidate from another workspace cannot reach this workspace's roster.
// The directory is keyed by workspace, so the two never share a target key.
func TestRosterBound_CrossWorkspaceCandidateNeverAppears(t *testing.T) {
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()
	auth := newCountingAuthorizer()
	tracker := newTestPresenceTracker(5*time.Minute, clk)

	h := newPresenceTestHub(auth, tracker)
	h.instanceID = "node-a"
	h.presenceInstanceID = "node-a"
	h.distributed = true
	h.directory = shared.view("node-a")

	foreignKey := targetKey{workspaceID: "ws-2", targetType: TargetTypeChannel, targetID: "chan-1"}.String()
	if err := shared.view("node-a").Record(context.Background(), DirectoryEntry{
		UserID: "user-foreign", State: PresenceOnline, At: clk.Now(),
	}, []string{foreignKey}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	observer := newClient("c-observer", "user-observer", "ws-1", &fakeSender{})
	registerInHub(t, h, observer)
	tracker.Connect(observer.workspaceID, observer.userID, observer.id)
	subscribeInHubState(t, h, observer, TargetTypeChannel, "chan-1")

	snapshot := snapshotFor(t, h, observer, TargetTypeChannel, "chan-1")
	for _, user := range snapshot.Users {
		if user.UserID == "user-foreign" {
			t.Fatalf("a candidate from another workspace appeared: %+v", snapshot.Users)
		}
	}
}

// The batch authorizer never asks about anybody the caller did not offer, and
// the answer is a subset of what it was given.
func TestRosterBound_BatchInputComesOnlyFromTheDirectory(t *testing.T) {
	auth := newCountingAuthorizer()
	h, observer, _ := crowdedTarget(t, auth, 5)

	snapshot := snapshotFor(t, h, observer, TargetTypeChannel, "chan-big")

	for _, user := range snapshot.Users {
		if !strings.HasPrefix(user.UserID, "user-") {
			t.Fatalf("unexpected subject in the roster: %q", user.UserID)
		}
	}
	_, sizes, _ := auth.stats()
	if sizes[0] != 5 {
		t.Fatalf("expected exactly the five seeded candidates, got %d", sizes[0])
	}
}
