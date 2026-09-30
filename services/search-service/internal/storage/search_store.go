package storage

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/nicrepository/nchat/libs/go/platform/authsession"
	"github.com/nicrepository/nchat/services/search-service/internal/domain"
)

type Queryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}
type PGXSearchStore struct{ pool Queryer }

func NewPGXSearchStore(pool Queryer) *PGXSearchStore { return &PGXSearchStore{pool: pool} }

// Authorization lives in these three CTEs and nowhere else. Every query binds
// the caller as $1 — the authenticated principal, never a client value — and
// every category reads conversations only through them, so a message, a file,
// a channel and a count can never disagree about what the caller may see.
//
// Groups and Files read them through materialized() (measured, #900 review;
// docs/api/search.md): inlined, the planner estimated `LIKE '%term%'` at a
// handful of rows, started from the workspace-wide table and probed
// visibility once per match — ~50k probes at 50k groups or files for a common
// term. Computed first, the caller's scope drives the query. Messages and
// Channels keep them inlined: the GIN index already narrows messages, and
// materializing measured slower there.
//
// searchScopeCTE is the caller's active workspace and role; an inactive caller
// or membership resolves no row and therefore every search returns nothing.
const searchScopeCTE = `search_scope AS (
 SELECT w.id AS workspace_id, wm.role FROM chat.workspaces w
 JOIN chat.workspace_members wm ON wm.workspace_id=w.id AND wm.user_id=$1 AND wm.status='active'
 JOIN auth.users caller ON caller.id=wm.user_id AND caller.status='active' AND caller.deleted_at IS NULL
 WHERE w.slug='default' AND w.status='active' LIMIT 1)`

// visibleChannelsCTE is chat.channel_visible_to_user (chat migration 000022)
// restricted to active channels of the scope: a channel member always, and a
// non-guest workspace member for a public channel. A private channel without
// membership, and any channel for a guest who is not a member, is absent.
const visibleChannelsCTE = `visible_channels AS (
 SELECT c.id, c.workspace_id, c.slug, c.display_name, c.type, c.description, c.is_general
 FROM search_scope scope
 JOIN chat.channels c ON c.workspace_id=scope.workspace_id AND c.status='active'
 WHERE EXISTS (SELECT 1 FROM chat.channel_members cm WHERE cm.channel_id=c.id AND cm.user_id=$1)
    OR (c.type='public' AND scope.role IN ('owner','admin','moderator','member')))`

// visibleDMsCTE is chat-service's GetVisibleConversationByID: an active
// direct or group conversation of the scope with the caller's active
// dm_members row. Workspace membership alone never grants a conversation.
const visibleDMsCTE = `visible_dms AS (
 SELECT dc.id, dc.workspace_id, dc.type, dc.title
 FROM search_scope scope
 JOIN chat.dm_conversations dc ON dc.workspace_id=scope.workspace_id AND dc.status='active'
 JOIN chat.dm_members dm ON dm.conversation_id=dc.id AND dm.user_id=$1 AND dm.status='active')`

const visibleConversationsCTEs = searchScopeCTE + `, ` + visibleChannelsCTE + `, ` + visibleDMsCTE

// materialized turns the authorization CTE heads above into MATERIALIZED ones.
func materialized(ctes string) string {
	return strings.ReplaceAll(ctes, " AS (\n", " AS MATERIALIZED (\n")
}

// conversationJoin attaches a message row `m` to the caller's view of its
// conversation. A row matching neither join is a conversation the caller may
// not read and is dropped by conversationVisible.
const conversationJoin = ` LEFT JOIN visible_channels vc ON vc.id=m.channel_id
 LEFT JOIN visible_dms vd ON vd.id=m.dm_conversation_id`
const conversationVisible = `(vc.id IS NOT NULL OR vd.id IS NOT NULL)`
const conversationColumns = `CASE WHEN vc.id IS NOT NULL THEN 'channel' ELSE 'dm' END AS conversation_kind,
 COALESCE(vc.id, vd.id) AS conversation_id, COALESCE(vc.type, vd.type) AS conversation_type,
 vc.display_name AS channel_name, vd.title AS dm_title`

// conversationNameExpr names a page row `p` the way the sidebar does: the
// channel name, the group title, or the other participant of a direct
// conversation. It runs on the already-limited page only, never per match.
const conversationNameExpr = `COALESCE(p.channel_name, CASE WHEN p.conversation_type='group' THEN NULLIF(BTRIM(p.dm_title), '')
 ELSE (SELECT ` + authsession.DisplayNameExpr + ` FROM chat.dm_members o JOIN auth.users u ON u.id=o.user_id
  WHERE o.conversation_id=p.conversation_id AND o.status='active' AND o.user_id<>$1 ORDER BY o.user_id LIMIT 1) END, '')`

