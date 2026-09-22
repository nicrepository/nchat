package storage_test

import (
	"sort"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// The administrable channel roster against a real PostgreSQL (issue #469).
//
// It carries the TestChannelMembershipContractPostgreSQL_ prefix deliberately:
// these are statements about which population "member of a channel" names, they
// run on the fixture that suite builds, and that prefix is the family the
// integration job executes (scripts/ci/go-integration-test.sh). A different
// name would be a test nothing runs.
//
// What only a database can prove here is the contrast the whole feature rests
// on. The details preview and the roster read the same rows, but the preview
// intersects them with a presence snapshot inside the query — so with nobody
// connected it is empty while the roster is not, and no amount of Go-level
// mocking can demonstrate that the two statements really disagree that way.

func rosterUserIDs(t *testing.T, page storage.ChannelRosterPage) []string {
	t.Helper()
	ids := make([]string, 0, len(page.Members))
	for _, member := range page.Members {
		ids = append(ids, member.UserID)
	}
	sort.Strings(ids)
	return ids
}

// The roster lists a member nobody can see connected, and the preview does not.
// That is the defect issue #469 would otherwise ship: an offline member with no
// row on screen cannot be removed.
func TestChannelMembershipContractPostgreSQL_RosterListsMembersThePresencePreviewDoesNot(t *testing.T) {
	pool, ctx := membershipContractPostgres(t)
	store := storage.NewPGXMemberStore(pool)
	for _, userID := range []string{mcOwner, mcAdmin, mcMember} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO chat.channel_members (channel_id, user_id, role) VALUES ($1::uuid, $2::uuid, 'member')`,
			mcPrivate, userID,
		); err != nil {
			t.Fatalf("seed channel membership: %v", err)
		}
	}

	// Nobody is connected: the preview's presence filter selects no rows.
	preview, err := store.ListOnlineChannelMemberProfiles(ctx, mcWorkspace, mcPrivate, nil, domain.MaxChannelDetailsMembers)
	if err != nil {
		t.Fatalf("ListOnlineChannelMemberProfiles: %v", err)
	}
	if len(preview.Online) != 0 {
		t.Fatalf("the presence preview must be empty with nobody online, got %+v", preview.Online)
	}

	roster, err := store.ListChannelMemberRoster(ctx, mcWorkspace, mcPrivate, domain.MaxChannelDetailsMembers)
	if err != nil {
		t.Fatalf("ListChannelMemberRoster: %v", err)
	}
	assertSameIDs(t, "roster", rosterUserIDs(t, roster), sortedIDs(mcOwner, mcAdmin, mcMember))
	// The same number the panel already shows as the channel's size.
	if roster.TotalCount != 3 || preview.TotalCount != 3 {
		t.Fatalf("roster total %d / preview total %d, want 3 and 3", roster.TotalCount, preview.TotalCount)
	}
	// The role each row carries is the channel role, read from the same row the
	// removal deletes.
	for _, member := range roster.Members {
		if member.Role != domain.ChannelRoleMember {
			t.Fatalf("member %s has role %q, want the seeded channel role", member.UserID, member.Role)
		}
		if member.DisplayName == "" {
			t.Fatalf("member %s came back without a resolved display name", member.UserID)
		}
	}
}

// A public channel's implicit readers are not members and must not be offered
// for removal: there is no row to delete, and a button that refuses is worse
// than no button. The divergence itself belongs to issue #883; what this pins
// is that the roster reads membership and never visibility.
func TestChannelMembershipContractPostgreSQL_RosterExcludesImplicitPublicReaders(t *testing.T) {
	pool, ctx := membershipContractPostgres(t)
	store := storage.NewPGXMemberStore(pool)

	if !channelVisibleToUser(t, pool, ctx, mcPublic, mcMember) {
		t.Fatal("fixture precondition: a workspace member reads a public channel")
	}
	if hasExplicitChannelMembership(t, pool, ctx, mcPublic, mcMember) {
		t.Fatal("fixture precondition: that reader has no chat.channel_members row")
	}

	roster, err := store.ListChannelMemberRoster(ctx, mcWorkspace, mcPublic, domain.MaxChannelDetailsMembers)
	if err != nil {
		t.Fatalf("ListChannelMemberRoster: %v", err)
	}
	if len(roster.Members) != 0 || roster.TotalCount != 0 {
		t.Fatalf("a public channel with no rows must have an empty roster, got %+v", roster)
	}
}

// Liveness and isolation are the query's, not the caller's: a suspended
// account, a workspace membership that is no longer active and a channel in
// another workspace all disappear from the roster without anyone filtering
// them in Go.
func TestChannelMembershipContractPostgreSQL_RosterDropsInactiveIdentitiesAndForeignChannels(t *testing.T) {
	pool, ctx := membershipContractPostgres(t)
	store := storage.NewPGXMemberStore(pool)
	for _, userID := range []string{mcOwner, mcAdmin, mcModerator} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO chat.channel_members (channel_id, user_id, role) VALUES ($1::uuid, $2::uuid, 'member')`,
			mcPrivate, userID,
		); err != nil {
			t.Fatalf("seed channel membership: %v", err)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE auth.users SET status = 'suspended' WHERE id = $1::uuid`, mcAdmin); err != nil {
		t.Fatalf("suspend account: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE chat.workspace_members SET status = 'left' WHERE workspace_id = $1::uuid AND user_id = $2::uuid`,
		mcWorkspace, mcModerator,
	); err != nil {
		t.Fatalf("deactivate workspace membership: %v", err)
	}

	roster, err := store.ListChannelMemberRoster(ctx, mcWorkspace, mcPrivate, domain.MaxChannelDetailsMembers)
	if err != nil {
		t.Fatalf("ListChannelMemberRoster: %v", err)
	}
	assertSameIDs(t, "roster", rosterUserIDs(t, roster), sortedIDs(mcOwner))
	if roster.TotalCount != 1 {
		t.Fatalf("TotalCount = %d, want 1 — the count and the page are one query", roster.TotalCount)
	}

	// The same channel, asked for under another workspace: the join on
	// chat.channels is what makes this empty rather than a tenant leak.
	foreign, err := store.ListChannelMemberRoster(ctx, mcGeneral, mcPrivate, domain.MaxChannelDetailsMembers)
	if err != nil {
		t.Fatalf("ListChannelMemberRoster (foreign workspace): %v", err)
	}
	if len(foreign.Members) != 0 || foreign.TotalCount != 0 {
		t.Fatalf("a channel read under the wrong workspace must be empty, got %+v", foreign)
	}
}

// The page is capped and the total is not: a channel larger than the cap still
// reports its real size, which is what the panel's shortfall note states.
func TestChannelMembershipContractPostgreSQL_RosterCapsThePageWithoutTruncatingTheTotal(t *testing.T) {
	pool, ctx := membershipContractPostgres(t)
	store := storage.NewPGXMemberStore(pool)
	for _, user := range mcRoles {
		if _, err := pool.Exec(ctx,
			`INSERT INTO chat.channel_members (channel_id, user_id, role) VALUES ($1::uuid, $2::uuid, 'member')`,
			mcPrivate, user.userID,
		); err != nil {
			t.Fatalf("seed channel membership: %v", err)
		}
	}

	roster, err := store.ListChannelMemberRoster(ctx, mcWorkspace, mcPrivate, 2)
	if err != nil {
		t.Fatalf("ListChannelMemberRoster: %v", err)
	}
	if len(roster.Members) != 2 {
		t.Fatalf("page = %d members, want the requested 2", len(roster.Members))
	}
	if roster.TotalCount != len(mcRoles) {
		t.Fatalf("TotalCount = %d, want the whole membership %d", roster.TotalCount, len(mcRoles))
	}
}
