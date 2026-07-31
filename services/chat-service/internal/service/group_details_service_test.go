package service_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Group-details service tests (issue #441).
//
// The point of these is that a group is a chat.dm_conversations row, not a
// channel: access is participation in the conversation, and a 1:1 conversation
// — which this issue deliberately leaves without a panel — must be refused as
// firmly as one the caller has nothing to do with.

func groupConversation() domain.DMConversation {
	return domain.DMConversation{
		ID:          "conv-1",
		WorkspaceID: "ws-1",
		Type:        domain.DMConversationTypeGroup,
		Title:       "Time de Infra",
		Status:      domain.DMConversationStatusActive,
		CreatedBy:   "user-1",
		CreatedAt:   time.Date(2024, 3, 4, 15, 0, 0, 0, time.UTC),
	}
}

func participantsOf(n int) []domain.DMParticipantProfile {
	participants := make([]domain.DMParticipantProfile, 0, n)
	for i := 0; i < n; i++ {
		participants = append(participants, domain.DMParticipantProfile{
			UserID:      fmt.Sprintf("user-%03d", i),
			DisplayName: fmt.Sprintf("Pessoa %03d", i),
		})
	}
	return participants
}

func groupDetailsInput(limit int) service.GroupDetailsInput {
	return service.GroupDetailsInput{
		WorkspaceID:      "ws-1",
		CallerID:         "user-1",
		ConversationID:   "conv-1",
		ParticipantLimit: limit,
	}
}

func TestDMService_GetGroupDetails_ReturnsTheConversationAndItsParticipants(t *testing.T) {
	dms := &fakeDMStore{
		visibleConversation: groupConversation(),
		participants: storage.DMParticipantPage{
			Participants: participantsOf(3),
			// Deliberately larger than the page: the panel shows this, never
			// len(Participants).
			TotalCount: 18,
		},
	}

	got, err := service.NewDMService(dms, newFakeMemberStore()).
		GetGroupDetails(context.Background(), groupDetailsInput(domain.MaxDMDetailsParticipants))
	if err != nil {
		t.Fatalf("GetGroupDetails: %v", err)
	}
	if got.Conversation.ID != "conv-1" || got.Conversation.Title != "Time de Infra" {
		t.Fatalf("unexpected conversation: %+v", got.Conversation)
	}
	if got.ParticipantCount != 18 {
		t.Fatalf("participant count must come from the store, got %d", got.ParticipantCount)
	}
	if len(got.Participants) != 3 {
		t.Fatalf("unexpected participants: %+v", got.Participants)
	}
	if len(dms.participantCalls) != 1 {
		t.Fatalf("expected exactly one participant query, got %d", len(dms.participantCalls))
	}
	call := dms.participantCalls[0]
	if call.workspaceID != "ws-1" || call.conversationID != "conv-1" ||
		call.limit != domain.MaxDMDetailsParticipants {
		t.Fatalf("participant query must be scoped to the resolved conversation: %+v", call)
	}
}

func TestDMService_GetGroupDetails_CapsThePreviewWithoutTruncatingTheTotal(t *testing.T) {
	roster := participantsOf(domain.MaxDMDetailsParticipants + 9)
	dms := &fakeDMStore{
		visibleConversation: groupConversation(),
		participants:        storage.DMParticipantPage{Participants: roster, TotalCount: len(roster)},
	}

	got, err := service.NewDMService(dms, newFakeMemberStore()).
		GetGroupDetails(context.Background(), groupDetailsInput(domain.MaxDMDetailsParticipants))
	if err != nil {
		t.Fatalf("GetGroupDetails: %v", err)
	}
	if len(got.Participants) != domain.MaxDMDetailsParticipants {
		t.Fatalf("expected exactly the cap, got %d", len(got.Participants))
	}
	if got.ParticipantCount != len(roster) {
		t.Fatalf("the total must not be truncated by the preview, got %d", got.ParticipantCount)
	}
	// Deterministic page: the store's order is preserved, not re-sorted here.
	if got.Participants[0].UserID != roster[0].UserID {
		t.Fatalf("expected the store's ordering to survive, got %+v", got.Participants[0])
	}
}

// A 1:1 conversation is out of scope for this issue. It must be refused with
// the same bare ErrNotFound a stranger's conversation gets, so the route can
// never be used to learn that a direct conversation exists.
func TestDMService_GetGroupDetails_RefusesDirectConversations(t *testing.T) {
	direct := groupConversation()
	direct.Type = domain.DMConversationTypeDirect
	dms := &fakeDMStore{
		visibleConversation: direct,
		participants:        storage.DMParticipantPage{Participants: participantsOf(2), TotalCount: 2},
	}

	_, err := service.NewDMService(dms, newFakeMemberStore()).
		GetGroupDetails(context.Background(), groupDetailsInput(0))
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for a 1:1 conversation, got %v", err)
	}
	if len(dms.participantCalls) != 0 {
		t.Fatal("a 1:1 conversation must never have its participants read")
	}
}

// The type is checked against the row the database returned, so a client that
// asks for a group cannot make a direct conversation answer by saying so.
func TestDMService_GetGroupDetails_TrustsTheStoredTypeOnly(t *testing.T) {
	direct := groupConversation()
	direct.Type = domain.DMConversationTypeDirect
	dms := &fakeDMStore{visibleConversation: direct}

	_, err := service.NewDMService(dms, newFakeMemberStore()).
		GetGroupDetails(context.Background(), service.GroupDetailsInput{
			WorkspaceID: "ws-1", CallerID: "user-1", ConversationID: "conv-1",
		})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestDMService_GetGroupDetails_RefusesConversationsTheCallerCannotSee(t *testing.T) {
	// GetVisibleConversationByID already folds "missing", "archived", "other
	// workspace" and "not a participant" into ErrNotFound; the service must
	// propagate that without reading anyone's profile.
	dms := &fakeDMStore{getVisibleErr: domain.ErrNotFound}

	_, err := service.NewDMService(dms, newFakeMemberStore()).
		GetGroupDetails(context.Background(), groupDetailsInput(0))
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if len(dms.participantCalls) != 0 {
		t.Fatal("an unreachable conversation must never have its participants read")
	}
}

func TestDMService_GetGroupDetails_SurfacesAParticipantQueryFailure(t *testing.T) {
	dms := &fakeDMStore{
		visibleConversation: groupConversation(),
		participantsErr:     errors.New("boom"),
	}

	_, err := service.NewDMService(dms, newFakeMemberStore()).
		GetGroupDetails(context.Background(), groupDetailsInput(0))
	if err == nil {
		t.Fatal("expected the participant query failure to surface")
	}
}