// Messages ranks full-text matches across every conversation the caller can
// read. The GIN index narrows the rows; visibility is decided by the joins.
// c.RankedAt is the ranking clock and is always set by the service.
func (s *PGXSearchStore) Messages(ctx context.Context, userID, query string, limit int, c domain.MessageCursor) ([]domain.MessageResult, error) {
	sql := `WITH ` + visibleConversationsCTEs + `, search_query AS (SELECT plainto_tsquery('portuguese',$2) AS query),
 ranked AS (
 SELECT m.id, m.sender_id, m.body_text, m.created_at, ` + conversationColumns + `,
 chat.message_search_rank(m.search_vector,search_query.query,m.created_at,$8) AS score
 FROM chat.messages m` + conversationJoin + ` CROSS JOIN search_query
 WHERE m.status='active' AND m.search_vector IS NOT NULL AND m.search_vector @@ search_query.query AND ` + conversationVisible + `
), p AS (
 SELECT * FROM ranked WHERE (NOT $4 OR (score,created_at,id)<($5,$6,$7::uuid))
 ORDER BY score DESC,created_at DESC,id DESC LIMIT $3
) SELECT p.id, p.conversation_kind, p.conversation_id, p.conversation_type, ` + conversationNameExpr + `,
 p.sender_id, ` + authsession.DisplayNameExpr + `, u.avatar_url, p.body_text, p.created_at, p.score
 FROM p JOIN auth.users u ON u.id=p.sender_id
 ORDER BY p.score DESC,p.created_at DESC,p.id DESC`
	var score, createdAt, cursorID any
	if c.Version != 0 {
		score, createdAt, cursorID = c.Score, c.CreatedAt, c.ID
	}
	return collect(ctx, s.pool, "messages", sql, func(rows pgx.Rows) (domain.MessageResult, error) {
		var v domain.MessageResult
		err := rows.Scan(&v.ID, &v.ConversationKind, &v.ConversationID, &v.ConversationType, &v.ConversationName,
			&v.SenderID, &v.SenderDisplayName, &v.SenderAvatarURL, &v.BodyText, &v.CreatedAt, &v.Score)
		return v, err
	}, userID, query, limit, c.Version != 0, score, createdAt, cursorID, c.RankedAt)
}

// LegacyMessages is the pre-#900 message search, kept for clients that route
// every result to a channel: active public channels only, the caller's view of
// them decided by the same visible_channels predicate, ranked against now()
// exactly as before so its cursors stay interchangeable with the previous
// release. It can never return a DM, a group or a private channel.
func (s *PGXSearchStore) LegacyMessages(ctx context.Context, userID, query string, limit int, c domain.LegacyMessageCursor) ([]domain.LegacyMessageResult, error) {
	sql := `WITH ` + searchScopeCTE + `, ` + visibleChannelsCTE + `, search_query AS (SELECT plainto_tsquery('portuguese',$2) AS query),
 ranked AS (
 SELECT m.id, m.channel_id, vc.display_name AS channel_name, m.sender_id, ` + authsession.DisplayNameExpr + ` AS sender_name, m.body_text, m.created_at,
 chat.message_search_rank(m.search_vector,search_query.query,m.created_at) AS score
 FROM chat.messages m JOIN visible_channels vc ON vc.id=m.channel_id AND vc.type='public'
 JOIN auth.users u ON u.id=m.sender_id CROSS JOIN search_query
 WHERE m.status='active' AND m.search_vector IS NOT NULL AND m.search_vector @@ search_query.query
) SELECT id,channel_id,channel_name,sender_id,sender_name,body_text,created_at,score FROM ranked
 WHERE (NOT $4 OR (score,created_at,id)<($5,$6,$7::uuid)) ORDER BY score DESC,created_at DESC,id DESC LIMIT $3`
	var score, createdAt, cursorID any
	if c.Version != 0 {
		score, createdAt, cursorID = c.Score, c.CreatedAt, c.ID
	}
	return collect(ctx, s.pool, "legacy messages", sql, func(rows pgx.Rows) (domain.LegacyMessageResult, error) {
		var v domain.LegacyMessageResult
		err := rows.Scan(&v.ID, &v.ChannelID, &v.ChannelName, &v.SenderID, &v.SenderDisplayName, &v.BodyText, &v.CreatedAt, &v.Score)
		return v, err
	}, userID, query, limit, c.Version != 0, score, createdAt, cursorID)
}

