package storage_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// RF-74 guest scope, exercised against the real database.
//
// chat.channel_visible_to_user is the authority for channel read access — the
// listing, message reads, pins, favorites, reactions, attachment downloads and
// the WebSocket subscription authorizer all run it. The rest of the suite
// asserts the migration's *text*; this file asserts its *behaviour*, which is
// the only thing that actually keeps a guest out of a channel.
const (
	rbacWorkspace = "d1000000-0000-4000-8000-000000000001"
	rbacOtherWS   = "d2000000-0000-4000-8000-000000000001"
	rbacGeneral   = "d1000000-0000-4000-8000-000000000020"
	rbacOtherGenl = "d2000000-0000-4000-8000-000000000020"
	rbacPublic    = "d1000000-0000-4000-8000-000000000021"
	rbacPrivate   = "d1000000-0000-4000-8000-000000000022"

	rbacOwner       = "d1000000-0000-4000-8000-00000000000a"
	rbacAdmin       = "d1000000-0000-4000-8000-00000000000b"
	rbacModerator   = "d1000000-0000-4000-8000-00000000000c"
	rbacMember      = "d1000000-0000-4000-8000-00000000000d"
	rbacGuestIn     = "d1000000-0000-4000-8000-00000000000e"
	rbacGuestOut    = "d1000000-0000-4000-8000-00000000000f"
	rbacSuspended   = "d1000000-0000-4000-8000-000000000010"
	rbacForeigner   = "d1000000-0000-4000-8000-000000000011"
	rbacPublicMsg   = "d1000000-0000-4000-8000-000000000030"
	rbacPrivateMsg  = "d1000000-0000-4000-8000-000000000031"
	rbacUnknownRole = "d1000000-0000-4000-8000-000000000012"
)

