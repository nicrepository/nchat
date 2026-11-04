package storage

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// InviteAllMeetingParticipants resolves current eligible conversation members
// server-side. A bounded atomic insert preserves existing roles and RSVP.
func (s *PGXMeetingStore) InviteAllMeetingParticipants(ctx context.Context, scope MeetingScope) (int64, error) {
	scope, err := scope.canonical()
	if err != nil {
		return 0, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	added, err := inviteAllMeetingParticipantsInTx(ctx, tx, scope)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return added, nil
}

func resolveMeetingBulk(ctx context.Context, tx pgx.Tx, m domain.Meeting, actorID string) ([]string, error) {
	// Lock only the workspace and conversation before resolving IDs. Locking
	// actor memberships/accounts first would invert the multi-user lock order.
	a := meetingAuthority{meeting: m}
	batch := &pgx.Batch{}
	batch.Queue(`SELECT status = 'active' FROM chat.workspaces WHERE id = $1 FOR SHARE`, m.WorkspaceID).QueryRow(optionalRow(&a.workspaceActive))
	a.queueConversation(batch)
	if err := tx.SendBatch(ctx, batch).Close(); err != nil {
		return nil, err
	}
	actor, err := loadMeetingActor(ctx, tx, m, actorID)
	if err != nil {
		return nil, err
	}
	if err := domain.AuthorizeMeetingParticipantBulk(m, actor); err != nil {
		return nil, err
	}
	items, err := queryMeetingCandidates(ctx, tx, m, m.OrganizerUserID, MeetingPageQuery{Limit: MaxMeetingBulkMembers})
	if err != nil {
		return nil, err
	}
	if len(items) > MaxMeetingBulkMembers {
		return nil, domain.ErrInvalidInput
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.UserID)
	}
	return ids, nil
}

func inviteAllMeetingParticipantsInTx(ctx context.Context, tx pgx.Tx, scope MeetingScope) (int64, error) {
	m, err := scanMeeting(tx.QueryRow(ctx, `SELECT `+meetingColumns+` FROM chat.meetings
 WHERE id = $1 AND workspace_id = $2 FOR UPDATE`, scope.MeetingID, scope.WorkspaceID))
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, domain.ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	ids, err := resolveMeetingBulk(ctx, tx, m, scope.ActorID)
	if err != nil {
		return 0, err
	}
	authority, err := lockMeetingAuthority(ctx, tx, m, append(ids, scope.ActorID))
	if err != nil {
		return 0, err
	}
	actor := authority.actor(scope.ActorID)
	if err := domain.AuthorizeMeetingParticipantBulk(m, actor); err != nil {
		return 0, err
	}
	eligible := eligibleMeetingBulkIDs(m, authority, ids)
	result, err := tx.Exec(ctx, `INSERT INTO chat.meeting_participants (meeting_id, user_id, role)
 SELECT $1, id, 'attendee' FROM unnest($2::uuid[]) AS members(id)
 ON CONFLICT (meeting_id, user_id) DO NOTHING`, m.ID, eligible)
	if err != nil {
		return 0, err
	}

	return result.RowsAffected(), nil
}

func eligibleMeetingBulkIDs(m domain.Meeting, authority *meetingAuthority, ids []string) []string {
	eligible := make([]string, 0, len(ids))
	for _, id := range ids {
		if authority.user(id).listed && domain.MeetingInviteeEligible(m, authority.actor(id)) {
			eligible = append(eligible, id)
		}
	}
	return eligible
}