func (s *PGXSearchStore) Users(ctx context.Context, userID, query string, limit int, c domain.NameCursor) ([]domain.UserResult, error) {
	sql := `WITH ` + searchScopeCTE + ` SELECT u.id,COALESCE(NULLIF(BTRIM(u.full_name), ''),NULLIF(BTRIM(u.display_name), ''),'') AS display_name,u.avatar_url,LOWER(COALESCE(NULLIF(BTRIM(u.full_name), ''),NULLIF(BTRIM(u.display_name), ''),'')) AS sort_name
 FROM search_scope scope JOIN chat.workspace_members wm ON wm.workspace_id=scope.workspace_id AND wm.status='active'
 JOIN auth.users u ON u.id=wm.user_id AND u.status='active' AND u.deleted_at IS NULL
 WHERE LOWER(COALESCE(NULLIF(BTRIM(u.full_name), ''),NULLIF(BTRIM(u.display_name), ''),'')) LIKE $2 ESCAPE '\' AND (NOT $4 OR (LOWER(COALESCE(NULLIF(BTRIM(u.full_name), ''),NULLIF(BTRIM(u.display_name), ''),'')),u.id)>($5,$6::uuid))
 ORDER BY sort_name ASC,u.id ASC LIMIT $3`
	name, cursorID := nameCursorValues(c)
	return collect(ctx, s.pool, "users", sql, func(rows pgx.Rows) (domain.UserResult, error) {
		var v domain.UserResult
		err := rows.Scan(&v.ID, &v.DisplayName, &v.AvatarURL, &v.SortName)
		return v, err
	}, userID, likeQuery(query), limit, c.Version != 0, name, cursorID)
}

// Channels matches visible channels by name or slug. member_count is counted
// for the page rows only, under the channel-details predicate (#877).
func (s *PGXSearchStore) Channels(ctx context.Context, userID, query string, limit int, c domain.NameCursor) ([]domain.ChannelResult, error) {
	sql := `WITH ` + searchScopeCTE + `, ` + visibleChannelsCTE + `, p AS (
 SELECT vc.*, LOWER(vc.display_name) AS sort_name FROM visible_channels vc
 WHERE (LOWER(vc.display_name) LIKE $2 ESCAPE '\' OR LOWER(vc.slug) LIKE $2 ESCAPE '\') AND (NOT $4 OR (LOWER(vc.display_name),vc.id)>($5,$6::uuid))
 ORDER BY sort_name ASC,vc.id ASC LIMIT $3
) SELECT p.id, p.slug, p.display_name, p.type, NULLIF(BTRIM(p.description), ''), (
 SELECT count(*) FROM chat.channel_members cm
 JOIN chat.workspace_members wm ON wm.workspace_id=p.workspace_id AND wm.user_id=cm.user_id AND wm.status='active'
 JOIN auth.users u ON u.id=cm.user_id AND u.status='active' AND u.deleted_at IS NULL
 WHERE cm.channel_id=p.id), p.is_general, p.sort_name
 FROM p ORDER BY p.sort_name ASC,p.id ASC`
	name, cursorID := nameCursorValues(c)
	return collect(ctx, s.pool, "channels", sql, func(rows pgx.Rows) (domain.ChannelResult, error) {
		var v domain.ChannelResult
		err := rows.Scan(&v.ID, &v.Slug, &v.DisplayName, &v.Type, &v.Description, &v.MemberCount, &v.IsGeneral, &v.SortName)
		return v, err
	}, userID, likeQuery(query), limit, c.Version != 0, name, cursorID)
}

// Groups matches the titles of groups the caller participates in. The count
// is the group-details predicate; last_message_at is the sidebar's activity
// signal. Both are computed for the page rows only.
func (s *PGXSearchStore) Groups(ctx context.Context, userID, query string, limit int, c domain.NameCursor) ([]domain.GroupResult, error) {
	sql := `WITH ` + materialized(searchScopeCTE+`, `+visibleDMsCTE) + `, p AS (
 SELECT vd.id, vd.workspace_id, BTRIM(vd.title) AS title, LOWER(BTRIM(vd.title)) AS sort_name FROM visible_dms vd
 WHERE vd.type='group' AND LOWER(BTRIM(COALESCE(vd.title, ''))) LIKE $2 ESCAPE '\' AND (NOT $4 OR (LOWER(BTRIM(vd.title)),vd.id)>($5,$6::uuid))
 ORDER BY sort_name ASC,vd.id ASC LIMIT $3
) SELECT p.id, p.title, (
 SELECT count(*) FROM chat.dm_members dm
 JOIN chat.workspace_members wm ON wm.workspace_id=p.workspace_id AND wm.user_id=dm.user_id AND wm.status='active'
 JOIN auth.users u ON u.id=dm.user_id AND u.status='active' AND u.deleted_at IS NULL
 WHERE dm.conversation_id=p.id AND dm.status='active'), (
 SELECT m.created_at FROM chat.messages m
 WHERE m.workspace_id=p.workspace_id AND m.dm_conversation_id=p.id AND m.status<>'pending_link_scan'
 ORDER BY m.created_at DESC, m.id DESC LIMIT 1), p.sort_name
 FROM p ORDER BY p.sort_name ASC,p.id ASC`
	name, cursorID := nameCursorValues(c)
	return collect(ctx, s.pool, "groups", sql, func(rows pgx.Rows) (domain.GroupResult, error) {
		var v domain.GroupResult
		err := rows.Scan(&v.ID, &v.Title, &v.ParticipantCount, &v.LastMessageAt, &v.SortName)
		return v, err
	}, userID, likeQuery(query), limit, c.Version != 0, name, cursorID)
}

