package storage

import (
	"context"
	"fmt"
	"hash/fnv"

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

	// Acquire before the next statement takes its READ COMMITTED snapshot. A
	// lock inside the toggle statement would wait correctly but retain a stale
	// pre-wait snapshot. The key is per message and user, so adding different
	// emojis from two tabs cannot both consume the final reaction slot.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, reactionAdvisoryKey(input)); err != nil {
		return result, fmt.Errorf("acquire reaction tuple lock: %w", err)
	}
	result, err = toggleAuthorizedReaction(ctx, tx, input)
	if err != nil {
		return ToggleReactionResult{}, err
	}
	result.MessageID = input.MessageID
	if err := tx.Commit(ctx); err != nil {
		return result, fmt.Errorf("commit reaction toggle: %w", err)
	}
	return result, nil
}

func toggleAuthorizedReaction(ctx context.Context, tx pgx.Tx, input ToggleReactionInput) (result ToggleReactionResult, err error) {
	rows, err := tx.Query(ctx, `
		WITH base AS MATERIALIZED (
			SELECT m.channel_id, m.dm_conversation_id
			FROM chat.messages m
			JOIN chat.workspace_members wm
			  ON wm.workspace_id = m.workspace_id AND wm.user_id = $2 AND wm.status = 'active'
			WHERE m.workspace_id = $1 AND m.id = $3 AND m.status = 'active'
			FOR SHARE OF m, wm
		),
		public_channel AS MATERIALIZED (
			SELECT b.channel_id::text AS channel_id, ''::text AS dm_id
			FROM base b
			JOIN chat.channels c ON c.id = b.channel_id AND c.status = 'active' AND c.type = 'public'
			-- A public channel is reachable by workspace membership alone for
			-- every role except guest (RF-74), whose reach is only the channels
			-- it belongs to. A guest that does belong is admitted here, by the
			-- chat.channel_members half of the shared function -- not by the
			-- private_channel branch below, which only ever matches c.type =
			-- 'private'. This branch is the only one that admits a guest.
			WHERE chat.channel_visible_to_user(c.id, $2)
			FOR SHARE OF c
		),
		private_channel AS MATERIALIZED (
			SELECT b.channel_id::text AS channel_id, ''::text AS dm_id
			FROM base b
			JOIN chat.channels c ON c.id = b.channel_id AND c.status = 'active' AND c.type = 'private'
			JOIN chat.channel_members cm ON cm.channel_id = c.id AND cm.user_id = $2
			FOR SHARE OF c, cm
		),
		direct_message AS MATERIALIZED (
			SELECT ''::text AS channel_id, b.dm_conversation_id::text AS dm_id
			FROM base b
			JOIN chat.dm_conversations dc ON dc.id = b.dm_conversation_id AND dc.status = 'active'
			JOIN chat.dm_members dm
			  ON dm.conversation_id = dc.id AND dm.user_id = $2 AND dm.status = 'active'
			FOR SHARE OF dc, dm
		),
		authorized AS MATERIALIZED (
			SELECT * FROM public_channel
			UNION ALL SELECT * FROM private_channel
			UNION ALL SELECT * FROM direct_message
		),
	deleted AS (
			DELETE FROM chat.message_reactions r
			USING authorized a
			WHERE r.message_id = $3 AND r.user_id = $2 AND r.emoji = $4
		RETURNING r.message_id
	),
	user_reaction_count AS MATERIALIZED (
		SELECT count(*)::int AS count
		FROM chat.message_reactions r
		JOIN authorized a ON true
		WHERE r.message_id = $3 AND r.user_id = $2
	),
	inserted AS (
		INSERT INTO chat.message_reactions (message_id, user_id, emoji)
		SELECT $3, $2, $4 FROM authorized
		WHERE NOT EXISTS (SELECT 1 FROM deleted)
		  AND (SELECT count FROM user_reaction_count) < 5
			RETURNING emoji, user_id, created_at
		),
		final_reactions AS MATERIALIZED (
			SELECT r.emoji, r.user_id, r.created_at
			FROM chat.message_reactions r
			JOIN authorized a ON true
			WHERE r.message_id = $3 AND (r.user_id <> $2 OR r.emoji <> $4)
			UNION ALL
			SELECT emoji, user_id, created_at FROM inserted
		),
		aggregated AS (
			SELECT emoji, count(*)::int AS count, bool_or(user_id = $2) AS reacted_by_me,
			       min(created_at) AS first_created_at
			FROM final_reactions
			GROUP BY emoji
		)
		SELECT a.channel_id, a.dm_id, EXISTS (SELECT 1 FROM inserted),
		       NOT EXISTS (SELECT 1 FROM deleted)
		         AND NOT EXISTS (SELECT 1 FROM inserted)
		         AND (SELECT count FROM user_reaction_count) >= 5 AS limit_reached,
		       COALESCE(g.emoji, ''), COALESCE(g.count, 0), COALESCE(g.reacted_by_me, false)
		FROM authorized a
		LEFT JOIN aggregated g ON true
		ORDER BY g.first_created_at NULLS LAST, g.emoji`,
		input.WorkspaceID, input.UserID, input.MessageID, input.Emoji)
	if err != nil {
		return result, fmt.Errorf("toggle authorized reaction: %w", err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		found = true
		var limitReached bool
		var reaction domain.MessageReaction
		if err := rows.Scan(
			&result.ChannelID, &result.DMID, &result.Added, &limitReached,
			&reaction.Emoji, &reaction.Count, &reaction.ReactedByMe,
		); err != nil {
			return ToggleReactionResult{}, fmt.Errorf("scan reaction toggle: %w", err)
		}
		if limitReached {
			return ToggleReactionResult{}, fmt.Errorf("%w: maximum of 5 active reactions per user and message", domain.ErrReactionLimitReached)
		}
		if reaction.Emoji != "" {
			result.Reactions = append(result.Reactions, reaction)
		}
	}
	if err := rows.Err(); err != nil {
		return ToggleReactionResult{}, fmt.Errorf("iterate reaction toggle: %w", err)
	}
	if !found {
		return ToggleReactionResult{}, domain.ErrNotFound
	}
	return result, nil
}

// reactionAdvisoryKey scopes serialization to one user's reactions on one
// message. Hash collisions can only add contention; the reaction primary key
// still enforces uniqueness. NUL separators are unambiguous for UUIDs.
func reactionAdvisoryKey(input ToggleReactionInput) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(input.MessageID + "\x00" + input.UserID))
	return int64(h.Sum64()) //nolint:gosec // Intentional uint64→int64 reinterpretation for PostgreSQL advisory locks.
}
