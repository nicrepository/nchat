package storage

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// MeetingInvitee is a normalized internal identity and an assignable role.
type MeetingInvitee struct {
	UserID string
	Role   domain.MeetingRole
}

type MeetingCreation struct {
	Meeting        domain.Meeting
	Participants   []MeetingInvitee
	IdempotencyKey string
	RequestHash    string
}

type MeetingCreationResult struct {
	Meeting  domain.Meeting
	Replayed bool
}

func (s *PGXMeetingStore) CreateMeetingWithParticipants(ctx context.Context, input MeetingCreation) (MeetingCreationResult, error) {
	if input.IdempotencyKey == "" {
		return MeetingCreationResult{}, domain.ErrInvalidInput
	}
	return s.createMeeting(ctx, input)
}

func (s *PGXMeetingStore) createMeeting(ctx context.Context, input MeetingCreation) (MeetingCreationResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return MeetingCreationResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := createMeetingInTx(ctx, tx, input)
	if err != nil {
		return MeetingCreationResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return MeetingCreationResult{}, err
	}
	return result, nil
}

func createMeetingInTx(ctx context.Context, tx pgx.Tx, input MeetingCreation) (MeetingCreationResult, error) {
	if input.IdempotencyKey != "" {
		id, replay, err := claimMeetingCreation(ctx, tx, input)
		if err != nil {
			return MeetingCreationResult{}, err
		}
		if replay {
			return replayMeetingCreation(ctx, tx, input.Meeting, id)
		}
	}
	if err := authorizeMeetingCreation(ctx, tx, input); err != nil {
		return MeetingCreationResult{}, err
	}
	m, err := insertMeeting(ctx, tx, input.Meeting)
	if err != nil {
		return MeetingCreationResult{}, err
	}
	for _, p := range input.Participants {
		if _, err := tx.Exec(ctx, `INSERT INTO chat.meeting_participants (meeting_id, user_id, role, invited_at, updated_at)
 VALUES ($1, $2, $3, $4, $4)`, m.ID, p.UserID, p.Role, m.CreatedAt); err != nil {
			return MeetingCreationResult{}, err
		}
	}
	return MeetingCreationResult{Meeting: m}, nil
}

func authorizeMeetingCreation(ctx context.Context, tx pgx.Tx, input MeetingCreation) error {
	m := input.Meeting
	ids := []string{m.OrganizerUserID}
	for _, p := range input.Participants {
		ids = append(ids, p.UserID)
	}
	authority, err := lockMeetingAuthority(ctx, tx, m, ids)
	if err != nil {
		return err
	}
	organizer := authority.actor(m.OrganizerUserID)
	if err := domain.AuthorizeMeeting(m, organizer, domain.MeetingCapabilityCreate); err != nil {
		return err
	}
	return authorizeInitialMeetingParticipants(m, organizer, authority, input.Participants)
}

func authorizeInitialMeetingParticipants(m domain.Meeting, organizer domain.MeetingActor, authority *meetingAuthority, participants []MeetingInvitee) error {
	// The new roster's organizer is implicit, before its row is inserted.
	organizer.Participant = &domain.MeetingParticipant{MeetingID: m.ID, UserID: m.OrganizerUserID, Role: domain.MeetingRoleOrganizer}
	for _, p := range participants {
		if p.UserID == m.OrganizerUserID || p.Role == "" {
			return domain.ErrInvalidInput
		}
		if err := domain.AuthorizeMeetingParticipantChange(m, organizer, nil, p.Role); err != nil {
			return err
		}
		if !domain.MeetingInviteeEligible(m, authority.actor(p.UserID)) {
			return domain.ErrMeetingParticipantNotEligible
		}
	}
	return nil
}

// A claim is taken before all other locks. Only creation touches this table;
// its unique key serializes same-key requests, and its deferred FK prevents an
// orphan at commit. Rollback releases the key for another legitimate attempt.
func claimMeetingCreation(ctx context.Context, tx pgx.Tx, input MeetingCreation) (string, bool, error) {
	m := input.Meeting
	var id string
	err := tx.QueryRow(ctx, `INSERT INTO chat.meeting_creation_requests
 (workspace_id, actor_user_id, idempotency_key, request_hash, meeting_id)
 VALUES ($1, $2, $3, $4, $5)
 ON CONFLICT (workspace_id, actor_user_id, idempotency_key) DO NOTHING
 RETURNING meeting_id::text`, m.WorkspaceID, m.OrganizerUserID, input.IdempotencyKey, input.RequestHash, m.ID).Scan(&id)
	if err == nil {
		return id, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", false, err
	}
	var hash string
	if err := tx.QueryRow(ctx, `SELECT request_hash, meeting_id::text FROM chat.meeting_creation_requests
 WHERE workspace_id = $1 AND actor_user_id = $2 AND idempotency_key = $3`,
		m.WorkspaceID, m.OrganizerUserID, input.IdempotencyKey).Scan(&hash, &id); err != nil {
		return "", false, err
	}
	if hash != input.RequestHash {
		return "", false, domain.ErrIdempotencyKeyReused
	}
	return id, true, nil
}

// Return the original resource's current representation, as channel creation
// does. Hold the meeting and fresh authority through commit; a key grants no
// access, and replay never writes the meeting or roster.
func replayMeetingCreation(ctx context.Context, tx pgx.Tx, intent domain.Meeting, id string) (MeetingCreationResult, error) {
	m, err := scanMeeting(tx.QueryRow(ctx, `SELECT `+meetingColumns+` FROM chat.meetings
 WHERE id = $1 AND workspace_id = $2 FOR SHARE`, id, intent.WorkspaceID))
	if errors.Is(err, pgx.ErrNoRows) {
		return MeetingCreationResult{}, domain.ErrNotFound
	}
	if err != nil {
		return MeetingCreationResult{}, err
	}
	actor, _, err := lockMeetingActors(ctx, tx, m, intent.OrganizerUserID, "")
	if err != nil {
		return MeetingCreationResult{}, err
	}
	if err := domain.AuthorizeMeeting(m, actor, domain.MeetingCapabilityView); err != nil {
		return MeetingCreationResult{}, err
	}
	return MeetingCreationResult{Meeting: m, Replayed: true}, nil
}
