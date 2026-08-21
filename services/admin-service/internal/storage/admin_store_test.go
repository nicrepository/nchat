package storage_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
	"github.com/nicrepository/nchat/services/admin-service/internal/storage"
)

func newMock(t *testing.T) pgxmock.PgxPoolIface {
	t.Helper()
	mock, err := pgxmock.NewPool(pgxmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	t.Cleanup(mock.Close)
	return mock
}

func principalRows(isAdmin bool, capabilities []string) *pgxmock.Rows {
	return pgxmock.NewRows([]string{"email", "display_name", "avatar_url", "is_admin", "capabilities"}).
		AddRow("admin@example.test", "Admin", "/avatars/a.png", isAdmin, capabilities)
}

func TestAuthorizeHandshake_ReturnsCapabilities(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`FROM active_session AS s`).
		WithArgs("session-1", "user-1").
		WillReturnRows(principalRows(true, []string{"admin.audit.read", "admin.users.read"}))

	principal, err := storage.NewPGXAdminStore(mock).AuthorizeHandshake(context.Background(), "user-1", "session-1")
	if err != nil {
		t.Fatalf("AuthorizeHandshake: %v", err)
	}
	if principal.UserID != "user-1" || principal.Email != "admin@example.test" {
		t.Fatalf("unexpected principal: %+v", principal)
	}
	if !principal.Capabilities.Has(domain.CapabilityAuditRead) || !principal.Capabilities.Has(domain.CapabilityUsersRead) {
		t.Fatalf("expected both grants, got %v", principal.Capabilities.Effective())
	}
	if principal.Capabilities.Has(domain.CapabilityUsersManage) {
		t.Fatal("a grant that was not stored must not be held")
	}
}

// Being logged in to NChat is not administrative authority. The query returns a
// row for any live session; the absence of an admin principal is a 403, not a
// pass.
func TestAuthorizeHandshake_ActiveSessionWithoutPrincipalIsForbidden(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`FROM active_session AS s`).
		WithArgs("session-1", "user-1").
		WillReturnRows(principalRows(false, nil))

	_, err := storage.NewPGXAdminStore(mock).AuthorizeHandshake(context.Background(), "user-1", "session-1")
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

// No row means the login session is revoked, expired, or the user is suspended
// or deleted. All of them are the same answer to the caller.
func TestAuthorizeHandshake_NoActiveSessionIsUnauthorized(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`FROM active_session AS s`).
		WithArgs("session-1", "user-1").
		WillReturnError(pgx.ErrNoRows)

	_, err := storage.NewPGXAdminStore(mock).AuthorizeHandshake(context.Background(), "user-1", "session-1")
	if !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}

func TestAuthorizeHandshake_DatabaseErrorIsNotADenial(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`FROM active_session AS s`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnError(errors.New("connection reset"))

	_, err := storage.NewPGXAdminStore(mock).AuthorizeHandshake(context.Background(), "user-1", "session-1")
	if errors.Is(err, domain.ErrUnauthorized) || errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("an outage must not be reported as an authorization answer, got %v", err)
	}
	if err == nil {
		t.Fatal("expected an error")
	}
}

