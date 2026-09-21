package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// ConversationAbout is the authoritative metadata the "Sobre" block of a
// details panel renders that the conversation row alone cannot answer
// (issue #894): the conversation's own description, and its creator resolved to
// a name a person can read.
//
// It is one type for channels and groups because it is one question asked of
// two aggregates. Each store answers it against its own table; nothing about
// the answer differs by kind, so the panel reads one shape.
//
// Both fields are empty string for "absent", and absent is a real state:
//
//   - Description is empty when the row holds NULL or a blank string — a
//     conversation created before migration 000053, or one nobody has
//     described. The panel
//     renders its empty state, which is the truth rather than a placeholder.
//   - CreatorDisplayName is empty when there is no historically trustworthy
//     identity to show: created_by was NULL, or that account is deleted,
//     disabled, or no longer an active member of this workspace. The panel
//     renders a neutral state.
//
// There is deliberately no creator user ID here. Nothing in this flow navigates
// to the creator, so the UUID has no job beyond the join that resolved it, and
// a field that exists only to be serialised is how a UUID reaches a browser and
// then becomes a fallback label.
type ConversationAbout struct {
	Description        string
	CreatorDisplayName string
}

// conversationAboutQuery builds the About read for one conversation table.
//
// table and alias are compile-time constants from the two callers below, never
// anything a request carries, so the concatenation has no injection surface;
// the workspace and the conversation are bound parameters as everywhere else.
// It exists because the two tables answer the same question with the same
// shape, and writing the query twice would be two chances for the channel's
// creator and the group's creator to be resolved by different rules.
//
// The creator's visual name is resolved the one way this domain resolves a
// name: full_name when set, display_name otherwise, empty when neither is
// usable — the same expression chat.channel_members, chat.dm_members and the
// conversation event store already use. A second rule here would let the person
// who created a channel be named one way in the roster and another way above
// it.
//
// The identity joins are LEFT and every one of their conditions sits on the
// join rather than in WHERE, so an unresolvable creator yields an empty name
// instead of no row: "this conversation has no nameable creator" and "this
// conversation does not exist" are different answers and the caller
// distinguishes them.
//
// Requiring an active workspace membership is what keeps this from being a
// directory lookup. Someone who created a channel and then left the workspace,
// or whose account was deleted, is not named here — their identity is no longer
// this workspace's to hand out, and the panel says nothing rather than
// something stale.
func conversationAboutQuery(table, alias string) string {
	return `
		SELECT COALESCE(` + alias + `.description, ''),
		       COALESCE(
		           NULLIF(BTRIM(creator_u.full_name), ''),
		           NULLIF(BTRIM(creator_u.display_name), ''),
		           ''
		       )
		FROM ` + table + ` ` + alias + `
		LEFT JOIN chat.workspace_members creator_wm
		  ON creator_wm.workspace_id = $1::uuid
		 AND creator_wm.user_id = ` + alias + `.created_by
		 AND creator_wm.status = 'active'
		LEFT JOIN auth.users creator_u
		  ON creator_u.id = creator_wm.user_id
		 AND creator_u.status = 'active'
		 AND creator_u.deleted_at IS NULL
		WHERE ` + alias + `.workspace_id = $1::uuid
		  AND ` + alias + `.id = $2::uuid`
}

// GetChannelAbout returns a channel's description and its creator's resolved
// display name, in one query.
//
// Not an authorization boundary, and never the first thing a request does: the
// caller settles visibility with GetVisibleChannelByID before calling this —
// the same contract ListOnlineChannelMemberProfiles states. The workspace_id
// filter here is tenant isolation in depth, so that a channel UUID from another
// workspace selects nothing; it is not the read permission itself.
//
// One query, one creator, no per-row lookup: the identity is resolved by the
// join that reads the channel rather than by a follow-up request, which is what
// keeps "who made this" from costing a round trip per panel.
func (s *PGXChannelStore) GetChannelAbout(
	ctx context.Context, workspaceID, channelID string,
) (ConversationAbout, error) {
	return scanConversationAbout(
		ctx, s.pool, "get channel about",
		conversationAboutQuery("chat.channels", "c"), workspaceID, channelID,
	)
}

// GetConversationAbout returns a conversation's description and its creator's
// resolved display name, in one query.
//
// The DM-side twin of GetChannelAbout, with the same contract: access is
// settled by GetVisibleConversationByID first, and the workspace_id filter is
// isolation in depth rather than the permission. It is not restricted to
// groups — the type check belongs to the caller, which has already read the row
// and knows which panel it is answering for.
func (s *PGXDMStore) GetConversationAbout(
	ctx context.Context, workspaceID, conversationID string,
) (ConversationAbout, error) {
	return scanConversationAbout(
		ctx, s.pool, "get conversation about",
		conversationAboutQuery("chat.dm_conversations", "dc"), workspaceID, conversationID,
	)
}

// scanConversationAbout runs one of the two queries above and maps a missing
// row to domain.ErrNotFound.
//
// No rows here means the aggregate stopped being readable between the
// visibility check and this read — archived, deleted, or moved. ErrNotFound is
// what the details endpoints already answer for every such case, so the race
// collapses into the outcome the caller would have produced a moment earlier
// rather than into a 500.
func scanConversationAbout(
	ctx context.Context, pool Pool, op, query string, args ...any,
) (ConversationAbout, error) {
	var about ConversationAbout
	err := pool.QueryRow(ctx, query, args...).Scan(&about.Description, &about.CreatorDisplayName)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ConversationAbout{}, domain.ErrNotFound
		}
		return ConversationAbout{}, fmt.Errorf("%s: %w", op, err)
	}
	return about, nil
}
