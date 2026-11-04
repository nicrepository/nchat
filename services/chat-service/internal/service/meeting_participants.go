package service

import (
	"context"
	"strings"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

func (s *MeetingService) ListMeetingParticipants(ctx context.Context, scope storage.MeetingScope, q storage.MeetingPageQuery) (storage.MeetingParticipantPage, error) {
	q, err := q.Normalize()
	if err != nil || q.Search != "" {
		return storage.MeetingParticipantPage{}, domain.ErrInvalidInput
	}
	return s.store.ListMeetingParticipants(ctx, scope, q)
}

func (s *MeetingService) ListMeetingCandidates(ctx context.Context, scope storage.MeetingCandidateScope, q storage.MeetingPageQuery) (storage.MeetingCandidatePage, error) {
	q, err := q.Normalize()
	if err != nil {
		return storage.MeetingCandidatePage{}, err
	}
	id, err := canonicalizeUserID(scope.Conversation.ID)
	if err != nil {
		return storage.MeetingCandidatePage{}, domain.ErrInvalidInput
	}
	if scope.Conversation.Kind != domain.MeetingConversationChannel && scope.Conversation.Kind != domain.MeetingConversationDM {
		return storage.MeetingCandidatePage{}, domain.ErrInvalidInput
	}
	scope.Conversation.ID = id
	return s.store.ListMeetingCandidates(ctx, scope, q)
}

func (s *MeetingService) AddMeetingParticipant(ctx context.Context, scope storage.MeetingScope, userID string, role domain.MeetingRole) (domain.MeetingParticipant, error) {
	id, err := canonicalizeUserID(strings.TrimSpace(userID))
	if err != nil {
		return domain.MeetingParticipant{}, domain.ErrInvalidInput
	}
	if role == "" {
		role = domain.MeetingRoleAttendee
	}
	if !assignableMeetingParticipantRole(role) {
		return domain.MeetingParticipant{}, domain.ErrInvalidInput
	}
	return s.store.AddMeetingParticipant(ctx, scope, id, role)
}

func (s *MeetingService) ChangeMeetingParticipantRole(ctx context.Context, scope storage.MeetingScope, userID string, role domain.MeetingRole) (domain.MeetingParticipant, error) {
	id, err := canonicalizeUserID(userID)
	if err != nil || !assignableMeetingParticipantRole(role) {
		return domain.MeetingParticipant{}, domain.ErrInvalidInput
	}
	return s.store.ChangeMeetingParticipantRole(ctx, scope, id, role)
}

func assignableMeetingParticipantRole(role domain.MeetingRole) bool {
	return role == domain.MeetingRoleAttendee || role == domain.MeetingRoleCohost
}

func (s *MeetingService) RemoveMeetingParticipant(ctx context.Context, scope storage.MeetingScope, userID string) error {
	id, err := canonicalizeUserID(userID)
	if err != nil {
		return domain.ErrInvalidInput
	}
	return s.store.RemoveMeetingParticipant(ctx, scope, id)
}

func (s *MeetingService) InviteAllMeetingParticipants(ctx context.Context, scope storage.MeetingScope) (int64, error) {
	return s.store.InviteAllMeetingParticipants(ctx, scope)
}
