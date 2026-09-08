package storage

import (
	"context"
	"fmt"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// Target kinds for a notification preference. The same two strings the sidebar
// pin store uses, declared separately so neither store depends on the other's
// vocabulary.
const (
	NotificationPrefTargetChannel = "channel"
	NotificationPrefTargetDM      = "dm"
)

// MutedConversation is one silenced target of one user.
type MutedConversation struct {
	TargetType string
	TargetID   string
}

// NotificationPrefStore persists the caller's private mute preference.
//
// Its write statements repeat the current visibility predicate, exactly like
// SidebarPinStore's do, so a stale client-side list cannot turn a revoked
// membership into a preference row — and, for channels, they also repeat the
// general-channel invariant, so the structural refusal holds in SQL and not only
// in the service.
type NotificationPrefStore interface {
	// Mute silences targetID for userID. Idempotent: muting twice is one row.
	Mute(ctx context.Context, workspaceID, userID, targetType, targetID string) error
	// Unmute restores notifications. Idempotent, and deliberately unguarded by
	// visibility: a user must always be able to undo their own preference, even
	// for a conversation they can no longer see.
	Unmute(ctx context.Context, userID, targetType, targetID string) error
	// ListMuted returns every preference of this (workspace, user) that still
	// points at something they can see.
	ListMuted(ctx context.Context, workspaceID, userID string) ([]MutedConversation, error)
	// FilterMutedUsers returns, of the users given, those who silenced this one
	// target. It is ListMuted asked the other way round, for the realtime
	// fan-out: there the target is fixed and the recipients vary, so asking per
	// user would be one query per subscriber of every message.
	//
	// No visibility predicate, and that is the same reasoning the notification
	// worker's projection is written on: the write path already established
	// membership, and re-checking it on read would let a revoked membership
	// *undo* a mute — the direction that alerts somebody who asked not to be.
	// The caller has separately authorised every user it passes in.
	FilterMutedUsers(
		ctx context.Context, workspaceID, targetType, targetID string, userIDs []string,
	) ([]string, error)
}

type PGXNotificationPrefStore struct{ pool Pool }

func NewPGXNotificationPrefStore(pool Pool) *PGXNotificationPrefStore {
	return &PGXNotificationPrefStore{pool: pool}
}

// muteChannelSQL admits exactly the channels this user may silence.
//
// Three conditions and the third is the point: `c.is_general = false` is the
// structural invariant, enforced here rather than only above, so a direct call
// that bypassed the service still cannot silence the general channel. The
// identity test is the column, never the display name.
const muteChannelSQL = `
	WITH authorized AS (
		SELECT c.id, c.workspace_id
		FROM chat.channels c
		JOIN chat.workspaces w ON w.id = c.workspace_id AND w.status = 'active'
		JOIN chat.workspace_members wm
		  ON wm.workspace_id = c.workspace_id AND wm.user_id = $2 AND wm.status = 'active'
		WHERE c.id = $3 AND c.workspace_id = $1 AND c.status = 'active'
		  AND c.is_general = false
		  AND chat.channel_visible_to_user(c.id, $2::uuid)
	), ins AS (
		INSERT INTO chat.conversation_notification_prefs (user_id, workspace_id, channel_id)
		SELECT $2, workspace_id, id FROM authorized
		ON CONFLICT (user_id, channel_id) WHERE channel_id IS NOT NULL DO NOTHING
	)
	SELECT EXISTS (SELECT 1 FROM authorized)`

// muteDMSQL admits a DM or group the user actively participates in. There is no
// general-channel analogue here: a conversation is silenceable for whoever is
// in it, and a 1:1 is no different from a group in that respect.
const muteDMSQL = `
	WITH authorized AS (
		SELECT dc.id, dc.workspace_id
		FROM chat.dm_conversations dc
		JOIN chat.workspaces w ON w.id = dc.workspace_id AND w.status = 'active'
		JOIN chat.workspace_members wm
		  ON wm.workspace_id = dc.workspace_id AND wm.user_id = $2 AND wm.status = 'active'
		JOIN chat.dm_members dm
		  ON dm.conversation_id = dc.id AND dm.user_id = $2 AND dm.status = 'active'
		WHERE dc.id = $3 AND dc.workspace_id = $1 AND dc.status = 'active'
	), ins AS (
		INSERT INTO chat.conversation_notification_prefs (user_id, workspace_id, dm_conversation_id)
		SELECT $2, workspace_id, id FROM authorized
		ON CONFLICT (user_id, dm_conversation_id) WHERE dm_conversation_id IS NOT NULL DO NOTHING
	)
	SELECT EXISTS (SELECT 1 FROM authorized)`

func (s *PGXNotificationPrefStore) Mute(ctx context.Context, workspaceID, userID, targetType, targetID string) error {
	query, err := muteSQLFor(targetType)
	if err != nil {
		return err
	}
	var allowed bool
	if err := s.pool.QueryRow(ctx, query, workspaceID, userID, targetID).Scan(&allowed); err != nil {
		return fmt.Errorf("mute conversation: %w", err)
	}
	if !allowed {
		// One answer for "no such conversation", "you cannot see it" and "it is
		// the general channel". The first two must stay indistinguishable so the
		// endpoint cannot be used to probe which IDs exist; the third is folded
		// in because the UI never offers the action for #geral anyway, and
		// naming it here would say more than the caller needs.
		return domain.ErrNotFound
	}
	return nil
}

func muteSQLFor(targetType string) (string, error) {
	switch targetType {
	case NotificationPrefTargetChannel:
		return muteChannelSQL, nil
	case NotificationPrefTargetDM:
		return muteDMSQL, nil
	default:
		return "", domain.ErrInvalidInput
	}
}

func (s *PGXNotificationPrefStore) Unmute(ctx context.Context, userID, targetType, targetID string) error {
	var query string
	switch targetType {
	case NotificationPrefTargetChannel:
		query = `DELETE FROM chat.conversation_notification_prefs WHERE user_id = $1 AND channel_id = $2`
	case NotificationPrefTargetDM:
		query = `DELETE FROM chat.conversation_notification_prefs WHERE user_id = $1 AND dm_conversation_id = $2`
	default:
		return domain.ErrInvalidInput
	}
	if _, err := s.pool.Exec(ctx, query, userID, targetID); err != nil {
		return fmt.Errorf("unmute conversation: %w", err)
	}
	return nil
}

func (s *PGXNotificationPrefStore) ListMuted(ctx context.Context, workspaceID, userID string) ([]MutedConversation, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT 'channel', p.channel_id::text
		FROM chat.conversation_notification_prefs p
		JOIN chat.channels c ON c.id = p.channel_id AND c.workspace_id = $1 AND c.status = 'active'
		JOIN chat.workspaces w ON w.id = c.workspace_id AND w.status = 'active'
		JOIN chat.workspace_members wm
		  ON wm.workspace_id = c.workspace_id AND wm.user_id = $2 AND wm.status = 'active'
		WHERE p.user_id = $2 AND p.workspace_id = $1
		  AND chat.channel_visible_to_user(c.id, $2::uuid)
		UNION ALL
		SELECT 'dm', p.dm_conversation_id::text
		FROM chat.conversation_notification_prefs p
		JOIN chat.dm_conversations dc ON dc.id = p.dm_conversation_id AND dc.workspace_id = $1 AND dc.status = 'active'
		JOIN chat.workspaces w ON w.id = dc.workspace_id AND w.status = 'active'
		JOIN chat.workspace_members wm
		  ON wm.workspace_id = dc.workspace_id AND wm.user_id = $2 AND wm.status = 'active'
		JOIN chat.dm_members dm ON dm.conversation_id = dc.id AND dm.user_id = $2 AND dm.status = 'active'
		WHERE p.user_id = $2 AND p.workspace_id = $1`, workspaceID, userID)
	if err != nil {
		return nil, fmt.Errorf("list muted conversations: %w", err)
	}
	defer rows.Close()
	muted := make([]MutedConversation, 0)
	for rows.Next() {
		var item MutedConversation
		if err := rows.Scan(&item.TargetType, &item.TargetID); err != nil {
			return nil, fmt.Errorf("scan muted conversation: %w", err)
		}
		muted = append(muted, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate muted conversations: %w", err)
	}
	return muted, nil
}

// filterMutedChannelSQL and filterMutedDMSQL read the one target against a list
// of recipients. Keyed by (user_id, target), which is exactly the shape of the
// partial unique indexes 000037 created, so this is an index lookup per user
// rather than a scan.
const filterMutedChannelSQL = `
	SELECT p.user_id::text
	FROM chat.conversation_notification_prefs p
	WHERE p.workspace_id = $1::uuid
	  AND p.channel_id = $2::uuid
	  AND p.user_id = ANY($3::uuid[])`

const filterMutedDMSQL = `
	SELECT p.user_id::text
	FROM chat.conversation_notification_prefs p
	WHERE p.workspace_id = $1::uuid
	  AND p.dm_conversation_id = $2::uuid
	  AND p.user_id = ANY($3::uuid[])`

func (s *PGXNotificationPrefStore) FilterMutedUsers(
	ctx context.Context, workspaceID, targetType, targetID string, userIDs []string,
) ([]string, error) {
	if len(userIDs) == 0 {
		return nil, nil
	}
	var query string
	switch targetType {
	case NotificationPrefTargetChannel:
		query = filterMutedChannelSQL
	case NotificationPrefTargetDM:
		query = filterMutedDMSQL
	default:
		// Fail closed the way the rest of this store does: an unrecognised kind
		// is not a reason to report anybody as muted, and it is not a reason to
		// report anybody as unmuted either — so it is an error, not an answer.
		return nil, domain.ErrInvalidInput
	}
	rows, err := s.pool.Query(ctx, query, workspaceID, targetID, userIDs)
	if err != nil {
		return nil, fmt.Errorf("filter muted users: %w", err)
	}
	defer rows.Close()
	muted := make([]string, 0, len(userIDs))
	for rows.Next() {
		var userID string
		if err := rows.Scan(&userID); err != nil {
			return nil, fmt.Errorf("scan muted user: %w", err)
		}
		muted = append(muted, userID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate muted users: %w", err)
	}
	return muted, nil
}
