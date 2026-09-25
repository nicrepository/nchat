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

// Membership-contract suite for issues #881 and #883.
//
// It records, against a real PostgreSQL with every current chat migration
// applied, what "member of a channel" means today on each of the four surfaces
// the issue #877 family touches: visibility, explicit membership, the details
// count, mention autocomplete and candidate search.
//
// Issue #883 closes the former public-channel divergence by materializing each
// eligible active workspace member in chat.channel_members. These assertions
// keep visibility, roster/count, mention autocomplete and candidate search on
// that shared population while preserving the explicit-invite rule for guests.
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
// The channels are inserted before workspace membership. The installed trigger
// therefore materializes eligible users into both public channels as each
// workspace membership becomes active, while leaving the private channel and
// guest memberships untouched.
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
// Candidate search follows channel access. This helper exercises the storage
// query's independent visibility revalidation.
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

// Issue #883 closes the old public-channel divergence: every active eligible
// workspace member is materialized into every active public channel. Visibility,
// roster/count, mentions and add-member candidates therefore describe the same
// population. Guests remain outside until explicitly invited.
func TestChannelMembershipContractPostgreSQL_PublicChannelMaterializesEligibleWorkspaceMembers(t *testing.T) {
	pool, ctx := membershipContractPostgres(t)
	store := storage.NewPGXMemberStore(pool)

	members := sortedIDs(mcOwner, mcAdmin, mcModerator, mcMember)

	t.Run("eligible roles are visible and hold membership rows", func(t *testing.T) {
		for _, user := range mcRoles {
			member := domain.WorkspaceMember{
				WorkspaceID: mcWorkspace, UserID: user.userID,
				Role: user.role, Status: domain.MemberStatusActive,
			}
			visible := channelVisibleToUser(t, pool, ctx, mcPublic, user.userID)
			if visible != domain.CanReachPublicChannels(&member) {
				t.Fatalf("role %q: installed visibility = %t", user.role, visible)
			}
			if got := hasExplicitChannelMembership(t, pool, ctx, mcPublic, user.userID); got != domain.CanReachPublicChannels(&member) {
				t.Fatalf("role %q: membership = %t, want eligibility %t", user.role, got, domain.CanReachPublicChannels(&member))
			}
		}
	})

	t.Run("details count and online preview use the materialized population", func(t *testing.T) {
		page, err := store.ListOnlineChannelMemberProfiles(ctx, mcWorkspace, mcPublic, members, domain.MaxChannelDetailsMembers)
		if err != nil {
			t.Fatalf("ListOnlineChannelMemberProfiles: %v", err)
		}
		if page.TotalCount != len(members) || page.OnlineCount != len(members) || len(page.Online) != len(members) {
			t.Fatalf("page = %d total / %d online / %d preview, want %d/%d/%d",
				page.TotalCount, page.OnlineCount, len(page.Online), len(members), len(members), len(members))
		}
	})

	t.Run("mention autocomplete offers every materialized member", func(t *testing.T) {
		assertSameIDs(t, "mention candidates", mentionUserIDs(t, store, ctx, mcPublic), members)
	})

	t.Run("only the guest remains an add-member candidate", func(t *testing.T) {
		got := addMemberCandidateIDs(t, store, ctx, mcPublic, mcAdmin)
		assertSameIDs(t, "add-member candidates", got, []string{mcGuest})
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
		got := addMemberCandidateIDs(t, store, ctx, mcPrivate, mcMember)
		assertSameIDs(t, "add-member candidates", got, sortedIDs(mcOwner, mcAdmin, mcModerator, mcGuest))
	})
}

// RF-18 makes #geral a structural exception: its materialized membership
// includes every eligible active workspace role, including guests. That row
// does not grant guests implicit access to any other public channel.
func TestChannelMembershipContractPostgreSQL_GeneralChannelMaterializesEveryActiveRole(t *testing.T) {
	pool, ctx := membershipContractPostgres(t)
	store := storage.NewPGXMemberStore(pool)

	nonGuests := sortedIDs(mcOwner, mcAdmin, mcModerator, mcMember)
	synced := sortedIDs(mcOwner, mcAdmin, mcModerator, mcMember, mcGuest)

	t.Run("public-membership trigger leaves the guest for the structural sync", func(t *testing.T) {
		for _, userID := range nonGuests {
			if !hasExplicitChannelMembership(t, pool, ctx, mcGeneral, userID) {
				t.Fatalf("%s holds no #geral row after workspace activation", userID)
			}
		}
		if hasExplicitChannelMembership(t, pool, ctx, mcGeneral, mcGuest) {
			t.Error("ordinary public-channel trigger unexpectedly included the guest")
		}
	})

	t.Run("the structural sync adds the guest once and is idempotent", func(t *testing.T) {
		inserted, err := store.SyncGeneralMemberships(ctx, mcWorkspace)
		if err != nil {
			t.Fatalf("SyncGeneralMemberships: %v", err)
		}
		if inserted != 1 {
			t.Fatalf("sync inserted %d rows, want the guest only", inserted)
		}
		if again, err := store.SyncGeneralMemberships(ctx, mcWorkspace); err != nil || again != 0 {
			t.Fatalf("repeat sync inserted %d rows: %v", again, err)
		}
	})

	t.Run("guest reads geral through its row but no ordinary public channel", func(t *testing.T) {
		if !channelVisibleToUser(t, pool, ctx, mcGeneral, mcGuest) {
			t.Error("guest cannot read its materialized #geral membership")
		}
		if !hasExplicitChannelMembership(t, pool, ctx, mcGeneral, mcGuest) {
			t.Error("guest has no materialized #geral row")
		}
		if channelVisibleToUser(t, pool, ctx, mcPublic, mcGuest) {
			t.Error("#geral membership widened guest access to another public channel")
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

	t.Run("no synchronized member remains as a candidate", func(t *testing.T) {
		got := addMemberCandidateIDs(t, store, ctx, mcGeneral, mcAdmin)
		assertSameIDs(t, "add-member candidates", got, []string{})
	})
}
