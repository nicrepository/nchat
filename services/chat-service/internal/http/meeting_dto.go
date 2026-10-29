package httpapi

import (
	"bytes"
	"encoding/json"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

type meetingConversationJSON struct {
	Kind domain.MeetingConversationKind `json:"kind"`
	ID   string                         `json:"id"`
}

type meetingInviteeJSON struct {
	UserID string             `json:"user_id"`
	Role   domain.MeetingRole `json:"role,omitempty"`
}

type createMeetingRequest struct {
	Title        string                   `json:"title"`
	Description  string                   `json:"description"`
	StartsAt     time.Time                `json:"starts_at"`
	EndsAt       time.Time                `json:"ends_at"`
	Timezone     string                   `json:"timezone"`
	JoinPolicy   domain.MeetingJoinPolicy `json:"join_policy"`
	Conversation *meetingConversationJSON `json:"conversation"`
	Participants []meetingInviteeJSON     `json:"participants"`
}

func (r createMeetingRequest) input(scope storage.MeetingScope, key string) service.CreateMeetingInput {
	var conversation *domain.MeetingConversation
	if r.Conversation != nil {
		conversation = &domain.MeetingConversation{Kind: r.Conversation.Kind, ID: r.Conversation.ID}
	}
	participants := make([]storage.MeetingInvitee, 0, len(r.Participants))
	for _, p := range r.Participants {
		participants = append(participants, storage.MeetingInvitee{UserID: p.UserID, Role: p.Role})
	}
	return service.CreateMeetingInput{
		WorkspaceID: scope.WorkspaceID, ActorID: scope.ActorID, IdempotencyKey: key,
		Details: domain.MeetingDetails{Title: r.Title, Description: r.Description,
			StartsAt: r.StartsAt, EndsAt: r.EndsAt, Timezone: r.Timezone, JoinPolicy: r.JoinPolicy},
		Conversation: conversation, Participants: participants,
	}
}

// A present field must have a value. encoding/json normally treats null like
// an absent pointer, which would silently turn an invalid PATCH into a no-op.
type meetingField[T any] struct{ value *T }

func (f *meetingField[T]) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return domain.ErrInvalidInput
	}
	var value T
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	f.value = &value
	return nil
}

type patchMeetingRequest struct {
	Title       meetingField[string]                   `json:"title"`
	Description meetingField[string]                   `json:"description"`
	StartsAt    meetingField[time.Time]                `json:"starts_at"`
	EndsAt      meetingField[time.Time]                `json:"ends_at"`
	Timezone    meetingField[string]                   `json:"timezone"`
	JoinPolicy  meetingField[domain.MeetingJoinPolicy] `json:"join_policy"`
}

func (r patchMeetingRequest) patch() service.MeetingPatch {
	return service.MeetingPatch{Title: r.Title.value, Description: r.Description.value,
		StartsAt: r.StartsAt.value, EndsAt: r.EndsAt.value, Timezone: r.Timezone.value, JoinPolicy: r.JoinPolicy.value}
}

type meetingJSON struct {
	ID              string                   `json:"id"`
	WorkspaceID     string                   `json:"workspace_id"`
	OrganizerUserID string                   `json:"organizer_user_id"`
	Conversation    *meetingConversationJSON `json:"conversation"`
	Title           string                   `json:"title"`
	Description     string                   `json:"description"`
	StartsAt        time.Time                `json:"starts_at"`
	EndsAt          time.Time                `json:"ends_at"`
	Timezone        string                   `json:"timezone"`
	JoinPolicy      domain.MeetingJoinPolicy `json:"join_policy"`
	Status          domain.MeetingStatus     `json:"status"`
	Version         int64                    `json:"version"`
	CreatedAt       time.Time                `json:"created_at"`
	UpdatedAt       time.Time                `json:"updated_at"`
}

func mapMeetingJSON(m domain.Meeting) meetingJSON {
	var conversation *meetingConversationJSON
	if m.Conversation != nil {
		conversation = &meetingConversationJSON{Kind: m.Conversation.Kind, ID: m.Conversation.ID}
	}
	return meetingJSON{ID: m.ID, WorkspaceID: m.WorkspaceID, OrganizerUserID: m.OrganizerUserID,
		Conversation: conversation, Title: m.Title, Description: m.Description, StartsAt: m.StartsAt,
		EndsAt: m.EndsAt, Timezone: m.Timezone, JoinPolicy: m.JoinPolicy, Status: m.Status,
		Version: m.Version, CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt}
}