// newRBACPool resets the chat schema, applies every chat migration and seeds one
// workspace holding all five RF-74 roles plus the states that must fail closed.
//
// Skips unless CHAT_TEST_DATABASE_URL points at a *_test database — the same
// gate and the same destructive-database guard the other Postgres suites use.
func newRBACPool(t *testing.T) *pgxpool.Pool {
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
	if _, err := pool.Exec(ctx, readAllChatUpMigrations(t)); err != nil {
		t.Fatalf("apply chat migrations: %v", err)
	}

	// One transaction: a deferred constraint requires each workspace to hold
	// exactly one active public general channel by commit time.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin seed: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, seed := range []struct {
		sql  string
		args []any
	}{
		// chat.message_pins.pinned_by references auth.users, so every actor that
		// pins must exist as a user before the pin cases run.
		{sql: `INSERT INTO auth.users (id, email, display_name) VALUES
			($1, 'owner@rbac.test',     'Owner'),
			($2, 'admin@rbac.test',     'Admin'),
			($3, 'moderator@rbac.test', 'Moderator'),
			($4, 'member@rbac.test',    'Member'),
			($5, 'guest-in@rbac.test',  'Guest In'),
			($6, 'guest-out@rbac.test', 'Guest Out')
			ON CONFLICT (id) DO NOTHING`,
			args: []any{rbacOwner, rbacAdmin, rbacModerator, rbacMember, rbacGuestIn, rbacGuestOut}},
		{sql: `INSERT INTO chat.workspaces (id, slug, name, status) VALUES
			($1, 'rbac-ws', 'RBAC WS', 'active'),
			($2, 'rbac-other', 'Other WS', 'active')`,
			args: []any{rbacWorkspace, rbacOtherWS}},
		{sql: `INSERT INTO chat.channels (id, workspace_id, slug, display_name, type, is_general, status) VALUES
			($1, $5, 'geral', 'geral', 'public', true, 'active'),
			($2, $6, 'geral', 'geral', 'public', true, 'active'),
			($3, $5, 'publico', 'Publico', 'public', false, 'active'),
			($4, $5, 'privado', 'Privado', 'private', false, 'active')`,
			args: []any{rbacGeneral, rbacOtherGenl, rbacPublic, rbacPrivate, rbacWorkspace, rbacOtherWS}},
		// All five RF-74 roles, active, in the same workspace. Only the role
		// varies, so any difference in outcome is the role and nothing else.
		{sql: `INSERT INTO chat.workspace_members (workspace_id, user_id, role, status) VALUES
			($1, $3, 'owner',     'active'),
			($1, $4, 'admin',     'active'),
			($1, $5, 'moderator', 'active'),
			($1, $6, 'member',    'active'),
			($1, $7, 'guest',     'active'),
			($1, $8, 'guest',     'active'),
			($1, $9, 'member',    'suspended'),
			($2, $10,'owner',     'active')`,
			args: []any{
				rbacWorkspace, rbacOtherWS,
				rbacOwner, rbacAdmin, rbacModerator, rbacMember,
				rbacGuestIn, rbacGuestOut, rbacSuspended, rbacForeigner,
			}},
		// rbacGuestIn is explicitly added to both non-general channels; every
		// other role holds no channel membership at all, so a public channel
		// they can read is read by workspace role alone.
		{sql: `INSERT INTO chat.channel_members (channel_id, user_id, role) VALUES
			($1, $3, 'member'),
			($2, $3, 'member')`,
			args: []any{rbacPublic, rbacPrivate, rbacGuestIn}},
		{sql: `INSERT INTO chat.messages (id, workspace_id, channel_id, sender_id, body_text, status) VALUES
			($1, $3, $4, $6, 'public message', 'active'),
			($2, $3, $5, $6, 'private message', 'active')`,
			args: []any{rbacPublicMsg, rbacPrivateMsg, rbacWorkspace, rbacPublic, rbacPrivate, rbacGuestIn}},
	} {
		if _, err := tx.Exec(ctx, seed.sql, seed.args...); err != nil {
			t.Fatalf("seed rbac fixtures: %v", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit seed: %v", err)
	}
	return pool
}

func channelVisible(t *testing.T, pool *pgxpool.Pool, channelID, userID string) bool {
	t.Helper()
	var visible bool
	if err := pool.QueryRow(t.Context(),
		`SELECT chat.channel_visible_to_user($1, $2)`, channelID, userID).Scan(&visible); err != nil {
		t.Fatalf("channel_visible_to_user: %v", err)
	}
	return visible
}

// The guest boundary, stated as behaviour: workspace membership alone reaches a
// public channel for every role except guest, and a private channel is reached
// by channel membership alone for every role including owner and admin.
func TestChannelVisibleToUser_RF74RoleScopePostgreSQL(t *testing.T) {
	pool := newRBACPool(t)

	for _, test := range []struct {
		name      string
		channelID string
		userID    string
		want      bool
	}{
		// A public channel by workspace role alone.
		{name: "owner reads public", channelID: rbacPublic, userID: rbacOwner, want: true},
		{name: "admin reads public", channelID: rbacPublic, userID: rbacAdmin, want: true},
		{name: "moderator reads public", channelID: rbacPublic, userID: rbacModerator, want: true},
		{name: "member reads public", channelID: rbacPublic, userID: rbacMember, want: true},

		// The guest half: included or nothing.
		{name: "guest reads the public channel it was added to", channelID: rbacPublic, userID: rbacGuestIn, want: true},
		{name: "guest cannot read a public channel it was not added to", channelID: rbacPublic, userID: rbacGuestOut, want: false},
		{name: "guest does not get general for free", channelID: rbacGeneral, userID: rbacGuestOut, want: false},
		{name: "included guest still does not get general", channelID: rbacGeneral, userID: rbacGuestIn, want: false},

		// A private channel takes channel membership from everybody. A workspace
		// admin does not read a room it was not invited to.
		{name: "owner cannot read private without membership", channelID: rbacPrivate, userID: rbacOwner, want: false},
		{name: "admin cannot read private without membership", channelID: rbacPrivate, userID: rbacAdmin, want: false},
		{name: "moderator cannot read private without membership", channelID: rbacPrivate, userID: rbacModerator, want: false},
		{name: "member cannot read private without membership", channelID: rbacPrivate, userID: rbacMember, want: false},
		{name: "guest reads the private channel it was added to", channelID: rbacPrivate, userID: rbacGuestIn, want: true},

		// Fail-closed states.
		{name: "suspended membership reads nothing", channelID: rbacPublic, userID: rbacSuspended, want: false},
		{name: "another workspace reads nothing", channelID: rbacPublic, userID: rbacForeigner, want: false},
		{name: "no membership at all reads nothing", channelID: rbacPublic, userID: rbacUnknownRole, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := channelVisible(t, pool, test.channelID, test.userID); got != test.want {
				t.Fatalf("channel_visible_to_user = %v, want %v", got, test.want)
			}
		})
	}
}

