package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// MaxPinsPerContainer caps how many messages may be pinned in a single channel
// or DM (RF-05 abuse ceiling). No UI to configure it in the MVP.
const MaxPinsPerContainer = 50

type ListPinsResult struct {
	Pins       []domain.PinnedMessage
	TotalCount int
}

// PinStore is the persistence interface for channel/DM message pins (RF-05).
//
// The store enforces current read access, message⇄target binding (IDOR guard),
// idempotency, and the per-container cap.
type PinStore interface {
	// AddPin pins messageID in targetID. Idempotent: pinning an already-pinned
	// message succeeds. Returns ErrNotFound when the message does not exist,
	// is deleted, unreadable, or does not belong to targetID (IDOR guard).
	// Returns ErrPinLimitReached when the target is already at MaxPinsPerContainer.
	AddPin(ctx context.Context, workspaceID, targetType, targetID, messageID, pinnedByUserID string) error

	// RemovePin unpins messageID from targetID. Idempotent: removing a
	// nonexistent pin is a no-op.
	RemovePin(ctx context.Context, workspaceID, targetType, targetID, messageID, actorUserID string) error

	// ListPins returns a target's pins, newest pin first, capped at
	// MaxPinsPerContainer. viewerID scopes the per-message is_favorited flag so the
	// rows carry the same contract as every other message query. Deleted
	// messages (RF-14) remain listed; the handler renders the placeholder.
	ListPins(ctx context.Context, workspaceID, targetType, targetID, viewerID string) (ListPinsResult, error)
}

// PGXPinStore implements PinStore using a pgx connection pool.
type PGXPinStore struct{ pool Pool }

// NewPGXPinStore creates a PGXPinStore backed by the given pool.
func NewPGXPinStore(pool Pool) *PGXPinStore { return &PGXPinStore{pool: pool} }

const pinMessageTargetPredicate = `(
				($1 = 'channel' AND m.channel_id = $2 AND m.dm_conversation_id IS NULL)
				OR ($1 = 'dm' AND m.dm_conversation_id = $2 AND m.channel_id IS NULL)
			  )`

func (s *PGXPinStore) AddPin(ctx context.Context, workspaceID, targetType, targetID, messageID, pinnedByUserID string) error {
	// Authorization, IDOR guard, idempotency and the cap resolve in one
	// statement. The insert only fires when the message belongs to the target,
	// is active/readable, the message is not already pinned, and the target is below
	// the cap. The outer SELECT reports which precondition held so the caller can
	// map to the right status (404 vs 409 vs 204).
	var msgExists, already, inserted bool
	err := s.pool.QueryRow(ctx, `
		WITH msg AS (
			SELECT m.id
			FROM chat.messages m`+messageAccessJoins("$4")+`
			WHERE m.workspace_id = $3 AND m.id = $5 AND m.status = 'active'
			  AND `+pinMessageTargetPredicate+`
			  AND `+messageAccessPredicate+`
		),
		existing AS (
			SELECT 1 FROM chat.message_pins WHERE target_type = $1 AND target_id = $2 AND message_id = $5
		),
		cnt AS (
			SELECT count(*) AS c FROM chat.message_pins WHERE target_type = $1 AND target_id = $2
		),
		ins AS (
			INSERT INTO chat.message_pins (target_type, target_id, message_id, pinned_by_user_id)
			SELECT $1, $2, id, $4 FROM msg
			WHERE (SELECT c FROM cnt) < $6
			  AND NOT EXISTS (SELECT 1 FROM existing)
			ON CONFLICT (target_type, target_id, message_id) DO NOTHING
			RETURNING message_id
		)
		SELECT
			EXISTS (SELECT 1 FROM msg),
			EXISTS (SELECT 1 FROM existing),
			EXISTS (SELECT 1 FROM ins)`,
		targetType, targetID, workspaceID, pinnedByUserID, messageID, MaxPinsPerContainer,
	).Scan(&msgExists, &already, &inserted)
	if err != nil {
		return fmt.Errorf("add pin: %w", err)
	}
	if !msgExists {
		// Non-enumerating: missing, deleted, and wrong-channel are identical.
		return domain.ErrNotFound
	}
	if already || inserted {
		return nil
	}
	// Message is valid and not yet pinned, but nothing was inserted: cap reached.
	return domain.ErrPinLimitReached
}

func (s *PGXPinStore) RemovePin(ctx context.Context, workspaceID, targetType, targetID, messageID, actorUserID string) error {
	var readable bool
	err := s.pool.QueryRow(ctx, `
		WITH readable AS (
			SELECT m.id
			FROM chat.messages m`+messageAccessJoins("$4")+`
			WHERE m.workspace_id = $3 AND m.id = $5
			  AND `+pinMessageTargetPredicate+`
			  AND `+messageAccessPredicate+`
		),
		deleted AS (
			DELETE FROM chat.message_pins p
			USING chat.messages m, readable
			WHERE p.message_id = m.id
			  AND m.id = readable.id
			  AND m.workspace_id = $3
			  AND p.target_type = $1 AND p.target_id = $2 AND p.message_id = $5
			RETURNING p.message_id
		)
		SELECT EXISTS (SELECT 1 FROM readable)`,
		targetType, targetID, workspaceID, actorUserID, messageID,
	).Scan(&readable)
	if err != nil {
		return fmt.Errorf("remove pin: %w", err)
	}
	if !readable {
		return domain.ErrNotFound
	}
	return nil
}

