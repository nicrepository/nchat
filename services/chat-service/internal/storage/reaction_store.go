package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

type ToggleReactionInput struct {
	WorkspaceID string
	UserID      string
	MessageID   string
	Emoji       string
}

type ToggleReactionResult struct {
	MessageID string
	ChannelID string
	DMID      string
	Added     bool
	Reactions []domain.MessageReaction
}

type ReactionStore interface {
	ToggleReaction(context.Context, ToggleReactionInput) (ToggleReactionResult, error)
}

type PGXReactionStore struct{ pool Pool }

func NewPGXReactionStore(pool Pool) *PGXReactionStore { return &PGXReactionStore{pool: pool} }

func (s *PGXReactionStore) ToggleReaction(ctx context.Context, input ToggleReactionInput) (result ToggleReactionResult, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return result, fmt.Errorf("begin reaction toggle: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// ponytail: lock the message, not each reaction tuple; move to advisory tuple
	// locks only if reaction throughput on a single message becomes measurable.
	err = tx.QueryRow(ctx, `
		SELECT COALESCE(m.channel_id::text, ''), COALESCE(m.dm_conversation_id::text, '')
		FROM chat.messages m
		JOIN chat.workspace_members wm
		  ON wm.workspace_id = m.workspace_id AND wm.user_id = $2 AND wm.status = 'active'
		LEFT JOIN chat.channels c
		  ON c.id = m.channel_id AND c.status = 'active'
		LEFT JOIN chat.channel_members cm
		  ON cm.channel_id = m.channel_id AND cm.user_id = $2
		LEFT JOIN chat.dm_conversations dc
		  ON dc.id = m.dm_conversation_id AND dc.status = 'active'
		LEFT JOIN chat.dm_members dm
		  ON dm.conversation_id = m.dm_conversation_id AND dm.user_id = $2 AND dm.status = 'active'
		WHERE m.workspace_id = $1 AND m.id = $3 AND m.status = 'active'
		  AND ((m.channel_id IS NOT NULL AND c.id IS NOT NULL AND (c.type = 'public' OR cm.user_id IS NOT NULL))
		    OR (m.dm_conversation_id IS NOT NULL AND dc.id IS NOT NULL AND dm.user_id IS NOT NULL))
		FOR UPDATE OF m`, input.WorkspaceID, input.UserID, input.MessageID).
		Scan(&result.ChannelID, &result.DMID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ToggleReactionResult{}, domain.ErrNotFound
	}
	if err != nil {
		return result, fmt.Errorf("authorize reaction target: %w", err)
	}

	tag, err := tx.Exec(ctx, `DELETE FROM chat.message_reactions WHERE message_id = $1 AND user_id = $2 AND emoji = $3`,
		input.MessageID, input.UserID, input.Emoji)
	if err != nil {
		return result, fmt.Errorf("remove reaction: %w", err)
	}
	result.Added = tag.RowsAffected() == 0
	if result.Added {
		if _, err = tx.Exec(ctx, `INSERT INTO chat.message_reactions (message_id, user_id, emoji) VALUES ($1, $2, $3)`,
			input.MessageID, input.UserID, input.Emoji); err != nil {
			return result, fmt.Errorf("add reaction: %w", err)
		}
	}

	rows, err := tx.Query(ctx, `
		SELECT emoji, count(*)::int, bool_or(user_id = $2)
		FROM chat.message_reactions
		WHERE message_id = $1
		GROUP BY emoji
		ORDER BY min(created_at), emoji`, input.MessageID, input.UserID)
	if err != nil {
		return result, fmt.Errorf("list reaction aggregate: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var reaction domain.MessageReaction
		if err := rows.Scan(&reaction.Emoji, &reaction.Count, &reaction.ReactedByMe); err != nil {
			return result, fmt.Errorf("scan reaction aggregate: %w", err)
		}
		result.Reactions = append(result.Reactions, reaction)
	}
	if err := rows.Err(); err != nil {
		return result, fmt.Errorf("iterate reaction aggregate: %w", err)
	}
	result.MessageID = input.MessageID
	if err := tx.Commit(ctx); err != nil {
		return result, fmt.Errorf("commit reaction toggle: %w", err)
	}
	return result, nil
}
