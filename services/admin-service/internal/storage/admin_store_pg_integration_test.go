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

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
	"github.com/nicrepository/nchat/services/admin-service/internal/storage"
)

// The administrative authorization boundary is two SQL statements. A mock can
// confirm which statement is sent and with which binds; only a database can
// confirm what those statements actually admit and refuse — that a swapped
// user/session pair matches nothing, that a removed role disappears from the
// answer, that a suspended principal stops being an administrator.
//
// Gated on ADMIN_TEST_DATABASE_URL, in the same shape as auth-service's
// PostgreSQL tests, and skipped when it is unset.
//
//	ADMIN_TEST_DATABASE_URL=postgresql://nchat@localhost:5432/nchat_test \
//	  go test ./internal/storage/... -run PostgreSQL

func connectAdminTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("ADMIN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ADMIN_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// applyAuthMigrations rebuilds the auth schema from the real migration files,
// so the tables, constraints and seeded roles under test are the ones the
// platform ships rather than a hand-written approximation.
func applyAuthMigrations(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	applyMigrations(t, pool, "auth")
}

// applyMigrations rebuilds one migration domain's schema from its real files.
//
// Domains are applied in the order the caller names them, because chat's tables
// reference auth's; the reset drops them in reverse for the same reason.
func applyMigrations(t *testing.T, pool *pgxpool.Pool, domains ...string) {
	t.Helper()
	ctx := context.Background()

	var database string
	if err := pool.QueryRow(ctx, `SELECT current_database()`).Scan(&database); err != nil {
		t.Fatalf("current database: %v", err)
	}
	if !strings.HasSuffix(database, "_test") {
		t.Fatalf("refusing to run destructive migrations on non-test database %q", database)
	}
	for i := len(domains) - 1; i >= 0; i-- {
		if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS `+domains[i]+` CASCADE`); err != nil {
			t.Fatalf("reset %s schema: %v", domains[i], err)
		}
	}
	t.Cleanup(func() {
		for i := len(domains) - 1; i >= 0; i-- {
			_, _ = pool.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+domains[i]+` CASCADE`)
		}
	})
	for _, domain := range domains {
		applyMigrationDomain(t, pool, domain)
	}
}

func applyMigrationDomain(t *testing.T, pool *pgxpool.Pool, domain string) {
	t.Helper()
	ctx := context.Background()

	_, currentFile, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(currentFile), "..", "..", "..", "..", "migrations", domain)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read migrations dir: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".up.sql") {
			names = append(names, entry.Name())
		}
	}
	// Numeric prefixes, applied in order: 000008 references tables 000001 created.
	sort.Strings(names)
	for _, name := range names {
		sql, readErr := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // repo-local migrations
		if readErr != nil {
			t.Fatalf("read %s: %v", name, readErr)
		}
		if _, execErr := pool.Exec(ctx, string(sql)); execErr != nil {
			t.Fatalf("apply %s: %v", name, execErr)
		}
	}
}

// adminFixture is one administrator with one live chat session.
type adminFixture struct {
	userID    string
	sessionID string
	// otherUserID is a second, non-administrator account. It exists so the
	// swap and cross-user cases have a real second identity to confuse the
	// query with.
	otherUserID    string
	otherSessionID string
}

func seedAdminFixture(t *testing.T, pool *pgxpool.Pool) adminFixture {
	t.Helper()
	ctx := context.Background()
	var fixture adminFixture

	fixture.userID = insertUser(t, pool, "admin@example.test")
	fixture.otherUserID = insertUser(t, pool, "member@example.test")
	fixture.sessionID = insertSession(t, pool, fixture.userID, time.Hour, time.Hour*8)
	fixture.otherSessionID = insertSession(t, pool, fixture.otherUserID, time.Hour, time.Hour*8)

	if _, err := pool.Exec(ctx,
		`INSERT INTO auth.admin_principals (user_id) VALUES ($1)`, fixture.userID); err != nil {
		t.Fatalf("insert admin principal: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO auth.admin_principal_roles (user_id, role_slug) VALUES ($1, 'platform-auditor')`,
		fixture.userID); err != nil {
		t.Fatalf("grant admin role: %v", err)
	}
	return fixture
}

func insertUser(t *testing.T, pool *pgxpool.Pool, email string) string {
	t.Helper()
	var id string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO auth.users (email, display_name, full_name, status, auth_source, email_verified_at)
		VALUES ($1, 'Display', 'Full Name', 'active', 'manual', now())
		RETURNING id::text`, email).Scan(&id)
	if err != nil {
		t.Fatalf("insert user %s: %v", email, err)
	}
	return id
}

func insertSession(t *testing.T, pool *pgxpool.Pool, userID string, idle, absolute time.Duration) string {
	t.Helper()
	var id string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO auth.user_sessions (user_id, refresh_token_hash, idle_expires_at, absolute_expires_at)
		VALUES ($1, encode(gen_random_bytes(16), 'hex'),
		        now() + make_interval(secs => $2), now() + make_interval(secs => $3))
		RETURNING id::text`, userID, idle.Seconds(), absolute.Seconds()).Scan(&id)
	if err != nil {
		t.Fatalf("insert session: %v", err)
	}
	return id
}

