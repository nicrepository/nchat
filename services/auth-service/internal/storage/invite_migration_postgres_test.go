//go:build integration

package storage_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/storage"
)

// These tests run against a real PostgreSQL server because the properties they
// check do not exist anywhere else: a migration's effect on existing rows, a
// partial unique index, a foreign key, FOR UPDATE, advisory locks and the
// commit boundary are all server behaviour. A mock can only replay whatever the
// test author believed the server would do, which is precisely the belief under
// examination here.
//
// They carry the `integration` build tag and are addressed by the same harness
// convention the media-service integration test uses: a per-service DSN
// variable, a database whose name must end in _test, and migrations read from
// migrations/ rather than from a second migration library.

const authTestDatabaseURLEnv = "AUTH_TEST_DATABASE_URL"

const (
	pgWorkspaceA      = "a0000000-0000-4000-8000-000000000001"
	pgWorkspaceB      = "a0000000-0000-4000-8000-000000000002"
	pgWorkspaceAbsent = "a0000000-0000-4000-8000-0000000000ff"

	pgLegacyPending  = "b0000000-0000-4000-8000-000000000001"
	pgLegacyExpired  = "b0000000-0000-4000-8000-000000000002"
	pgLegacyAccepted = "b0000000-0000-4000-8000-000000000003"
)

// ── Harness ────────────────────────────────────────────────────────────────

func integrationDSN(t *testing.T) string {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(authTestDatabaseURLEnv))
	if dsn == "" {
		t.Fatalf("%s must be set for PostgreSQL integration tests, e.g. postgres://user:pass@127.0.0.1:5432/nchat_auth_test", authTestDatabaseURLEnv)
	}
	return dsn
}

func connectTestDatabase(t *testing.T, ctx context.Context) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(ctx, integrationDSN(t))
	if err != nil {
		t.Fatalf("connect PostgreSQL test database: %v", err)
	}
	t.Cleanup(func() {
		if err := conn.Close(context.Background()); err != nil {
			t.Errorf("close PostgreSQL test connection: %v", err)
		}
	})

	// The schema resets below are destructive. Refusing anything but a _test
	// database is what keeps a mistyped DSN from dropping a real one.
	var databaseName string
	if err := conn.QueryRow(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
		t.Fatalf("read current database: %v", err)
	}
	if !strings.HasSuffix(databaseName, "_test") {
		t.Fatalf("refusing destructive integration test against non-test database %q", databaseName)
	}
	return conn
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve integration test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", "..", "..", ".."))
}

// migrationFiles lists one domain's migrations in apply order, which is
// filename order — the same rule the repository's migration runner uses.
func migrationFiles(t *testing.T, domainName, suffix string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(repositoryRoot(t), "migrations", domainName, "*"+suffix))
	if err != nil {
		t.Fatalf("list %s migrations: %v", domainName, err)
	}
	sort.Strings(matches)
	return matches
}

func applyMigrationFile(t *testing.T, ctx context.Context, conn *pgx.Conn, path string) {
	t.Helper()
	body, err := os.ReadFile(path) //nolint:gosec // repo-local migrations, path built from migrations/
	if err != nil {
		t.Fatalf("read migration %s: %v", path, err)
	}
	if _, err := conn.Exec(ctx, string(body)); err != nil {
		t.Fatalf("apply %s: %v", filepath.Base(path), err)
	}
}

// applyMigrationsBefore008 brings the database to the state immediately
// preceding the migration under test: every auth and chat migration except
// auth/000008, its companion chat/000019, and auth/000009, which comes after
// them and is applied explicitly by the tests that need it.
func applyMigrationsBefore008(t *testing.T, ctx context.Context, conn *pgx.Conn) {
	t.Helper()
	if _, err := conn.Exec(ctx, `DROP SCHEMA IF EXISTS chat CASCADE; DROP SCHEMA IF EXISTS auth CASCADE`); err != nil {
		t.Fatalf("reset PostgreSQL test schemas: %v", err)
	}
	for _, domainName := range []string{"auth", "chat"} {
		for _, path := range migrationFiles(t, domainName, ".up.sql") {
			base := filepath.Base(path)
			if strings.HasPrefix(base, "000008_invite_workspace_scope") ||
				strings.HasPrefix(base, "000019_invite_workspace_fk") ||
				strings.HasPrefix(base, "000009_bootstrap_auth_attempts") {
				continue
			}
			applyMigrationFile(t, ctx, conn, path)
		}
	}
}

