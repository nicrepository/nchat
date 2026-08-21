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

func (s *PGXSearchStore) Messages(ctx context.Context, userID, query string, limit int, c domain.MessageCursor) ([]domain.MessageResult, error) {
	sql := `WITH search_scope AS (
 SELECT w.id AS workspace_id FROM chat.workspaces w
 JOIN chat.workspace_members wm ON wm.workspace_id=w.id AND wm.user_id=$1 AND wm.status='active'
 JOIN auth.users caller ON caller.id=wm.user_id AND caller.status='active' AND caller.deleted_at IS NULL
 WHERE w.slug='default' AND w.status='active' LIMIT 1
), search_query AS (SELECT plainto_tsquery('portuguese',$2) AS query), ranked AS (
 SELECT m.id,m.channel_id,c.display_name AS channel_name,m.sender_id,` + authsession.DisplayNameExpr + ` AS sender_name,m.body_text,m.created_at,
 chat.message_search_rank(m.search_vector,search_query.query,m.created_at) AS score
 FROM chat.messages m JOIN search_scope scope ON scope.workspace_id=m.workspace_id
 JOIN chat.channels c ON c.id=m.channel_id AND c.workspace_id=scope.workspace_id AND c.type='public' AND c.status='active'
 JOIN auth.users u ON u.id=m.sender_id CROSS JOIN search_query
 WHERE m.status='active' AND m.search_vector IS NOT NULL AND search_vector @@ search_query.query
) SELECT id,channel_id,channel_name,sender_id,sender_name,body_text,created_at,score FROM ranked
 WHERE (NOT $4 OR (score,created_at,id)<($5,$6,$7::uuid)) ORDER BY score DESC,created_at DESC,id DESC LIMIT $3`
	var score, createdAt, cursorID any
	if c.Version != 0 {
		score, createdAt, cursorID = c.Score, c.CreatedAt, c.ID
	}
	rows, err := s.pool.Query(ctx, sql, userID, query, limit, c.Version != 0, score, createdAt, cursorID)
	if err != nil {
		return nil, fmt.Errorf("query messages: %w", err)
	}
	defer rows.Close()
	out := make([]domain.MessageResult, 0)
	for rows.Next() {
		var v domain.MessageResult
		if err := rows.Scan(&v.ID, &v.ChannelID, &v.ChannelName, &v.SenderID, &v.SenderDisplayName, &v.BodyText, &v.CreatedAt, &v.Score); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate messages: %w", err)
	}
	return out, nil
}

func (s *PGXSearchStore) Users(ctx context.Context, userID, query string, limit int, c domain.NameCursor) ([]domain.UserResult, error) {
	sql := `WITH search_scope AS (SELECT w.id AS workspace_id FROM chat.workspaces w JOIN chat.workspace_members caller_membership ON caller_membership.workspace_id=w.id AND caller_membership.user_id=$1 AND caller_membership.status='active' JOIN auth.users caller ON caller.id=caller_membership.user_id AND caller.status='active' AND caller.deleted_at IS NULL WHERE w.slug='default' AND w.status='active' LIMIT 1) SELECT u.id,COALESCE(NULLIF(BTRIM(u.full_name), ''),NULLIF(BTRIM(u.display_name), ''),'') AS display_name,u.avatar_url,LOWER(COALESCE(NULLIF(BTRIM(u.full_name), ''),NULLIF(BTRIM(u.display_name), ''),'')) AS sort_name
 FROM search_scope scope JOIN chat.workspace_members wm ON wm.workspace_id=scope.workspace_id AND wm.status='active'
 JOIN auth.users u ON u.id=wm.user_id AND u.status='active' AND u.deleted_at IS NULL
 WHERE LOWER(COALESCE(NULLIF(BTRIM(u.full_name), ''),NULLIF(BTRIM(u.display_name), ''),'')) LIKE $2 ESCAPE '\' AND (NOT $4 OR (LOWER(COALESCE(NULLIF(BTRIM(u.full_name), ''),NULLIF(BTRIM(u.display_name), ''),'')),u.id)>($5,$6::uuid))
 ORDER BY sort_name ASC,u.id ASC LIMIT $3`
	name, cursorID := nameCursorValues(c)
	rows, err := s.pool.Query(ctx, sql, userID, likeQuery(query), limit, c.Version != 0, name, cursorID)
	if err != nil {
		return nil, fmt.Errorf("query users: %w", err)
	}
	defer rows.Close()
	out := make([]domain.UserResult, 0)
	for rows.Next() {
		var v domain.UserResult
		if err := rows.Scan(&v.ID, &v.DisplayName, &v.AvatarURL, &v.SortName); err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate users: %w", err)
	}
	return out, nil
}

func (s *PGXSearchStore) Channels(ctx context.Context, userID, query string, limit int, c domain.NameCursor) ([]domain.ChannelResult, error) {
	sql := `WITH search_scope AS (SELECT w.id AS workspace_id FROM chat.workspaces w JOIN chat.workspace_members wm ON wm.workspace_id=w.id AND wm.user_id=$1 AND wm.status='active' JOIN auth.users caller ON caller.id=wm.user_id AND caller.status='active' AND caller.deleted_at IS NULL WHERE w.slug='default' AND w.status='active' LIMIT 1) SELECT ch.id,ch.slug,ch.display_name,ch.is_general,LOWER(ch.display_name) AS sort_name FROM search_scope scope
 JOIN chat.channels ch ON ch.workspace_id=scope.workspace_id AND ch.type='public' AND ch.status='active'
 WHERE (LOWER(ch.display_name) LIKE $2 ESCAPE '\' OR LOWER(ch.slug) LIKE $2 ESCAPE '\') AND (NOT $4 OR (LOWER(ch.display_name),ch.id)>($5,$6::uuid))
 ORDER BY sort_name ASC,ch.id ASC LIMIT $3`
	name, cursorID := nameCursorValues(c)
	rows, err := s.pool.Query(ctx, sql, userID, likeQuery(query), limit, c.Version != 0, name, cursorID)
	if err != nil {
		return nil, fmt.Errorf("query channels: %w", err)
	}
	defer rows.Close()
	out := make([]domain.ChannelResult, 0)
	for rows.Next() {
		var v domain.ChannelResult
		if err := rows.Scan(&v.ID, &v.Slug, &v.DisplayName, &v.IsGeneral, &v.SortName); err != nil {
			return nil, fmt.Errorf("scan channel: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate channels: %w", err)
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
