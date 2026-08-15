package storage

import (
	"strings"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// RF-21 visibility, asserted on the predicates the read paths share.
//
// The security review found `status` checked in some message reads and not in
// others, so a withheld message was invisible where somebody had remembered the
// filter and readable where they had not — GET by id, channel history and DM
// history among them. The fix was one definition applied by every read; these
// pin what that definition means. The behaviour itself is exercised against a
// real database in link_scan_visibility_postgres_test.go.

func TestVisibilityPredicateHidesPendingFromEveryoneButItsSender(t *testing.T) {
	predicate := messageVisibilityPredicate("m", "$3")

	// The state it excludes, and only that state: a deleted message is
	// deliberately still returned as a tombstone, and the read paths depend on
	// that, so a blanket `status = 'active'` would have been a different bug.
	if !strings.Contains(predicate, "pending_link_scan") {
		t.Fatalf("the predicate does not mention the withheld state: %s", predicate)
	}
	if strings.Contains(predicate, "deleted") {
		t.Fatalf("the predicate must not exclude tombstones: %s", predicate)
	}
	// The exemption is scoped to the row's own sender, so it cannot widen to
	// anyone else.
	if !strings.Contains(predicate, "m.sender_id = $3::uuid") {
		t.Fatalf("the sender exemption is not row-scoped: %s", predicate)
	}
}

// The projections are stricter than the reads: a withheld message must not
// reorder anybody's sidebar, not even its author's, because it has been shown to
// nobody and is not activity yet.
func TestProjectionPredicateExcludesPendingOutright(t *testing.T) {
	predicate := messageNotPendingPredicate("m")

	if !strings.Contains(predicate, "pending_link_scan") {
		t.Fatalf("predicate: %s", predicate)
	}
	if strings.Contains(predicate, "sender_id") {
		t.Fatalf("a projection must not carry a sender exemption: %s", predicate)
	}
}

// A verdict this package does not recognise is not a verdict.
//
// LoadLinkVerdicts drops any status that is not final, so a corrupted row, a
// value written by a newer version, or a hand-edited database cannot clear a
// message. Asserted here because it is a one-line guard that is easy to lose.
func TestOnlyFinalVerdictsAreLoadable(t *testing.T) {
	for _, status := range []string{"", "pending", "unknown", "SAFE", "future"} {
		if verdictIsLoadable(status) {
			t.Fatalf("%q was accepted as a verdict", status)
		}
	}
	for _, status := range []string{"safe", "malicious"} {
		if !verdictIsLoadable(status) {
			t.Fatalf("%q was rejected as a verdict", status)
		}
	}
}

// The reconnect answer, as a mapping.
//
// The branch that matters is the last one: a message stored as `deleted` is only
// reported as *blocked* when a malicious verdict explains it. Telling an author
// their link was malicious when the message may simply have been deleted is a
// claim with no evidence behind it, and it is exactly the inference the design
// forbids.
func TestLinkSafetyStateOnlyClaimsBlockedWithAVerdictBehindIt(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    string
		malicious bool
		want      domain.LinkSafetyState
	}{
		{"withheld", "pending_link_scan", false, domain.LinkSafetyStatePending},
		{"published", "active", false, domain.LinkSafetyStateActive},
		{"refused", "deleted", true, domain.LinkSafetyStateBlocked},
		{"deleted for some other reason", "deleted", false, domain.LinkSafetyStateDeleted},
		// A verdict cannot promote a withheld message into a terminal answer on
		// its own: the resolver decides that, in one transaction, and until it has
		// the honest answer is still "pending".
		{"withheld with a verdict landing", "pending_link_scan", true, domain.LinkSafetyStatePending},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := linkSafetyState(tc.status, tc.malicious); got != tc.want {
				t.Fatalf("linkSafetyState(%q, %v) = %q, want %q", tc.status, tc.malicious, got, tc.want)
			}
		})
	}
}

// The scoping is in the SQL, so it is asserted on the SQL: no reading of this
// query may return a row the caller did not write.
func TestLinkSafetyStatesQueryIsSenderScoped(t *testing.T) {
	if !strings.Contains(linkSafetyStatesQuery, "m.sender_id = $2::uuid") {
		t.Fatalf("the query is not sender-scoped: %s", linkSafetyStatesQuery)
	}
	if !strings.Contains(linkSafetyStatesQuery, "m.workspace_id = $1::uuid") {
		t.Fatalf("the query is not workspace-scoped: %s", linkSafetyStatesQuery)
	}
	// Business state, not delivery state. Reading the outbox here would make the
	// answer depend on whether a dispatcher had run.
	if strings.Contains(linkSafetyStatesQuery, "message_publish_outbox") {
		t.Fatalf("the answer is derived from the outbox: %s", linkSafetyStatesQuery)
	}
	// Bound to the content the message currently carries, the same binding the
	// promotion uses.
	if !strings.Contains(linkSafetyStatesQuery, "mls.fingerprint = COALESCE(m.link_safety_fingerprint, '')") {
		t.Fatalf("the verdict is not bound to this content: %s", linkSafetyStatesQuery)
	}
}