func applyInviteScopeUp(t *testing.T, ctx context.Context, conn *pgx.Conn) {
	t.Helper()
	root := repositoryRoot(t)
	applyMigrationFile(t, ctx, conn, filepath.Join(root, "migrations", "auth", "000008_invite_workspace_scope.up.sql"))
	applyMigrationFile(t, ctx, conn, filepath.Join(root, "migrations", "chat", "000019_invite_workspace_fk.up.sql"))
}

// applyInviteScopeDown rolls back in reverse order, as the runner does.
func applyInviteScopeDown(t *testing.T, ctx context.Context, conn *pgx.Conn) {
	t.Helper()
	root := repositoryRoot(t)
	applyMigrationFile(t, ctx, conn, filepath.Join(root, "migrations", "chat", "000019_invite_workspace_fk.down.sql"))
	applyMigrationFile(t, ctx, conn, filepath.Join(root, "migrations", "auth", "000008_invite_workspace_scope.down.sql"))
}

// migratedDatabase is the common arrangement: schemas reset, every migration
// applied, fixtures seeded.
func migratedDatabase(t *testing.T, ctx context.Context) *pgx.Conn {
	t.Helper()
	conn := connectTestDatabase(t, ctx)
	applyMigrationsBefore008(t, ctx, conn)
	applyInviteScopeUp(t, ctx, conn)
	seedWorkspaces(t, ctx, conn)
	return conn
}

// seedWorkspace creates a workspace and its #geral channel together. They must
// be one transaction: a deferred constraint trigger requires every workspace to
// own exactly one active public general channel by commit time.
func seedWorkspace(t *testing.T, ctx context.Context, conn *pgx.Conn, id, slug, status string) {
	t.Helper()
	if _, err := conn.Exec(ctx, `
		BEGIN;
		INSERT INTO chat.workspaces (id, slug, name, status)
		VALUES ('`+id+`'::uuid, '`+slug+`', '`+slug+`', '`+status+`')
		ON CONFLICT (id) DO NOTHING;
		INSERT INTO chat.channels (workspace_id, slug, display_name, type, status, is_general)
		SELECT '`+id+`'::uuid, 'geral', 'geral', 'public', 'active', true
		WHERE NOT EXISTS (
			SELECT 1 FROM chat.channels WHERE workspace_id = '`+id+`'::uuid AND is_general = true);
		COMMIT;`); err != nil {
		t.Fatalf("seed workspace %s: %v", slug, err)
	}
}

func seedWorkspaces(t *testing.T, ctx context.Context, conn *pgx.Conn) {
	t.Helper()
	seedWorkspace(t, ctx, conn, pgWorkspaceA, "ws-a", "active")
	seedWorkspace(t, ctx, conn, pgWorkspaceB, "ws-b", "active")
}

