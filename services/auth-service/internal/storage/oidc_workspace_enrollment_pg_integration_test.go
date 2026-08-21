package storage_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/storage"
)

// A user provisioned by the first SSO login must come out of it able to use
// the chat: without a chat.workspace_members row every chat endpoint answers
// 403, which is what issue #604 reported. Only the database can show this —
// the workspace is resolved by the seeded slug, and the enrollment shares the
// provisioning transaction.
func newOIDCEnrollmentStore(t *testing.T) (*storage.PGXOIDCStore, *pgxpool.Pool) {
	t.Helper()
	pool := connectAuthTestDB(t)
	applyAuthMigrations(t, pool)
	applyChatMigrations(t, pool)
	return storage.NewPGXOIDCStore(pool), pool
}

func oidcEnrollmentInput(subject, email string) domain.OIDCSessionInput {
	return oidcConcurrentInput(subject, email)
}

type membershipRow struct {
	workspaceID string
	role        string
	status      string
	joinedAt    time.Time
}

func readMemberships(t *testing.T, pool *pgxpool.Pool, userID string) []membershipRow {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT workspace_id::text, role, status, joined_at
		FROM chat.workspace_members
		WHERE user_id::text = $1
		ORDER BY workspace_id`, userID)
	if err != nil {
		t.Fatalf("read memberships: %v", err)
	}
	defer rows.Close()
	var out []membershipRow
	for rows.Next() {
		var m membershipRow
		if err := rows.Scan(&m.workspaceID, &m.role, &m.status, &m.joinedAt); err != nil {
			t.Fatalf("scan membership: %v", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate memberships: %v", err)
	}
	return out
}

func defaultWorkspaceID(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(), `
		SELECT id::text FROM chat.workspaces WHERE slug = 'default' AND status = 'active'`,
	).Scan(&id); err != nil {
		t.Fatalf("read default workspace: %v", err)
	}
	return id
}

func TestPGXOIDCStore_FirstLoginEnrollsUserAsMemberOfDefaultWorkspace(t *testing.T) {
	store, pool := newOIDCEnrollmentStore(t)

	created, err := store.CreateOIDCSessionAndExchange(context.Background(),
		oidcEnrollmentInput("enroll-subject", "enroll@example.test"),
		oidcConcurrentExchangeBuilder())
	if err != nil {
		t.Fatalf("first login: %v", err)
	}

	memberships := readMemberships(t, pool, created.User.ID)
	if len(memberships) != 1 {
		t.Fatalf("want exactly one membership, got %d: %+v", len(memberships), memberships)
	}
	got := memberships[0]
	if got.workspaceID != defaultWorkspaceID(t, pool) {
		t.Fatalf("membership points at workspace %q, want the active default workspace", got.workspaceID)
	}
	if got.role != "member" || got.status != "active" {
		t.Fatalf("want role=member status=active, got role=%q status=%q", got.role, got.status)
	}
}

// The second login of the same identity is an ordinary sign-in, not a
// provisioning event: it must leave the membership exactly as it found it.
func TestPGXOIDCStore_SecondLoginDoesNotTouchMembership(t *testing.T) {
	store, pool := newOIDCEnrollmentStore(t)
	ctx := context.Background()

	first, err := store.CreateOIDCSessionAndExchange(ctx,
		oidcEnrollmentInput("repeat-subject", "repeat@example.test"),
		oidcConcurrentExchangeBuilder())
	if err != nil {
		t.Fatalf("first login: %v", err)
	}
	before := readMemberships(t, pool, first.User.ID)

	second, err := store.CreateOIDCSessionAndExchange(ctx,
		oidcEnrollmentInput("repeat-subject", "repeat@example.test"),
		oidcConcurrentExchangeBuilder())
	if err != nil {
		t.Fatalf("second login: %v", err)
	}
	if second.User.ID != first.User.ID {
		t.Fatalf("second login resolved to a different user: %q vs %q", second.User.ID, first.User.ID)
	}

	after := readMemberships(t, pool, first.User.ID)
	if len(after) != 1 {
		t.Fatalf("want exactly one membership after the second login, got %d: %+v", len(after), after)
	}
	if after[0] != before[0] {
		t.Fatalf("membership changed on the second login: %+v -> %+v", before[0], after[0])
	}
}

// RBAC is administered inside NChat: a role granted after provisioning must
// survive every later SSO login, and a membership an administrator suspended
// or that the user left must not be silently reactivated by signing in again.
func TestPGXOIDCStore_ExistingMembershipIsNeverRewrittenByLogin(t *testing.T) {
	for _, tc := range []struct {
		name   string
		role   string
		status string
	}{
		{"owner stays owner", "owner", "active"},
		{"admin stays admin", "admin", "active"},
		{"moderator stays moderator", "moderator", "active"},
		{"member stays member", "member", "active"},
		{"suspended is not reactivated", "member", "suspended"},
		{"left is not reactivated", "member", "left"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, pool := newOIDCEnrollmentStore(t)
			ctx := context.Background()
			subject := "rbac-" + uuid.NewString()
			email := subject + "@example.test"

			created, err := store.CreateOIDCSessionAndExchange(ctx,
				oidcEnrollmentInput(subject, email), oidcConcurrentExchangeBuilder())
			if err != nil {
				t.Fatalf("first login: %v", err)
			}
			if _, err := pool.Exec(ctx, `
				UPDATE chat.workspace_members SET role = $2, status = $3 WHERE user_id::text = $1`,
				created.User.ID, tc.role, tc.status); err != nil {
				t.Fatalf("set membership state: %v", err)
			}

			if _, err := store.CreateOIDCSessionAndExchange(ctx,
				oidcEnrollmentInput(subject, email), oidcConcurrentExchangeBuilder()); err != nil {
				t.Fatalf("later login: %v", err)
			}

			after := readMemberships(t, pool, created.User.ID)
			if len(after) != 1 {
				t.Fatalf("want exactly one membership, got %d: %+v", len(after), after)
			}
			if after[0].role != tc.role || after[0].status != tc.status {
				t.Fatalf("membership was rewritten: want role=%q status=%q, got role=%q status=%q",
					tc.role, tc.status, after[0].role, after[0].status)
			}
		})
	}
}

// A membership an administrator deleted outright must stay deleted: the login
// of an existing user is not a repair mechanism.
func TestPGXOIDCStore_RemovedMembershipIsNotRecreated(t *testing.T) {
	store, pool := newOIDCEnrollmentStore(t)
	ctx := context.Background()

	created, err := store.CreateOIDCSessionAndExchange(ctx,
		oidcEnrollmentInput("removed-subject", "removed@example.test"),
		oidcConcurrentExchangeBuilder())
	if err != nil {
		t.Fatalf("first login: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM chat.workspace_members WHERE user_id::text = $1`, created.User.ID); err != nil {
		t.Fatalf("delete membership: %v", err)
	}

	if _, err := store.CreateOIDCSessionAndExchange(ctx,
		oidcEnrollmentInput("removed-subject", "removed@example.test"),
		oidcConcurrentExchangeBuilder()); err != nil {
		t.Fatalf("later login: %v", err)
	}

	if after := readMemberships(t, pool, created.User.ID); len(after) != 0 {
		t.Fatalf("login recreated a deliberately removed membership: %+v", after)
	}
}