// The query must keep using the shared cross-service session predicate. If it
// stopped, the console could outlive a revocation the rest of the platform
// already honours.
func TestHandshakeQueryUsesTheSharedActiveSessionContract(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`WITH active_session AS`).
		WithArgs("session-1", "user-1").
		WillReturnRows(principalRows(true, []string{"admin.superuser"}))

	if _, err := storage.NewPGXAdminStore(mock).AuthorizeHandshake(context.Background(), "user-1", "session-1"); err != nil {
		t.Fatalf("AuthorizeHandshake: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestCreateSession(t *testing.T) {
	mock := newMock(t)
	idle := time.Now().UTC().Add(15 * time.Minute)
	absolute := time.Now().UTC().Add(8 * time.Hour)
	mock.ExpectQuery(`INSERT INTO auth\.admin_sessions`).
		WithArgs("user-1", "auth-1", "hash-1", "10.0.0.1", "agent", idle, absolute).
		WillReturnRows(pgxmock.NewRows([]string{"id", "idle_expires_at", "absolute_expires_at"}).
			AddRow("session-1", idle, absolute))

	session, err := storage.NewPGXAdminStore(mock).CreateSession(context.Background(), domain.AdminSessionInput{
		UserID: "user-1", AuthSessionID: "auth-1", SessionHash: "hash-1",
		IPAddress: "10.0.0.1", UserAgent: "agent",
		IdleExpiresAt: idle, AbsoluteExpiresAt: absolute,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if session.ID != "session-1" {
		t.Fatalf("unexpected session: %+v", session)
	}
}

func TestCreateSession_PropagatesFailure(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`INSERT INTO auth\.admin_sessions`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnError(errors.New("constraint violated"))

	if _, err := storage.NewPGXAdminStore(mock).CreateSession(context.Background(), domain.AdminSessionInput{}); err == nil {
		t.Fatal("expected an error")
	}
}

// The lookup and the idle renewal are one statement, and its WHERE clause is
// the enforcement point: a revoked, idle-expired or lifetime-expired session
// matches no row.
func TestTouchSession_RenewsTheIdleWindow(t *testing.T) {
	mock := newMock(t)
	idle := time.Now().UTC().Add(15 * time.Minute)
	absolute := time.Now().UTC().Add(8 * time.Hour)
	mock.ExpectQuery(`UPDATE auth\.admin_sessions AS s`).
		WithArgs("hash-1", float64(900)).
		WillReturnRows(pgxmock.NewRows([]string{"id", "user_id", "auth_session_id", "idle_expires_at", "absolute_expires_at"}).
			AddRow("session-1", "user-1", "auth-1", idle, absolute))

	session, err := storage.NewPGXAdminStore(mock).TouchSession(context.Background(), "hash-1", 15*time.Minute)
	if err != nil {
		t.Fatalf("TouchSession: %v", err)
	}
	if session.UserID != "user-1" || session.AuthSessionID != "auth-1" {
		t.Fatalf("unexpected session: %+v", session)
	}
}

func TestTouchSession_NoRowIsUnauthorized(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`UPDATE auth\.admin_sessions AS s`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnError(pgx.ErrNoRows)

	_, err := storage.NewPGXAdminStore(mock).TouchSession(context.Background(), "hash-1", time.Minute)
	if !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}

func TestTouchSession_DatabaseErrorIsNotADenial(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`UPDATE auth\.admin_sessions AS s`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnError(errors.New("deadlock"))

	_, err := storage.NewPGXAdminStore(mock).TouchSession(context.Background(), "hash-1", time.Minute)
	if errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("an outage must not read as a revoked session, got %v", err)
	}
}

func TestRevokeSession_IsIdempotent(t *testing.T) {
	mock := newMock(t)
	mock.ExpectExec(`UPDATE auth\.admin_sessions`).
		WithArgs("hash-1", "admin_logout").
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	if err := storage.NewPGXAdminStore(mock).RevokeSession(context.Background(), "hash-1", "admin_logout"); err != nil {
		t.Fatalf("revoking an unknown session must not error, got %v", err)
	}
}

func TestRevokeSession_PropagatesFailure(t *testing.T) {
	mock := newMock(t)
	mock.ExpectExec(`UPDATE auth\.admin_sessions`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnError(errors.New("boom"))

	if err := storage.NewPGXAdminStore(mock).RevokeSession(context.Background(), "hash-1", "x"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestAppendAudit_EncodesMetadataAsAJSONObject(t *testing.T) {
	mock := newMock(t)
	mock.ExpectExec(`INSERT INTO auth\.admin_audit_events`).
		WithArgs("user-1", "admin.session.create", "admin.session", "success", "req-1", `{"method":"POST"}`).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	err := storage.NewPGXAdminStore(mock).AppendAudit(context.Background(), domain.AuditEvent{
		ActorUserID: "user-1", Action: domain.AuditActionSessionCreate, Resource: "admin.session",
		Result: domain.AuditResultSuccess, CorrelationID: "req-1",
		Metadata: map[string]string{"method": "POST"},
	})
	if err != nil {
		t.Fatalf("AppendAudit: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestAppendAudit_EmptyMetadataBecomesAnEmptyObject(t *testing.T) {
	mock := newMock(t)
	mock.ExpectExec(`INSERT INTO auth\.admin_audit_events`).
		WithArgs("", "admin.authorization.deny", "", "denied", "", "{}").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	err := storage.NewPGXAdminStore(mock).AppendAudit(context.Background(), domain.AuditEvent{
		Action: domain.AuditActionAuthorizationDeny, Result: domain.AuditResultDenied,
	})
	if err != nil {
		t.Fatalf("AppendAudit: %v", err)
	}
}

func TestAppendAudit_PropagatesFailure(t *testing.T) {
	mock := newMock(t)
	mock.ExpectExec(`INSERT INTO auth\.admin_audit_events`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnError(errors.New("boom"))

	if err := storage.NewPGXAdminStore(mock).AppendAudit(context.Background(), domain.AuditEvent{Action: "x"}); err == nil {
		t.Fatal("expected an error")
	}
}

func TestListAuditEvents(t *testing.T) {
	mock := newMock(t)
	occurred := time.Now().UTC()
	mock.ExpectQuery(`FROM auth\.admin_audit_events AS e`).
		WithArgs(nil, 10).
		WillReturnRows(pgxmock.NewRows([]string{"id", "occurred_at", "actor_user_id", "email", "action", "resource", "result", "correlation_id"}).
			AddRow(int64(2), occurred, "user-1", "admin@example.test", "admin.session.create", "admin.session", "success", "req-2").
			AddRow(int64(1), occurred, "", "", "admin.authorization.deny", "/audit/events", "denied", ""))

	entries, err := storage.NewPGXAdminStore(mock).ListAuditEvents(context.Background(), domain.AuditFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListAuditEvents: %v", err)
	}
	if len(entries) != 2 || entries[0].ID != 2 || entries[0].Result != domain.AuditResultSuccess {
		t.Fatalf("unexpected entries: %+v", entries)
	}
	if entries[1].ActorUserID != "" || entries[1].Result != domain.AuditResultDenied {
		t.Fatalf("unexpected anonymous entry: %+v", entries[1])
	}
}

func TestListAuditEvents_PropagatesQueryFailure(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`FROM auth\.admin_audit_events AS e`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnError(errors.New("boom"))

	if _, err := storage.NewPGXAdminStore(mock).ListAuditEvents(context.Background(), domain.AuditFilter{Limit: 10}); err == nil {
		t.Fatal("expected an error")
	}
}

func TestListAuditEvents_PropagatesScanFailure(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`FROM auth\.admin_audit_events AS e`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("not-an-id"))

	if _, err := storage.NewPGXAdminStore(mock).ListAuditEvents(context.Background(), domain.AuditFilter{Limit: 10}); err == nil {
		t.Fatal("expected a scan error")
	}
}

func TestPing(t *testing.T) {
	mock := newMock(t)
	mock.ExpectPing()

	if err := storage.NewPGXAdminStore(mock).Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

// A store built without a pool must refuse rather than panic: it is the state a
// partially wired pod is in, and every method has to fail closed there.
func TestNilPoolRefusesEveryOperation(t *testing.T) {
	store := storage.NewPGXAdminStore(nil)
	ctx := context.Background()

	if _, err := store.AuthorizeHandshake(ctx, "u", "s"); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("AuthorizeHandshake: expected ErrUnavailable, got %v", err)
	}
	if _, err := store.ReauthorizeSession(ctx, "u", "s"); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("ReauthorizeSession: expected ErrUnavailable, got %v", err)
	}
	if _, err := store.CreateSession(ctx, domain.AdminSessionInput{}); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("CreateSession: expected ErrUnavailable, got %v", err)
	}
	if _, err := store.TouchSession(ctx, "h", time.Minute); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("TouchSession: expected ErrUnavailable, got %v", err)
	}
	if err := store.RevokeSession(ctx, "h", "r"); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("RevokeSession: expected ErrUnavailable, got %v", err)
	}
	if err := store.AppendAudit(ctx, domain.AuditEvent{}); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("AppendAudit: expected ErrUnavailable, got %v", err)
	}
	if _, err := store.ListAuditEvents(ctx, domain.AuditFilter{Limit: 10}); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("ListAuditEvents: expected ErrUnavailable, got %v", err)
	}
	if err := store.Ping(ctx); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("Ping: expected ErrUnavailable, got %v", err)
	}
}