func testPool(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(ctx, integrationDSN(t))
	if err != nil {
		t.Fatalf("open PostgreSQL test pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// inviteState is the part of a row this suite compares before and after a
// migration. Timestamps are included precisely because a careless migration
// touches them.
type inviteState struct {
	status      string
	workspaceID *string
	createdAt   time.Time
	updatedAt   time.Time
	expiresAt   time.Time
	acceptedAt  *time.Time
	revokedAt   *time.Time
}

func readInviteState(t *testing.T, ctx context.Context, conn *pgx.Conn, id string) inviteState {
	t.Helper()
	var s inviteState
	// workspace_id is selected only when the column exists, so this reader
	// works on both sides of the migration.
	var hasWorkspaceColumn bool
	if err := conn.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = 'auth' AND table_name = 'user_invites' AND column_name = 'workspace_id')`,
	).Scan(&hasWorkspaceColumn); err != nil {
		t.Fatalf("probe workspace_id column: %v", err)
	}

	query := `SELECT status, created_at, updated_at, expires_at, accepted_at, revoked_at, NULL::text
		FROM auth.user_invites WHERE id = $1::uuid`
	if hasWorkspaceColumn {
		query = `SELECT status, created_at, updated_at, expires_at, accepted_at, revoked_at, workspace_id::text
			FROM auth.user_invites WHERE id = $1::uuid`
	}
	if err := conn.QueryRow(ctx, query, id).
		Scan(&s.status, &s.createdAt, &s.updatedAt, &s.expiresAt, &s.acceptedAt, &s.revokedAt, &s.workspaceID); err != nil {
		t.Fatalf("read invite %s: %v", id, err)
	}
	return s
}

func seedLegacyInvites(t *testing.T, ctx context.Context, conn *pgx.Conn) {
	t.Helper()
	// Written before the workspace binding existed: no workspace column, and
	// three different lifecycle states so the migration is checked against each.
	if _, err := conn.Exec(ctx, `
		INSERT INTO auth.user_invites (id, email, token_hash, status, expires_at, accepted_at, revoked_at, created_at, updated_at)
		VALUES
			($1::uuid, 'legacy-pending@example.test',  'legacy-hash-pending',  'pending',  now() + interval '48 hours', NULL,                     NULL, now() - interval '10 days', now() - interval '10 days'),
			($2::uuid, 'legacy-expired@example.test',  'legacy-hash-expired',  'expired',  now() - interval '2 hours',  NULL,                     NULL, now() - interval '20 days', now() - interval '15 days'),
			($3::uuid, 'legacy-accepted@example.test', 'legacy-hash-accepted', 'accepted', now() + interval '48 hours', now() - interval '5 days', NULL, now() - interval '30 days', now() - interval '5 days')`,
		pgLegacyPending, pgLegacyExpired, pgLegacyAccepted,
	); err != nil {
		t.Fatalf("seed legacy invites: %v", err)
	}
}

func objectExists(t *testing.T, ctx context.Context, conn *pgx.Conn, query string, args ...any) bool {
	t.Helper()
	var exists bool
	if err := conn.QueryRow(ctx, query, args...).Scan(&exists); err != nil {
		t.Fatalf("probe database object: %v", err)
	}
	return exists
}

func columnExists(t *testing.T, ctx context.Context, conn *pgx.Conn, column string) bool {
	return objectExists(t, ctx, conn, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = 'auth' AND table_name = 'user_invites' AND column_name = $1)`, column)
}

func indexExists(t *testing.T, ctx context.Context, conn *pgx.Conn, name string) bool {
	return objectExists(t, ctx, conn, `
		SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname = 'auth' AND indexname = $1)`, name)
}

func constraintExists(t *testing.T, ctx context.Context, conn *pgx.Conn, name string) bool {
	return objectExists(t, ctx, conn, `
		SELECT EXISTS (
			SELECT 1 FROM pg_constraint c
			JOIN pg_class t ON t.oid = c.conrelid
			JOIN pg_namespace n ON n.oid = t.relnamespace
			WHERE n.nspname = 'auth' AND t.relname = 'user_invites' AND c.conname = $1)`, name)
}

// ── Migration up/down ──────────────────────────────────────────────────────

// The finding this test exists for: the migration used to revoke every legacy
// pending invite, and the down could not restore them. It now changes no
// existing row at all, which is what makes it reversible — so the assertion is
// literally "the three legacy rows are identical before, after up, and after
// down".
func TestInviteWorkspaceScopeMigration_PreservesLegacyRowsAndReverses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	conn := connectTestDatabase(t, ctx)
	applyMigrationsBefore008(t, ctx, conn)
	seedLegacyInvites(t, ctx, conn)

	before := map[string]inviteState{}
	for _, id := range []string{pgLegacyPending, pgLegacyExpired, pgLegacyAccepted} {
		before[id] = readInviteState(t, ctx, conn, id)
	}
	if before[pgLegacyPending].status != "pending" {
		t.Fatalf("fixture is not pending: %+v", before[pgLegacyPending])
	}

	applyInviteScopeUp(t, ctx, conn)
	seedWorkspaces(t, ctx, conn)

	t.Run("legacy rows survive the up untouched", func(t *testing.T) {
		for id, want := range before {
			got := readInviteState(t, ctx, conn, id)
			if got.status != want.status {
				t.Errorf("invite %s: status changed %q → %q", id, want.status, got.status)
			}
			if !got.createdAt.Equal(want.createdAt) || !got.updatedAt.Equal(want.updatedAt) || !got.expiresAt.Equal(want.expiresAt) {
				t.Errorf("invite %s: timestamps changed\nbefore %+v\nafter  %+v", id, want, got)
			}
			if !equalTimePtr(got.acceptedAt, want.acceptedAt) || !equalTimePtr(got.revokedAt, want.revokedAt) {
				t.Errorf("invite %s: lifecycle timestamps changed\nbefore %+v\nafter  %+v", id, want, got)
			}
			if got.workspaceID != nil {
				t.Errorf("invite %s: legacy row must keep a NULL workspace, got %q", id, *got.workspaceID)
			}
		}
	})

	t.Run("new schema objects exist", func(t *testing.T) {
		for _, column := range []string{"workspace_id", "invite_kind"} {
			if !columnExists(t, ctx, conn, column) {
				t.Errorf("expected column %s after the up", column)
			}
		}
		for _, index := range []string{
			"idx_user_invites_pending_workspace_email",
			"idx_user_invites_workspace_status",
			"idx_user_invites_inviter_window",
			"idx_user_invites_workspace_kind_status",
		} {
			if !indexExists(t, ctx, conn, index) {
				t.Errorf("expected index %s after the up", index)
			}
		}
		for _, constraint := range []string{
			"user_invites_pending_workspace_check",
			"user_invites_invite_kind_check",
			"user_invites_workspace_id_fkey",
		} {
			if !constraintExists(t, ctx, conn, constraint) {
				t.Errorf("expected constraint %s after the up", constraint)
			}
		}
		// Legacy rows default to the ordinary kind: the migration must not
		// retroactively grant anyone an owner-creating invite.
		var kind string
		if err := conn.QueryRow(ctx, `SELECT invite_kind FROM auth.user_invites WHERE id = $1::uuid`, pgLegacyPending).Scan(&kind); err != nil {
			t.Fatalf("read legacy invite kind: %v", err)
		}
		if kind != string(domain.InviteKindMember) {
			t.Errorf("legacy invites must default to member, got %q", kind)
		}
	})

	t.Run("pending uniqueness is per workspace", func(t *testing.T) {
		insert := func(workspaceID, email, hash string) error {
			_, err := conn.Exec(ctx, `
				INSERT INTO auth.user_invites (workspace_id, email, token_hash, status, expires_at)
				VALUES ($1::uuid, $2, $3, 'pending', now() + interval '48 hours')`,
				workspaceID, email, hash)
			return err
		}
		if err := insert(pgWorkspaceA, "shared@example.test", "scoped-hash-a"); err != nil {
			t.Fatalf("first scoped invite: %v", err)
		}
		// The same address in another workspace is a different onboarding and
		// must be allowed: a global constraint here is the cross-tenant block
		// this migration removes.
		if err := insert(pgWorkspaceB, "shared@example.test", "scoped-hash-b"); err != nil {
			t.Fatalf("same address in another workspace must be allowed: %v", err)
		}
		if err := insert(pgWorkspaceA, "shared@example.test", "scoped-hash-a2"); err == nil {
			t.Fatal("a second pending invite for the same (workspace, email) must be rejected")
		}
	})

	t.Run("legacy rows do not collide under the partial index", func(t *testing.T) {
		// Two workspace-less pending rows would collide under a plain unique
		// index on (workspace_id, email); the index is partial on a non-null
		// workspace, so they simply are not in it.
		if _, err := conn.Exec(ctx, `
			INSERT INTO auth.user_invites (email, token_hash, status, expires_at, created_at, updated_at)
			VALUES ('legacy-pending@example.test', 'legacy-hash-pending-2', 'expired', now() - interval '1 hour', now(), now())`,
		); err != nil {
			t.Fatalf("a second unscoped row must not collide: %v", err)
		}
	})

	t.Run("foreign key rejects an unknown workspace", func(t *testing.T) {
		_, err := conn.Exec(ctx, `
			INSERT INTO auth.user_invites (workspace_id, email, token_hash, status, expires_at)
			VALUES ($1::uuid, 'orphan@example.test', 'orphan-hash', 'pending', now() + interval '48 hours')`,
			pgWorkspaceAbsent)
		if err == nil {
			t.Fatal("an invite naming a workspace that does not exist must be rejected")
		}
	})

	t.Run("a new pending invite cannot omit the workspace", func(t *testing.T) {
		// The check is NOT VALID, so it never looked at the legacy rows — but it
		// still binds every row written from now on.
		_, err := conn.Exec(ctx, `
			INSERT INTO auth.user_invites (email, token_hash, status, expires_at)
			VALUES ('unscoped@example.test', 'unscoped-hash', 'pending', now() + interval '48 hours')`)
		if err == nil {
			t.Fatal("a new pending invite without a workspace must be rejected")
		}
	})

	t.Run("down removes the schema and preserves the legacy rows", func(t *testing.T) {
		// Scoped rows must go first: the column they depend on is about to be
		// dropped, and leaving them would prove nothing about the legacy ones.
		if _, err := conn.Exec(ctx, `DELETE FROM auth.user_invites WHERE workspace_id IS NOT NULL`); err != nil {
			t.Fatalf("clear scoped invites: %v", err)
		}
		applyInviteScopeDown(t, ctx, conn)

		for _, column := range []string{"workspace_id", "invite_kind"} {
			if columnExists(t, ctx, conn, column) {
				t.Errorf("column %s must be gone after the down", column)
			}
		}
		for _, index := range []string{
			"idx_user_invites_pending_workspace_email",
			"idx_user_invites_workspace_status",
			"idx_user_invites_inviter_window",
			"idx_user_invites_workspace_kind_status",
		} {
			if indexExists(t, ctx, conn, index) {
				t.Errorf("index %s must be gone after the down", index)
			}
		}
		for _, constraint := range []string{
			"user_invites_pending_workspace_check",
			"user_invites_invite_kind_check",
			"user_invites_workspace_id_fkey",
		} {
			if constraintExists(t, ctx, conn, constraint) {
				t.Errorf("constraint %s must be gone after the down", constraint)
			}
		}

		// The point of the whole exercise: the rows are as they were seeded.
		for id, want := range before {
			got := readInviteState(t, ctx, conn, id)
			if got.status != want.status {
				t.Errorf("invite %s: status changed by the round trip %q → %q", id, want.status, got.status)
			}
			if !got.createdAt.Equal(want.createdAt) || !got.updatedAt.Equal(want.updatedAt) || !got.expiresAt.Equal(want.expiresAt) {
				t.Errorf("invite %s: timestamps changed by the round trip\nbefore %+v\nafter  %+v", id, want, got)
			}
			if !equalTimePtr(got.acceptedAt, want.acceptedAt) || !equalTimePtr(got.revokedAt, want.revokedAt) {
				t.Errorf("invite %s: lifecycle timestamps changed by the round trip", id)
			}
		}
	})
}

