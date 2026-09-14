package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nicrepository/nchat/libs/go/platform/authsession"
	"github.com/nicrepository/nchat/services/notification-service/internal/domain"
)

// principalQuery answers both authorisation questions in one statement: is this
// session live, and which workspace is this caller an active member of.
//
// Two scalar subqueries rather than a join, for the same reason media-service's
// authorizer uses that shape: a single row always comes back, and the two NULLs
// are distinguishable. A join would collapse "no session" and "session but no
// membership" into zero rows, and those are a 401 and a 403.
//
// The workspace is the canonical one the rest of NChat resolves — slug 'default'
// and status 'active', as in search-service and auth-service's enrolment — and
// it is reached only *through* active_session, so the user whose membership is
// checked is the user the session names and never one a request mentioned.
const principalQuery = authsession.ActiveSessionCTE + `
	SELECT
		(SELECT user_id::text FROM active_session) AS user_id,
		(SELECT w.id::text
		   FROM active_session AS active
		   JOIN chat.workspaces AS w
		     ON w.slug = 'default' AND w.status = 'active'
		   JOIN chat.workspace_members AS wm
		     ON wm.workspace_id = w.id
		    AND wm.user_id = active.user_id
		    AND wm.status = 'active'
		  LIMIT 1) AS workspace_id`

// PGXPrincipalResolver resolves the authenticated actor and their workspace.
type PGXPrincipalResolver struct {
	pool Pool
}

// NewPGXPrincipalResolver creates a resolver backed by the given pool.
func NewPGXPrincipalResolver(pool Pool) *PGXPrincipalResolver {
	return &PGXPrincipalResolver{pool: pool}
}

// Resolve returns the principal for a validated access token's (userID,
// sessionID) pair.
//
// The token claims are an input to this query, never its conclusion: the session
// row has to exist, be unrevoked and unexpired, and belong to an active user, or
// the caller is unauthenticated no matter what the token said. A database
// failure is neither of those and is returned as itself — a dependency that
// cannot answer must never be read as permission.
func (r *PGXPrincipalResolver) Resolve(ctx context.Context, userID, sessionID string) (domain.Principal, error) {
	var resolvedUser, workspace pgtype.Text
	err := r.pool.QueryRow(ctx, principalQuery, sessionID, userID).Scan(&resolvedUser, &workspace)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Principal{}, domain.ErrUnauthenticated
		}
		return domain.Principal{}, fmt.Errorf("resolve push principal: %w", err)
	}
	if !resolvedUser.Valid {
		return domain.Principal{}, domain.ErrUnauthenticated
	}
	if !workspace.Valid {
		return domain.Principal{}, domain.ErrForbidden
	}
	return domain.Principal{UserID: resolvedUser.String, WorkspaceID: workspace.String}, nil
}
