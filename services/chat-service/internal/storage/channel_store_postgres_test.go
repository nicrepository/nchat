package storage_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Fixture identifiers for the channel-creation authorization tests.
const (
	chanWorkspace      = "c1000000-0000-4000-8000-000000000001"
	chanOtherWorkspace = "c2000000-0000-4000-8000-000000000001"
	chanDisabledWS     = "c3000000-0000-4000-8000-000000000001"
	chanGeneral        = "c1000000-0000-4000-8000-000000000020"
	chanOtherGeneral   = "c2000000-0000-4000-8000-000000000020"
	chanDisabledGenrl  = "c3000000-0000-4000-8000-000000000020"
	chanOwner          = "c1000000-0000-4000-8000-00000000000a"
	chanAdmin          = "c1000000-0000-4000-8000-00000000000b"
	chanMember         = "c1000000-0000-4000-8000-00000000000c"
	chanGuest          = "c1000000-0000-4000-8000-00000000000d"
	chanModerator      = "c1000000-0000-4000-8000-000000000012"
	chanSuspended      = "c1000000-0000-4000-8000-00000000000e"
	chanStranger       = "c1000000-0000-4000-8000-00000000000f"
	chanForeignOwner   = "c1000000-0000-4000-8000-000000000010"
	chanDisabledOwner  = "c1000000-0000-4000-8000-000000000011"
)

// newChannelAuthzPool prepares a schema-reset test database and seeds the
// workspaces, memberships and roles the creation-authorization cases share.
//
// Skips unless CHAT_TEST_DATABASE_URL points at a *_test database, the same gate
// and the same safety check the DM eligibility suite uses.
func newChannelAuthzPool(t *testing.T) *pgxpool.Pool {
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

	// One transaction: a deferred constraint requires every workspace to hold
	// exactly one active public general channel by commit time, so the workspaces
	// and their #geral channels cannot be inserted in separate statements.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin seed: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	for _, seed := range []struct {
		sql  string
		args []any
	}{
		{sql: `INSERT INTO chat.workspaces (id, slug, name, status) VALUES
			($1, 'chan-authz-a', 'Authz A', 'active'),
			($2, 'chan-authz-b', 'Authz B', 'active'),
			($3, 'chan-authz-c', 'Authz C', 'disabled')`,
			args: []any{chanWorkspace, chanOtherWorkspace, chanDisabledWS}},
		{sql: `INSERT INTO chat.channels (id, workspace_id, slug, display_name, type, is_general, status) VALUES
			($1, $4, 'geral', 'geral', 'public', true, 'active'),
			($2, $5, 'geral', 'geral', 'public', true, 'active'),
			($3, $6, 'geral', 'geral', 'public', true, 'active')`,
			args: []any{chanGeneral, chanOtherGeneral, chanDisabledGenrl, chanWorkspace, chanOtherWorkspace, chanDisabledWS}},
		// Every role in the workspace is active, so status and workspace are the
		// only things that vary among the roles that may create a channel. The
		// guest is the one role RF-74 excludes, and it is seeded active
		// precisely so the denial cannot be blamed on membership status.
		{sql: `INSERT INTO chat.workspace_members (workspace_id, user_id, role, status) VALUES
			($1, $4,  'owner',     'active'),
			($1, $5,  'admin',     'active'),
			($1, $6,  'member',    'active'),
			($1, $7,  'guest',     'active'),
			($1, $8,  'member',    'suspended'),
			($1, $11, 'moderator', 'active'),
			($2, $9,  'owner',     'active'),
			($3, $10, 'owner',     'active')`,
			args: []any{
				chanWorkspace, chanOtherWorkspace, chanDisabledWS,
				chanOwner, chanAdmin, chanMember, chanGuest, chanSuspended,
				chanForeignOwner, chanDisabledOwner, chanModerator,
			}},
	} {
		if _, err := tx.Exec(ctx, seed.sql, seed.args...); err != nil {
			t.Fatalf("seed channel authz fixtures: %v", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit seed: %v", err)
	}
	return pool
}

func countChannelRows(t *testing.T, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var total int
	if err := pool.QueryRow(t.Context(), query, args...).Scan(&total); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return total
}

// TestPGXChannelStoreCreateForActiveMemberPostgreSQL is the authoritative
// evidence that channel creation is authorized by the database, against real
// PostgreSQL: the membership predicate and the INSERT are one statement, so the
// role never enters it and a caller without an active membership produces no
// row at all.
func TestPGXChannelStoreCreateForActiveMemberPostgreSQL(t *testing.T) {
	pool := newChannelAuthzPool(t)
	store := storage.NewPGXChannelStore(pool)
	ctx := t.Context()

	for _, test := range []struct {
		name        string
		workspaceID string
		actor       string
		slug        string
		wantErr     error
	}{
		{name: "active owner", workspaceID: chanWorkspace, actor: chanOwner, slug: "by-owner"},
		{name: "active admin", workspaceID: chanWorkspace, actor: chanAdmin, slug: "by-admin"},
		{name: "active member", workspaceID: chanWorkspace, actor: chanMember, slug: "by-member"},
		{name: "active moderator", workspaceID: chanWorkspace, actor: chanModerator, slug: "by-moderator"},
		// RF-74: the guest is active and in the right workspace, and is refused
		// on role alone. This is the database half of domain.CanCreateChannel —
		// the INSERT re-derives the role list, so the service check is not the
		// only thing standing between a guest and a channel.
		{name: "active guest", workspaceID: chanWorkspace, actor: chanGuest, slug: "by-guest", wantErr: domain.ErrForbidden},
		{name: "suspended member", workspaceID: chanWorkspace, actor: chanSuspended, slug: "by-suspended", wantErr: domain.ErrForbidden},
		{name: "no membership", workspaceID: chanWorkspace, actor: chanStranger, slug: "by-stranger", wantErr: domain.ErrForbidden},
		{name: "membership in another workspace", workspaceID: chanWorkspace, actor: chanForeignOwner, slug: "by-foreign", wantErr: domain.ErrForbidden},
		{name: "disabled workspace", workspaceID: chanDisabledWS, actor: chanDisabledOwner, slug: "by-disabled", wantErr: domain.ErrForbidden},
		{name: "no actor at all", workspaceID: chanWorkspace, actor: "", slug: "by-nobody", wantErr: domain.ErrForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			created, err := store.CreateChannelForActiveMember(ctx, storage.CreateChannelInput{
				WorkspaceID: test.workspaceID,
				Slug:        test.slug,
				DisplayName: test.slug,
				Type:        domain.ChannelTypePublic,
				CreatedBy:   test.actor,
			})
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("error = %v, want %v", err, test.wantErr)
				}
				if n := countChannelRows(t, pool, `SELECT count(*) FROM chat.channels WHERE slug = $1`, test.slug); n != 0 {
					t.Fatalf("denied creation persisted %d channel row(s)", n)
				}
				return
			}
			if err != nil {
				t.Fatalf("CreateChannelForActiveMember: %v", err)
			}
			// created_by and workspace_id come out of the authorized context, so
			// they can only be the actor and the workspace the database checked.
			if created.CreatedBy != test.actor || created.WorkspaceID != test.workspaceID {
				t.Fatalf("channel = %+v, want created_by %s in %s", created, test.actor, test.workspaceID)
			}
		})
	}
}