// The role test is an allowlist, so a role the function does not recognise is
// denied rather than treated as a full member. Without this, widening the CHECK
// constraint later would silently widen read access as a side effect.
func TestChannelVisibleToUser_UnrecognisedRoleIsDeniedPostgreSQL(t *testing.T) {
	pool := newRBACPool(t)
	ctx := t.Context()

	// Bypass the CHECK to simulate a role a future migration might add: the
	// question is what the function does with a value it was not taught.
	if _, err := pool.Exec(ctx,
		`ALTER TABLE chat.workspace_members DROP CONSTRAINT workspace_members_role_check`); err != nil {
		t.Fatalf("drop role check: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO chat.workspace_members (workspace_id, user_id, role, status)
		 VALUES ($1, $2, 'auditor', 'active')`, rbacWorkspace, rbacUnknownRole); err != nil {
		t.Fatalf("seed unrecognised role: %v", err)
	}

	if channelVisible(t, pool, rbacPublic, rbacUnknownRole) {
		t.Fatal("an unrecognised role read a public channel; the role test must be an allowlist")
	}
	if channelVisible(t, pool, rbacGeneral, rbacUnknownRole) {
		t.Fatal("an unrecognised role read #geral; the role test must be an allowlist")
	}
}

// RF-05 pin/unpin takes the read access of the message and no role beyond it
// (SECURITY.md), and RF-74 did not carve out an exception for guests. So a guest
// inside the channel pins and unpins, and a guest outside it gets the same
// non-enumerating ErrNotFound it gets when reading.
func TestPGXPinStore_GuestPinFollowsReadAccessPostgreSQL(t *testing.T) {
	pool := newRBACPool(t)
	store := storage.NewPGXPinStore(pool)
	ctx := t.Context()

	t.Run("included guest pins and unpins", func(t *testing.T) {
		if err := store.AddPin(ctx, rbacWorkspace, "channel", rbacPublic, rbacPublicMsg, rbacGuestIn); err != nil {
			t.Fatalf("AddPin for an included guest: %v", err)
		}
		var pins int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM chat.message_pins WHERE target_id = $1 AND message_id = $2`,
			rbacPublic, rbacPublicMsg).Scan(&pins); err != nil {
			t.Fatalf("count pins: %v", err)
		}
		if pins != 1 {
			t.Fatalf("pins = %d, want 1", pins)
		}
		if err := store.RemovePin(ctx, rbacWorkspace, "channel", rbacPublic, rbacPublicMsg, rbacGuestIn); err != nil {
			t.Fatalf("RemovePin for an included guest: %v", err)
		}
	})

	t.Run("included guest pins in the private channel it belongs to", func(t *testing.T) {
		if err := store.AddPin(ctx, rbacWorkspace, "channel", rbacPrivate, rbacPrivateMsg, rbacGuestIn); err != nil {
			t.Fatalf("AddPin in a private channel the guest belongs to: %v", err)
		}
	})

	t.Run("excluded guest cannot pin", func(t *testing.T) {
		err := store.AddPin(ctx, rbacWorkspace, "channel", rbacPublic, rbacPublicMsg, rbacGuestOut)
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("AddPin for an excluded guest = %v, want ErrNotFound", err)
		}
	})

	t.Run("excluded guest cannot unpin what somebody else pinned", func(t *testing.T) {
		if err := store.AddPin(ctx, rbacWorkspace, "channel", rbacPublic, rbacPublicMsg, rbacMember); err != nil {
			t.Fatalf("seed pin as member: %v", err)
		}
		err := store.RemovePin(ctx, rbacWorkspace, "channel", rbacPublic, rbacPublicMsg, rbacGuestOut)
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("RemovePin for an excluded guest = %v, want ErrNotFound", err)
		}
		var pins int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM chat.message_pins WHERE target_id = $1 AND message_id = $2`,
			rbacPublic, rbacPublicMsg).Scan(&pins); err != nil {
			t.Fatalf("count pins: %v", err)
		}
		if pins != 1 {
			t.Fatalf("a denied unpin removed the row: pins = %d, want 1", pins)
		}
	})

	// A workspace admin is not a member of the private channel, so it cannot
	// pin there either: pin follows read access for every role, not just guests.
	t.Run("admin cannot pin in a private channel it does not belong to", func(t *testing.T) {
		err := store.AddPin(ctx, rbacWorkspace, "channel", rbacPrivate, rbacPrivateMsg, rbacAdmin)
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("AddPin for a non-member admin = %v, want ErrNotFound", err)
		}
	})
}

// RF-74 category management, exercised against the real SQL.
//
// domain.CanManageChannelCategories admits owner, admin and workspace moderator,
// and all four mutations re-derive that set in their own statement. Rename and
// delete previously carried their own owner/admin list, so a moderator passed
// the service and was then refused by the store with ErrNotFound — a divergence
// no unit test could see, because the role list only exists in SQL.
//
// The four operations are asserted together on purpose: the bug was that they
// disagreed, so testing one of them proves nothing about the others.
func TestPGXChannelStore_CategoryManagementRolesPostgreSQL(t *testing.T) {
	for _, test := range []struct {
		name    string
		caller  string
		allowed bool
	}{
		{name: "owner", caller: rbacOwner, allowed: true},
		{name: "admin", caller: rbacAdmin, allowed: true},
		{name: "workspace moderator", caller: rbacModerator, allowed: true},
		{name: "member", caller: rbacMember, allowed: false},
		{name: "guest", caller: rbacGuestIn, allowed: false},
		{name: "suspended membership", caller: rbacSuspended, allowed: false},
		{name: "member of another workspace", caller: rbacForeigner, allowed: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			pool := newRBACPool(t)
			store := storage.NewPGXChannelStore(pool)
			ctx := t.Context()

			// A category the caller did not create, so rename/delete are tested
			// against an existing row rather than against their own create.
			seeded, err := store.CreateChannelCategoryForManager(ctx, storage.CreateChannelCategoryInput{
				WorkspaceID: rbacWorkspace, CallerID: rbacOwner, Name: "Seeded",
			})
			if err != nil {
				t.Fatalf("seed category as owner: %v", err)
			}

			created, err := store.CreateChannelCategoryForManager(ctx, storage.CreateChannelCategoryInput{
				WorkspaceID: rbacWorkspace, CallerID: test.caller, Name: "Created",
			})
			if test.allowed != (err == nil) {
				t.Fatalf("create: err = %v, want allowed = %v", err, test.allowed)
			}

			if _, err := store.RenameChannelCategoryForManager(ctx, storage.RenameChannelCategoryInput{
				WorkspaceID: rbacWorkspace, CallerID: test.caller, CategoryID: seeded.ID, Name: "Renamed",
			}); test.allowed != (err == nil) {
				t.Fatalf("rename: err = %v, want allowed = %v", err, test.allowed)
			}

			ordered := []string{seeded.ID}
			if test.allowed {
				ordered = []string{created.ID, seeded.ID}
			}
			if _, err := store.ReorderChannelCategoriesForManager(ctx, storage.ReorderChannelCategoriesInput{
				WorkspaceID: rbacWorkspace, CallerID: test.caller, OrderedIDs: ordered,
			}); test.allowed != (err == nil) {
				t.Fatalf("reorder: err = %v, want allowed = %v", err, test.allowed)
			}

			if err := store.DeleteChannelCategoryForManager(ctx, rbacWorkspace, seeded.ID, test.caller); test.allowed != (err == nil) {
				t.Fatalf("delete: err = %v, want allowed = %v", err, test.allowed)
			}

			// A denied mutation must leave the row exactly as it was: the
			// authorization is in the same statement as the write, so a refusal
			// cannot half-apply.
			if !test.allowed {
				var name string
				if err := pool.QueryRow(ctx,
					`SELECT name FROM chat.channel_categories WHERE id = $1`, seeded.ID).Scan(&name); err != nil {
					t.Fatalf("a denied delete removed the category: %v", err)
				}
				if name != "Seeded" {
					t.Fatalf("a denied rename applied: name = %q", name)
				}
			}
		})
	}
}

// The per-channel moderator on chat.channel_members must not become a workspace
// moderator. A plain member that moderates a channel still cannot touch the
// workspace's categories.
func TestPGXChannelStore_ChannelModeratorIsNotACategoryManagerPostgreSQL(t *testing.T) {
	pool := newRBACPool(t)
	store := storage.NewPGXChannelStore(pool)
	ctx := t.Context()

	if _, err := pool.Exec(ctx,
		`INSERT INTO chat.channel_members (channel_id, user_id, role) VALUES ($1, $2, 'moderator')`,
		rbacPublic, rbacMember); err != nil {
		t.Fatalf("seed channel moderator: %v", err)
	}

	if _, err := store.CreateChannelCategoryForManager(ctx, storage.CreateChannelCategoryInput{
		WorkspaceID: rbacWorkspace, CallerID: rbacMember, Name: "By channel moderator",
	}); err == nil {
		t.Fatal("a chat.channel_members moderator created a workspace category")
	}
}