func TestPGXAdminStore_AuthorizeHandshakePostgreSQL(t *testing.T) {
	pool := connectAdminTestDB(t)
	applyAuthMigrations(t, pool)
	fixture := seedAdminFixture(t, pool)
	store := storage.NewPGXAdminStore(pool)
	ctx := context.Background()

	t.Run("valid session and user", func(t *testing.T) {
		principal, err := store.AuthorizeHandshake(ctx, fixture.userID, fixture.sessionID)
		if err != nil {
			t.Fatalf("AuthorizeHandshake: %v", err)
		}
		if principal.UserID != fixture.userID || principal.Email != "admin@example.test" {
			t.Fatalf("unexpected principal: %+v", principal)
		}
		if principal.DisplayName != "Full Name" {
			t.Fatalf("expected the platform display-name rule, got %q", principal.DisplayName)
		}
		if !principal.Capabilities.Has(domain.CapabilityAuditRead) {
			t.Fatalf("expected the granted capability, got %v", principal.Capabilities.Effective())
		}
		if principal.Capabilities.Has(domain.CapabilityUsersManage) {
			t.Fatal("platform-auditor must not grant a manage capability")
		}
	})

	// The argument swap the review asked about, executed against a real
	// database: both values are UUIDs, so nothing type-checks it — only the
	// query does, and it must match no row.
	t.Run("user and session swapped", func(t *testing.T) {
		_, err := store.AuthorizeHandshake(ctx, fixture.sessionID, fixture.userID)
		if !errors.Is(err, domain.ErrUnauthorized) {
			t.Fatalf("expected ErrUnauthorized for swapped arguments, got %v", err)
		}
	})

	t.Run("session belonging to another user", func(t *testing.T) {
		_, err := store.AuthorizeHandshake(ctx, fixture.userID, fixture.otherSessionID)
		if !errors.Is(err, domain.ErrUnauthorized) {
			t.Fatalf("expected ErrUnauthorized, got %v", err)
		}
	})

	t.Run("authenticated non-administrator", func(t *testing.T) {
		_, err := store.AuthorizeHandshake(ctx, fixture.otherUserID, fixture.otherSessionID)
		if !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("being signed in is not administrative authority: got %v", err)
		}
	})

	t.Run("revoked session", func(t *testing.T) {
		sessionID := insertSession(t, pool, fixture.userID, time.Hour, 8*time.Hour)
		if _, err := pool.Exec(ctx,
			`UPDATE auth.user_sessions SET revoked_at = now() WHERE id = $1`, sessionID); err != nil {
			t.Fatalf("revoke session: %v", err)
		}
		if _, err := store.AuthorizeHandshake(ctx, fixture.userID, sessionID); !errors.Is(err, domain.ErrUnauthorized) {
			t.Fatalf("expected ErrUnauthorized, got %v", err)
		}
	})

	// The handshake keeps the platform idle window. This is the clause the
	// per-request check deliberately drops, so it is asserted here and denied
	// below.
	t.Run("idle-expired session", func(t *testing.T) {
		sessionID := insertSession(t, pool, fixture.userID, -time.Minute, 8*time.Hour)
		if _, err := store.AuthorizeHandshake(ctx, fixture.userID, sessionID); !errors.Is(err, domain.ErrUnauthorized) {
			t.Fatalf("expected ErrUnauthorized for an idle-expired session, got %v", err)
		}
	})

	t.Run("suspended user", func(t *testing.T) {
		userID := insertUser(t, pool, "suspended@example.test")
		sessionID := insertSession(t, pool, userID, time.Hour, 8*time.Hour)
		if _, err := pool.Exec(ctx, `INSERT INTO auth.admin_principals (user_id) VALUES ($1)`, userID); err != nil {
			t.Fatalf("insert principal: %v", err)
		}
		if _, err := pool.Exec(ctx, `UPDATE auth.users SET status = 'suspended' WHERE id = $1`, userID); err != nil {
			t.Fatalf("suspend user: %v", err)
		}
		if _, err := store.AuthorizeHandshake(ctx, userID, sessionID); !errors.Is(err, domain.ErrUnauthorized) {
			t.Fatalf("expected ErrUnauthorized, got %v", err)
		}
	})
}

