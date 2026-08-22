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
	// Resource (channel/group-DM) call token fixtures (issue #622/#609): a
	// resource call now requires a live chat.call_participant_leases row in
	// addition to membership/visibility. Each scenario needs its own target
	// (chat.calls has at most one active row per workspace+target_type+
	// target_id — calls_one_active_resource_idx), so each lease state below
	// gets its own private channel/group-DM, all with pgUserActive as a
	// member, mirroring pgChannelAllowed/pgDMAllowed exactly.
	pgChannelWithLease    = "94000000-0000-4000-8000-000000000005"
	pgChannelNoLease      = "94000000-0000-4000-8000-000000000006"
	pgChannelExpiredLease = "94000000-0000-4000-8000-000000000007"
	pgChannelOthersLease  = "94000000-0000-4000-8000-000000000008"
	pgDMWithLease         = "95000000-0000-4000-8000-000000000005"
	pgDMNoLease           = "95000000-0000-4000-8000-000000000006"

	pgResourceCallChannelWithLease    = "96000000-0000-4000-8000-000000000004"
	pgResourceCallChannelNoLease      = "96000000-0000-4000-8000-000000000005"
	pgResourceCallChannelExpiredLease = "96000000-0000-4000-8000-000000000006"
	pgResourceCallChannelOthersLease  = "96000000-0000-4000-8000-000000000007"
	pgResourceCallDMWithLease         = "96000000-0000-4000-8000-000000000008"
	pgResourceCallDMNoLease           = "96000000-0000-4000-8000-000000000009"
	pgParticipationChannelCurrent     = "97000000-0000-4000-8000-000000000001"
	pgParticipationChannelStale       = "97000000-0000-4000-8000-000000000002"
	pgParticipationDMCurrent          = "97000000-0000-4000-8000-000000000003"
	pgParticipationExpired            = "97000000-0000-4000-8000-000000000004"
	pgParticipationOtherUser          = "97000000-0000-4000-8000-000000000005"
	pgMissingResource                 = "99000000-0000-4000-8000-000000000001"
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
		name            string
		kind            domain.ResourceKind
		resourceID      string
		userID          string
		sessionID       string
		participationID string
		wantID          string
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
		// issue #622/#609: resource call token authorization now requires a
		// live participant lease, on top of membership/visibility.
		{name: "channel call current fence allowed", kind: domain.ResourceKindCall, resourceID: pgResourceCallChannelWithLease, userID: pgUserActive, sessionID: pgSessionActive, participationID: pgParticipationChannelCurrent, wantID: pgResourceCallChannelWithLease},
		{name: "channel call stale fence denied", kind: domain.ResourceKindCall, resourceID: pgResourceCallChannelWithLease, userID: pgUserActive, sessionID: pgSessionActive, participationID: pgParticipationChannelStale, wantErr: domain.ErrNotFound},
		{name: "channel call missing fence cannot use fenced lease", kind: domain.ResourceKindCall, resourceID: pgResourceCallChannelWithLease, userID: pgUserActive, sessionID: pgSessionActive, wantErr: domain.ErrNotFound},
		{name: "channel call wrong-call fence denied", kind: domain.ResourceKindCall, resourceID: pgResourceCallChannelWithLease, userID: pgUserActive, sessionID: pgSessionActive, participationID: pgParticipationDMCurrent, wantErr: domain.ErrNotFound},
		{name: "channel call + membership + no lease denied", kind: domain.ResourceKindCall, resourceID: pgResourceCallChannelNoLease, userID: pgUserActive, sessionID: pgSessionActive, wantErr: domain.ErrNotFound},
		{name: "channel call + expired current fence denied", kind: domain.ResourceKindCall, resourceID: pgResourceCallChannelExpiredLease, userID: pgUserActive, sessionID: pgSessionActive, participationID: pgParticipationExpired, wantErr: domain.ErrNotFound},
		{name: "channel call + fence belongs to another user denied", kind: domain.ResourceKindCall, resourceID: pgResourceCallChannelOthersLease, userID: pgUserActive, sessionID: pgSessionActive, participationID: pgParticipationOtherUser, wantErr: domain.ErrNotFound},
		{name: "group DM call current fence allowed", kind: domain.ResourceKindCall, resourceID: pgResourceCallDMWithLease, userID: pgUserActive, sessionID: pgSessionActive, participationID: pgParticipationDMCurrent, wantID: pgResourceCallDMWithLease},
		{name: "group DM call stale fence denied", kind: domain.ResourceKindCall, resourceID: pgResourceCallDMWithLease, userID: pgUserActive, sessionID: pgSessionActive, participationID: pgParticipationChannelCurrent, wantErr: domain.ErrNotFound},
		{name: "group DM call + membership + no lease denied", kind: domain.ResourceKindCall, resourceID: pgResourceCallDMNoLease, userID: pgUserActive, sessionID: pgSessionActive, wantErr: domain.ErrNotFound},
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
				ParticipationID: tt.participationID,
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
		// chat.channel_visible_to_user, which every channel/resource-call
		// authorization query in this file depends on, is (re)defined here
		// as a CREATE OR REPLACE — it needs no other migration between this
		// one and 000003 above.
		{domain: "chat", name: "000022_workspace_moderator_and_guest_channel_scope.up.sql"},
		// issue #622/#609: chat.calls.target_type/target_id and
		// chat.call_participant_leases (the resource-call token's lease
		// requirement) were both added here. Without this migration, every
		// resource-call fixture and the authorization query itself
		// (callAuthorizationQuery references c.target_type and
		// chat.call_participant_leases) would fail against a schema that
		// simply does not have those columns/tables yet.
		{domain: "chat", name: "000028_resource_call_lifecycle.up.sql"},
		{domain: "chat", name: "000035_call_participant_lease_identity.up.sql"},
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
			('94000000-0000-4000-8000-000000000004', '93000000-0000-4000-8000-000000000001', 'archived', 'Archived', 'private', 'archived', false),
			-- issue #622/#609: one private channel per resource-call lease
			-- scenario below — each needs its own target (calls_one_active_
			-- resource_idx allows only one active call per target).
			('94000000-0000-4000-8000-000000000005', '93000000-0000-4000-8000-000000000001', 'with-lease', 'With Lease', 'private', 'active', false),
			('94000000-0000-4000-8000-000000000006', '93000000-0000-4000-8000-000000000001', 'no-lease', 'No Lease', 'private', 'active', false),
			('94000000-0000-4000-8000-000000000007', '93000000-0000-4000-8000-000000000001', 'expired-lease', 'Expired Lease', 'private', 'active', false),
			('94000000-0000-4000-8000-000000000008', '93000000-0000-4000-8000-000000000001', 'others-lease', 'Others Lease', 'private', 'active', false);
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
			('94000000-0000-4000-8000-000000000004', '91000000-0000-4000-8000-000000000001'),
			('94000000-0000-4000-8000-000000000005', '91000000-0000-4000-8000-000000000001'),
			('94000000-0000-4000-8000-000000000006', '91000000-0000-4000-8000-000000000001'),
			('94000000-0000-4000-8000-000000000007', '91000000-0000-4000-8000-000000000001'),
			('94000000-0000-4000-8000-000000000008', '91000000-0000-4000-8000-000000000001');

		INSERT INTO chat.dm_conversations (id, workspace_id, type, status, created_by, direct_pair_key) VALUES
			('95000000-0000-4000-8000-000000000001', '93000000-0000-4000-8000-000000000001', 'group', 'active', '91000000-0000-4000-8000-000000000001', NULL),
			('95000000-0000-4000-8000-000000000002', '93000000-0000-4000-8000-000000000001', 'group', 'active', '91000000-0000-4000-8000-000000000002', NULL),
			('95000000-0000-4000-8000-000000000003', '93000000-0000-4000-8000-000000000002', 'group', 'active', '91000000-0000-4000-8000-000000000002', NULL),
			('95000000-0000-4000-8000-000000000004', '93000000-0000-4000-8000-000000000001', 'direct', 'active', '91000000-0000-4000-8000-000000000001', '2:1-user:2:2-user'),
			-- issue #622/#609: one group DM per resource-call lease scenario.
			('95000000-0000-4000-8000-000000000005', '93000000-0000-4000-8000-000000000001', 'group', 'active', '91000000-0000-4000-8000-000000000001', NULL),
			('95000000-0000-4000-8000-000000000006', '93000000-0000-4000-8000-000000000001', 'group', 'active', '91000000-0000-4000-8000-000000000001', NULL);
		INSERT INTO chat.dm_members (conversation_id, user_id) VALUES
			('95000000-0000-4000-8000-000000000001', '91000000-0000-4000-8000-000000000001'),
			('95000000-0000-4000-8000-000000000001', '91000000-0000-4000-8000-000000000004'),
			('95000000-0000-4000-8000-000000000001', '91000000-0000-4000-8000-000000000005'),
			('95000000-0000-4000-8000-000000000001', '91000000-0000-4000-8000-000000000006'),
			('95000000-0000-4000-8000-000000000002', '91000000-0000-4000-8000-000000000002'),
			('95000000-0000-4000-8000-000000000003', '91000000-0000-4000-8000-000000000001'),
			('95000000-0000-4000-8000-000000000004', '91000000-0000-4000-8000-000000000001'),
			('95000000-0000-4000-8000-000000000005', '91000000-0000-4000-8000-000000000001'),
			('95000000-0000-4000-8000-000000000006', '91000000-0000-4000-8000-000000000001');
		INSERT INTO chat.calls (id, workspace_id, request_id, caller_id, callee_id, target_type, target_id, call_type, status, expires_at) VALUES
			('96000000-0000-4000-8000-000000000001', '93000000-0000-4000-8000-000000000001', '96100000-0000-4000-8000-000000000001', '91000000-0000-4000-8000-000000000001', '91000000-0000-4000-8000-000000000002', 'user', '91000000-0000-4000-8000-000000000002', 'video', 'active', now() + interval '30 seconds'),
			('96000000-0000-4000-8000-000000000002', '93000000-0000-4000-8000-000000000001', '96100000-0000-4000-8000-000000000002', '91000000-0000-4000-8000-000000000001', '91000000-0000-4000-8000-000000000002', 'user', '91000000-0000-4000-8000-000000000002', 'audio', 'ringing', now() + interval '30 seconds'),
			('96000000-0000-4000-8000-000000000003', '93000000-0000-4000-8000-000000000001', '96100000-0000-4000-8000-000000000003', '91000000-0000-4000-8000-000000000002', '91000000-0000-4000-8000-000000000003', 'user', '91000000-0000-4000-8000-000000000003', 'audio', 'active', now() + interval '30 seconds');

		-- issue #622/#609: resource (channel/group-DM) call token fixtures —
		-- each call row pairs with the lease state its scenario needs, on
		-- its own dedicated target.
		INSERT INTO chat.calls (id, workspace_id, request_id, caller_id, callee_id, target_type, target_id, call_type, status, expires_at) VALUES
			('96000000-0000-4000-8000-000000000004', '93000000-0000-4000-8000-000000000001', '96100000-0000-4000-8000-000000000004', '91000000-0000-4000-8000-000000000001', NULL, 'channel', '94000000-0000-4000-8000-000000000005', 'audio', 'active', now() + interval '30 seconds'),
			('96000000-0000-4000-8000-000000000005', '93000000-0000-4000-8000-000000000001', '96100000-0000-4000-8000-000000000005', '91000000-0000-4000-8000-000000000001', NULL, 'channel', '94000000-0000-4000-8000-000000000006', 'audio', 'active', now() + interval '30 seconds'),
			('96000000-0000-4000-8000-000000000006', '93000000-0000-4000-8000-000000000001', '96100000-0000-4000-8000-000000000006', '91000000-0000-4000-8000-000000000001', NULL, 'channel', '94000000-0000-4000-8000-000000000007', 'audio', 'active', now() + interval '30 seconds'),
			('96000000-0000-4000-8000-000000000007', '93000000-0000-4000-8000-000000000001', '96100000-0000-4000-8000-000000000007', '91000000-0000-4000-8000-000000000001', NULL, 'channel', '94000000-0000-4000-8000-000000000008', 'audio', 'active', now() + interval '30 seconds'),
			('96000000-0000-4000-8000-000000000008', '93000000-0000-4000-8000-000000000001', '96100000-0000-4000-8000-000000000008', '91000000-0000-4000-8000-000000000001', NULL, 'dm', '95000000-0000-4000-8000-000000000005', 'audio', 'active', now() + interval '30 seconds'),
			('96000000-0000-4000-8000-000000000009', '93000000-0000-4000-8000-000000000001', '96100000-0000-4000-8000-000000000009', '91000000-0000-4000-8000-000000000001', NULL, 'dm', '95000000-0000-4000-8000-000000000006', 'audio', 'active', now() + interval '30 seconds');

		INSERT INTO chat.call_participant_leases (call_id, user_id, expires_at, participation_id) VALUES
			('96000000-0000-4000-8000-000000000004', '91000000-0000-4000-8000-000000000001', now() + interval '30 minutes', '97000000-0000-4000-8000-000000000001'),
			('96000000-0000-4000-8000-000000000006', '91000000-0000-4000-8000-000000000001', now() - interval '1 minute', '97000000-0000-4000-8000-000000000004'),
			('96000000-0000-4000-8000-000000000007', '91000000-0000-4000-8000-000000000002', now() + interval '30 minutes', '97000000-0000-4000-8000-000000000005'),
			('96000000-0000-4000-8000-000000000008', '91000000-0000-4000-8000-000000000001', now() + interval '30 minutes', '97000000-0000-4000-8000-000000000003');
		COMMIT;
	`)
	if err != nil {
		t.Fatalf("seed PostgreSQL authorization fixtures: %v", err)
	}
}
