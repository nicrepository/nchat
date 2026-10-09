package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

const (
	ConversationReadTargetChannel = "channel"
	ConversationReadTargetDM      = "dm"
)

// A read state row holds two independent claims (issue #1082, migration 000068):
//
//   - the legacy boundary, last_read_at: everything created at or before it is
//     read. last_read_message_id beside it is informational and never compared;
//   - the message cursor, (cursor_created_at, cursor_message_id): that message
//     and everything before it in the canonical (created_at, id) order are read.
//
// A message is read when either claim covers it. Both are prefixes of the same
// total order, so their union is the later of the two, and since neither ever
// moves back, neither does the union. A writer that predates the cursor only
// knows the boundary, so it can neither corrupt nor reinterpret the cursor.
type ConversationReadStateStore interface {
	MarkRead(ctx context.Context, workspaceID, userID, targetType, targetID string, lastReadMessageID *string) (domain.ConversationReadState, error)
	ReadStates(ctx context.Context, workspaceID, userID string) (map[string]domain.ConversationReadState, error)
}

type PGXConversationReadStateStore struct{ pool Pool }

func NewPGXConversationReadStateStore(pool Pool) *PGXConversationReadStateStore {
	return &PGXConversationReadStateStore{pool: pool}
}

// readTarget is what differs between a channel and a DM read cursor: who may
// read the conversation, and which column names it. The cursor rule itself is
// one statement for both (issue #1082).
type readTarget struct {
	kind       string
	authorized string
	column     string
	// visible lists the conversations of this kind the caller may read, as
	// alias `conv`, joined to the caller's read state as alias `rs`.
	visible string
}

var readTargets = map[string]readTarget{
	ConversationReadTargetChannel: {
		kind: ConversationReadTargetChannel,
		authorized: `
			SELECT c.id, c.workspace_id
			FROM chat.channels c
			JOIN chat.workspaces w ON w.id = c.workspace_id AND w.status = 'active'
			JOIN chat.workspace_members wm ON wm.workspace_id = c.workspace_id AND wm.user_id = $2 AND wm.status = 'active'
			WHERE c.id = $3 AND c.workspace_id = $1 AND c.status = 'active'
			  AND chat.channel_visible_to_user(c.id, $2::uuid)`,
		column: "channel_id",
		visible: `
			FROM chat.channels conv
			JOIN chat.workspaces w ON w.id = conv.workspace_id AND w.status = 'active'
			JOIN chat.workspace_members wm ON wm.workspace_id = conv.workspace_id AND wm.user_id = $2 AND wm.status = 'active'
			LEFT JOIN chat.conversation_read_state rs ON rs.user_id = $2 AND rs.workspace_id = $1 AND rs.channel_id = conv.id
			WHERE conv.workspace_id = $1 AND conv.status = 'active' AND chat.channel_visible_to_user(conv.id, $2::uuid)`,
	},
	ConversationReadTargetDM: {
		kind: ConversationReadTargetDM,
		authorized: `
			SELECT dc.id, dc.workspace_id
			FROM chat.dm_conversations dc
			JOIN chat.workspaces w ON w.id = dc.workspace_id AND w.status = 'active'
			JOIN chat.workspace_members wm ON wm.workspace_id = dc.workspace_id AND wm.user_id = $2 AND wm.status = 'active'
			JOIN chat.dm_members dm ON dm.conversation_id = dc.id AND dm.user_id = $2 AND dm.status = 'active'
			WHERE dc.id = $3 AND dc.workspace_id = $1 AND dc.status = 'active'`,
		column: "dm_conversation_id",
		visible: `
			FROM chat.dm_conversations conv
			JOIN chat.workspaces w ON w.id = conv.workspace_id AND w.status = 'active'
			JOIN chat.workspace_members wm ON wm.workspace_id = conv.workspace_id AND wm.user_id = $2 AND wm.status = 'active'
			JOIN chat.dm_members dm ON dm.conversation_id = conv.id AND dm.user_id = $2 AND dm.status = 'active'
			LEFT JOIN chat.conversation_read_state rs ON rs.user_id = $2 AND rs.workspace_id = $1 AND rs.dm_conversation_id = conv.id
			WHERE conv.workspace_id = $1 AND conv.status = 'active'`,
	},
}

// markReadQuery advances the caller's message cursor in one statement.
//
// The cursor is a position in the timeline's canonical (created_at, id) order —
// the order every message listing uses — taken from the message itself, never
// from the request's clock or the client. $4 names the message read through;
// NULL means "the whole conversation", resolved here as its newest message the
// caller can see, so a mark-all consumes exactly the snapshot that existed.
//
// The message must belong to the authorized conversation and be visible to the
// caller, or nothing is written. The cursor only moves forward, compared
// against the latest committed row under ON CONFLICT, so concurrent, duplicate
// and out-of-order requests all leave the greatest position.
//
// The legacy boundary is raised to one microsecond below the cursor's instant
// — the newest instant every message of which the cursor already covers — and
// never lowered. That keeps a release reading only the boundary to a subset of
// what was read, and leaves the union of the two claims unchanged. The
// informational message id is left alone.
func markReadQuery(target readTarget) string {
	return `
		WITH authorized AS (` + target.authorized + `
		), target AS (
			SELECT m.id, m.created_at
			FROM chat.messages m
			JOIN authorized a ON a.workspace_id = m.workspace_id AND a.id = m.` + target.column + `
			WHERE ($4::uuid IS NULL OR m.id = $4::uuid)
			  AND ` + messageVisibilityPredicate("m", "$2") + `
			ORDER BY m.created_at DESC, m.id DESC
			LIMIT 1
		), advanced AS (
			INSERT INTO chat.conversation_read_state AS rs
				(user_id, workspace_id, ` + target.column + `, last_read_at, cursor_created_at, cursor_message_id)
			SELECT $2, a.workspace_id, a.id, t.created_at - interval '1 microsecond', t.created_at, t.id
			FROM authorized a CROSS JOIN target t
			ON CONFLICT (user_id, ` + target.column + `) WHERE ` + target.column + ` IS NOT NULL DO UPDATE
			SET cursor_created_at = EXCLUDED.cursor_created_at,
				cursor_message_id = EXCLUDED.cursor_message_id,
				last_read_at = GREATEST(rs.last_read_at, EXCLUDED.last_read_at),
				updated_at = now()
			WHERE rs.cursor_created_at IS NULL
			   OR (rs.cursor_created_at, rs.cursor_message_id) < (EXCLUDED.cursor_created_at, EXCLUDED.cursor_message_id)
		)
		SELECT EXISTS (SELECT 1 FROM authorized), EXISTS (SELECT 1 FROM target)`
}