// A private channel gets its creator's membership in the same transaction, and
// a public one gets none.
func TestPGXChannelStoreCreateForActiveMemberSeedsPrivateMembershipPostgreSQL(t *testing.T) {
	pool := newChannelAuthzPool(t)
	store := storage.NewPGXChannelStore(pool)
	ctx := t.Context()

	private, err := store.CreateChannelForActiveMember(ctx, storage.CreateChannelInput{
		WorkspaceID:             chanWorkspace,
		Slug:                    "private-room",
		DisplayName:             "Private Room",
		Type:                    domain.ChannelTypePrivate,
		CreatedBy:               chanMember,
		EnsureCreatorMemberRole: domain.ChannelRoleMember,
	})
	if err != nil {
		t.Fatalf("create private channel: %v", err)
	}
	if n := countChannelRows(t, pool,
		`SELECT count(*) FROM chat.channel_members WHERE channel_id = $1 AND user_id = $2`,
		private.ID, chanMember); n != 1 {
		t.Fatalf("private creator memberships = %d, want 1", n)
	}

	public, err := store.CreateChannelForActiveMember(ctx, storage.CreateChannelInput{
		WorkspaceID: chanWorkspace,
		Slug:        "public-room",
		DisplayName: "Public Room",
		Type:        domain.ChannelTypePublic,
		CreatedBy:   chanMember,
	})
	if err != nil {
		t.Fatalf("create public channel: %v", err)
	}
	if n := countChannelRows(t, pool,
		`SELECT count(*) FROM chat.channel_members WHERE channel_id = $1`, public.ID); n != 0 {
		t.Fatalf("public channel got %d channel_members row(s), want 0", n)
	}
}