func equalTimePtr(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

// A migration that fails must leave nothing behind. Each file is wrapped in its
// own BEGIN/COMMIT, so a mid-file error rolls the whole file back.
func TestInviteWorkspaceScopeMigration_UpIsAllOrNothing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	conn := connectTestDatabase(t, ctx)
	applyMigrationsBefore008(t, ctx, conn)
	applyInviteScopeUp(t, ctx, conn)

	// Re-applying fails on the already-present column, part-way through the
	// file. Nothing it would have added afterwards may survive that failure.
	body, err := os.ReadFile(filepath.Join(repositoryRoot(t), "migrations", "auth", "000008_invite_workspace_scope.up.sql"))
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if _, err := conn.Exec(ctx, string(body)); err == nil {
		t.Fatal("expected re-applying the up migration to fail")
	}
	// The file failed part-way through; the server discards its work when the
	// enclosing transaction ends.
	if _, err := conn.Exec(ctx, `ROLLBACK`); err != nil {
		t.Fatalf("end the aborted transaction: %v", err)
	}

	// The schema is exactly what the successful apply left: the failed run
	// neither removed what was there nor left a half-built object behind.
	for _, column := range []string{"workspace_id", "invite_kind"} {
		if !columnExists(t, ctx, conn, column) {
			t.Errorf("the failed re-apply must not have removed column %s", column)
		}
	}
	for _, index := range []string{
		"idx_user_invites_pending_workspace_email",
		"idx_user_invites_workspace_status",
		"idx_user_invites_inviter_window",
		"idx_user_invites_workspace_kind_status",
	} {
		if !indexExists(t, ctx, conn, index) {
			t.Errorf("the failed re-apply must not have removed index %s", index)
		}
	}
	assertNoOpenTransactions(t, ctx, conn)
}

