package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// CreateDirectConversationInput holds storage-only fields for creating or
// reactivating a canonical 1:1 DM.
type CreateDirectConversationInput struct {
	WorkspaceID        string
	CreatedBy          string
	DirectPairKey      string
	ParticipantUserIDs []string
}

type CreateDirectConversationResult struct {
	Conversation domain.DMConversation
	Created      bool
}

// CreateGroupConversationInput holds storage-only fields for group DM creation.
type CreateGroupConversationInput struct {
	WorkspaceID        string
	CreatedBy          string
	Title              string
	ParticipantUserIDs []string
}

// DMStore is the persistence interface for direct and group DM conversations.
type DMStore interface {
	CreateDirectConversation(ctx context.Context, input CreateDirectConversationInput) (CreateDirectConversationResult, error)
	CreateGroupConversation(ctx context.Context, input CreateGroupConversationInput) (domain.DMConversation, error)
	ListVisibleConversationsByUser(ctx context.Context, workspaceID, userID string) ([]domain.DMConversation, error)
	// ListVisibleConversationsWithParticipantIDs returns active DM conversations
	// visible to userID, each annotated with the full list of active member user
	// IDs and, for direct 1:1 conversations, the display name of the other
	// participant as seen by userID. A single SQL query (no N+1) is used.
	ListVisibleConversationsWithParticipantIDs(ctx context.Context, workspaceID, userID string) ([]domain.DMConversationWithParticipantIDs, error)
	GetVisibleConversationByID(ctx context.Context, workspaceID, conversationID, userID string) (domain.DMConversation, error)
	// ListParticipantProfiles returns up to limit active participants of
	// conversationID in workspaceID plus the total number of active
	// participants, in one round trip. The caller's access to the conversation
	// must already have been settled.
	ListParticipantProfiles(ctx context.Context, workspaceID, conversationID string, limit int) (DMParticipantPage, error)
}

// DMParticipantPage is one capped page of a conversation's participants plus the
// total the same predicate matches. TotalCount is the authoritative figure the
// UI displays; len(Participants) is only how many fit in the page and must never
// be shown as the participant count.
type DMParticipantPage struct {
	Participants []domain.DMParticipantProfile
	TotalCount   int
}

// ListParticipantProfiles returns a capped page of a conversation's active
// participants and the full total, in a single query.
//
// Every predicate that makes a participant "real" is applied here and nowhere
// else: the conversation must be active and belong to workspaceID, the
// participant's dm_members row must be status 'active' (so someone who left is
// gone), they must still be an active member of the workspace, and their
// account must be active and not deleted. That is the same shape
// GetVisibleConversationByID uses to decide the caller may read the
// conversation at all, so "who is in this group" cannot mean one thing for
// access and another for display.
//
// The dc.workspace_id filter is what keeps a conversation UUID from another
// tenant from ever resolving here, and the conversation_id filter is what keeps
// participants of another group out of this one's list.
//
// Unlike the channel panel, presence does not select rows: a group lists every
// active participant and the caller annotates presence afterwards, so an
// offline participant is never dropped and never loses a slot.
//
// COUNT(*) OVER () is evaluated before LIMIT, so the total describes the whole
// matching set and not the page; a second COUNT query would be a second chance
// to drift from this one's predicate.
func (s *PGXDMStore) ListParticipantProfiles(
	ctx context.Context, workspaceID, conversationID string, limit int,
) (DMParticipantPage, error) {
	if limit <= 0 || limit > domain.MaxDMDetailsParticipants {
		limit = domain.MaxDMDetailsParticipants
	}
	rows, err := s.pool.Query(ctx, `
		SELECT u.id::text,
		       COALESCE(
		           NULLIF(BTRIM(u.full_name), ''),
		           NULLIF(BTRIM(u.display_name), ''),
		           ''
		       ) AS display_name,
		       COALESCE(u.avatar_url, '') AS avatar_url,
		       COUNT(*) OVER () AS total_count
		FROM chat.dm_members dm
		JOIN chat.dm_conversations dc
		  ON dc.id = dm.conversation_id
		 AND dc.workspace_id = $1::uuid
		 AND dc.status = 'active'
		JOIN chat.workspace_members wm
		  ON wm.workspace_id = dc.workspace_id
		 AND wm.user_id = dm.user_id
		 AND wm.status = 'active'
		JOIN auth.users u ON u.id = dm.user_id AND u.status = 'active' AND u.deleted_at IS NULL
		WHERE dm.conversation_id = $2::uuid
		  AND dm.status = 'active'
		ORDER BY lower(u.display_name), u.id
		LIMIT $3`, workspaceID, conversationID, limit)
	if err != nil {
		return DMParticipantPage{}, fmt.Errorf("list dm participant profiles: %w", err)
	}
	defer rows.Close()

	page := DMParticipantPage{Participants: make([]domain.DMParticipantProfile, 0, limit)}
	for rows.Next() {
		var profile domain.DMParticipantProfile
		var total int
		if err := rows.Scan(&profile.UserID, &profile.DisplayName, &profile.AvatarURL, &total); err != nil {
			return DMParticipantPage{}, fmt.Errorf("scan dm participant profile: %w", err)
		}
		page.TotalCount = total
		page.Participants = append(page.Participants, profile)
	}
	if err := rows.Err(); err != nil {
		return DMParticipantPage{}, fmt.Errorf("iterate dm participant profiles: %w", err)
	}
	return page, nil
}

