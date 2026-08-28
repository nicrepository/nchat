package ws

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// Subject authorization for rosters (RF-58, SR-444-01).
//
// The directory stores presence assertions. It is not a roster and cannot answer
// whether somebody still belongs in a conversation, so everything that turns its
// contents into something a user sees has to ask the domain first.
//
// The case these tests are about is the one nothing else catches: a person whose
// access is removed while they are idle. They produce no transition, so no
// incremental check ever revisits them, and their assertion sits in the store.

// idleRevokedFixture leaves a stale assertion for `subject` in `targetID` and
// then revokes their access, without any further presence activity.
func idleRevokedFixture(
	t *testing.T, targetType TargetType, targetID, subject string,
) (*Hub, *fakeDirectory, *revocableAuthorizer, *Client) {
	t.Helper()
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()
	tracker := newTestPresenceTracker(5*time.Minute, clk)

	auth := newRevocableAuthorizer()
	h := newPresenceTestHub(auth, tracker)
	h.instanceID = "node-a"
	h.presenceInstanceID = "node-a"
	h.distributed = true
	h.directory = shared.view("node-a")

	subjectClient := newClient("c-subject", subject, "ws-1", &fakeSender{})
	registerInHub(t, h, subjectClient)
	tracker.Connect(subjectClient.workspaceID, subjectClient.userID, subjectClient.id)
	subscribeInHubState(t, h, subjectClient, targetType, targetID)
	h.handleSubscribed(subjectClient, targetType, targetID, 0)
	drainPresenceEvents(t, h)

	key := targetKey{workspaceID: "ws-1", targetType: targetType, targetID: targetID}.String()
	entries, err := shared.view("reader").Present(context.Background(), key)
	if err != nil {
		t.Fatalf("directory read: %v", err)
	}
	if len(aggregateRoster(entries)) != 1 {
		t.Fatalf("expected the subject to be asserted before the revoke: %+v", entries)
	}

	// Access is removed. The subject stays connected and does nothing at all —
	// no message, no activity, no transition.
	auth.revoke(subject, targetID)

	observer := newClient("c-observer", "user-observer", "ws-1", &fakeSender{})
	registerInHub(t, h, observer)
	subscribeInHubState(t, h, observer, targetType, targetID)
	return h, shared, auth, observer
}

func rosterNames(users []PresencePayload) []string {
	names := make([]string, 0, len(users))
	for _, user := range users {
		names = append(names, user.UserID)
	}
	return names
}

func assertAbsent(t *testing.T, users []PresencePayload, subject string) {
	t.Helper()
	for _, user := range users {
		if user.UserID == subject {
			t.Fatalf("a subject who lost access is still in the roster: %v", rosterNames(users))
		}
	}
}

// A member removed from a private channel while idle.
func TestSubjectAuth_RevokedChannelMemberIsNotInSnapshotOrReconciliation(t *testing.T) {
	h, shared, _, observer := idleRevokedFixture(t, TargetTypeChannel, "chan-private", "user-b")
	key := targetKey{workspaceID: "ws-1", targetType: TargetTypeChannel, targetID: "chan-private"}.String()

	// The stale assertion is deliberately still in the store.
	entries, err := shared.view("reader").Present(context.Background(), key)
	if err != nil {
		t.Fatalf("directory read: %v", err)
	}
	if len(aggregateRoster(entries)) != 1 {
		t.Fatal("this test needs the stale assertion to still be there")
	}

	// The initial snapshot an authorized reader receives.
	snapshot := snapshotFor(t, h, observer, TargetTypeChannel, "chan-private")
	assertAbsent(t, snapshot.Users, "user-b")

	// And the reconciliation sweep, which is the other way a roster reaches a
	// client.
	h.reconcilePresence()
	for _, roster := range reconciledRosters(t, h) {
		assertAbsent(t, roster.Users, "user-b")
	}
}