func (s *PGXPinStore) ListPins(ctx context.Context, workspaceID, targetType, targetID, viewerID string) (ListPinsResult, error) {
	rows, err := s.pool.Query(ctx, `
		WITH target_access AS (
			SELECT EXISTS (
				SELECT 1
				FROM chat.workspaces w
				JOIN chat.workspace_members wm
				  ON wm.workspace_id = w.id AND wm.user_id = $4 AND wm.status = 'active'
				LEFT JOIN chat.channels c
				  ON $1 = 'channel' AND c.id = $2 AND c.workspace_id = w.id AND c.status = 'active'
				LEFT JOIN chat.channel_members cm
				  ON cm.channel_id = c.id AND cm.user_id = $4
				LEFT JOIN chat.dm_conversations dc
				  ON $1 = 'dm' AND dc.id = $2 AND dc.workspace_id = w.id AND dc.status = 'active'
				LEFT JOIN chat.dm_members dm
				  ON dm.conversation_id = dc.id AND dm.user_id = $4 AND dm.status = 'active'
				WHERE w.id = $3 AND w.status = 'active'
				  AND (
					($1 = 'channel' AND c.id IS NOT NULL AND (c.type = 'public' OR cm.user_id IS NOT NULL))
					OR ($1 = 'dm' AND dc.id IS NOT NULL AND dm.user_id IS NOT NULL)
				  )
			) AS allowed
		),
		authorized_pins AS (
			SELECT `+listMessageColumns("m", "$4")+`, p.pinned_at, p.pinned_by_user_id
			FROM chat.message_pins p
			JOIN chat.messages m
			  ON m.id = p.message_id
			`+messageAccessJoins("$4")+`
			LEFT JOIN auth.users u
			  ON u.id = m.sender_id
			CROSS JOIN target_access a
			WHERE a.allowed
			  AND m.workspace_id = $3
			  AND p.target_type = $1 AND p.target_id = $2
			  AND `+pinMessageTargetPredicate+`
			  AND `+messageAccessPredicate+`
		),
		pin_rows AS (
			SELECT *, count(*) OVER() AS total_count
			FROM authorized_pins
			ORDER BY pinned_at DESC, id DESC
			LIMIT $5
		)
		SELECT a.allowed, TRUE AS has_pin, r.*
		FROM target_access a
		JOIN pin_rows r ON TRUE
		UNION ALL
		SELECT a.allowed, FALSE AS has_pin,
			''::text, ''::text, ''::text, ''::text, ''::text,
			'user'::text, ''::text, 'v1'::text, 'active'::text,
			''::text, ''::text, ''::text,
			NULL::timestamptz, 0::integer, NULL::timestamptz,
			'epoch'::timestamptz, 'epoch'::timestamptz,
			''::text, ''::text, FALSE,
			'epoch'::timestamptz, '00000000-0000-0000-0000-000000000000'::uuid, 0::bigint
		FROM target_access a
		WHERE NOT EXISTS (SELECT 1 FROM pin_rows)
		ORDER BY has_pin DESC, pinned_at DESC, id DESC`,
		targetType, targetID, workspaceID, viewerID, MaxPinsPerContainer,
	)
	if err != nil {
		return ListPinsResult{}, fmt.Errorf("list pins: %w", err)
	}
	defer rows.Close()

	pins := make([]domain.PinnedMessage, 0, MaxPinsPerContainer)
	total := 0
	sawRow := false
	for rows.Next() {
		sawRow = true
		var pin domain.PinnedMessage
		var allowed, hasPin bool
		var editedAt, deletedAt *time.Time
		var rowTotal int
		msg := &pin.Message
		if err := rows.Scan(
			&allowed, &hasPin,
			&msg.ID, &msg.WorkspaceID,
			&msg.ChannelID, &msg.DMConversationID,
			&msg.SenderID,
			(*string)(&msg.Kind), &msg.BodyText, (*string)(&msg.BodyFormat), (*string)(&msg.Status),
			&msg.ParentMessageID, &msg.ForwardedFromMessageID, &msg.ReferencedMessageID,
			&editedAt, &msg.EditCount, &deletedAt,
			&msg.CreatedAt, &msg.UpdatedAt,
			&msg.SenderDisplayName, &msg.SenderEmail,
			&msg.IsFavorited,
			&pin.PinnedAt, &pin.PinnedByUserID,
			&rowTotal,
		); err != nil {
			return ListPinsResult{}, fmt.Errorf("scan pin row: %w", err)
		}
		if !allowed {
			return ListPinsResult{}, domain.ErrNotFound
		}
		if !hasPin {
			continue
		}
		total = rowTotal
		if editedAt != nil {
			msg.EditedAt = *editedAt
		}
		if deletedAt != nil {
			msg.DeletedAt = *deletedAt
		}
		pins = append(pins, pin)
	}
	if err := rows.Err(); err != nil {
		return ListPinsResult{}, fmt.Errorf("iterate pin rows: %w", err)
	}
	if !sawRow {
		return ListPinsResult{}, domain.ErrNotFound
	}
	return ListPinsResult{Pins: pins, TotalCount: total}, nil
}
