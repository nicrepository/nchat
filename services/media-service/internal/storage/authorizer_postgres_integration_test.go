//go:build integration

package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nicrepository/nchat/services/media-service/internal/domain"
	"github.com/nicrepository/nchat/services/media-service/internal/service"
)

const (
	pgUserActive   = "91000000-0000-4000-8000-000000000001"
	pgUserOther    = "91000000-0000-4000-8000-000000000002"
	pgUserInactive = "91000000-0000-4000-8000-000000000003"
	// DisplayNameExpr precedence fixtures (issue #540 follow-up, Code Quality
	// achado 2): three users exercising the three outcomes the expression
	// must produce, proven against a real PostgreSQL COALESCE/NULLIF/BTRIM
	// evaluation rather than the Go-side string constant.
	pgUserNameFromFullName    = "91000000-0000-4000-8000-000000000004"
	pgUserNameFromDisplayName = "91000000-0000-4000-8000-000000000005"
	pgUserNameEmpty           = "91000000-0000-4000-8000-000000000006"

	pgSessionActive           = "92000000-0000-4000-8000-000000000001"
	pgSessionRevoked          = "92000000-0000-4000-8000-000000000002"
	pgSessionIdleExpired      = "92000000-0000-4000-8000-000000000003"
	pgSessionAbsolute         = "92000000-0000-4000-8000-000000000004"
	pgSessionOtherUser        = "92000000-0000-4000-8000-000000000005"
	pgSessionInactiveUser     = "92000000-0000-4000-8000-000000000006"
	pgSessionNameFromFullName = "92000000-0000-4000-8000-000000000007"
	pgSessionNameFromDisplay  = "92000000-0000-4000-8000-000000000008"
	pgSessionNameEmpty        = "92000000-0000-4000-8000-000000000009"
	pgChannelAllowed          = "94000000-0000-4000-8000-000000000001"
	pgChannelNoMembership     = "94000000-0000-4000-8000-000000000002"
	pgChannelCrossWorkspace   = "94000000-0000-4000-8000-000000000003"
	pgChannelInactive         = "94000000-0000-4000-8000-000000000004"
	pgDMAllowed               = "95000000-0000-4000-8000-000000000001"
	pgDMNoParticipation       = "95000000-0000-4000-8000-000000000002"
	pgDMCrossWorkspace        = "95000000-0000-4000-8000-000000000003"
	pgDMDirect                = "95000000-0000-4000-8000-000000000004"
	pgCallActive              = "96000000-0000-4000-8000-000000000001"
	pgCallRinging             = "96000000-0000-4000-8000-000000000002"
	pgCallNonParticipant      = "96000000-0000-4000-8000-000000000003"
	pgMissingResource         = "99000000-0000-4000-8000-000000000001"
)