// A failure after the channel insert must take the channel with it: a duplicate
// slug in the same transaction leaves no channel and no membership behind.
func TestPGXChannelStoreCreateForActiveMemberRollsBackEverythingPostgreSQL(t *testing.T) {
	pool := newChannelAuthzPool(t)
	store := storage.NewPGXChannelStore(pool)
	ctx := t.Context()

	input := storage.CreateChannelInput{
		WorkspaceID:             chanWorkspace,
		Slug:                    "rollback-room",
		DisplayName:             "Rollback Room",
		Type:                    domain.ChannelTypePrivate,
		CreatedBy:               chanMember,
		EnsureCreatorMemberRole: domain.ChannelRoleMember,
	}
	if _, err := store.CreateChannelForActiveMember(ctx, input); err != nil {
		t.Fatalf("seed first channel: %v", err)
	}
	if _, err := store.CreateChannelForActiveMember(ctx, input); !errors.Is(err, domain.ErrDuplicateSlug) {
		t.Fatalf("error = %v, want ErrDuplicateSlug", err)
	}
	if n := countChannelRows(t, pool,
		`SELECT count(*) FROM chat.channels WHERE workspace_id = $1 AND slug = $2`,
		chanWorkspace, input.Slug); n != 1 {
		t.Fatalf("channels named %q = %d, want exactly the first one", input.Slug, n)
	}
	// The pool must be usable afterwards, which it would not be if the failed
	// attempt had left its transaction open.
	if _, err := pool.Exec(ctx, `SELECT 1`); err != nil {
		t.Fatalf("pool unusable after rollback: %v", err)
	}
}

// TestPGXChannelStoreCreateLosesToConcurrentRevocationPostgreSQL closes the
// TOCTOU window this method exists for.
//
// A revocation is opened and held, then a creation starts and blocks on the same
// membership row. The revocation commits; the creation is released, re-evaluates
// its predicate against the committed row version, finds no active membership,
// and inserts nothing. The barriers are channels, so the interleaving is exactly
// this one on every run — no sleeps.
func TestPGXChannelStoreCreateLosesToConcurrentRevocationPostgreSQL(t *testing.T) {
	pool := newChannelAuthzPool(t)
	store := storage.NewPGXChannelStore(pool)
	ctx := t.Context()

	// Step 1: a revocation takes the row lock on the membership and holds it.
	revocation, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin revocation: %v", err)
	}
	defer func() { _ = revocation.Rollback(context.Background()) }()
	if _, err := revocation.Exec(ctx,
		`UPDATE chat.workspace_members SET status = 'suspended' WHERE workspace_id = $1 AND user_id = $2`,
		chanWorkspace, chanMember); err != nil {
		t.Fatalf("revoke membership: %v", err)
	}

	// Step 2: the creation starts on another connection and blocks on that row.
	type result struct {
		channel domain.Channel
		err     error
	}
	done := make(chan result, 1)
	go func() {
		channel, err := store.CreateChannelForActiveMember(context.Background(), storage.CreateChannelInput{
			WorkspaceID: chanWorkspace,
			Slug:        "race-room",
			DisplayName: "Race Room",
			Type:        domain.ChannelTypePublic,
			CreatedBy:   chanMember,
		})
		done <- result{channel: channel, err: err}
	}()

	// The creation is waiting on a lock, not merely slow: pg_stat_activity says
	// so, and waiting for that fact is what makes the ordering deterministic
	// instead of timing-dependent.
	waitForBlockedBackend(t, pool)

	select {
	case got := <-done:
		t.Fatalf("creation completed before the revocation committed: %+v", got)
	default:
	}

	// Steps 3 and 4: the revocation commits, releasing the creation.
	if err := revocation.Commit(ctx); err != nil {
		t.Fatalf("commit revocation: %v", err)
	}

	// Steps 5 and 6: the creation must fail, and leave nothing behind.
	got := <-done
	if !errors.Is(got.err, domain.ErrForbidden) {
		t.Fatalf("creation error = %v, want ErrForbidden", got.err)
	}
	if n := countChannelRows(t, pool, `SELECT count(*) FROM chat.channels WHERE slug = 'race-room'`); n != 0 {
		t.Fatalf("revoked member created %d channel row(s)", n)
	}
	if n := countChannelRows(t, pool, `SELECT count(*) FROM chat.channel_members WHERE user_id = $1`, chanMember); n != 0 {
		t.Fatalf("revoked member kept %d channel membership row(s)", n)
	}
	// No transaction left open by the losing side.
	if n := countChannelRows(t, pool,
		`SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND state = 'idle in transaction'`); n != 0 {
		t.Fatalf("%d connection(s) left idle in transaction", n)
	}
}

