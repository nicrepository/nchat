package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"slices"
	"strings"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

type meetingStore interface {
	ListMeetingParticipants(context.Context, storage.MeetingScope, storage.MeetingPageQuery) (storage.MeetingParticipantPage, error)
	ListMeetingCandidates(context.Context, storage.MeetingCandidateScope, storage.MeetingPageQuery) (storage.MeetingCandidatePage, error)
	AddMeetingParticipant(context.Context, storage.MeetingScope, string, domain.MeetingRole) (domain.MeetingParticipant, error)
	ChangeMeetingParticipantRole(context.Context, storage.MeetingScope, string, domain.MeetingRole) (domain.MeetingParticipant, error)
	RemoveMeetingParticipant(context.Context, storage.MeetingScope, string) error
	InviteAllMeetingParticipants(context.Context, storage.MeetingScope) (int64, error)
	CreateMeetingWithParticipants(context.Context, storage.MeetingCreation) (storage.MeetingCreationResult, error)
	PatchMeeting(context.Context, storage.MeetingScope, int64, storage.MeetingPatch) (domain.Meeting, error)
	TransitionMeeting(context.Context, storage.MeetingScope, int64, domain.MeetingStatus) (domain.Meeting, error)
}

type MeetingService struct{ store meetingStore }

func NewMeetingService(store meetingStore) *MeetingService { return &MeetingService{store: store} }

type CreateMeetingInput struct {
	WorkspaceID    string
	ActorID        string
	Details        domain.MeetingDetails
	Conversation   *domain.MeetingConversation
	Participants   []storage.MeetingInvitee
	IdempotencyKey string
}

type CreateMeetingResult struct {
	Meeting  domain.Meeting
	Replayed bool
}

var meetingCreationKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

func (s *MeetingService) CreateMeeting(ctx context.Context, input CreateMeetingInput) (CreateMeetingResult, error) {
	creation, err := normalizeMeetingCreation(input)
	if err != nil {
		return CreateMeetingResult{}, err
	}
	result, err := s.store.CreateMeetingWithParticipants(ctx, creation)
	if err != nil {
		return CreateMeetingResult{}, err
	}
	return CreateMeetingResult{Meeting: result.Meeting, Replayed: result.Replayed}, nil
}

func normalizeMeetingCreation(input CreateMeetingInput) (storage.MeetingCreation, error) {
	key := input.IdempotencyKey
	if !meetingCreationKeyPattern.MatchString(key) {
		return storage.MeetingCreation{}, domain.ErrInvalidInput
	}
	m, err := domain.NewMeeting(input.WorkspaceID, input.ActorID, input.Conversation, input.Details)
	if err != nil {
		return storage.MeetingCreation{}, err
	}
	participants, err := normalizeMeetingInvitees(m.OrganizerUserID, input.Participants)
	if err != nil {
		return storage.MeetingCreation{}, err
	}
	hash, err := meetingCreationHash(m, participants)
	if err != nil {
		return storage.MeetingCreation{}, domain.ErrInvalidInput
	}
	return storage.MeetingCreation{Meeting: m, Participants: participants, IdempotencyKey: key, RequestHash: hash}, nil
}

func normalizeMeetingInvitees(organizer string, raw []storage.MeetingInvitee) ([]storage.MeetingInvitee, error) {
	participants := make([]storage.MeetingInvitee, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for _, p := range raw {
		id, err := canonicalizeUserID(strings.TrimSpace(p.UserID))
		if err != nil || id == organizer || seen[id] {
			return nil, domain.ErrInvalidInput
		}
		if p.Role == "" {
			p.Role = domain.MeetingRoleAttendee
		}
		if p.Role != domain.MeetingRoleAttendee && p.Role != domain.MeetingRoleCohost {
			return nil, domain.ErrInvalidInput
		}
		seen[id] = true
		participants = append(participants, storage.MeetingInvitee{UserID: id, Role: p.Role})
	}
	slices.SortFunc(participants, func(a, b storage.MeetingInvitee) int { return strings.Compare(a.UserID, b.UserID) })
	return participants, nil
}

// Hash only normalized, persisted intent. Struct field order and sorted invitees
// are stable; generated identity and execution timestamps are not intent.
func meetingCreationHash(m domain.Meeting, participants []storage.MeetingInvitee) (string, error) {
	intent := struct {
		Schema       string
		Details      domain.MeetingDetails
		Conversation *domain.MeetingConversation
		Participants []storage.MeetingInvitee
	}{"meeting-create/v1", m.MeetingDetails, m.Conversation, participants}
	encoded, err := json.Marshal(intent)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// MeetingPatch is shared with the transactional store; the service never
// resolves omitted values from a separate read.
type MeetingPatch = storage.MeetingPatch

type UpdateMeetingInput struct {
	Scope           storage.MeetingScope
	ExpectedVersion int64
	Patch           MeetingPatch
}

func (s *MeetingService) UpdateMeeting(ctx context.Context, input UpdateMeetingInput) (domain.Meeting, error) {
	if input.ExpectedVersion < 1 || input.Patch == (MeetingPatch{}) {
		return domain.Meeting{}, domain.ErrInvalidInput
	}
	return s.store.PatchMeeting(ctx, input.Scope, input.ExpectedVersion, input.Patch)
}

func (s *MeetingService) CancelMeeting(ctx context.Context, scope storage.MeetingScope, expectedVersion int64) (domain.Meeting, error) {
	if expectedVersion < 1 {
		return domain.Meeting{}, domain.ErrInvalidInput
	}
	return s.store.TransitionMeeting(ctx, scope, expectedVersion, domain.MeetingStatusCancelled)
}
