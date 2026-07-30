//go:build integration

package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/storage"
)

// Workspace resolution against real membership rows. The predicate that decides
// which tenant a request acts on lives in SQL, so this is where it is checked.
// Shared harness helpers live in invite_migration_postgres_test.go.

// ── Workspace resolution against real SQL ──────────────────────────────────

func seedAdminMembership(t *testing.T, ctx context.Context, conn *pgx.Conn, userID, workspaceID, role, status string) {
	t.Helper()
	if _, err := conn.Exec(ctx, `
		INSERT INTO chat.workspace_members (workspace_id, user_id, role, status)
		VALUES ($1::uuid, $2::uuid, $3, $4)
		ON CONFLICT (workspace_id, user_id) DO UPDATE SET role = EXCLUDED.role, status = EXCLUDED.status`,
		workspaceID, userID, role, status,
	); err != nil {
		t.Fatalf("seed membership: %v", err)
	}
}

func seedUser(t *testing.T, ctx context.Context, conn *pgx.Conn, email string) string {
	t.Helper()
	var id string
	if err := conn.QueryRow(ctx, `
		INSERT INTO auth.users (email, display_name, status) VALUES ($1, 'Test', 'active')
		RETURNING id::text`, email).Scan(&id); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return id
}

func TestResolveAdminWorkspaceID_AgainstRealMemberships(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const inactiveWorkspace = "a0000000-0000-4000-8000-000000000003"
	conn := migratedDatabase(t, ctx)
	seedWorkspace(t, ctx, conn, inactiveWorkspace, "ws-disabled", "disabled")

	noneUser := seedUser(t, ctx, conn, "none@example.test")
	memberUser := seedUser(t, ctx, conn, "member-only@example.test")
	seedAdminMembership(t, ctx, conn, memberUser, pgWorkspaceA, "member", "active")
	singleUser := seedUser(t, ctx, conn, "single@example.test")
	seedAdminMembership(t, ctx, conn, singleUser, pgWorkspaceA, "admin", "active")
	doubleUser := seedUser(t, ctx, conn, "double@example.test")
	seedAdminMembership(t, ctx, conn, doubleUser, pgWorkspaceA, "owner", "active")
	seedAdminMembership(t, ctx, conn, doubleUser, pgWorkspaceB, "admin", "active")
	inactiveUser := seedUser(t, ctx, conn, "inactive-ws@example.test")
	seedAdminMembership(t, ctx, conn, inactiveUser, inactiveWorkspace, "owner", "active")
	suspendedUser := seedUser(t, ctx, conn, "suspended@example.test")
	seedAdminMembership(t, ctx, conn, suspendedUser, pgWorkspaceA, "admin", "suspended")

	store := storage.NewPGXUserStore(testPool(t, ctx))

	for _, tt := range []struct {
		name     string
		userID   string
		selector string
		want     string
		wantErr  error
	}{
		{name: "no membership", userID: noneUser, wantErr: domain.ErrForbidden},
		{name: "member only", userID: memberUser, wantErr: domain.ErrForbidden},
		{name: "inactive workspace", userID: inactiveUser, wantErr: domain.ErrForbidden},
		{name: "suspended membership", userID: suspendedUser, wantErr: domain.ErrForbidden},
		{name: "single admin workspace", userID: singleUser, want: pgWorkspaceA},
		{name: "two admin workspaces", userID: doubleUser, wantErr: domain.ErrWorkspaceSelectionRequired},
		{name: "selector A", userID: doubleUser, selector: pgWorkspaceA, want: pgWorkspaceA},
		{name: "selector B", userID: doubleUser, selector: pgWorkspaceB, want: pgWorkspaceB},
		{name: "selector where only a member", userID: memberUser, selector: pgWorkspaceA, wantErr: domain.ErrForbidden},
		{name: "selector for an unknown workspace", userID: doubleUser, selector: pgWorkspaceAbsent, wantErr: domain.ErrForbidden},
		{name: "selector for an inactive workspace", userID: inactiveUser, selector: inactiveWorkspace, wantErr: domain.ErrForbidden},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := store.ResolveAdminWorkspaceID(ctx, tt.userID, tt.selector)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("expected %v, got %v", tt.wantErr, err)
				}
				if got != "" {
					t.Fatalf("a refused resolution must yield no workspace, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveAdminWorkspaceID: %v", err)
			}
			if got != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, got)
			}
		})
	}
}