// ── Store behaviour against real SQL ───────────────────────────────────────

func seedPolicy(t *testing.T, ctx context.Context, conn *pgx.Conn) {
	t.Helper()
	if _, err := conn.Exec(ctx, `
		INSERT INTO auth.auth_policy_settings (id) VALUES (1) ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatalf("seed policy settings: %v", err)
	}
}

// insertInvite writes an invite directly, bypassing the store, so a test can
// arrange a state the store would refuse to create.
func insertInvite(t *testing.T, ctx context.Context, conn *pgx.Conn, workspaceID *string, email, tokenHash string, kind domain.InviteKind) string {
	t.Helper()
	var id string
	if err := conn.QueryRow(ctx, `
		INSERT INTO auth.user_invites (workspace_id, email, token_hash, status, expires_at, invite_kind)
		VALUES ($1::uuid, $2, $3, 'pending', now() + interval '48 hours', $4)
		RETURNING id::text`,
		workspaceID, email, tokenHash, string(kind),
	).Scan(&id); err != nil {
		t.Fatalf("insert invite: %v", err)
	}
	return id
}

// A legacy unscoped invite is refused by the acceptance path. The migration
// deliberately leaves such rows pending, so this is the control that stops them
// being honoured.
func TestAcceptInviteTx_RejectsLegacyUnscopedInvite(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// The row is seeded before the migration runs, which is the only way this
	// state arises: afterwards the NOT VALID check refuses a pending invite
	// without a workspace. That is the shape a database upgraded from before
	// the workspace binding actually holds.
	conn := connectTestDatabase(t, ctx)
	applyMigrationsBefore008(t, ctx, conn)
	if _, err := conn.Exec(ctx, `
		INSERT INTO auth.user_invites (email, token_hash, status, expires_at)
		VALUES ('legacy@example.test', 'legacy-token-hash', 'pending', now() + interval '48 hours')`); err != nil {
		t.Fatalf("seed legacy invite: %v", err)
	}
	applyInviteScopeUp(t, ctx, conn)
	seedWorkspaces(t, ctx, conn)
	seedPolicy(t, ctx, conn)

	store := storage.NewPGXInviteStore(testPool(t, ctx))
	_, err := store.AcceptInviteTx(ctx, "legacy-token-hash", "Legacy", "Legacy User", "argon2id-hash")
	if !errors.Is(err, domain.ErrInviteWorkspaceMissing) {
		t.Fatalf("expected ErrInviteWorkspaceMissing, got %v", err)
	}

	// The refusal writes nothing: the invite is not consumed and no account
	// was created for an invite that names no workspace.
	var status string
	if err := conn.QueryRow(ctx, `SELECT status FROM auth.user_invites WHERE token_hash = 'legacy-token-hash'`).Scan(&status); err != nil {
		t.Fatalf("re-read legacy invite: %v", err)
	}
	if status != "pending" {
		t.Fatalf("a refused acceptance must not consume the invite, status is now %q", status)
	}
	assertCount(t, ctx, conn, 0, `SELECT count(*) FROM auth.users WHERE email = 'legacy@example.test'`)
	assertNoOpenTransactions(t, ctx, conn)
}

func TestCreateInvite_RequiresAnExistingWorkspace(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	conn := migratedDatabase(t, ctx)
	seedPolicy(t, ctx, conn)
	store := storage.NewPGXInviteStore(testPool(t, ctx))

	if _, err := store.CreateInvite(ctx, domain.AdminInviteInput{
		WorkspaceID: pgWorkspaceAbsent, ActorID: "", Email: "orphan@example.test",
		DisplayName: "Orphan", Kind: domain.InviteKindMember,
	}, "orphan-token-hash", time.Now().Add(48*time.Hour), `{"k":"v"}`, domain.InviteRateLimit{}); err == nil {
		t.Fatal("an invite into a workspace that does not exist must be refused")
	}

	var rows int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM auth.user_invites WHERE token_hash = 'orphan-token-hash'`).Scan(&rows); err != nil {
		t.Fatalf("count invites: %v", err)
	}
	if rows != 0 {
		t.Fatalf("a refused creation must leave no invite, found %d", rows)
	}
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM auth.email_outbox`).Scan(&rows); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	if rows != 0 {
		t.Fatalf("a refused creation must queue no e-mail, found %d", rows)
	}
}

// ── Assertions ─────────────────────────────────────────────────────────────

func assertExactlyOneWinner(t *testing.T, errs [2]error, wantLoserErr error) {
	t.Helper()
	var winners, losers int
	for _, err := range errs {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, wantLoserErr):
			losers++
		default:
			t.Fatalf("unexpected failure from a racing acceptance: %v", err)
		}
	}
	if winners != 1 || losers != 1 {
		t.Fatalf("expected exactly one success and one conflict, got %d/%d (errs: %v)", winners, losers, errs)
	}
}

func assertCount(t *testing.T, ctx context.Context, conn *pgx.Conn, want int, query string, args ...any) {
	t.Helper()
	var got int
	if err := conn.QueryRow(ctx, query, args...).Scan(&got); err != nil {
		t.Fatalf("count query %q: %v", query, err)
	}
	if got != want {
		t.Fatalf("expected %d for %s, got %d", want, strings.TrimSpace(query), got)
	}
}

// A leaked transaction would hold locks and eventually wedge the service, so
// it is asserted rather than assumed.
func assertNoOpenTransactions(t *testing.T, ctx context.Context, conn *pgx.Conn) {
	t.Helper()
	var idle int
	if err := conn.QueryRow(ctx, `
		SELECT count(*) FROM pg_stat_activity
		WHERE datname = current_database()
		  AND pid <> pg_backend_pid()
		  AND state IN ('idle in transaction', 'idle in transaction (aborted)')`,
	).Scan(&idle); err != nil {
		t.Fatalf("probe transaction state: %v", err)
	}
	if idle != 0 {
		t.Fatalf("expected no connection left in a transaction, found %d", idle)
	}
}