// The mirror image: the creation gets the lock first, so the revocation is the
// one that waits, and the channel is created legitimately.
func TestPGXChannelStoreCreateWinsAgainstLaterRevocationPostgreSQL(t *testing.T) {
	pool := newChannelAuthzPool(t)
	store := storage.NewPGXChannelStore(pool)
	ctx := t.Context()

	channel, err := store.CreateChannelForActiveMember(ctx, storage.CreateChannelInput{
		WorkspaceID: chanWorkspace,
		Slug:        "won-room",
		DisplayName: "Won Room",
		Type:        domain.ChannelTypePublic,
		CreatedBy:   chanMember,
	})
	if err != nil {
		t.Fatalf("CreateChannelForActiveMember: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE chat.workspace_members SET status = 'suspended' WHERE workspace_id = $1 AND user_id = $2`,
		chanWorkspace, chanMember); err != nil {
		t.Fatalf("revoke after creation: %v", err)
	}
	if n := countChannelRows(t, pool, `SELECT count(*) FROM chat.channels WHERE id = $1`, channel.ID); n != 1 {
		t.Fatalf("channel created before the revocation was lost")
	}
}

// waitForBlockedBackend blocks until some backend in this database is waiting on
// a lock, so the test proceeds on an observed state rather than on elapsed time.
func waitForBlockedBackend(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := t.Context()
	for {
		var blocked bool
		err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM pg_stat_activity
				WHERE datname = current_database()
				  AND wait_event_type = 'Lock'
			)`).Scan(&blocked)
		if err != nil {
			t.Fatalf("poll for blocked backend: %v", err)
		}
		if blocked {
			return
		}
		if ctx.Err() != nil {
			t.Fatal("no backend blocked on a lock before the test deadline")
		}
	}
}

// TestPGXChannelStoreDisplayNameConstraintPostgreSQL is the evidence that the
// display_name cap is enforced by the database, and that adding it does not
// depend on the state of existing data.
//
// The fixture is deliberately hostile: a legacy channel far over the limit is
// inserted *before* the constraint exists, which is exactly the situation a
// validated CHECK would abort the deploy on. The NOT VALID constraint is added
// on top of it, and from that moment every new INSERT and UPDATE is checked
// while the legacy row is left alone — readable, untruncated, and only forced
// into range if someone tries to update it.
func TestPGXChannelStoreDisplayNameConstraintPostgreSQL(t *testing.T) {
	pool := newChannelAuthzPool(t)
	ctx := t.Context()

	const legacyChannel = "c1000000-0000-4000-8000-000000000030"
	legacyName := strings.Repeat("L", 400)

	// Step 1: a row that the constraint would reject, written before it exists.
	// newChannelAuthzPool already applied every migration, so the constraint has
	// to come off first for the legacy state to be reproducible at all.
	if _, err := pool.Exec(ctx,
		`ALTER TABLE chat.channels DROP CONSTRAINT IF EXISTS channels_display_name_length_check`); err != nil {
		t.Fatalf("drop constraint to stage legacy data: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO chat.channels (id, workspace_id, slug, display_name, type)
		VALUES ($1, $2, 'legacy', $3, 'public')`,
		legacyChannel, chanWorkspace, legacyName); err != nil {
		t.Fatalf("seed legacy oversized channel: %v", err)
	}

	// Step 2: the migration itself must apply over that row rather than abort.
	if _, err := pool.Exec(ctx, readChatMigration(t, "000016_channel_display_name_length.up.sql")); err != nil {
		t.Fatalf("migration must apply with legacy data present, got: %v", err)
	}

	var stillThere string
	if err := pool.QueryRow(ctx,
		`SELECT display_name FROM chat.channels WHERE id = $1`, legacyChannel).Scan(&stillThere); err != nil {
		t.Fatalf("read legacy channel back: %v", err)
	}
	if stillThere != legacyName {
		t.Fatalf("legacy name was altered: %d code points, want %d",
			utf8.RuneCountInString(stillThere), utf8.RuneCountInString(legacyName))
	}

	var validated bool
	if err := pool.QueryRow(ctx, `
		SELECT convalidated FROM pg_constraint
		WHERE conname = 'channels_display_name_length_check'`).Scan(&validated); err != nil {
		t.Fatalf("read constraint state: %v", err)
	}
	if validated {
		t.Fatal("constraint was created validated; it must be NOT VALID so the deploy never scans the table")
	}

	isCheckViolation := func(err error) bool {
		var pgErr *pgconn.PgError
		return errors.As(err, &pgErr) &&
			pgErr.Code == "23514" &&
			pgErr.ConstraintName == "channels_display_name_length_check"
	}

	// Step 3 and 4: new writes are checked from this point on.
	t.Run("insert", func(t *testing.T) {
		for _, test := range []struct {
			name        string
			displayName string
			wantInsert  bool
		}{
			{name: "100 ascii", displayName: strings.Repeat("a", 100), wantInsert: true},
			{name: "101 ascii", displayName: strings.Repeat("a", 101)},
			// char_length counts characters, so 100 emoji fit and 101 do not —
			// the same answer Go's utf8.RuneCountInString gives.
			{name: "100 emoji", displayName: strings.Repeat("😀", 100), wantInsert: true},
			{name: "101 emoji", displayName: strings.Repeat("😀", 101)},
			{name: "empty", displayName: ""},
			// btrim mirrors the service's TrimSpace on both ends of the rule.
			{name: "whitespace only", displayName: "     "},
			{name: "at the limit once trimmed", displayName: "  " + strings.Repeat("a", 100) + "  ", wantInsert: true},
		} {
			t.Run(test.name, func(t *testing.T) {
				slug := "ins-" + strings.ReplaceAll(test.name, " ", "-")
				_, err := pool.Exec(ctx, `
					INSERT INTO chat.channels (workspace_id, slug, display_name, type)
					VALUES ($1, $2, $3, 'public')`,
					chanWorkspace, slug, test.displayName)
				if test.wantInsert {
					if err != nil {
						t.Fatalf("insert rejected a name the rule allows: %v", err)
					}
					return
				}
				if err == nil {
					t.Fatal("insert accepted a display_name the constraint must reject")
				}
				if !isCheckViolation(err) {
					t.Fatalf("rejected by %v, want channels_display_name_length_check", err)
				}
			})
		}
	})

	t.Run("update of a compliant row", func(t *testing.T) {
		const target = "c1000000-0000-4000-8000-000000000031"
		if _, err := pool.Exec(ctx, `
			INSERT INTO chat.channels (id, workspace_id, slug, display_name, type)
			VALUES ($1, $2, 'renameable', 'Fine', 'public')`, target, chanWorkspace); err != nil {
			t.Fatalf("seed renameable channel: %v", err)
		}
		_, err := pool.Exec(ctx,
			`UPDATE chat.channels SET display_name = $2 WHERE id = $1`, target, strings.Repeat("a", 101))
		if err == nil {
			t.Fatal("update to 101 code points was accepted")
		}
		if !isCheckViolation(err) {
			t.Fatalf("rejected by %v, want channels_display_name_length_check", err)
		}
	})

	// Steps 5 and 6: the legacy row is grandfathered for reads, but any update
	// has to bring it into range — it cannot be carried forward out of bounds.
	t.Run("update of the legacy row", func(t *testing.T) {
		_, err := pool.Exec(ctx,
			`UPDATE chat.channels SET display_name = $2 WHERE id = $1`, legacyChannel, strings.Repeat("M", 300))
		if err == nil {
			t.Fatal("legacy row was updated to another out-of-range value")
		}
		if !isCheckViolation(err) {
			t.Fatalf("rejected by %v, want channels_display_name_length_check", err)
		}

		if _, err := pool.Exec(ctx,
			`UPDATE chat.channels SET display_name = $2 WHERE id = $1`, legacyChannel, "Legacy, shortened"); err != nil {
			t.Fatalf("legacy row must be fixable: %v", err)
		}
	})

	// The rollback removes the constraint and nothing else, and re-applying is a
	// no-op-safe operation for a redeploy.
	t.Run("down and up again", func(t *testing.T) {
		if _, err := pool.Exec(ctx, readChatMigration(t, "000016_channel_display_name_length.down.sql")); err != nil {
			t.Fatalf("down migration: %v", err)
		}
		var remaining int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM pg_constraint
			WHERE conname = 'channels_display_name_length_check'`).Scan(&remaining); err != nil {
			t.Fatalf("count constraints: %v", err)
		}
		if remaining != 0 {
			t.Fatalf("down migration left %d constraint(s) behind", remaining)
		}
		// Data survives the rollback untouched.
		var count int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM chat.channels WHERE workspace_id = $1`, chanWorkspace).Scan(&count); err != nil {
			t.Fatalf("count channels: %v", err)
		}
		if count == 0 {
			t.Fatal("down migration removed channel rows")
		}
		if _, err := pool.Exec(ctx, readChatMigration(t, "000016_channel_display_name_length.up.sql")); err != nil {
			t.Fatalf("re-applying the migration must work: %v", err)
		}
	})
}
