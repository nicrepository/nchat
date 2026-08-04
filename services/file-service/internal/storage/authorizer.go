package storage

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/nicrepository/nchat/libs/go/platform/authsession"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
)

// PGXDestinationAuthorizer answers "may this session attach a file here, and
// which workspace does 'here' belong to" in a single round trip.
//
// The chat schema lives in the same database, so this reads it directly with a
// tightly scoped query rather than adding a network hop to chat-service. That
// is the pattern media-service already established for the same question
// (services/media-service/internal/storage/authorizer.go); introducing a
// distributed call here would be a new coupling with no precedent.
type PGXDestinationAuthorizer struct {
	pool Pool
}

func NewPGXDestinationAuthorizer(pool Pool) *PGXDestinationAuthorizer {
	return &PGXDestinationAuthorizer{pool: pool}
}

// AuthorizeDestination validates the session and the destination together and
// returns the destination's canonical workspace. The workspace is never taken
// from the request: it is read from the destination row.
//
// Absent and inaccessible are the same answer (domain.ErrNotFound) so the
// endpoint cannot be used to probe which channels or conversations exist.
func (a *PGXDestinationAuthorizer) AuthorizeDestination(
	ctx context.Context, input service.DestinationAuthInput,
) (service.AuthorizedDestination, error) {
	if a == nil || a.pool == nil {
		return service.AuthorizedDestination{}, domain.ErrDependenciesUnavailable
	}

	var query string
	switch input.Destination.Kind {
	case domain.DestinationKindChannel:
		query = channelDestinationQuery
	case domain.DestinationKindDM:
		query = dmDestinationQuery
	default:
		return service.AuthorizedDestination{}, fmt.Errorf("%w: invalid destination kind", domain.ErrInvalidInput)
	}

	var sessionExpiresAt pgtype.Timestamptz
	var destinationID, workspaceID pgtype.Text
	var maxUploadBytes pgtype.Int8
	err := a.pool.QueryRow(ctx, query, input.SessionID, input.UserID, input.Destination.ID).
		Scan(&sessionExpiresAt, &destinationID, &workspaceID, &maxUploadBytes)
	if err != nil {
		return service.AuthorizedDestination{}, fmt.Errorf("authorize attachment destination: %w", err)
	}
	if !sessionExpiresAt.Valid {
		return service.AuthorizedDestination{}, domain.ErrUnauthorized
	}
	if !destinationID.Valid || !workspaceID.Valid {
		return service.AuthorizedDestination{}, domain.ErrNotFound
	}
	// An invalid value is left as zero rather than substituted here; the service
	// resolves it through uploadpolicy.Effective, which answers the default and
	// never "no limit".
	return service.AuthorizedDestination{
		ID:               destinationID.String,
		WorkspaceID:      workspaceID.String,
		MaxUploadBytes:   maxUploadBytes.Int64,
		SessionExpiresAt: sessionExpiresAt.Time.UTC(),
	}, nil
}

// channelDestinationQuery mirrors the policy chat-service applies when creating
// a channel message (services/chat-service/internal/storage/message_store.go):
// active workspace, active workspace membership, active channel in that
// workspace, and either a public channel or an explicit channel membership.
// Attaching a file to a channel is a write to that channel, so it is gated by
// the same rule rather than by a second, divergent one.
//
// A stale chat.channel_members row cannot rescue a suspended or departed
// workspace member: the workspace_members join filters status independently.
// The channel's own workspace_id is what the caller gets back, so a channel
// from another workspace can never be attributed to the caller's.
const channelDestinationQuery = authsession.ActiveSessionCTE + `,
	authorized_destination AS (
		SELECT c.id, c.workspace_id, w.max_upload_bytes
		FROM active_session AS active
		JOIN chat.channels AS c ON c.id = $3
		JOIN chat.workspaces AS w
		  ON w.id = c.workspace_id AND w.status = 'active'
		JOIN chat.workspace_members AS wm
		  ON wm.workspace_id = c.workspace_id
		 AND wm.user_id = active.user_id
		 AND wm.status = 'active'
		LEFT JOIN chat.channel_members AS cm
		  ON cm.channel_id = c.id AND cm.user_id = active.user_id
		WHERE c.status = 'active'
		  AND (c.type = 'public' OR cm.user_id IS NOT NULL)
	)
	SELECT
		(SELECT session_expires_at FROM active_session) AS session_expires_at,
		(SELECT id::text FROM authorized_destination) AS destination_id,
		(SELECT workspace_id::text FROM authorized_destination) AS workspace_id,
		(SELECT max_upload_bytes FROM authorized_destination) AS max_upload_bytes`

// dmDestinationQuery requires active participation in the conversation, in an
// active workspace the caller is still an active member of.
const dmDestinationQuery = authsession.ActiveSessionCTE + `,
	authorized_destination AS (
		SELECT dc.id, dc.workspace_id, w.max_upload_bytes
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
	)
	SELECT
		(SELECT session_expires_at FROM active_session) AS session_expires_at,
		(SELECT id::text FROM authorized_destination) AS destination_id,
		(SELECT workspace_id::text FROM authorized_destination) AS workspace_id,
		(SELECT max_upload_bytes FROM authorized_destination) AS max_upload_bytes`