func TestPGXResourceAuthorizerPostgreSQLPredicates(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("MEDIA_TEST_DATABASE_URL"))
	if dsn == "" {
		t.Fatal("MEDIA_TEST_DATABASE_URL must be set for PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect PostgreSQL test database: %v", err)
	}
	t.Cleanup(func() {
		if err := conn.Close(context.Background()); err != nil {
			t.Errorf("close PostgreSQL test connection: %v", err)
		}
	})

	requireTestDatabase(t, ctx, conn)
	resetAndMigrateMediaSchemas(t, ctx, conn)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = conn.Exec(cleanupCtx, `DROP SCHEMA IF EXISTS chat CASCADE; DROP SCHEMA IF EXISTS auth CASCADE`)
	})
	seedAuthorizerFixtures(t, ctx, conn)

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL test pool: %v", err)
	}
	defer pool.Close()
	authorizer := NewPGXResourceAuthorizer(pool)

	tests := []struct {
		name       string
		kind       domain.ResourceKind
		resourceID string
		userID     string
		sessionID  string
		wantID     string
		// nil means "don't care about display name" for this row — never
		// used for the three precedence rows below, where "" (both empty)
		// is itself the assertion and must not be skipped.
		wantDisplayName *string
		wantErr         error
	}{
		{name: "active session and channel member allowed", kind: domain.ResourceKindChannel, resourceID: pgChannelAllowed, userID: pgUserActive, sessionID: pgSessionActive, wantID: pgChannelAllowed, wantDisplayName: strPtr("Media Active")},
		// DisplayNameExpr precedence (Code Quality achado 2): proven end to
		// end against a real PostgreSQL COALESCE/NULLIF/BTRIM evaluation, not
		// just the Go-side string constant. Uses the DM authorization path
		// (not channel): display name resolution is shared by every resource
		// kind through the same authorizedResourceSelect fragment, and DM
		// authorization needs no dependency beyond dm_members/workspace_members.
		{name: "display name: full_name wins when both are set", kind: domain.ResourceKindDM, resourceID: pgDMAllowed, userID: pgUserNameFromFullName, sessionID: pgSessionNameFromFullName, wantID: pgDMAllowed, wantDisplayName: strPtr("Pedro Completo")},
		{name: "display name: falls back to display_name when full_name is empty", kind: domain.ResourceKindDM, resourceID: pgDMAllowed, userID: pgUserNameFromDisplayName, sessionID: pgSessionNameFromDisplay, wantID: pgDMAllowed, wantDisplayName: strPtr("Pedro")},
		{name: "display name: empty when both full_name and display_name are empty", kind: domain.ResourceKindDM, resourceID: pgDMAllowed, userID: pgUserNameEmpty, sessionID: pgSessionNameEmpty, wantID: pgDMAllowed, wantDisplayName: strPtr("")},
		{name: "active session and DM participant allowed", kind: domain.ResourceKindDM, resourceID: pgDMAllowed, userID: pgUserActive, sessionID: pgSessionActive, wantID: pgDMAllowed},
		{name: "active call participant allowed", kind: domain.ResourceKindCall, resourceID: pgCallActive, userID: pgUserActive, sessionID: pgSessionActive, wantID: pgCallActive},
		{name: "ringing call denied", kind: domain.ResourceKindCall, resourceID: pgCallRinging, userID: pgUserActive, sessionID: pgSessionActive, wantErr: domain.ErrNotFound},
		{name: "active call nonparticipant denied", kind: domain.ResourceKindCall, resourceID: pgCallNonParticipant, userID: pgUserActive, sessionID: pgSessionActive, wantErr: domain.ErrNotFound},
		{name: "revoked session unauthorized", kind: domain.ResourceKindChannel, resourceID: pgChannelAllowed, userID: pgUserActive, sessionID: pgSessionRevoked, wantErr: domain.ErrUnauthorized},
		{name: "idle expired session unauthorized", kind: domain.ResourceKindChannel, resourceID: pgChannelAllowed, userID: pgUserActive, sessionID: pgSessionIdleExpired, wantErr: domain.ErrUnauthorized},
		{name: "absolute expired session unauthorized", kind: domain.ResourceKindChannel, resourceID: pgChannelAllowed, userID: pgUserActive, sessionID: pgSessionAbsolute, wantErr: domain.ErrUnauthorized},
		{name: "SID owned by another user unauthorized", kind: domain.ResourceKindChannel, resourceID: pgChannelAllowed, userID: pgUserActive, sessionID: pgSessionOtherUser, wantErr: domain.ErrUnauthorized},
		{name: "inactive user unauthorized", kind: domain.ResourceKindChannel, resourceID: pgChannelAllowed, userID: pgUserInactive, sessionID: pgSessionInactiveUser, wantErr: domain.ErrUnauthorized},
		{name: "private channel without membership inaccessible", kind: domain.ResourceKindChannel, resourceID: pgChannelNoMembership, userID: pgUserActive, sessionID: pgSessionActive, wantErr: domain.ErrNotFound},
		{name: "cross workspace channel inaccessible", kind: domain.ResourceKindChannel, resourceID: pgChannelCrossWorkspace, userID: pgUserActive, sessionID: pgSessionActive, wantErr: domain.ErrNotFound},
		{name: "inactive channel inaccessible", kind: domain.ResourceKindChannel, resourceID: pgChannelInactive, userID: pgUserActive, sessionID: pgSessionActive, wantErr: domain.ErrNotFound},
		{name: "DM without participation inaccessible", kind: domain.ResourceKindDM, resourceID: pgDMNoParticipation, userID: pgUserActive, sessionID: pgSessionActive, wantErr: domain.ErrNotFound},
		{name: "cross workspace DM inaccessible", kind: domain.ResourceKindDM, resourceID: pgDMCrossWorkspace, userID: pgUserActive, sessionID: pgSessionActive, wantErr: domain.ErrNotFound},
		{name: "direct DM cannot use resource mode even with active participation", kind: domain.ResourceKindDM, resourceID: pgDMDirect, userID: pgUserActive, sessionID: pgSessionActive, wantErr: domain.ErrNotFound},
		{name: "missing resource matches inaccessible result", kind: domain.ResourceKindChannel, resourceID: pgMissingResource, userID: pgUserActive, sessionID: pgSessionActive, wantErr: domain.ErrNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := authorizer.Authorize(context.Background(), service.AuthorizationInput{
				Kind: tt.kind, ResourceID: tt.resourceID,
				UserID: tt.userID, SessionID: tt.sessionID,
			})
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("expected %v, got result=%+v err=%v", tt.wantErr, result, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("authorize: %v", err)
			}
			if result.ID != tt.wantID || result.SessionExpiresAt.IsZero() {
				t.Fatalf("unexpected authorized resource: %+v", result)
			}
			if tt.wantDisplayName != nil && result.DisplayName != *tt.wantDisplayName {
				t.Fatalf("expected display name %q, got %q", *tt.wantDisplayName, result.DisplayName)
			}
		})
	}
}