// The per-request check must not carry the chat session's idle window. The
// console never refreshes a chat session, so keeping that clause would evict a
// working administrator on a timer belonging to a tab nobody is using — while
// everything that is a real revocation stays checked.
func TestReauthorizeSession_DropsOnlyTheChatIdleWindow(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`FROM auth\.user_sessions AS s`).
		WithArgs("session-1", "user-1").
		WillReturnRows(principalRows(true, []string{"admin.audit.read"}))

	principal, err := storage.NewPGXAdminStore(mock).ReauthorizeSession(context.Background(), "user-1", "session-1")
	if err != nil {
		t.Fatalf("ReauthorizeSession: %v", err)
	}
	if !principal.Capabilities.Has(domain.CapabilityAuditRead) {
		t.Fatal("expected the stored capability")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestReauthorizeSessionQueryChecksEveryRealRevocation(t *testing.T) {
	required := []string{
		"s.revoked_at IS NULL",
		"s.absolute_expires_at IS NULL OR s.absolute_expires_at > now()",
		"u.status = 'active'",
		"u.deleted_at IS NULL",
		"auth.admin_principals",
		"p.status = 'active'",
	}
	query := storage.ReauthorizeQueryForTest
	for _, fragment := range required {
		if !strings.Contains(query, fragment) {
			t.Errorf("the per-request check is missing %q", fragment)
		}
	}
	if strings.Contains(query, "s.idle_expires_at") {
		t.Error("the per-request check must not depend on the chat session's idle window")
	}
}

func TestReauthorizeSession_NoRowIsUnauthorized(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`FROM auth\.user_sessions AS s`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(pgx.ErrNoRows)

	if _, err := storage.NewPGXAdminStore(mock).ReauthorizeSession(context.Background(), "user-1", "session-1"); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}

// The Go API names the actor first and the SQL binds the session first. That
// swap is deliberate and happens in exactly one place; this pins it.
//
// It would not fail loudly if it were wrong — both values are UUIDs, so a
// swapped bind matches no row and every administrator is quietly refused — so
// the assertion is on the bind positions themselves.
func TestAuthorizeHandshake_BindsSessionThenUser(t *testing.T) {
	const (
		userID    = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		sessionID = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	)
	mock := newMock(t)
	mock.ExpectQuery(`WITH active_session AS`).
		WithArgs(sessionID, userID).
		WillReturnRows(principalRows(true, []string{"admin.audit.read"}))

	if _, err := storage.NewPGXAdminStore(mock).AuthorizeHandshake(context.Background(), userID, sessionID); err != nil {
		t.Fatalf("AuthorizeHandshake: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestReauthorizeSession_BindsSessionThenUser(t *testing.T) {
	const (
		userID    = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		sessionID = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	)
	mock := newMock(t)
	mock.ExpectQuery(`FROM auth\.user_sessions AS s`).
		WithArgs(sessionID, userID).
		WillReturnRows(principalRows(true, []string{"admin.audit.read"}))

	if _, err := storage.NewPGXAdminStore(mock).ReauthorizeSession(context.Background(), userID, sessionID); err != nil {
		t.Fatalf("ReauthorizeSession: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// A caller that passed the arguments the other way round must be visible as a
// bind mismatch, not silently accepted. This is the negative half of the two
// tests above: it proves they would actually catch a swap.
func TestAuthorizeHandshake_SwappedArgumentsDoNotMatchTheExpectedBinds(t *testing.T) {
	const (
		userID    = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		sessionID = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	)
	mock := newMock(t)
	mock.ExpectQuery(`WITH active_session AS`).
		WithArgs(sessionID, userID).
		WillReturnRows(principalRows(true, nil))

	// Deliberately swapped at the call site.
	if _, err := storage.NewPGXAdminStore(mock).AuthorizeHandshake(context.Background(), sessionID, userID); err == nil {
		t.Fatal("a swapped call must not satisfy the expected bind order")
	}
}

// The handshake keeps the full platform session predicate, including the idle
// window, and the per-request check deliberately drops only that one clause.
// If the two ever converged, either a stale token would buy a privileged
// session or a working administrator would be evicted by the chat's timer.
func TestHandshakeAndReauthorizationQueriesDoNotConverge(t *testing.T) {
	handshake := storage.HandshakeQueryForTest
	reauthorize := storage.ReauthorizeQueryForTest

	if handshake == reauthorize {
		t.Fatal("the two authorization queries must stay distinct")
	}
	if !strings.Contains(handshake, "s.idle_expires_at > now()") {
		t.Error("the handshake must keep the platform idle-window check")
	}
	if strings.Contains(reauthorize, "idle_expires_at") {
		t.Error("the per-request check must not depend on the chat session's idle window")
	}
	// Everything that is a real revocation is checked by both.
	for _, fragment := range []string{
		"s.revoked_at IS NULL",
		"u.status = 'active'",
		"u.deleted_at IS NULL",
		"s.id = $1",
		"s.user_id = $2",
	} {
		if !strings.Contains(handshake, fragment) {
			t.Errorf("handshake query is missing %q", fragment)
		}
		if !strings.Contains(reauthorize, fragment) {
			t.Errorf("reauthorization query is missing %q", fragment)
		}
	}
}