// Files matches filenames of attachments sent in a conversation the caller can
// read. Each branch starts from the caller's visible conversations and, per
// conversation (LATERAL), reads its attachments through the existing
// per-destination partial indexes (idx_attachments_channel /
// idx_attachments_conversation), so only files the caller could open are ever
// filtered by name. LATERAL with OFFSET 0 (which keeps PostgreSQL from pulling
// the subquery back up) is what pins that order: the planner estimates
// `LIKE '%term%'` at a couple of rows and, left free, scanned every attachment
// of the workspace and joined the scope afterwards (measured: 7 s at 50k
// files, docs/api/search.md). The message that carries the attachment must be
// active and in that same conversation. Only states a message can show are
// listed; storage keys and key material are not in the projection.
func (s *PGXSearchStore) Files(ctx context.Context, userID, query string, limit int, c domain.TimeCursor) ([]domain.FileResult, error) {
	sql := `WITH ` + materialized(visibleConversationsCTEs) + `, hits AS (
 ` + fileBranch("channel", "visible_channels", "channel_id", "channel_id", "v.display_name", "NULL") + `
 UNION ALL
 ` + fileBranch("dm", "visible_dms", "conversation_id", "dm_conversation_id", "NULL", "v.title") + `
), p AS (
 SELECT * FROM hits WHERE (NOT $4 OR (created_at,id)<($5,$6::uuid))
 ORDER BY created_at DESC,id DESC LIMIT $3
) SELECT p.id, p.original_filename, p.content_type, p.size_bytes, p.status, p.preview_status, p.message_id,
 p.conversation_kind, p.conversation_id, p.conversation_type, ` + conversationNameExpr + `, p.created_at
 FROM p ORDER BY p.created_at DESC,p.id DESC`
	var createdAt, cursorID any
	if c.Version != 0 {
		createdAt, cursorID = c.CreatedAt, c.ID
	}
	return collect(ctx, s.pool, "files", sql, func(rows pgx.Rows) (domain.FileResult, error) {
		var v domain.FileResult
		err := rows.Scan(&v.ID, &v.Filename, &v.ContentType, &v.SizeBytes, &v.Status, &v.PreviewStatus, &v.MessageID,
			&v.ConversationKind, &v.ConversationID, &v.ConversationType, &v.ConversationName, &v.CreatedAt)
		return v, err
	}, userID, likeQuery(query), limit, c.Version != 0, createdAt, cursorID)
}

// fileBranch reads one destination kind's attachments from its visible
// conversations `v`. Every argument is a fixed identifier from Files, never
// input.
func fileBranch(kind, visible, attachmentColumn, messageColumn, channelName, dmTitle string) string {
	return `SELECT a.id, a.original_filename, COALESCE(NULLIF(a.detected_mime, ''), a.declared_mime) AS content_type,
 a.size_bytes, a.status, a.preview_status, m.id AS message_id, m.created_at,
 '` + kind + `' AS conversation_kind, v.id AS conversation_id, v.type AS conversation_type,
 ` + channelName + `::text AS channel_name, ` + dmTitle + `::text AS dm_title
 FROM ` + visible + ` v
 CROSS JOIN LATERAL (
  SELECT a.* FROM files.attachments a
  WHERE a.workspace_id=v.workspace_id AND a.destination_kind='` + kind + `' AND a.` + attachmentColumn + `=v.id
   AND a.deleted_at IS NULL AND a.status IN ('pending_scan','clean','rejected')
   AND LOWER(a.original_filename) LIKE $2 ESCAPE '\'
  OFFSET 0
 ) a
 JOIN chat.message_attachments ma ON ma.attachment_id=a.id
 JOIN chat.messages m ON m.id=ma.message_id AND m.` + messageColumn + `=v.id AND m.status='active'`
}

func collect[T any](ctx context.Context, pool Queryer, label, sql string, scan func(pgx.Rows) (T, error), args ...any) ([]T, error) {
	rows, err := pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("query %s: %w", label, err)
	}
	defer rows.Close()
	out := make([]T, 0)
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("scan %s: %w", label, err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s: %w", label, err)
	}
	return out, nil
}

func likeQuery(q string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + strings.ToLower(replacer.Replace(q)) + "%"
}

func nameCursorValues(c domain.NameCursor) (any, any) {
	if c.Version == 0 {
		return nil, nil
	}
	return c.Name, c.ID
}