func TestPGXAdminStore_ReauthorizeSessionPostgreSQL(t *testing.T) {
	pool := connectAdminTestDB(t)
	applyAuthMigrations(t, pool)
	fixture := seedAdminFixture(t, pool)
	store := storage.NewPGXAdminStore(pool)
	ctx := context.Background()

	t.Run("valid principal and capabilities", func(t *testing.T) {
		principal, err := store.ReauthorizeSession(ctx, fixture.userID, fixture.sessionID)
		if err != nil {
			t.Fatalf("ReauthorizeSession: %v", err)
		}
		if !principal.Capabilities.Has(domain.CapabilityAuditRead) {
			t.Fatalf("expected the granted capability, got %v", principal.Capabilities.Effective())
		}
	})

	t.Run("user and session swapped", func(t *testing.T) {
		if _, err := store.ReauthorizeSession(ctx, fixture.sessionID, fixture.userID); !errors.Is(err, domain.ErrUnauthorized) {
			t.Fatalf("expected ErrUnauthorized for swapped arguments, got %v", err)
		}
	})

	// The console does not refresh the chat session, so an idle chat window
	// must not evict a working administrator. This is the one clause that
	// separates the two queries.
	t.Run("chat session idle but not revoked", func(t *testing.T) {
		sessionID := insertSession(t, pool, fixture.userID, -time.Minute, 8*time.Hour)
		if _, err := store.ReauthorizeSession(ctx, fixture.userID, sessionID); err != nil {
			t.Fatalf("an idle chat session must not end administrative access: %v", err)
		}
	})

	t.Run("explicitly revoked chat session", func(t *testing.T) {
		sessionID := insertSession(t, pool, fixture.userID, time.Hour, 8*time.Hour)
		if _, err := pool.Exec(ctx,
			`UPDATE auth.user_sessions SET revoked_at = now() WHERE id = $1`, sessionID); err != nil {
			t.Fatalf("revoke session: %v", err)
		}
		if _, err := store.ReauthorizeSession(ctx, fixture.userID, sessionID); !errors.Is(err, domain.ErrUnauthorized) {
			t.Fatalf("expected ErrUnauthorized, got %v", err)
		}
	})

	t.Run("chat session past its absolute lifetime", func(t *testing.T) {
		sessionID := insertSession(t, pool, fixture.userID, time.Hour, -time.Minute)
		if _, err := store.ReauthorizeSession(ctx, fixture.userID, sessionID); !errors.Is(err, domain.ErrUnauthorized) {
			t.Fatalf("expected ErrUnauthorized, got %v", err)
		}
	})

	// Removing a role must be visible on the very next call: the capabilities
	// are read from the database each time rather than cached in a token.
	t.Run("capability removed mid-session", func(t *testing.T) {
		userID := insertUser(t, pool, "revocable@example.test")
		sessionID := insertSession(t, pool, userID, time.Hour, 8*time.Hour)
		if _, err := pool.Exec(ctx, `INSERT INTO auth.admin_principals (user_id) VALUES ($1)`, userID); err != nil {
			t.Fatalf("insert principal: %v", err)
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO auth.admin_principal_roles (user_id, role_slug) VALUES ($1, 'platform-superuser')`,
			userID); err != nil {
			t.Fatalf("grant role: %v", err)
		}

		principal, err := store.ReauthorizeSession(ctx, userID, sessionID)
		if err != nil || !principal.Capabilities.Has(domain.CapabilitySuperuser) {
			t.Fatalf("expected the superuser grant, got %+v (%v)", principal.Capabilities.Effective(), err)
		}

		if _, err := pool.Exec(ctx, `DELETE FROM auth.admin_principal_roles WHERE user_id = $1`, userID); err != nil {
			t.Fatalf("revoke role: %v", err)
		}

		principal, err = store.ReauthorizeSession(ctx, userID, sessionID)
		if err != nil {
			t.Fatalf("ReauthorizeSession: %v", err)
		}
		if !principal.Capabilities.IsEmpty() {
			t.Fatalf("expected no capabilities after the role was removed, got %v", principal.Capabilities.Effective())
		}
	})

	t.Run("suspended principal", func(t *testing.T) {
		userID := insertUser(t, pool, "suspended-admin@example.test")
		sessionID := insertSession(t, pool, userID, time.Hour, 8*time.Hour)
		if _, err := pool.Exec(ctx,
			`INSERT INTO auth.admin_principals (user_id, status) VALUES ($1, 'suspended')`, userID); err != nil {
			t.Fatalf("insert suspended principal: %v", err)
		}
		if _, err := store.ReauthorizeSession(ctx, userID, sessionID); !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("a suspended principal is not an administrator: got %v", err)
		}
	})
}

// The seeded roles are part of the authorization model, so the migration that
// creates them is checked here rather than assumed.
func TestPGXAdminStore_SeededRolesPostgreSQL(t *testing.T) {
	pool := connectAdminTestDB(t)
	applyAuthMigrations(t, pool)
	ctx := context.Background()

	var superuser, auditor int
	if err := pool.QueryRow(ctx, `
		SELECT
		  count(*) FILTER (WHERE role_slug = 'platform-superuser'),
		  count(*) FILTER (WHERE role_slug = 'platform-auditor')
		FROM auth.admin_role_capabilities`).Scan(&superuser, &auditor); err != nil {
		t.Fatalf("count seeded capabilities: %v", err)
	}
	if superuser != 1 || auditor != 7 {
		t.Fatalf("unexpected seeded roles: superuser=%d auditor=%d", superuser, auditor)
	}

	// No principal is created by the migration: an administrator is granted
	// deliberately, never provisioned by a schema change.
	var principals int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM auth.admin_principals`).Scan(&principals); err != nil {
		t.Fatalf("count principals: %v", err)
	}
	if principals != 0 {
		t.Fatalf("migrations must not create administrators, found %d", principals)
	}
}

// The administrative session table references the principal, not the user.
// These prove the invariant the schema now carries: a session cannot exist for
// someone who is not an administrator, and removing the administrator removes
// their sessions rather than leaving rows every request would refuse anyway.
func TestPGXAdminStore_SessionRequiresAnAdminPrincipalPostgreSQL(t *testing.T) {
	pool := connectAdminTestDB(t)
	applyAuthMigrations(t, pool)
	fixture := seedAdminFixture(t, pool)
	store := storage.NewPGXAdminStore(pool)
	ctx := context.Background()

	newSessionInput := func(userID, authSessionID, hash string) domain.AdminSessionInput {
		return domain.AdminSessionInput{
			UserID:            userID,
			AuthSessionID:     authSessionID,
			SessionHash:       hash,
			IdleExpiresAt:     time.Now().UTC().Add(15 * time.Minute),
			AbsoluteExpiresAt: time.Now().UTC().Add(8 * time.Hour),
		}
	}

	t.Run("session for an administrator is created", func(t *testing.T) {
		session, err := store.CreateSession(ctx, newSessionInput(fixture.userID, fixture.sessionID, "hash-admin"))
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		if session.ID == "" {
			t.Fatal("expected a session id")
		}
	})

	// The handshake reads the principal and writes the session in two
	// statements. This is the gap that closes: a non-administrator cannot be
	// given a session even if the read somehow said otherwise.
	t.Run("session for a non-administrator is refused by the database", func(t *testing.T) {
		_, err := store.CreateSession(ctx, newSessionInput(fixture.otherUserID, fixture.otherSessionID, "hash-member"))
		if err == nil {
			t.Fatal("expected the foreign key to refuse a session without an admin principal")
		}
	})

	t.Run("deleting the principal removes their sessions", func(t *testing.T) {
		userID := insertUser(t, pool, "cascade@example.test")
		sessionID := insertSession(t, pool, userID, time.Hour, 8*time.Hour)
		if _, err := pool.Exec(ctx, `INSERT INTO auth.admin_principals (user_id) VALUES ($1)`, userID); err != nil {
			t.Fatalf("insert principal: %v", err)
		}
		if _, err := store.CreateSession(ctx, newSessionInput(userID, sessionID, "hash-cascade")); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}

		if _, err := pool.Exec(ctx, `DELETE FROM auth.admin_principals WHERE user_id = $1`, userID); err != nil {
			t.Fatalf("delete principal: %v", err)
		}

		var remaining int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM auth.admin_sessions WHERE user_id = $1`, userID).Scan(&remaining); err != nil {
			t.Fatalf("count sessions: %v", err)
		}
		if remaining != 0 {
			t.Fatalf("expected the sessions to cascade away, %d remain", remaining)
		}
	})

	// Deleting the person still works end to end: users -> principals ->
	// sessions, all by cascade.
	t.Run("deleting the user cascades through the principal", func(t *testing.T) {
		userID := insertUser(t, pool, "full-cascade@example.test")
		sessionID := insertSession(t, pool, userID, time.Hour, 8*time.Hour)
		if _, err := pool.Exec(ctx, `INSERT INTO auth.admin_principals (user_id) VALUES ($1)`, userID); err != nil {
			t.Fatalf("insert principal: %v", err)
		}
		if _, err := store.CreateSession(ctx, newSessionInput(userID, sessionID, "hash-user-cascade")); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}

		if _, err := pool.Exec(ctx, `DELETE FROM auth.users WHERE id = $1`, userID); err != nil {
			t.Fatalf("delete user: %v", err)
		}

		var remaining int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM auth.admin_sessions WHERE user_id = $1`, userID).Scan(&remaining); err != nil {
			t.Fatalf("count sessions: %v", err)
		}
		if remaining != 0 {
			t.Fatalf("expected the sessions to cascade away, %d remain", remaining)
		}
	})
}