func strPtr(s string) *string { return &s }

func requireTestDatabase(t *testing.T, ctx context.Context, conn *pgx.Conn) {
	t.Helper()
	var databaseName string
	if err := conn.QueryRow(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
		t.Fatalf("read current database: %v", err)
	}
	if !strings.HasSuffix(databaseName, "_test") {
		t.Fatalf("refusing destructive integration test against non-test database %q", databaseName)
	}
}

func resetAndMigrateMediaSchemas(t *testing.T, ctx context.Context, conn *pgx.Conn) {
	t.Helper()
	if _, err := conn.Exec(ctx, `DROP SCHEMA IF EXISTS chat CASCADE; DROP SCHEMA IF EXISTS auth CASCADE`); err != nil {
		t.Fatalf("reset PostgreSQL test schemas: %v", err)
	}
	for _, migration := range []struct {
		domain string
		name   string
	}{
		{domain: "auth", name: "000001_auth_identity_schema.up.sql"},
		{domain: "chat", name: "000001_chat_domain_schema.up.sql"},
		{domain: "chat", name: "000002_chat_enforce_channel_workspace_isolation.up.sql"},
		{domain: "chat", name: "000003_chat_dm_conversations.up.sql"},
		{domain: "chat", name: "000019_call_lifecycle.up.sql"},
	} {
		if _, err := conn.Exec(ctx, readRepositoryMigration(t, migration.domain, migration.name)); err != nil {
			t.Fatalf("apply %s/%s: %v", migration.domain, migration.name, err)
		}
	}
}

func readRepositoryMigration(t *testing.T, domainName, name string) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve integration test path")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", "..", "..", ".."))
	body, err := os.ReadFile(filepath.Join(repositoryRoot, "migrations", domainName, name))
	if err != nil {
		t.Fatalf("read migration %s/%s: %v", domainName, name, err)
	}
	return string(body)
}

