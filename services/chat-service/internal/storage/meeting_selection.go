package storage

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

const MaxMeetingPageSize = 100
const MaxMeetingBulkMembers = 500

// MeetingPageQuery uses a typed UUID cursor: stable, non-sensitive and indexed.
type MeetingPageQuery struct {
	After  string
	Search string
	Limit  int
}

func (q MeetingPageQuery) Normalize() (MeetingPageQuery, error) {
	if q.Limit == 0 {
		q.Limit = 50
	}
	if q.Limit < 1 || q.Limit > MaxMeetingPageSize {
		return q, domain.ErrInvalidInput
	}
	if q.After != "" {
		id, err := uuid.Parse(q.After)
		if err != nil || id == uuid.Nil {
			return q, domain.ErrInvalidInput
		}
		q.After = id.String()
	}
	q.Search = strings.TrimSpace(q.Search)
	if !utf8.ValidString(q.Search) || utf8.RuneCountInString(q.Search) > 100 || strings.ContainsFunc(q.Search, unicode.IsControl) {
		return q, domain.ErrInvalidInput
	}
	return q, nil
}

type MeetingCandidate struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
}

type MeetingCandidatePage struct {
	Items      []MeetingCandidate `json:"items"`
	NextCursor string             `json:"next_cursor,omitempty"`
}

type MeetingParticipantPage struct {
	Items      []domain.MeetingParticipant
	NextCursor string
}

type MeetingCandidateScope struct {
	WorkspaceID  string
	ActorID      string
	Conversation domain.MeetingConversation
}

// Discovery requires permission to create in this conversation, independently
// of any meeting's join policy. The synthetic meeting is never persisted.
func (s *PGXMeetingStore) ListMeetingCandidates(ctx context.Context, scope MeetingCandidateScope, query MeetingPageQuery) (MeetingCandidatePage, error) {
	m, err := normalizeMeetingCandidateScope(scope)
	if err != nil {
		return MeetingCandidatePage{}, err
	}
	query, err = query.Normalize()
	if err != nil {
		return MeetingCandidatePage{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return MeetingCandidatePage{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	authority, err := lockMeetingAuthority(ctx, tx, m, []string{m.OrganizerUserID})
	if err != nil {
		return MeetingCandidatePage{}, err
	}
	if err = domain.AuthorizeMeeting(m, authority.actor(m.OrganizerUserID), domain.MeetingCapabilityCreate); err != nil {
		return MeetingCandidatePage{}, err
	}
	items, err := queryMeetingCandidates(ctx, tx, m, m.OrganizerUserID, query)
	if err != nil {
		return MeetingCandidatePage{}, err
	}
	page := MeetingCandidatePage{Items: items}
	if len(items) > query.Limit {
		page.Items = items[:query.Limit]
		page.NextCursor = page.Items[len(page.Items)-1].UserID
	}
	if err := tx.Commit(ctx); err != nil {
		return MeetingCandidatePage{}, err
	}
	return page, nil
}

func normalizeMeetingCandidateScope(scope MeetingCandidateScope) (domain.Meeting, error) {
	canonical, err := (MeetingScope{WorkspaceID: scope.WorkspaceID, ActorID: scope.ActorID, MeetingID: scope.Conversation.ID}).canonical()
	if err != nil {
		return domain.Meeting{}, err
	}
	if scope.Conversation.Kind != domain.MeetingConversationChannel && scope.Conversation.Kind != domain.MeetingConversationDM {
		return domain.Meeting{}, domain.ErrInvalidInput
	}
	scope.Conversation.ID = canonical.MeetingID
	return domain.Meeting{ID: uuid.Nil.String(), WorkspaceID: canonical.WorkspaceID, OrganizerUserID: canonical.ActorID, Conversation: &scope.Conversation}, nil
}

// Explicit memberships define the contextual set, even in public channels.
// Visibility still uses the canonical policy; it does not imply selection of
// every workspace member. Prefix matching follows member_store.go (no wildcard).
func queryMeetingCandidates(ctx context.Context, tx pgx.Tx, m domain.Meeting, exclude string, q MeetingPageQuery) ([]MeetingCandidate, error) {
	rows, err := tx.Query(ctx, `SELECT u.id::text,
 COALESCE(NULLIF(btrim(u.full_name), ''), u.display_name)
 FROM (
 SELECT user_id FROM chat.channel_members WHERE $2::text = 'channel' AND channel_id = $3::uuid
 UNION ALL
 SELECT user_id FROM chat.dm_members WHERE $2::text = 'dm' AND conversation_id = $3::uuid AND status = 'active'
 ) members
 JOIN chat.workspace_members wm ON wm.user_id = members.user_id AND wm.workspace_id = $1
 JOIN auth.users u ON u.id = wm.user_id AND u.status = 'active' AND u.deleted_at IS NULL
 WHERE wm.workspace_id = $1 AND wm.status = 'active'
 AND wm.role IN ('owner', 'admin', 'moderator', 'member')
 AND u.id <> $4::uuid AND ($5::uuid IS NULL OR u.id > $5::uuid)
 AND (left(lower(u.display_name), length($6)) = lower($6)
      OR left(lower(COALESCE(u.full_name, '')), length($6)) = lower($6))
 ORDER BY u.id LIMIT $7`, m.WorkspaceID, m.Conversation.Kind, m.Conversation.ID, exclude, nullableMeetingCursor(q.After), q.Search, q.Limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]MeetingCandidate, 0)
	for rows.Next() {
		var item MeetingCandidate
		if err := rows.Scan(&item.UserID, &item.DisplayName); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func nullableMeetingCursor(cursor string) any {
	if cursor == "" {
		return nil
	}
	return cursor
}

func (s *PGXMeetingStore) ListMeetingParticipants(ctx context.Context, scope MeetingScope, q MeetingPageQuery) (MeetingParticipantPage, error) {
	q, err := q.Normalize()
	if err != nil || q.Search != "" {
		return MeetingParticipantPage{}, domain.ErrInvalidInput
	}
	page := MeetingParticipantPage{Items: make([]domain.MeetingParticipant, 0)}
	err = s.withMeeting(ctx, scope, meetingRead, "", func(st *meetingTx) error {
		if err := domain.AuthorizeMeeting(st.meeting, st.actor, domain.MeetingCapabilityView); err != nil {
			return err
		}
		var err error
		page.Items, err = queryMeetingParticipantPage(ctx, st.tx, st.meeting.ID, q)
		return err
	})
	if err != nil {
		return MeetingParticipantPage{}, err
	}
	if len(page.Items) > q.Limit {
		page.Items = page.Items[:q.Limit]
		page.NextCursor = page.Items[len(page.Items)-1].UserID
	}
	return page, nil
}

func queryMeetingParticipantPage(ctx context.Context, tx pgx.Tx, meetingID string, q MeetingPageQuery) ([]domain.MeetingParticipant, error) {
	rows, err := tx.Query(ctx, `SELECT meeting_id::text, user_id::text, role, rsvp, invited_at, updated_at
 FROM chat.meeting_participants WHERE meeting_id = $1 AND ($2::uuid IS NULL OR user_id > $2::uuid)
 ORDER BY user_id LIMIT $3`, meetingID, nullableMeetingCursor(q.After), q.Limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.MeetingParticipant, 0)
	for rows.Next() {
		p, err := scanMeetingParticipant(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, p)
	}
	return items, rows.Err()
}
