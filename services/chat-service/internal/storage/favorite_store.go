package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// messageAccessJoins and messageAccessPredicate express "the user can read
// this message right now": active workspace + active workspace membership, and
// either a channel chat.channel_visible_to_user admits for this user or an
// active DM conversation the user is an active member of. They mirror the
// visibility rules used by the message list queries and the reaction store.
// Both AddFavorite and ListFavorites re-evaluate access server-side on every
// request — favorites made before an access revocation are filtered out.
//
// The channel half delegates to chat.channel_visible_to_user rather than
// restating it, which is what makes the RF-74 guest scope reach favorites and
// pins (PGXPinStore.AddPin and RemovePin build on these two): a guest may
// favorite and pin in a channel it belongs to, and neither in one it does not.
// Pin and unpin therefore stay exactly as strong as read access and no
// stronger, which is the RF-05 decision recorded in SECURITY.md.
func messageAccessJoins(userArg string) string {
	return `
	JOIN chat.workspaces w
	  ON w.id = m.workspace_id AND w.status = 'active'
	JOIN chat.workspace_members wm
	  ON wm.workspace_id = m.workspace_id AND wm.user_id = ` + userArg + ` AND wm.status = 'active'
	LEFT JOIN chat.channels c
	  ON c.id = m.channel_id AND c.status = 'active'
	LEFT JOIN chat.dm_conversations dc
	  ON dc.id = m.dm_conversation_id AND dc.status = 'active'
	LEFT JOIN chat.dm_members dm
	  ON dm.conversation_id = m.dm_conversation_id AND dm.user_id = ` + userArg + ` AND dm.status = 'active'`
}

func messageAccessPredicate(userArg string) string {
	return `(
		(m.channel_id IS NOT NULL AND c.id IS NOT NULL AND chat.channel_visible_to_user(c.id, ` + userArg + `::uuid))
		OR (m.dm_conversation_id IS NOT NULL AND dc.id IS NOT NULL AND dm.user_id IS NOT NULL)
	)`
}

// messageVisibilityPredicate hides a message that is still being link-scanned
// from everyone except the person who wrote it (RF-21).
//
// It is one definition, applied by every query that returns message rows,
// because the alternative is what the security review found: `status` was
// checked in some reads and not in others, so a withheld message was invisible
// in the paths somebody had remembered and readable in the paths they had not —
// GET by id, channel history and DM history among them.
//
// Why it is not simply `status = 'active'`: a deleted message is deliberately
// still returned, as a tombstone, and the read paths depend on that. So this
// excludes exactly one state and leaves the rest of the contract alone.
//
// Why the sender is exempt: their client has to be able to rebuild "this is
// still being checked" after a reload, and the message is their own. The
// exemption is scoped to the row's own sender_id, so it cannot widen to anyone
// else — and it is deliberately absent from the projections (sidebar activity,
// favourites listing, last message), which stay active-only because they are
// read *about* a conversation rather than *by* an author.
func messageVisibilityPredicate(alias, userArg string) string {
	return `(` + alias + `.status <> 'pending_link_scan' OR ` + alias + `.sender_id = ` + userArg + `::uuid)`
}

// messageNotPendingPredicate excludes withheld messages outright.
//
// Used by the projections that summarise a conversation for a viewer — the
// sidebar's last-activity timestamp and the favourites list. A withheld message
// must not reorder somebody's sidebar or light up a conversation, and unlike a
// direct read there is no "it is mine" case worth serving there: the author
// already knows they just sent it.
func messageNotPendingPredicate(alias string) string {
	return alias + `.status <> 'pending_link_scan'`
}

// AddFavoriteInput identifies the message the authenticated user wants to favorite.
// UserID always comes from the authenticated context, never from the payload.
type AddFavoriteInput struct {
	WorkspaceID string
	UserID      string
	MessageID   string
}

// ListFavoritesInput identifies one page of the caller's own favorites.
type ListFavoritesInput struct {
	WorkspaceID string
	UserID      string
	// BeforeCursor, when non-nil, restricts results to favorites older than the
	// cursor (cursor CreatedAt/ID refer to favorited_at/message_id).
	BeforeCursor *MessageCursor
	// Limit is the maximum number of entries to return. 0 uses the default (50);
	// values above the maximum (100) are capped.
	Limit int
}

// ListFavoritesResult is one page of favorites, newest favorite first.
type ListFavoritesResult struct {
	Favorites  []domain.FavoriteMessage
	NextCursor *MessageCursor
}

// FavoriteStore is the persistence interface for per-user message favorites.
type FavoriteStore interface {
	// AddFavorite records the favorite when the user can currently read the
	// message. Idempotent: favoriting an already-favorited message succeeds.
	// Returns ErrNotFound when the message does not exist, is deleted, or is
	// not readable by the user — a single non-enumerating answer.
	AddFavorite(ctx context.Context, input AddFavoriteInput) error

	// RemoveFavorite deletes the user's own favorite. Idempotent: removing a
	// nonexistent favorite is a no-op. No read-access check — users can always
	// drop their own bookmarks, including after losing channel access.
	RemoveFavorite(ctx context.Context, userID, messageID string) error

	// ListFavorites returns one page of the user's favorites, newest favorite
	// first, filtered by current read access. Deleted messages (RF-14) remain
	// listed; the handler renders the standard placeholder.
	ListFavorites(ctx context.Context, input ListFavoritesInput) (ListFavoritesResult, error)
}

