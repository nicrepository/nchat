package storage

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/nicrepository/nchat/libs/go/platform/authsession"
	"github.com/nicrepository/nchat/services/media-service/internal/domain"
	"github.com/nicrepository/nchat/services/media-service/internal/service"
)

type PGXResourceAuthorizer struct {
	pool Pool
}

func NewPGXResourceAuthorizer(pool Pool) *PGXResourceAuthorizer {
	return &PGXResourceAuthorizer{pool: pool}
}

func (a *PGXResourceAuthorizer) Authorize(ctx context.Context, input service.AuthorizationInput) (service.AuthorizedResource, error) {
	if a == nil || a.pool == nil {
		return service.AuthorizedResource{}, domain.ErrUnavailable
	}
	var query string
	switch input.Kind {
	case domain.ResourceKindChannel:
		query = channelAuthorizationQuery
	case domain.ResourceKindDM:
		query = dmAuthorizationQuery
	case domain.ResourceKindCall:
		query = callAuthorizationQuery
	default:
		return service.AuthorizedResource{}, domain.ErrInvalidInput
	}

	var sessionExpiresAt pgtype.Timestamptz
	var resourceID pgtype.Text
	if err := a.pool.QueryRow(ctx, query, input.SessionID, input.UserID, input.ResourceID).
		Scan(&sessionExpiresAt, &resourceID); err != nil {
		return service.AuthorizedResource{}, fmt.Errorf("authorize media resource: %w", err)
	}
	if !sessionExpiresAt.Valid {
		return service.AuthorizedResource{}, domain.ErrUnauthorized
	}
	if !resourceID.Valid {
		return service.AuthorizedResource{}, domain.ErrNotFound
	}
	return service.AuthorizedResource{
		ID:               resourceID.String,
		SessionExpiresAt: sessionExpiresAt.Time.UTC(),
	}, nil
}

const channelAuthorizationQuery = authsession.ActiveSessionCTE + `,
	authorized_resource AS (
		SELECT c.id
		FROM active_session AS active
		JOIN chat.channels AS c ON c.id = $3
		JOIN chat.workspaces AS w
		  ON w.id = c.workspace_id AND w.status = 'active'
		JOIN chat.workspace_members AS wm
		  ON wm.workspace_id = c.workspace_id
		 AND wm.user_id = active.user_id
		 AND wm.status = 'active'
		WHERE c.status = 'active'
		  AND chat.channel_visible_to_user(c.id, active.user_id)
	)
	SELECT
		(SELECT session_expires_at FROM active_session) AS session_expires_at,
		(SELECT id::text FROM authorized_resource) AS resource_id`

const dmAuthorizationQuery = authsession.ActiveSessionCTE + `,
	authorized_resource AS (
		SELECT dc.id
		FROM active_session AS active
		JOIN chat.dm_conversations AS dc ON dc.id = $3
		JOIN chat.workspaces AS w
		  ON w.id = dc.workspace_id AND w.status = 'active'
		JOIN chat.workspace_members AS wm
		  ON wm.workspace_id = dc.workspace_id
		 AND wm.user_id = active.user_id
		 AND wm.status = 'active'
		JOIN chat.dm_members AS dm
		  ON dm.conversation_id = dc.id
		 AND dm.user_id = active.user_id
		 AND dm.status = 'active'
		WHERE dc.status = 'active'
		  AND dc.type = 'group'
	)
	SELECT
		(SELECT session_expires_at FROM active_session) AS session_expires_at,
		(SELECT id::text FROM authorized_resource) AS resource_id`

const callAuthorizationQuery = authsession.ActiveSessionCTE + `,
	authorized_resource AS (
		SELECT c.id
		FROM active_session AS active
		JOIN chat.calls AS c ON c.id = $3
		JOIN chat.workspaces AS w
		  ON w.id = c.workspace_id AND w.status = 'active'
		JOIN chat.workspace_members AS wm
		  ON wm.workspace_id = c.workspace_id
		 AND wm.user_id = active.user_id
		 AND wm.status = 'active'
		WHERE c.status = 'active'
		  AND (c.caller_id = active.user_id OR c.callee_id = active.user_id)
	)
	SELECT
		(SELECT session_expires_at FROM active_session) AS session_expires_at,
		(SELECT id::text FROM authorized_resource) AS resource_id`