// PGXDMStore implements DMStore using a pgx connection pool.
type PGXDMStore struct {
	pool Pool
}

func NewPGXDMStore(pool Pool) *PGXDMStore {
	return &PGXDMStore{pool: pool}
}

func (s *PGXDMStore) CreateDirectConversation(ctx context.Context, input CreateDirectConversationInput) (CreateDirectConversationResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return CreateDirectConversationResult{}, fmt.Errorf("begin create direct conversation: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	result, err := createDirectConversation(ctx, tx, input)
	if err != nil {
		return CreateDirectConversationResult{}, err
	}
	if err := upsertEligibleDMMembers(ctx, tx, result.Conversation.ID, input.WorkspaceID, input.ParticipantUserIDs); err != nil {
		return CreateDirectConversationResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return CreateDirectConversationResult{}, fmt.Errorf("commit create direct conversation: %w", err)
	}
	committed = true
	return result, nil
}

func createDirectConversation(ctx context.Context, q dmQuerier, input CreateDirectConversationInput) (CreateDirectConversationResult, error) {
	var conversation domain.DMConversation
	err := q.QueryRow(ctx, `
		INSERT INTO chat.dm_conversations
			(workspace_id, type, title, status, created_by, direct_pair_key)
		VALUES ($1, 'direct', NULL, 'active', $2, $3)
		ON CONFLICT (workspace_id, direct_pair_key) WHERE type = 'direct'
		DO NOTHING
		RETURNING id, workspace_id, type, COALESCE(title, ''), status, created_by,
		          created_at, updated_at`,
		input.WorkspaceID, input.CreatedBy, input.DirectPairKey,
	).Scan(
		&conversation.ID, &conversation.WorkspaceID, (*string)(&conversation.Type),
		&conversation.Title, (*string)(&conversation.Status), &conversation.CreatedBy,
		&conversation.CreatedAt, &conversation.UpdatedAt,
	)
	if err == nil {
		return CreateDirectConversationResult{Conversation: conversation, Created: true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return CreateDirectConversationResult{}, fmt.Errorf("create direct conversation: %w", err)
	}

	err = q.QueryRow(ctx, `
		UPDATE chat.dm_conversations
		SET status = 'active', updated_at = now()
		WHERE workspace_id = $1
		  AND direct_pair_key = $2
		  AND type = 'direct'
		RETURNING id, workspace_id, type, COALESCE(title, ''), status, created_by,
		          created_at, updated_at`,
		input.WorkspaceID, input.DirectPairKey,
	).Scan(
		&conversation.ID, &conversation.WorkspaceID, (*string)(&conversation.Type),
		&conversation.Title, (*string)(&conversation.Status), &conversation.CreatedBy,
		&conversation.CreatedAt, &conversation.UpdatedAt,
	)
	if err != nil {
		return CreateDirectConversationResult{}, fmt.Errorf("get existing direct conversation: %w", err)
	}
	return CreateDirectConversationResult{Conversation: conversation}, nil
}

func (s *PGXDMStore) CreateGroupConversation(ctx context.Context, input CreateGroupConversationInput) (domain.DMConversation, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.DMConversation{}, fmt.Errorf("begin create group conversation: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	conversation, err := createGroupConversation(ctx, tx, input)
	if err != nil {
		return domain.DMConversation{}, err
	}
	if err := upsertEligibleDMMembers(ctx, tx, conversation.ID, input.WorkspaceID, input.ParticipantUserIDs); err != nil {
		return domain.DMConversation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.DMConversation{}, fmt.Errorf("commit create group conversation: %w", err)
	}
	committed = true
	return conversation, nil
}

func createGroupConversation(ctx context.Context, q dmQuerier, input CreateGroupConversationInput) (domain.DMConversation, error) {
	var title *string
	if input.Title != "" {
		title = &input.Title
	}

	var conversation domain.DMConversation
	err := q.QueryRow(ctx, `
		INSERT INTO chat.dm_conversations
			(workspace_id, type, title, status, created_by)
		VALUES ($1, 'group', $2, 'active', $3)
		RETURNING id, workspace_id, type, COALESCE(title, ''), status, created_by,
		          created_at, updated_at`,
		input.WorkspaceID, title, input.CreatedBy,
	).Scan(
		&conversation.ID, &conversation.WorkspaceID, (*string)(&conversation.Type),
		&conversation.Title, (*string)(&conversation.Status), &conversation.CreatedBy,
		&conversation.CreatedAt, &conversation.UpdatedAt,
	)
	if err != nil {
		return domain.DMConversation{}, fmt.Errorf("create group conversation: %w", err)
	}
	return conversation, nil
}

type dmQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// upsertEligibleDMMembers adds every user in userIDs to conversationID, or none.
//
// This is the transactional backstop for DM participation, shared by the 1:1 and
// the ad-hoc group flows so both obey exactly one rule. The service layer checks
// eligibility before calling, but that check and the write are separate steps: an
// account suspended or deleted in between would otherwise still receive
// membership. Here the eligibility test and the insert are the same statement, so
// there is no window to interleave — a user who stopped being eligible simply
// produces no row.
//
// Eligibility mirrors MemberStore.GetEligibleDMMember: active workspace, active
// workspace membership, conversation in that same workspace, and an active,
// non-deleted auth.users row. Callers must pass de-duplicated IDs (both flows
// canonicalise and de-duplicate upstream); a repeated ID would make Postgres
// reject the whole statement rather than write a partial result.
//
// Fewer inserted rows than requested means at least one participant was not
// eligible. The generic domain.ErrForbidden is returned without naming them —
// the caller must not be able to probe account state — and the surrounding
// transaction is rolled back, leaving neither an orphan conversation nor a
// partial membership list.
func upsertEligibleDMMembers(ctx context.Context, q dmQuerier, conversationID, workspaceID string, userIDs []string) error {
	tag, err := q.Exec(ctx, `
		INSERT INTO chat.dm_members (conversation_id, user_id, role, status, left_at)
		SELECT $1, wm.user_id, 'member', 'active', NULL
		FROM unnest($3::uuid[]) AS candidate(user_id)
		JOIN chat.workspace_members wm
		  ON wm.workspace_id = $2 AND wm.user_id = candidate.user_id AND wm.status = 'active'
		JOIN chat.workspaces w
		  ON w.id = wm.workspace_id AND w.status = 'active'
		JOIN chat.dm_conversations dc
		  ON dc.id = $1 AND dc.workspace_id = wm.workspace_id
		JOIN auth.users u
		  ON u.id = wm.user_id AND u.status = 'active' AND u.deleted_at IS NULL
		ON CONFLICT (conversation_id, user_id)
		DO UPDATE SET role = 'member',
		              status = 'active',
		              left_at = NULL`,
		conversationID, workspaceID, userIDs,
	)
	if err != nil {
		return fmt.Errorf("upsert dm members: %w", err)
	}
	if tag.RowsAffected() != int64(len(userIDs)) {
		return domain.ErrForbidden
	}
	return nil
}

func (s *PGXDMStore) ListVisibleConversationsByUser(ctx context.Context, workspaceID, userID string) ([]domain.DMConversation, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT dc.id, dc.workspace_id, dc.type, COALESCE(dc.title, ''), dc.status,
		       dc.created_by, dc.created_at, dc.updated_at
		FROM chat.dm_conversations dc
		JOIN chat.workspaces w
		  ON w.id = dc.workspace_id AND w.status = 'active'
		JOIN chat.workspace_members wm
		  ON wm.workspace_id = dc.workspace_id AND wm.user_id = $2 AND wm.status = 'active'
		JOIN chat.dm_members dm
		  ON dm.conversation_id = dc.id AND dm.user_id = $2 AND dm.status = 'active'
		WHERE dc.workspace_id = $1
		  AND dc.status = 'active'
		ORDER BY dc.updated_at DESC, dc.created_at DESC`,
		workspaceID, userID,
	)
	if err != nil {
		return nil, fmt.Errorf("list visible dm conversations: %w", err)
	}
	defer rows.Close()

	var conversations []domain.DMConversation
	for rows.Next() {
		var conversation domain.DMConversation
		if err := rows.Scan(
			&conversation.ID, &conversation.WorkspaceID, (*string)(&conversation.Type),
			&conversation.Title, (*string)(&conversation.Status), &conversation.CreatedBy,
			&conversation.CreatedAt, &conversation.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan visible dm conversation: %w", err)
		}
		conversations = append(conversations, conversation)
	}
	return conversations, rows.Err()
}

// The counterpart identity (id, resolved name, avatar) is produced by a single
// LATERAL join in the same round-trip, so the sidebar never needs a
// per-conversation lookup and the counterpart row is scanned once, not once per
// column. It is excluded from the requesting user by comparing against
// dm.user_id — the caller's own membership row — so no client-supplied
// identifier and no UUID text formatting is involved.
// The visual name prefers full_name and falls back to display_name. full_name is
// used whenever it is present: OIDC users can receive full_name from the
// provider's profile claims (name, or given_name + family_name), and manual
// users may set it too, while display_name remains the fallback when full_name
// is absent. Blank and whitespace-only values are treated as absent.
// avatar_url is passed through verbatim and never fetched server-side; the
// client validates the scheme and falls back to initials.
// auth.users is joined without a status filter on purpose: the conversation is
// already authorized by membership, and hiding the name of a deactivated
// colleague would degrade a working conversation to a meaningless label.
// Anonymization of removed users is owned by auth-service at the source, which
// is also where avatar_url must be cleared.
// No column beyond id/full_name/display_name/avatar_url is selected: e-mail,
// status, auth source and external subject stay out of the sidebar contract.
func (s *PGXDMStore) ListVisibleConversationsWithParticipantIDs(ctx context.Context, workspaceID, userID string) ([]domain.DMConversationWithParticipantIDs, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT dc.id, dc.workspace_id, dc.type, COALESCE(dc.title, ''), dc.status,
		       dc.created_by, dc.created_at, dc.updated_at,
		       ARRAY(
		           SELECT dm2.user_id::text
		           FROM chat.dm_members dm2
		           WHERE dm2.conversation_id = dc.id AND dm2.status = 'active'
		           ORDER BY dm2.user_id
		       ) AS participant_ids,
		       COALESCE(cp.user_id, '')      AS counterpart_user_id,
		       COALESCE(cp.display_name, '') AS counterpart_display_name,
		       COALESCE(cp.avatar_url, '')   AS counterpart_avatar_url
		FROM chat.dm_conversations dc
		JOIN chat.workspaces w
		  ON w.id = dc.workspace_id AND w.status = 'active'
		JOIN chat.workspace_members wm
		  ON wm.workspace_id = dc.workspace_id AND wm.user_id = $2 AND wm.status = 'active'
		JOIN chat.dm_members dm
		  ON dm.conversation_id = dc.id AND dm.user_id = $2 AND dm.status = 'active'
		LEFT JOIN LATERAL (
		    SELECT other.user_id::text AS user_id,
		           COALESCE(
		               NULLIF(BTRIM(u.full_name), ''),
		               NULLIF(BTRIM(u.display_name), '')
		           ) AS display_name,
		           u.avatar_url AS avatar_url
		    FROM chat.dm_members other
		    JOIN auth.users u ON u.id = other.user_id
		    WHERE dc.type = 'direct'
		      AND other.conversation_id = dc.id
		      AND other.status = 'active'
		      AND other.user_id <> dm.user_id
		    ORDER BY other.user_id
		    LIMIT 1
		) cp ON true
		WHERE dc.workspace_id = $1
		  AND dc.status = 'active'
		ORDER BY dc.updated_at DESC, dc.created_at DESC`,
		workspaceID, userID,
	)
	if err != nil {
		return nil, fmt.Errorf("list visible dm conversations with participants: %w", err)
	}
	defer rows.Close()

	var conversations []domain.DMConversationWithParticipantIDs
	for rows.Next() {
		var c domain.DMConversationWithParticipantIDs
		if err := rows.Scan(
			&c.ID, &c.WorkspaceID, (*string)(&c.Type),
			&c.Title, (*string)(&c.Status), &c.CreatedBy,
			&c.CreatedAt, &c.UpdatedAt, &c.ParticipantIDs,
			&c.CounterpartUserID, &c.CounterpartDisplayName, &c.CounterpartAvatarURL,
		); err != nil {
			return nil, fmt.Errorf("scan visible dm conversation with participants: %w", err)
		}
		conversations = append(conversations, c)
	}
	return conversations, rows.Err()
}

func (s *PGXDMStore) GetVisibleConversationByID(ctx context.Context, workspaceID, conversationID, userID string) (domain.DMConversation, error) {
	var conversation domain.DMConversation
	err := s.pool.QueryRow(ctx, `
		SELECT dc.id, dc.workspace_id, dc.type, COALESCE(dc.title, ''), dc.status,
		       dc.created_by, dc.created_at, dc.updated_at
		FROM chat.dm_conversations dc
		JOIN chat.workspaces w
		  ON w.id = dc.workspace_id AND w.status = 'active'
		JOIN chat.workspace_members wm
		  ON wm.workspace_id = dc.workspace_id AND wm.user_id = $3 AND wm.status = 'active'
		JOIN chat.dm_members dm
		  ON dm.conversation_id = dc.id AND dm.user_id = $3 AND dm.status = 'active'
		WHERE dc.workspace_id = $1
		  AND dc.id = $2
		  AND dc.status = 'active'`,
		workspaceID, conversationID, userID,
	).Scan(
		&conversation.ID, &conversation.WorkspaceID, (*string)(&conversation.Type),
		&conversation.Title, (*string)(&conversation.Status), &conversation.CreatedBy,
		&conversation.CreatedAt, &conversation.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.DMConversation{}, domain.ErrNotFound
		}
		return domain.DMConversation{}, fmt.Errorf("get visible dm conversation: %w", err)
	}
	return conversation, nil
}
