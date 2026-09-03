package storage

import (
	"slices"
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
		name         string
		status       string
		malicious    bool
		inconclusive bool
		want         domain.LinkSafetyState
		wantReason   string
	}{
		{"withheld", "pending_link_scan", false, false, domain.LinkSafetyStatePending, ""},
		{"published", "active", false, false, domain.LinkSafetyStateActive, ""},
		{"refused", "deleted", true, false, domain.LinkSafetyStateBlocked, domain.LinkSafetyReasonMaliciousLink},
		{"deleted for some other reason", "deleted", false, false, domain.LinkSafetyStateDeleted, ""},
		{
			"refused for an inconclusive scan", "deleted", false, true,
			domain.LinkSafetyStateBlocked, domain.LinkSafetyReasonLinkCheckInconclusive,
		},
		{
			"malicious wins over inconclusive", "deleted", true, true,
			domain.LinkSafetyStateBlocked, domain.LinkSafetyReasonMaliciousLink,
		},
		// A verdict cannot promote a withheld message into a terminal answer on
		// its own: the resolver decides that, in one transaction, and until it has
		// the honest answer is still "pending".
		{"withheld with a verdict landing", "pending_link_scan", true, false, domain.LinkSafetyStatePending, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gotState, gotReason := linkSafetyState(tc.status, tc.malicious, tc.inconclusive)
			if gotState != tc.want || gotReason != tc.wantReason {
				t.Fatalf("linkSafetyState(%q, %v, %v) = (%q, %q), want (%q, %q)",
					tc.status, tc.malicious, tc.inconclusive, gotState, gotReason, tc.want, tc.wantReason)
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

// A notification parked while a scan ran is released only to a recipient who
// still has legitimate access to the target. Membership can change during the
// wait, so every part of the answer is re-read at promotion time rather than
// trusted from creation.
func TestPendingMentionPromotionRevalidatesRecipientTargetAccess(t *testing.T) {
	for _, predicate := range []string{
		// The tenant, the membership and the account, in that order.
		"mention_workspace.status = 'active'",
		"mention_member.status = 'active'",
		"mention_recipient.status = 'active'",
		"mention_recipient.deleted_at IS NULL",
		// The channel branch asks the project's one authority on channel
		// visibility, which is what makes the answer the same one every other
		// read path gives.
		"chat.channel_visible_to_user(mention_channel.id, mention.user_id)",
		"mention_channel.workspace_id = promoted.workspace_id",
		"mention_channel.status = 'active'",
		// The DM branch reads current membership of the current conversation.
		"JOIN chat.dm_members mention_dm_member",
		"mention_dm_member.status = 'active'",
		"mention_dm.workspace_id = promoted.workspace_id",
		"mention_dm.status = 'active'",
	} {
		if !strings.Contains(resolvePendingMessagesQuery, predicate) {
			t.Fatalf("pending notification promotion is missing %q", predicate)
		}
	}
}

// Two predicates that must not come back. Both described the promotion before
// it carried anything but mentions, and both would now silently drop real
// notifications — which is the kind of regression nothing else would report.
func TestPendingNotificationPromotionDoesNotNarrowTargetAccess(t *testing.T) {
	for predicate, why := range map[string]string{
		// A reply or a DM notification reaches somebody who need not be an
		// explicit channel member at all; requiring the row would drop every one
		// of those in a public channel, and would contradict
		// chat.channel_visible_to_user for the mentions it did release.
		"JOIN chat.channel_members mention_channel_member": "channel visibility is chat.channel_visible_to_user's answer",
		// A message withheld in a one-to-one DM has to release its notification
		// too; restricting the branch to group conversations would strand it.
		"mention_dm.type = 'group'": "a one-to-one conversation notifies its members like any other",
	} {
		if strings.Contains(resolvePendingMessagesQuery, predicate) {
			t.Fatalf("pending notification promotion must not require %q: %s", predicate, why)
		}
	}
}

// The admission locks the rows it charges for in this exact order. Two sends
// naming the same URLs in different orders must therefore produce the same
// sequence, and a URL repeated in one body must be locked once — a second lock
// attempt on a row this transaction already holds is the deadlock the ordering
// exists to prevent.
func TestUniqueSortedURLsIsAStableLockOrder(t *testing.T) {
	first := uniqueSortedURLs([]string{
		"https://example.test/b",
		"https://example.test/a",
		"https://example.test/b",
	})
	want := []string{"https://example.test/a", "https://example.test/b"}
	if !slices.Equal(first, want) {
		t.Fatalf("uniqueSortedURLs = %q, want %q", first, want)
	}

	// The reverse order is the other half of the invariant: the lock sequence is
	// a property of the set, not of how the body happened to spell it.
	second := uniqueSortedURLs([]string{
		"https://example.test/b",
		"https://example.test/a",
	})
	if !slices.Equal(first, second) {
		t.Fatalf("lock order depended on input order: %q vs %q", first, second)
	}
}
