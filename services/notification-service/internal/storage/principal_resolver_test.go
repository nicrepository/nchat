package storage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/notification-service/internal/domain"
	"github.com/nicrepository/nchat/services/notification-service/internal/storage"
)

// Issue #745: the resolver turns a token's claims into a server-derived actor
// and workspace, and never the other way round.
//
// The three outcomes have to stay distinguishable. Collapsing them is how a
// service ends up answering 401 for a member of no workspace, or — much worse —
// treating a database that could not answer as permission.

func principalRows(userID, workspaceID any) *pgxmock.Rows {
	return pgxmock.NewRows([]string{"user_id", "workspace_id"}).AddRow(userID, workspaceID)
}

func TestResolveReturnsTheSessionsOwnUserAndWorkspace(t *testing.T) {
	mock := newPushMock(t)
	mock.ExpectQuery(`FROM auth\.user_sessions`).
		WithArgs("session-1", "claimed-user").
		WillReturnRows(principalRows("resolved-user", testWorkspace))

	principal, err := storage.NewPGXPrincipalResolver(mock).
		Resolve(context.Background(), "claimed-user", "session-1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// The user comes back from the session row, not from the argument. That is
	// what makes the token's subject an input to the check rather than its
	// conclusion.
	if principal.UserID != "resolved-user" || principal.WorkspaceID != testWorkspace {
		t.Fatalf("Resolve = %+v", principal)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestResolveReportsAnInactiveSessionAsUnauthenticated(t *testing.T) {
	mock := newPushMock(t)
	mock.ExpectQuery(`FROM auth\.user_sessions`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(principalRows(nil, nil))

	_, err := storage.NewPGXPrincipalResolver(mock).
		Resolve(context.Background(), "user-1", "session-1")
	if !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("Resolve = %v, want ErrUnauthenticated", err)
	}
}

// A live session whose owner is not an active member of the workspace is
// authenticated and unauthorised, and those are different answers.
func TestResolveReportsAMissingMembershipAsForbidden(t *testing.T) {
	mock := newPushMock(t)
	mock.ExpectQuery(`FROM auth\.user_sessions`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(principalRows("resolved-user", nil))

	_, err := storage.NewPGXPrincipalResolver(mock).
		Resolve(context.Background(), "user-1", "session-1")
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("Resolve = %v, want ErrForbidden", err)
	}
}

func TestResolveReportsNoRowsAsUnauthenticated(t *testing.T) {
	mock := newPushMock(t)
	mock.ExpectQuery(`FROM auth\.user_sessions`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(pgx.ErrNoRows)

	_, err := storage.NewPGXPrincipalResolver(mock).
		Resolve(context.Background(), "user-1", "session-1")
	if !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("Resolve = %v, want ErrUnauthenticated", err)
	}
}

// The failure that matters most: a dependency that cannot answer must not be
// read as either identity or permission. It has to surface as itself so the
// handler answers 500 rather than proceeding with an empty principal.
func TestResolveDoesNotTurnADatabaseFailureIntoADecision(t *testing.T) {
	mock := newPushMock(t)
	mock.ExpectQuery(`FROM auth\.user_sessions`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("connection refused"))

	_, err := storage.NewPGXPrincipalResolver(mock).
		Resolve(context.Background(), "user-1", "session-1")
	if err == nil ||
		errors.Is(err, domain.ErrUnauthenticated) || errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("Resolve = %v, want a plain failure", err)
	}
}

// The query has to keep asking the questions that make a token insufficient on
// its own. A projection that stopped joining the session, or stopped requiring
// an active membership in an active workspace, would still compile and still
// return a principal.
func TestPrincipalQueryKeepsItsGuards(t *testing.T) {
	mock := newPushMock(t)
	for _, fragment := range []string{
		`WITH active_session AS`,
		`s\.revoked_at IS NULL`,
		`chat\.workspace_members`,
		`wm\.user_id = active\.user_id`,
		`wm\.status = 'active'`,
		`w\.slug = 'default'`,
	} {
		mock.ExpectQuery(fragment).
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(principalRows("resolved-user", testWorkspace))
		if _, err := storage.NewPGXPrincipalResolver(mock).
			Resolve(context.Background(), "user-1", "session-1"); err != nil {
			t.Fatalf("query is missing %q: %v", fragment, err)
		}
	}
}