// unreadPredicate is "message m is read by neither claim of read state rs". A
// missing row reads nothing.
const unreadPredicate = `m.created_at > COALESCE(rs.last_read_at, '-infinity'::timestamptz)
		AND (rs.cursor_created_at IS NULL OR (m.created_at, m.id) > (rs.cursor_created_at, rs.cursor_message_id))`

// readStateSelect lists the caller's read state for every conversation of one
// kind they may read: both claims, and the unread count — active messages from
// other users neither claim covers.
func readStateSelect(target readTarget) string {
	return `
		SELECT '` + target.kind + `', conv.id::text, rs.last_read_at, rs.cursor_created_at, rs.cursor_message_id::text,
			(SELECT COUNT(*) FROM chat.messages m
			 WHERE m.workspace_id = conv.workspace_id AND m.` + target.column + ` = conv.id AND m.status = 'active'
			   AND m.sender_id <> $2 AND ` + unreadPredicate + `)` + target.visible
}

// MarkRead moves the message cursor to lastReadMessageID, or to the newest
// visible message when it is nil, and returns the conversation's read state
// as it stands afterwards. A missing conversation, one the caller may not
// read, and a message outside it all answer the same ErrNotFound.
func (s *PGXConversationReadStateStore) MarkRead(ctx context.Context, workspaceID, userID, targetType, targetID string, lastReadMessageID *string) (domain.ConversationReadState, error) {
	target, ok := readTargets[targetType]
	if !ok {
		return domain.ConversationReadState{}, domain.ErrInvalidInput
	}
	var allowed, resolved bool
	if err := s.pool.QueryRow(ctx, markReadQuery(target), workspaceID, userID, targetID, lastReadMessageID).Scan(&allowed, &resolved); err != nil {
		return domain.ConversationReadState{}, fmt.Errorf("mark conversation read: %w", err)
	}
	// An empty conversation has nothing to read through; a named message that
	// did not resolve is not one this caller can claim to have read.
	if !allowed || (lastReadMessageID != nil && !resolved) {
		return domain.ConversationReadState{}, domain.ErrNotFound
	}
	// A second statement, deliberately: the upsert's own effect is not visible
	// to the rest of the statement that made it. Whatever another writer did in
	// between is newer still, and equally the truth.
	rows, err := s.pool.Query(ctx, readStateSelect(target)+` AND conv.id = $3`, workspaceID, userID, targetID)
	if err != nil {
		return domain.ConversationReadState{}, fmt.Errorf("read conversation read state: %w", err)
	}
	states, err := collectReadStates(rows)
	if err != nil {
		return domain.ConversationReadState{}, err
	}
	return states[targetType+"\x00"+targetID], nil
}

// ReadStates returns the caller's read state for every conversation they may
// read, keyed by target type and id joined by a NUL.
func (s *PGXConversationReadStateStore) ReadStates(ctx context.Context, workspaceID, userID string) (map[string]domain.ConversationReadState, error) {
	rows, err := s.pool.Query(ctx,
		readStateSelect(readTargets[ConversationReadTargetChannel])+`
		UNION ALL`+readStateSelect(readTargets[ConversationReadTargetDM]),
		workspaceID, userID)
	if err != nil {
		return nil, fmt.Errorf("list conversation read states: %w", err)
	}
	return collectReadStates(rows)
}

func collectReadStates(rows pgx.Rows) (map[string]domain.ConversationReadState, error) {
	defer rows.Close()
	states := make(map[string]domain.ConversationReadState)
	for rows.Next() {
		var targetType, targetID string
		var boundary, cursorAt *time.Time
		var cursorID *string
		var count int64
		if err := rows.Scan(&targetType, &targetID, &boundary, &cursorAt, &cursorID, &count); err != nil {
			return nil, fmt.Errorf("scan conversation read state: %w", err)
		}
		states[targetType+"\x00"+targetID] = domain.ConversationReadState{
			UnreadCount: int(count),
			ReadThrough: effectiveReadThrough(boundary, cursorAt, cursorID),
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate conversation read states: %w", err)
	}
	return states, nil
}

// effectiveReadThrough is the single point both claims add up to: the cursor
// when it reaches past the boundary's instant, else the boundary as an instant
// (no message id — the informational one is never exposed as a position).
func effectiveReadThrough(boundary, cursorAt *time.Time, cursorID *string) *domain.ReadThrough {
	if cursorAt != nil && cursorID != nil && (boundary == nil || cursorAt.After(*boundary)) {
		return &domain.ReadThrough{CreatedAt: *cursorAt, MessageID: cursorID}
	}
	if boundary == nil {
		return nil
	}
	return &domain.ReadThrough{CreatedAt: *boundary}
}
