package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// MaxPinsPerChannel caps how many messages may be pinned in a single channel
// (RF-05 abuse ceiling). No UI to configure it in the MVP.
const MaxPinsPerChannel = 50

// PinStore is the persistence interface for channel message pins (RF-05).
//
// Authorization (role + channel read access) is enforced by PinService before
// these methods are called; the store only enforces the message⇄channel
// binding (IDOR guard), idempotency, and the per-channel cap.
type PinStore interface {
	// AddPin pins messageID in channelID. Idempotent: pinning an already-pinned
	// message succeeds. Returns ErrNotFound when the message does not exist,
	// is deleted, or does not belong to channelID (IDOR guard). Returns
	// ErrPinLimitReached when the channel is already at MaxPinsPerChannel.
	AddPin(ctx context.Context, channelID, messageID, pinnedByUserID string) error

	// RemovePin unpins messageID from channelID. Idempotent: removing a
	// nonexistent pin is a no-op.
	RemovePin(ctx context.Context, channelID, messageID string) error

	// ListPins returns a channel's pins, newest pin first, capped at
	// MaxPinsPerChannel. viewerID scopes the per-message is_favorited flag so the
	// rows carry the same contract as every other message query. Deleted
	// messages (RF-14) remain listed; the handler renders the placeholder.
	ListPins(ctx context.Context, channelID, viewerID string) ([]domain.PinnedMessage, error)
}

// PGXPinStore implements PinStore using a pgx connection pool.
type PGXPinStore struct{ pool Pool }

// NewPGXPinStore creates a PGXPinStore backed by the given pool.
func NewPGXPinStore(pool Pool) *PGXPinStore { return &PGXPinStore{pool: pool} }

func (s *PGXPinStore) AddPin(ctx context.Context, channelID, messageID, pinnedByUserID string) error {
	// Authorization, IDOR guard, idempotency and the cap resolve in one
	// statement. The insert only fires when the message belongs to the channel
	// and is active, the message is not already pinned, and the channel is below
	// the cap. The outer SELECT reports which precondition held so the caller can
	// map to the right status (404 vs 409 vs 204).
	var msgExists, already, inserted bool
	err := s.pool.QueryRow(ctx, `
		WITH msg AS (
			SELECT id FROM chat.messages
			WHERE id = $2 AND channel_id = $1 AND status = 'active'
		),
		existing AS (
			SELECT 1 FROM chat.message_pins WHERE channel_id = $1 AND message_id = $2
		),
		cnt AS (
			SELECT count(*) AS c FROM chat.message_pins WHERE channel_id = $1
		),
		ins AS (
			INSERT INTO chat.message_pins (channel_id, message_id, pinned_by_user_id)
			SELECT $1, id, $3 FROM msg
			WHERE (SELECT c FROM cnt) < $4
			  AND NOT EXISTS (SELECT 1 FROM existing)
			ON CONFLICT (channel_id, message_id) DO NOTHING
			RETURNING message_id
		)
		SELECT
			EXISTS (SELECT 1 FROM msg),
			EXISTS (SELECT 1 FROM existing),
			EXISTS (SELECT 1 FROM ins)`,
		channelID, messageID, pinnedByUserID, MaxPinsPerChannel,
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

func (s *PGXPinStore) RemovePin(ctx context.Context, channelID, messageID string) error {
	if _, err := s.pool.Exec(ctx, `
		DELETE FROM chat.message_pins
		WHERE channel_id = $1 AND message_id = $2`,
		channelID, messageID,
	); err != nil {
		return fmt.Errorf("remove pin: %w", err)
	}
	return nil
}

func (s *PGXPinStore) ListPins(ctx context.Context, channelID, viewerID string) ([]domain.PinnedMessage, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+listMessageColumns("m", "$2")+`, p.pinned_at, p.pinned_by_user_id
		FROM chat.message_pins p
		JOIN chat.messages m
		  ON m.id = p.message_id
		LEFT JOIN auth.users u
		  ON u.id = m.sender_id
		WHERE p.channel_id = $1
		ORDER BY p.pinned_at DESC, p.message_id DESC
		LIMIT $3`,
		channelID, viewerID, MaxPinsPerChannel,
	)
	if err != nil {
		return nil, fmt.Errorf("list pins: %w", err)
	}
	defer rows.Close()

	var pins []domain.PinnedMessage
	for rows.Next() {
		var pin domain.PinnedMessage
		var editedAt, deletedAt *time.Time
		msg := &pin.Message
		if err := rows.Scan(
			&msg.ID, &msg.WorkspaceID,
			&msg.ChannelID, &msg.DMConversationID,
			&msg.SenderID,
			(*string)(&msg.Kind), &msg.BodyText, (*string)(&msg.BodyFormat), (*string)(&msg.Status),
			&msg.ParentMessageID, &msg.ForwardedFromMessageID, &msg.ReferencedMessageID,
			&editedAt, &deletedAt,
			&msg.CreatedAt, &msg.UpdatedAt,
			&msg.SenderDisplayName, &msg.SenderEmail,
			&msg.IsFavorited,
			&pin.PinnedAt, &pin.PinnedByUserID,
		); err != nil {
			return nil, fmt.Errorf("scan pin row: %w", err)
		}
		if editedAt != nil {
			msg.EditedAt = *editedAt
		}
		if deletedAt != nil {
			msg.DeletedAt = *deletedAt
		}
		pins = append(pins, pin)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pin rows: %w", err)
	}
	return pins, nil
}