// Failing closed is the point: with no active default workspace the login must
// leave nothing behind — no account, no session, no exchange code — rather
// than hand out tokens for a user who cannot open a channel.
func TestPGXOIDCStore_NoActiveDefaultWorkspaceRollsBackTheWholeLogin(t *testing.T) {
	store, pool := newOIDCEnrollmentStore(t)
	ctx := context.Background()

	if _, err := pool.Exec(ctx, `UPDATE chat.workspaces SET status = 'disabled' WHERE slug = 'default'`); err != nil {
		t.Fatalf("disable default workspace: %v", err)
	}

	_, err := store.CreateOIDCSessionAndExchange(ctx,
		oidcEnrollmentInput("orphan-subject", "orphan@example.test"),
		oidcConcurrentExchangeBuilder())
	if err == nil {
		t.Fatal("expected the login to fail without an active default workspace")
	}

	for _, check := range []struct {
		name string
		sql  string
	}{
		{"auth.users", `SELECT count(*) FROM auth.users WHERE external_subject = 'orphan-subject'`},
		{"auth.user_sessions", `SELECT count(*) FROM auth.user_sessions`},
		{"auth.oidc_exchange_codes", `SELECT count(*) FROM auth.oidc_exchange_codes`},
		{"chat.workspace_members", `SELECT count(*) FROM chat.workspace_members`},
	} {
		var n int
		if err := pool.QueryRow(ctx, check.sql).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", check.name, err)
		}
		if n != 0 {
			t.Fatalf("%s holds %d rows after the failed login, want 0", check.name, n)
		}
	}
}