// PGXFavoriteStore implements FavoriteStore using a pgx connection pool.
type PGXFavoriteStore struct{ pool Pool }

// NewPGXFavoriteStore creates a PGXFavoriteStore backed by the given pool.
func NewPGXFavoriteStore(pool Pool) *PGXFavoriteStore { return &PGXFavoriteStore{pool: pool} }

func (s *PGXFavoriteStore) AddFavorite(ctx context.Context, input AddFavoriteInput) error {
	// Authorization and insert happen in one statement: the data-modifying CTE
	// only inserts when the access CTE yields a row, and the outer SELECT
	// reports authorization so an already-favorited message (ON CONFLICT DO
	// NOTHING → zero inserted rows) still returns success.
	var authorized bool
	err := s.pool.QueryRow(ctx, `
		WITH authorized AS (
			SELECT m.id
			FROM chat.messages m`+messageAccessJoins("$2")+`
			WHERE m.workspace_id = $1 AND m.id = $3 AND m.status = 'active'
			  AND `+messageAccessPredicate("$2")+`
		),
		ins AS (
			INSERT INTO chat.message_favorites (user_id, message_id)
			SELECT $2, id FROM authorized
			ON CONFLICT (user_id, message_id) DO NOTHING
			RETURNING message_id
		)
		SELECT EXISTS (SELECT 1 FROM authorized)`,
		input.WorkspaceID, input.UserID, input.MessageID,
	).Scan(&authorized)
	if err != nil {
		return fmt.Errorf("add favorite: %w", err)
	}
	if !authorized {
		// Non-enumerating: missing, deleted, and unauthorized are identical.
		return domain.ErrNotFound
	}
	return nil
}

func (s *PGXFavoriteStore) RemoveFavorite(ctx context.Context, userID, messageID string) error {
	if _, err := s.pool.Exec(ctx, `
		DELETE FROM chat.message_favorites
		WHERE user_id = $1 AND message_id = $2`,
		userID, messageID,
	); err != nil {
		return fmt.Errorf("remove favorite: %w", err)
	}
	return nil
}

func (s *PGXFavoriteStore) ListFavorites(ctx context.Context, input ListFavoritesInput) (ListFavoritesResult, error) {
	limit := resolveLimit(input.Limit)

	// listMessageColumns("m", "$2") emits an EXISTS on message_favorites that is
	// trivially true here; it is kept so favorites rows scan with the exact
	// column contract of every other message query.
	query := `
		SELECT ` + listMessageColumns("m", "$2") + `, f.created_at
		FROM chat.message_favorites f
		JOIN chat.messages m
		  ON m.id = f.message_id AND m.workspace_id = $1` + messageAccessJoins("$2") + `
		LEFT JOIN auth.users u
		  ON u.id = m.sender_id
		WHERE f.user_id = $2
		  AND ` + messageAccessPredicate("$2") + `
		  AND ` + messageNotPendingPredicate("m")

	args := []any{input.WorkspaceID, input.UserID}
	if input.BeforeCursor != nil {
		query += `
		  AND (f.created_at, f.message_id) < ($3, $4::uuid)`
		args = append(args, input.BeforeCursor.CreatedAt, input.BeforeCursor.ID)
	}
	query += fmt.Sprintf(`
		ORDER BY f.created_at DESC, f.message_id DESC
		LIMIT $%d`, len(args)+1)
	args = append(args, limit+1)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return ListFavoritesResult{}, fmt.Errorf("list favorites: %w", err)
	}
	defer rows.Close()

	var favorites []domain.FavoriteMessage
	for rows.Next() {
		var fav domain.FavoriteMessage
		var editedAt, deletedAt *time.Time
		msg := &fav.Message
		if err := rows.Scan(
			&msg.ID, &msg.WorkspaceID,
			&msg.ChannelID, &msg.DMConversationID,
			&msg.SenderID,
			(*string)(&msg.Kind), &msg.BodyText, (*string)(&msg.BodyFormat), (*string)(&msg.Status),
			&msg.ParentMessageID, &msg.ForwardedFromMessageID, &msg.ReferencedMessageID,
			&editedAt, &msg.EditCount, &deletedAt,
			&msg.CreatedAt, &msg.UpdatedAt,
			(*string)(&msg.LinkSafety),
			&msg.SenderDisplayName, &msg.SenderEmail,
			&msg.IsFavorited,
			&fav.FavoritedAt,
		); err != nil {
			return ListFavoritesResult{}, fmt.Errorf("scan favorite row: %w", err)
		}
		if editedAt != nil {
			msg.EditedAt = *editedAt
		}
		if deletedAt != nil {
			msg.DeletedAt = *deletedAt
		}
		favorites = append(favorites, fav)
	}
	if err := rows.Err(); err != nil {
		return ListFavoritesResult{}, fmt.Errorf("iterate favorite rows: %w", err)
	}

	var nextCursor *MessageCursor
	if len(favorites) > limit {
		favorites = favorites[:limit]
		oldest := favorites[len(favorites)-1]
		nextCursor = &MessageCursor{CreatedAt: oldest.FavoritedAt, ID: oldest.Message.ID}
	}
	return ListFavoritesResult{Favorites: favorites, NextCursor: nextCursor}, nil
}