func seedAuthorizerFixtures(t *testing.T, ctx context.Context, conn *pgx.Conn) {
	t.Helper()
	_, err := conn.Exec(ctx, `
		BEGIN;
		INSERT INTO auth.users (id, email, display_name, status) VALUES
			('91000000-0000-4000-8000-000000000001', 'media-active@example.test', 'Media Active', 'active'),
			('91000000-0000-4000-8000-000000000002', 'media-other@example.test', 'Media Other', 'active'),
			('91000000-0000-4000-8000-000000000003', 'media-inactive@example.test', 'Media Inactive', 'suspended');
		INSERT INTO auth.users (id, email, display_name, full_name, status) VALUES
			('91000000-0000-4000-8000-000000000004', 'media-name-full@example.test', 'Pedro', 'Pedro Completo', 'active'),
			('91000000-0000-4000-8000-000000000005', 'media-name-display@example.test', 'Pedro', '', 'active'),
			('91000000-0000-4000-8000-000000000006', 'media-name-empty@example.test', '', '', 'active');
		INSERT INTO auth.user_sessions
			(id, user_id, refresh_token_hash, idle_expires_at, absolute_expires_at, revoked_at)
		VALUES
			('92000000-0000-4000-8000-000000000001', '91000000-0000-4000-8000-000000000001', 'media-session-active', now() + interval '30 minutes', now() + interval '60 minutes', NULL),
			('92000000-0000-4000-8000-000000000002', '91000000-0000-4000-8000-000000000001', 'media-session-revoked', now() + interval '30 minutes', now() + interval '60 minutes', now()),
			('92000000-0000-4000-8000-000000000003', '91000000-0000-4000-8000-000000000001', 'media-session-idle-expired', now() - interval '1 minute', now() + interval '60 minutes', NULL),
			('92000000-0000-4000-8000-000000000004', '91000000-0000-4000-8000-000000000001', 'media-session-absolute-expired', now() + interval '30 minutes', now() - interval '1 minute', NULL),
			('92000000-0000-4000-8000-000000000005', '91000000-0000-4000-8000-000000000002', 'media-session-other-user', now() + interval '30 minutes', now() + interval '60 minutes', NULL),
			('92000000-0000-4000-8000-000000000006', '91000000-0000-4000-8000-000000000003', 'media-session-inactive-user', now() + interval '30 minutes', now() + interval '60 minutes', NULL),
			('92000000-0000-4000-8000-000000000007', '91000000-0000-4000-8000-000000000004', 'media-session-name-full', now() + interval '30 minutes', now() + interval '60 minutes', NULL),
			('92000000-0000-4000-8000-000000000008', '91000000-0000-4000-8000-000000000005', 'media-session-name-display', now() + interval '30 minutes', now() + interval '60 minutes', NULL),
			('92000000-0000-4000-8000-000000000009', '91000000-0000-4000-8000-000000000006', 'media-session-name-empty', now() + interval '30 minutes', now() + interval '60 minutes', NULL);

		INSERT INTO chat.workspaces (id, slug, name, status) VALUES
			('93000000-0000-4000-8000-000000000001', 'media-workspace-a', 'Media Workspace A', 'active'),
			('93000000-0000-4000-8000-000000000002', 'media-workspace-b', 'Media Workspace B', 'active');
		INSERT INTO chat.channels (id, workspace_id, slug, display_name, type, status, is_general) VALUES
			('93000000-0000-4000-8000-000000000011', '93000000-0000-4000-8000-000000000001', 'geral', 'Geral', 'public', 'active', true),
			('93000000-0000-4000-8000-000000000012', '93000000-0000-4000-8000-000000000002', 'geral', 'Geral', 'public', 'active', true),
			('94000000-0000-4000-8000-000000000001', '93000000-0000-4000-8000-000000000001', 'allowed', 'Allowed', 'private', 'active', false),
			('94000000-0000-4000-8000-000000000002', '93000000-0000-4000-8000-000000000001', 'hidden', 'Hidden', 'private', 'active', false),
			('94000000-0000-4000-8000-000000000003', '93000000-0000-4000-8000-000000000002', 'cross', 'Cross', 'public', 'active', false),
			('94000000-0000-4000-8000-000000000004', '93000000-0000-4000-8000-000000000001', 'archived', 'Archived', 'private', 'archived', false);
		INSERT INTO chat.workspace_members (workspace_id, user_id, status) VALUES
			('93000000-0000-4000-8000-000000000001', '91000000-0000-4000-8000-000000000001', 'active'),
			('93000000-0000-4000-8000-000000000001', '91000000-0000-4000-8000-000000000002', 'active'),
			('93000000-0000-4000-8000-000000000001', '91000000-0000-4000-8000-000000000003', 'active'),
			('93000000-0000-4000-8000-000000000001', '91000000-0000-4000-8000-000000000004', 'active'),
			('93000000-0000-4000-8000-000000000001', '91000000-0000-4000-8000-000000000005', 'active'),
			('93000000-0000-4000-8000-000000000001', '91000000-0000-4000-8000-000000000006', 'active'),
			('93000000-0000-4000-8000-000000000002', '91000000-0000-4000-8000-000000000002', 'active');
		INSERT INTO chat.channel_members (channel_id, user_id) VALUES
			('94000000-0000-4000-8000-000000000001', '91000000-0000-4000-8000-000000000001'),
			('94000000-0000-4000-8000-000000000001', '91000000-0000-4000-8000-000000000003'),
			('94000000-0000-4000-8000-000000000004', '91000000-0000-4000-8000-000000000001');

		INSERT INTO chat.dm_conversations (id, workspace_id, type, status, created_by, direct_pair_key) VALUES
			('95000000-0000-4000-8000-000000000001', '93000000-0000-4000-8000-000000000001', 'group', 'active', '91000000-0000-4000-8000-000000000001', NULL),
			('95000000-0000-4000-8000-000000000002', '93000000-0000-4000-8000-000000000001', 'group', 'active', '91000000-0000-4000-8000-000000000002', NULL),
			('95000000-0000-4000-8000-000000000003', '93000000-0000-4000-8000-000000000002', 'group', 'active', '91000000-0000-4000-8000-000000000002', NULL),
			('95000000-0000-4000-8000-000000000004', '93000000-0000-4000-8000-000000000001', 'direct', 'active', '91000000-0000-4000-8000-000000000001', '2:1-user:2:2-user');
		INSERT INTO chat.dm_members (conversation_id, user_id) VALUES
			('95000000-0000-4000-8000-000000000001', '91000000-0000-4000-8000-000000000001'),
			('95000000-0000-4000-8000-000000000001', '91000000-0000-4000-8000-000000000004'),
			('95000000-0000-4000-8000-000000000001', '91000000-0000-4000-8000-000000000005'),
			('95000000-0000-4000-8000-000000000001', '91000000-0000-4000-8000-000000000006'),
			('95000000-0000-4000-8000-000000000002', '91000000-0000-4000-8000-000000000002'),
			('95000000-0000-4000-8000-000000000003', '91000000-0000-4000-8000-000000000001'),
			('95000000-0000-4000-8000-000000000004', '91000000-0000-4000-8000-000000000001');
		INSERT INTO chat.calls (id, workspace_id, request_id, caller_id, callee_id, call_type, status, expires_at) VALUES
			('96000000-0000-4000-8000-000000000001', '93000000-0000-4000-8000-000000000001', '96100000-0000-4000-8000-000000000001', '91000000-0000-4000-8000-000000000001', '91000000-0000-4000-8000-000000000002', 'video', 'active', now() + interval '30 seconds'),
			('96000000-0000-4000-8000-000000000002', '93000000-0000-4000-8000-000000000001', '96100000-0000-4000-8000-000000000002', '91000000-0000-4000-8000-000000000001', '91000000-0000-4000-8000-000000000002', 'audio', 'ringing', now() + interval '30 seconds'),
			('96000000-0000-4000-8000-000000000003', '93000000-0000-4000-8000-000000000001', '96100000-0000-4000-8000-000000000003', '91000000-0000-4000-8000-000000000002', '91000000-0000-4000-8000-000000000003', 'audio', 'active', now() + interval '30 seconds');
		COMMIT;
	`)
	if err != nil {
		t.Fatalf("seed PostgreSQL authorization fixtures: %v", err)
	}
}