// The periodic sweep also withdraws the assertion rather than merely hiding it,
// and never renews its lease.
func TestSubjectAuth_RevokedSubjectAssertionIsWithdrawnAndNotRenewed(t *testing.T) {
	h, shared, _, _ := idleRevokedFixture(t, TargetTypeChannel, "chan-private", "user-b")
	key := targetKey{workspaceID: "ws-1", targetType: TargetTypeChannel, targetID: "chan-private"}.String()

	// Not renewable even before the sweep: authorization is part of eligibility.
	for _, renewable := range h.renewableAssertionKeys() {
		if renewable == key {
			t.Fatal("an unauthorized assertion was eligible for lease renewal")
		}
	}

	h.heartbeatDirectory()

	entries, err := shared.view("reader").Present(context.Background(), key)
	if err != nil {
		t.Fatalf("directory read: %v", err)
	}
	if roster := aggregateRoster(entries); len(roster) != 0 {
		t.Fatalf("the assertion survived the sweep: %v", rosterNames(roster))
	}
	if got := h.assertedTargetsList("ws-1", "user-b"); len(got) != 0 {
		t.Fatalf("the ledger kept an unauthorized assertion: %v", got)
	}
}

// A Guest removed from a private channel is treated exactly the same way: the
// authority is the one authorizer, so there is no separate Guest path to get
// wrong.
func TestSubjectAuth_RevokedGuestIsNotExposed(t *testing.T) {
	h, _, _, observer := idleRevokedFixture(t, TargetTypeChannel, "chan-guest", "user-guest")

	snapshot := snapshotFor(t, h, observer, TargetTypeChannel, "chan-guest")
	assertAbsent(t, snapshot.Users, "user-guest")

	h.reconcilePresence()
	for _, roster := range reconciledRosters(t, h) {
		assertAbsent(t, roster.Users, "user-guest")
	}
}

// The same for a conversation: someone whose access to it is gone.
func TestSubjectAuth_SubjectWithoutDMAccessIsNotExposed(t *testing.T) {
	h, _, _, observer := idleRevokedFixture(t, TargetTypeDM, "group-1", "user-b")

	snapshot := snapshotFor(t, h, observer, TargetTypeDM, "group-1")
	assertAbsent(t, snapshot.Users, "user-b")

	h.reconcilePresence()
	for _, roster := range reconciledRosters(t, h) {
		assertAbsent(t, roster.Users, "user-b")
	}
}

// Losing the ability to check is not permission, and it is not a complete
// answer either: the subject is withheld and the snapshot says it is partial,
// rather than quietly claiming they are offline.
func TestSubjectAuth_UndecidableSubjectIsWithheldAndSnapshotIsIncomplete(t *testing.T) {
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()
	tracker := newTestPresenceTracker(5*time.Minute, clk)

	auth := &erroringAuthorizer{}
	h := newPresenceTestHub(auth, tracker)
	h.instanceID = "node-a"
	h.presenceInstanceID = "node-a"
	h.distributed = true
	h.directory = shared.view("node-a")

	subject := newClient("c-subject", "user-b", "ws-1", &fakeSender{})
	registerInHub(t, h, subject)
	tracker.Connect(subject.workspaceID, subject.userID, subject.id)
	subscribeInHubState(t, h, subject, TargetTypeChannel, "chan-1")
	h.handleSubscribed(subject, TargetTypeChannel, "chan-1", 0)
	drainPresenceEvents(t, h)

	observer := newClient("c-observer", "user-a", "ws-1", &fakeSender{})
	registerInHub(t, h, observer)
	subscribeInHubState(t, h, observer, TargetTypeChannel, "chan-1")

	// Authorization starts failing.
	auth.fail(errors.New("database unavailable"))

	snapshot := snapshotFor(t, h, observer, TargetTypeChannel, "chan-1")
	assertAbsent(t, snapshot.Users, "user-b")
	if snapshot.Complete {
		t.Fatal("a roster built from unanswered questions must not claim to be complete")
	}
}

// erroringAuthorizer allows everything until it starts failing.
type erroringAuthorizer struct {
	mu  sync.Mutex
	err error
}

func (a *erroringAuthorizer) fail(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.err = err
}

func (a *erroringAuthorizer) CanAccess(context.Context, string, string, TargetType, string) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return false, a.err
	}
	return true, nil
}
