package storage_test

import (
	"context"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Characterization suite for issue #881.
//
// It records, against a real PostgreSQL with every current chat migration
// applied, what "member of a channel" means today on each of the four surfaces
// the issue #877 family touches: visibility, explicit membership, the details
// count, mention autocomplete and candidate search.
//
// It deliberately records a divergence rather than a desired end state. In a
// public channel, "may read" and "has a chat.channel_members row" are different
// populations, and the last three surfaces all read the second one. Issue #883
// owns the decision about what effective channel membership should be for a
// public channel, and is expected to update these expectations when that
// contract changes — a failure here after #883 lands is the point, not a
// regression.
//
// What the suite must never become is a statement that the current behaviour is
// correct. Nothing below is named "desired", "correct" or "expected behaviour";
// the assertions say "today this is what happens", and the comments say which
// issue owns changing it.
//
// Every visibility assertion executes chat.channel_visible_to_user as it is
// actually installed after all migrations run. The predicate is never restated
// in Go or in test SQL: migration 000007 created the function and 000022
// replaced it, so a future migration can replace it again without editing
// either file, and only the installed function knows the active policy.

// Fixture identifiers for the membership-contract suite. A distinct prefix from
// every sibling fixture, so a leftover row can never satisfy an assertion here.
const (
	mcWorkspace = "d1000000-0000-4000-8000-000000000001"

	mcGeneral = "d1000000-0000-4000-8000-000000000020"
	mcPublic  = "d1000000-0000-4000-8000-000000000021"
	mcPrivate = "d1000000-0000-4000-8000-000000000022"

	mcOwner     = "d1000000-0000-4000-8000-00000000000a"
	mcAdmin     = "d1000000-0000-4000-8000-00000000000b"
	mcModerator = "d1000000-0000-4000-8000-00000000000c"
	mcMember    = "d1000000-0000-4000-8000-00000000000d"
	mcGuest     = "d1000000-0000-4000-8000-00000000000e"
)

// mcSearchPrefix is shared by every display name below, so one prefix search
// returns the whole fixture and the assertions are about membership rather than
// about which names happen to match.
const mcSearchPrefix = "Contract"

// mcRoles is the fixture's role assignment, and the order the role-agreement
// case iterates. Every user is seeded active: status is never what a denial
// below can be blamed on.
var mcRoles = []struct {
	userID string
	name   string
	role   domain.WorkspaceRole
}{
	{mcOwner, "Contract Owner", domain.WorkspaceRoleOwner},
	{mcAdmin, "Contract Admin", domain.WorkspaceRoleAdmin},
	{mcModerator, "Contract Moderator", domain.WorkspaceRoleModerator},
	{mcMember, "Contract Member", domain.WorkspaceRoleMember},
	{mcGuest, "Contract Guest", domain.WorkspaceRoleGuest},
}

// membershipContractPostgres prepares a schema-reset test database holding one
// active workspace with #geral, one ordinary public channel and one private
// channel, plus one active user in each of the five RF-74 workspace roles.
//
// The gate is the repository's existing one: CHAT_TEST_DATABASE_URL, and a
// refusal to run against a database whose name does not end in _test. No new
// bypass is introduced.
//
// The public channel is seeded with **no** chat.channel_members rows at all,
// including for a creator. That is not a shortcut for the test: it is what
// ChannelService.CreateChannel actually produces, because it sets
// EnsureCreatorMemberRole only for a private channel. #geral is seeded the same
// way, so the materialization case below can observe the sync do its work
// rather than inherit rows the fixture wrote.
func membershipContractPostgres(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	dsn := os.Getenv("CHAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("CHAT_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	var databaseName string
	if err := pool.QueryRow(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
		t.Fatalf("read current database: %v", err)
	}
	if !strings.HasSuffix(databaseName, "_test") {
		t.Fatalf("refusing destructive test against non-test database %q", databaseName)
	}
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS chat CASCADE`); err != nil {
		t.Fatalf("reset chat schema: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DROP SCHEMA IF EXISTS chat CASCADE`) })
	if _, err := pool.Exec(ctx, `
		CREATE SCHEMA IF NOT EXISTS auth;
		CREATE TABLE IF NOT EXISTS auth.users (
			id UUID PRIMARY KEY,
			email TEXT NOT NULL DEFAULT '',
			display_name TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'active',
			deleted_at TIMESTAMPTZ
		)`); err != nil {
		t.Fatalf("prepare auth schema: %v", err)
	}
	// ListOnlineChannelMemberProfiles projects full_name and avatar_url, which
	// the minimal table above does not carry. Aligned the same way the
	// add-members suite aligns it, so a database left over from either suite
	// satisfies both.
	if _, err := pool.Exec(ctx, `
		ALTER TABLE auth.users
			ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'active',
			ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ,
			ADD COLUMN IF NOT EXISTS email TEXT NOT NULL DEFAULT '',
			ADD COLUMN IF NOT EXISTS display_name TEXT NOT NULL DEFAULT '',
			ADD COLUMN IF NOT EXISTS full_name TEXT,
			ADD COLUMN IF NOT EXISTS avatar_url TEXT`); err != nil {
		t.Fatalf("align auth.users columns: %v", err)
	}
	if _, err := pool.Exec(ctx, readAllChatUpMigrations(t)); err != nil {
		t.Fatalf("apply chat migrations: %v", err)
	}

	// One transaction for the whole seed: deferred constraint triggers require
	// every workspace to hold exactly one active public general channel by
	// commit time, so a workspace inserted on its own would fail at commit.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin seed: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	for _, user := range mcRoles {
		if _, err := tx.Exec(ctx, `
			INSERT INTO auth.users (id, email, display_name, status, deleted_at)
			VALUES ($1, $2, $3, 'active', NULL)
			ON CONFLICT (id) DO UPDATE SET
				display_name = EXCLUDED.display_name, status = 'active', deleted_at = NULL`,
			user.userID, strings.ToLower(string(user.role))+"@membership-contract.test", user.name,
		); err != nil {
			t.Fatalf("seed user %s: %v", user.name, err)
		}
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO chat.workspaces (id, slug, name, status) VALUES ($1, 'membership-contract', 'Membership Contract', 'active')`,
		mcWorkspace,
	); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO chat.channels (id, workspace_id, slug, display_name, type, is_general, status) VALUES
			($1, $4, 'geral',    'Geral',    'public',  true,  'active'),
			($2, $4, 'anuncios', 'Anuncios', 'public',  false, 'active'),
			($3, $4, 'infra',    'Infra',    'private', false, 'active')`,
		mcGeneral, mcPublic, mcPrivate, mcWorkspace,
	); err != nil {
		t.Fatalf("seed channels: %v", err)
	}
	for _, user := range mcRoles {
		if _, err := tx.Exec(ctx,
			`INSERT INTO chat.workspace_members (workspace_id, user_id, role, status) VALUES ($1, $2, $3, 'active')`,
			mcWorkspace, user.userID, string(user.role),
		); err != nil {
			t.Fatalf("seed workspace member %s: %v", user.name, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit seed: %v", err)
	}
	return pool, ctx
}

// channelVisibleToUser executes chat.channel_visible_to_user as installed.
//
// The whole point of this helper is that it does not know the rule. It sends
// the two IDs to the database and returns whatever the deployed function says,
// so the tests below observe the active policy instead of a copy of it that
// could silently stop matching.
func channelVisibleToUser(t *testing.T, pool *pgxpool.Pool, ctx context.Context, channelID, userID string) bool {
	t.Helper()
	var visible bool
	if err := pool.QueryRow(ctx,
		`SELECT chat.channel_visible_to_user($1::uuid, $2::uuid)`, channelID, userID,
	).Scan(&visible); err != nil {
		t.Fatalf("chat.channel_visible_to_user(%s, %s): %v", channelID, userID, err)
	}
	return visible
}

// hasExplicitChannelMembership reports whether a chat.channel_members row
// exists, which is a different question from the one above and is asked
// separately on purpose.
func hasExplicitChannelMembership(t *testing.T, pool *pgxpool.Pool, ctx context.Context, channelID, userID string) bool {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM chat.channel_members WHERE channel_id = $1::uuid AND user_id = $2::uuid)`,
		channelID, userID,
	).Scan(&exists); err != nil {
		t.Fatalf("read channel membership: %v", err)
	}
	return exists
}

// mentionUserIDs runs the production mention-autocomplete query and returns the
// user IDs it offers, sorted so an assertion never depends on collation.
func mentionUserIDs(t *testing.T, store *storage.PGXMemberStore, ctx context.Context, channelID string) []string {
	t.Helper()
	candidates, err := store.SearchChannelMembers(ctx, mcWorkspace, channelID, mcSearchPrefix, 20)
	if err != nil {
		t.Fatalf("SearchChannelMembers: %v", err)
	}
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Type != domain.MentionTypeUser {
			t.Fatalf("mention candidate %q is %q, want a user", candidate.ID, candidate.Type)
		}
		ids = append(ids, candidate.ID)
	}
	sort.Strings(ids)
	return ids
}

// addMemberCandidateIDs runs the production candidate search and returns the
// user IDs it offers, sorted.
//
// callerID is an actor the current policy authorizes for this endpoint —
// domain.CanManageChannelMembers, which RF-74 states as active owner, admin or
// workspace moderator. The store does not re-derive the role (MemberService
// does, and PGXMemberStore.AddChannelMembers does for the write), so passing an
// authorized actor here is about running the query the way production runs it,
// not about asserting anything on authorization. Issue #881 changes no
// authorization rule; #884 owns any change to this endpoint's gate.
func addMemberCandidateIDs(
	t *testing.T, store *storage.PGXMemberStore, ctx context.Context, channelID, callerID string,
) []string {
	t.Helper()
	candidates, err := store.SearchChannelMemberCandidates(ctx, mcWorkspace, channelID, callerID, mcSearchPrefix, 20)
	if err != nil {
		t.Fatalf("SearchChannelMemberCandidates: %v", err)
	}
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, candidate.UserID)
	}
	sort.Strings(ids)
	return ids
}

func sortedIDs(ids ...string) []string {
	out := append([]string(nil), ids...)
	sort.Strings(out)
	return out
}

func assertSameIDs(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}

// The installed visibility function and domain.CanReachPublicChannels are two
// statements of one rule, and nothing makes them agree at compile time.
//
// This replaces an earlier #881 test that read the text of migration 000022 to
// decide the active policy. That was wrong in a way that mattered: 000007
// created chat.channel_visible_to_user and 000022 replaced it, so a later
// migration can replace it again without editing 000022, and the text would
// keep agreeing while the deployed rule moved. Here the function is called
// after every migration has run, so what is compared is what is installed.
//
// It is not a characterization of a divergence — the two agree today and must
// keep agreeing. Issue #882 owns the guest policy for #geral; whatever it
// decides, this test is what makes it move both statements together instead of
// one of them.
func TestChannelMembershipContractPostgreSQL_InstalledVisibilityMatchesTheDomainPredicate(t *testing.T) {
	pool, ctx := membershipContractPostgres(t)

	for _, user := range mcRoles {
		t.Run(string(user.role), func(t *testing.T) {
			member := domain.WorkspaceMember{
				WorkspaceID: mcWorkspace, UserID: user.userID,
				Role: user.role, Status: domain.MemberStatusActive,
			}
			reaches := domain.CanReachPublicChannels(&member)

			// No chat.channel_members row exists for anyone on either channel,
			// so what is being read is exactly the implicit half of the rule.
			for name, channelID := range map[string]string{
				"ordinary public channel": mcPublic,
				"#geral":                  mcGeneral,
			} {
				if got := channelVisibleToUser(t, pool, ctx, channelID, user.userID); got != reaches {
					t.Errorf("installed chat.channel_visible_to_user on %s = %t for role %q, but domain.CanReachPublicChannels = %t",
						name, got, user.role, reaches)
				}
			}

			// A private channel is the other half: no workspace role reaches it
			// implicitly, so the two statements must agree on false for
			// everybody, including the roles that reach public channels.
			if channelVisibleToUser(t, pool, ctx, mcPrivate, user.userID) {
				t.Errorf("role %q reaches a private channel with no membership row", user.role)
			}
		})
	}
}

// Characterization for #881. This intentionally records the current divergence.
// #883 owns the future effective-membership decision and is expected to update
// this characterization when that contract changes.
//
// In an ordinary public channel the four surfaces disagree about who is in it:
//
//   - chat.channel_visible_to_user admits every role CanReachPublicChannels
//     names, with no chat.channel_members row;
//   - ListOnlineChannelMemberProfiles counts chat.channel_members, so
//     member_count is 0 while those readers are reading;
//   - SearchChannelMembers offers nobody for mention autocomplete;
//   - SearchChannelMemberCandidates offers those same readers as people who
//     could still be added.
//
// None of that is asserted here as a good outcome. It is asserted so that #883
// cannot change it silently.
func TestChannelMembershipContractPostgreSQL_PublicChannelVisibilityDivergesFromExplicitMembership(t *testing.T) {
	pool, ctx := membershipContractPostgres(t)
	store := storage.NewPGXMemberStore(pool)

	// The readers of an ordinary public channel, under the current policy.
	readers := sortedIDs(mcOwner, mcAdmin, mcModerator, mcMember)

	t.Run("eligible roles read the channel without any membership row", func(t *testing.T) {
		for _, user := range mcRoles {
			member := domain.WorkspaceMember{
				WorkspaceID: mcWorkspace, UserID: user.userID,
				Role: user.role, Status: domain.MemberStatusActive,
			}
			visible := channelVisibleToUser(t, pool, ctx, mcPublic, user.userID)
			if visible != domain.CanReachPublicChannels(&member) {
				t.Fatalf("role %q: installed visibility = %t", user.role, visible)
			}
			// Asked separately, and this is the divergence in one line: the
			// answer above owes nothing to the answer below.
			if hasExplicitChannelMembership(t, pool, ctx, mcPublic, user.userID) {
				t.Fatalf("role %q unexpectedly holds a chat.channel_members row; the fixture seeds none", user.role)
			}
		}
	})

	t.Run("the details count reports zero members while those readers read", func(t *testing.T) {
		// Every reader is handed to the store as online, so nothing here can be
		// blamed on the presence snapshot being empty.
		page, err := store.ListOnlineChannelMemberProfiles(ctx, mcWorkspace, mcPublic, readers, domain.MaxChannelDetailsMembers)
		if err != nil {
			t.Fatalf("ListOnlineChannelMemberProfiles: %v", err)
		}
		// TotalCount, never len(page.Online): the preview is presence-filtered
		// and capped, and using its length as a count is the defect the channel
		// details contract already forbids.
		if page.TotalCount != 0 {
			t.Fatalf("TotalCount = %d, want the current characterization of 0 (counts chat.channel_members)", page.TotalCount)
		}
		if page.OnlineCount != 0 || len(page.Online) != 0 {
			t.Fatalf("online preview = %d/%d, want empty: presence intersects membership, and there is no membership",
				page.OnlineCount, len(page.Online))
		}
	})

	t.Run("mention autocomplete offers none of those readers", func(t *testing.T) {
		assertSameIDs(t, "mention candidates", mentionUserIDs(t, store, ctx, mcPublic), []string{})
	})

	t.Run("candidate search offers the readers as people who could be added", func(t *testing.T) {
		// The caller is excluded from its own candidate list by the query, so
		// the admin asking sees the other three readers plus the guest, who is
		// eligible to be added by design (RF-74: being added is the only way a
		// guest reaches any channel).
		got := addMemberCandidateIDs(t, store, ctx, mcPublic, mcAdmin)
		assertSameIDs(t, "add-member candidates", got, sortedIDs(mcOwner, mcModerator, mcMember, mcGuest))

		// Restated as the property that matters, so the failure message points
		// at the divergence rather than at a list literal.
		for _, reader := range readers {
			if reader == mcAdmin {
				continue
			}
			if !channelVisibleToUser(t, pool, ctx, mcPublic, reader) {
				t.Fatalf("fixture drift: %s should read this channel", reader)
			}
			found := false
			for _, candidate := range got {
				if candidate == reader {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("%s reads the channel but is not offered as a candidate; the characterization changed", reader)
			}
		}
	})
}

// Baseline, not a divergence: in a private channel the four surfaces already
// agree, because chat.channel_members is both the access rule and the roster.
//
// It is here so #883 cannot fix the public-channel contract by changing what a
// private channel means. Nothing in this case is expected to move.
func TestChannelMembershipContractPostgreSQL_PrivateChannelVisibilityMatchesExplicitMembership(t *testing.T) {
	pool, ctx := membershipContractPostgres(t)
	store := storage.NewPGXMemberStore(pool)

	// One explicit member, written through the production path rather than by
	// the fixture, so what is characterized is what the code actually produces.
	if _, err := store.AddChannelMember(ctx, mcPrivate, mcMember, domain.ChannelRoleMember); err != nil {
		t.Fatalf("AddChannelMember: %v", err)
	}

	t.Run("an outsider does not see it, whatever their workspace role", func(t *testing.T) {
		for _, user := range mcRoles {
			if user.userID == mcMember {
				continue
			}
			if channelVisibleToUser(t, pool, ctx, mcPrivate, user.userID) {
				t.Errorf("role %q reads a private channel it does not belong to", user.role)
			}
			if hasExplicitChannelMembership(t, pool, ctx, mcPrivate, user.userID) {
				t.Errorf("role %q unexpectedly holds a membership row", user.role)
			}
		}
	})

	t.Run("the explicit member sees it", func(t *testing.T) {
		if !channelVisibleToUser(t, pool, ctx, mcPrivate, mcMember) {
			t.Fatal("an explicit member does not see the private channel")
		}
		if !hasExplicitChannelMembership(t, pool, ctx, mcPrivate, mcMember) {
			t.Fatal("the explicit member has no chat.channel_members row")
		}
	})

	t.Run("roster, count and mention all describe that same row", func(t *testing.T) {
		page, err := store.ListOnlineChannelMemberProfiles(
			ctx, mcWorkspace, mcPrivate, []string{mcMember}, domain.MaxChannelDetailsMembers)
		if err != nil {
			t.Fatalf("ListOnlineChannelMemberProfiles: %v", err)
		}
		if page.TotalCount != 1 || page.OnlineCount != 1 || len(page.Online) != 1 {
			t.Fatalf("page = %d total / %d online / %d preview, want 1/1/1",
				page.TotalCount, page.OnlineCount, len(page.Online))
		}
		if page.Online[0].UserID != mcMember {
			t.Fatalf("preview holds %s, want the explicit member", page.Online[0].UserID)
		}
		assertSameIDs(t, "mention candidates", mentionUserIDs(t, store, ctx, mcPrivate), []string{mcMember})
	})

	t.Run("the current member is excluded from candidates", func(t *testing.T) {
		got := addMemberCandidateIDs(t, store, ctx, mcPrivate, mcAdmin)
		assertSameIDs(t, "add-member candidates", got, sortedIDs(mcOwner, mcModerator, mcGuest))
	})
}

// Baseline for #geral, and a characterization of the RF-74 guest boundary.
//
// #geral is the one public channel whose membership is materialized, by
// SyncGeneralMemberships, so its four surfaces agree the way a private
// channel's do — for the roles the sync covers. A guest is not one of them, and
// a guest also does not reach a public channel implicitly, so the two exclusions
// line up and #geral stays internally consistent.
//
// This records the policy as it is. Issue #882 owns consolidating RF-18's
// "every user joins #geral automatically" against RF-74's guest exclusion, and
// is expected to update this case. Nothing here decides that, auto-adds a
// guest, or touches CanReachPublicChannels or generalMembershipRoles.
func TestChannelMembershipContractPostgreSQL_GeneralChannelMaterializesMembershipForNonGuestRoles(t *testing.T) {
	pool, ctx := membershipContractPostgres(t)
	store := storage.NewPGXMemberStore(pool)

	synced := sortedIDs(mcOwner, mcAdmin, mcModerator, mcMember)

	t.Run("before the sync nobody holds a row, yet the eligible roles already read it", func(t *testing.T) {
		for _, user := range mcRoles {
			if hasExplicitChannelMembership(t, pool, ctx, mcGeneral, user.userID) {
				t.Fatalf("fixture drift: role %q already holds a #geral row", user.role)
			}
		}
		// The same divergence the public-channel case records, and the reason
		// the sync exists: access does not wait for materialization.
		for _, userID := range synced {
			if !channelVisibleToUser(t, pool, ctx, mcGeneral, userID) {
				t.Fatalf("%s cannot read #geral before the sync", userID)
			}
		}
	})

	t.Run("the sync materializes every covered role and no guest", func(t *testing.T) {
		inserted, err := store.SyncGeneralMemberships(ctx, mcWorkspace)
		if err != nil {
			t.Fatalf("SyncGeneralMemberships: %v", err)
		}
		if inserted != int64(len(synced)) {
			t.Fatalf("sync inserted %d rows, want %d", inserted, len(synced))
		}
		for _, userID := range synced {
			if !hasExplicitChannelMembership(t, pool, ctx, mcGeneral, userID) {
				t.Errorf("%s holds no #geral row after the sync", userID)
			}
		}
		if hasExplicitChannelMembership(t, pool, ctx, mcGeneral, mcGuest) {
			t.Error("the sync gave a guest a #geral row; RF-74 excludes guests from it")
		}
	})

	t.Run("the sync is idempotent", func(t *testing.T) {
		inserted, err := store.SyncGeneralMemberships(ctx, mcWorkspace)
		if err != nil {
			t.Fatalf("SyncGeneralMemberships (repeat): %v", err)
		}
		if inserted != 0 {
			t.Fatalf("repeat sync inserted %d rows, want 0", inserted)
		}
	})

	t.Run("the guest neither holds a row nor reads the channel", func(t *testing.T) {
		if channelVisibleToUser(t, pool, ctx, mcGeneral, mcGuest) {
			t.Error("installed visibility admits a guest to #geral with no membership row")
		}
		// The two exclusions agreeing is what keeps #geral consistent: the
		// guest is absent from the roster and absent from the readership, so
		// unlike an ordinary public channel there is no population mismatch.
		if hasExplicitChannelMembership(t, pool, ctx, mcGeneral, mcGuest) {
			t.Error("the guest holds a #geral membership row")
		}
	})

	t.Run("roster, count and mention describe the materialized rows", func(t *testing.T) {
		page, err := store.ListOnlineChannelMemberProfiles(
			ctx, mcWorkspace, mcGeneral, synced, domain.MaxChannelDetailsMembers)
		if err != nil {
			t.Fatalf("ListOnlineChannelMemberProfiles: %v", err)
		}
		if page.TotalCount != len(synced) {
			t.Fatalf("TotalCount = %d, want %d", page.TotalCount, len(synced))
		}
		if page.OnlineCount != len(synced) {
			t.Fatalf("OnlineCount = %d, want %d", page.OnlineCount, len(synced))
		}
		assertSameIDs(t, "mention candidates", mentionUserIDs(t, store, ctx, mcGeneral), synced)
	})

	t.Run("only the guest remains as a candidate", func(t *testing.T) {
		// Not a recommendation to add them: add-members refuses #geral in
		// MemberService before the store is ever reached. It is what this
		// query returns, and the gap between the two is itself part of what
		// #882 has to settle.
		got := addMemberCandidateIDs(t, store, ctx, mcGeneral, mcAdmin)
		assertSameIDs(t, "add-member candidates", got, []string{mcGuest})
	})
}
