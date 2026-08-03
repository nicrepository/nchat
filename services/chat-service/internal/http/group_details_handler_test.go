package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	httpapi "github.com/nicrepository/nchat/services/chat-service/internal/http"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
)

func groupConversation() domain.DMConversation {
	return domain.DMConversation{
		ID:          testConversationID,
		WorkspaceID: testWorkspaceID,
		Type:        domain.DMConversationTypeGroup,
		Title:       "Time de Infra",
		Status:      domain.DMConversationStatusActive,
		CreatedAt:   time.Date(2024, 3, 4, 15, 0, 0, 0, time.UTC),
	}
}

func groupDetailsRequest(conversationID string) *http.Request {
	r := requestWithUser(http.MethodGet, "/api/chat/dm/"+conversationID+"/details", nil)
	r.SetPathValue("conversationID", conversationID)
	return r
}

func serveGroupDetails(t *testing.T, handler *httpapi.DMHandler, conversationID string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.GroupDetails(rec, groupDetailsRequest(conversationID))
	return rec
}

func groupDetailsHandler(provider *fakeDMProvider) *httpapi.DMHandler {
	return httpapi.NewDMHandler(
		&fakeWorkspaceResolver{workspace: activeWorkspace()}, provider, &fakeDMRateLimiter{},
	)
}

func TestDMHandler_GroupDetails_ReturnsTheGroupAndItsParticipants(t *testing.T) {
	provider := &fakeDMProvider{groupDetails: service.GroupDetails{
		Conversation: groupConversation(),
		Participants: []domain.DMParticipantProfile{
			{UserID: "u-1", DisplayName: "Álvaro Neto", AvatarURL: "/media/a.png"},
			{UserID: "u-2", DisplayName: "Juliane Lino"},
		},
		// Larger than the preview: the panel shows this, never len(Participants).
		ParticipantCount: 18,
	}}
	// Only the first participant is connected — the second must still appear.
	presence := &fakePresence{online: []string{"u-1"}}
	handler := groupDetailsHandler(provider).WithPresence(presence)

	rec := serveGroupDetails(t, handler, testConversationID)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	data := detailsData(t, rec)
	if data["id"] != testConversationID || data["name"] != "Time de Infra" {
		t.Fatalf("unexpected conversation identity: %v", data)
	}
	if data["type"] != "group" {
		t.Fatalf("expected the domain conversation type, got %v", data["type"])
	}
	if data["created_at"] != "2024-03-04T15:00:00Z" {
		t.Fatalf("expected an RFC3339 UTC creation date, got %v", data["created_at"])
	}
	if data["participant_count"] != float64(18) {
		t.Fatalf("expected the store's total, got %v", data["participant_count"])
	}

	// A group is not a channel: none of the channel-only vocabulary may appear.
	for _, forbidden := range []string{"visibility", "slug", "description", "category", "member_count", "online_members"} {
		if _, present := data[forbidden]; present {
			t.Fatalf("group payload must not carry %q: %v", forbidden, data)
		}
	}

	participants, ok := data["participants"].([]any)
	if !ok || len(participants) != 2 {
		t.Fatalf("expected two participants, got %v", data["participants"])
	}
	first := participants[0].(map[string]any)
	if first["user_id"] != "u-1" || first["display_name"] != "Álvaro Neto" || first["presence"] != "online" {
		t.Fatalf("unexpected first participant: %v", first)
	}
	// Offline participants stay in the list and are labelled, never dropped.
	second := participants[1].(map[string]any)
	if second["user_id"] != "u-2" || second["presence"] != "offline" {
		t.Fatalf("an offline participant must remain, labelled: %v", second)
	}
	if _, present := second["avatar_url"]; present {
		t.Fatal("a participant without an avatar must omit the field")
	}
	// A group has no role to show, so none is serialised.
	if _, present := first["role"]; present {
		t.Fatalf("a group participant must not carry a role: %v", first)
	}

	if len(presence.asked) != 1 || presence.asked[0] != testWorkspaceID {
		t.Fatalf("presence must be resolved once, scoped by workspace, got %v", presence.asked)
	}
	if provider.lastDetailsInput.WorkspaceID != testWorkspaceID ||
		provider.lastDetailsInput.CallerID != msgTestUserID ||
		provider.lastDetailsInput.ConversationID != testConversationID {
		t.Fatalf("unexpected service input: %+v", provider.lastDetailsInput)
	}
	if provider.lastDetailsInput.ParticipantLimit != domain.MaxDMDetailsParticipants {
		t.Fatalf("expected the server-side cap, got %d", provider.lastDetailsInput.ParticipantLimit)
	}
}

// can_manage_members (issue #398) is always serialized, so a client that
// predates the field reads absent-as-false and hides the add action.
func TestDMHandler_GroupDetails_SerializesTheAddParticipantsPermission(t *testing.T) {
	for name, canManage := range map[string]bool{"permitted": true, "refused": false} {
		t.Run(name, func(t *testing.T) {
			provider := &fakeDMProvider{groupDetails: service.GroupDetails{
				Conversation:     groupConversation(),
				ParticipantCount: 1,
				CanManageMembers: canManage,
			}}

			rec := serveGroupDetails(t, groupDetailsHandler(provider), testConversationID)

			data := detailsData(t, rec)
			got, present := data["can_manage_members"].(bool)
			if !present {
				t.Fatalf("can_manage_members absent from the payload: %v", data)
			}
			if got != canManage {
				t.Fatalf("can_manage_members = %v, want %v", got, canManage)
			}
		})
	}
}

func TestDMHandler_GroupDetails_OmitsPresenceWhenNotTracked(t *testing.T) {
	provider := &fakeDMProvider{groupDetails: service.GroupDetails{
		Conversation:     groupConversation(),
		Participants:     []domain.DMParticipantProfile{{UserID: "u-1", DisplayName: "Álvaro"}},
		ParticipantCount: 1,
	}}
	// No WithPresence: the handler must not claim a status it does not know.
	rec := serveGroupDetails(t, groupDetailsHandler(provider), testConversationID)

	participants := detailsData(t, rec)["participants"].([]any)
	if len(participants) != 1 {
		t.Fatalf("participants must not depend on presence being wired: %v", participants)
	}
	if _, present := participants[0].(map[string]any)["presence"]; present {
		t.Fatal("presence must be absent when the server does not track it")
	}
}

func TestDMHandler_GroupDetails_HidesUnreachableConversationsBehindNotFound(t *testing.T) {
	for name, err := range map[string]error{
		// Missing, archived, other workspace, not a participant, and 1:1 all
		// arrive as ErrNotFound; a caller must not tell them apart.
		"not found": domain.ErrNotFound,
		"forbidden": domain.ErrForbidden,
	} {
		t.Run(name, func(t *testing.T) {
			rec := serveGroupDetails(t, groupDetailsHandler(&fakeDMProvider{groupDetailsErr: err}), testConversationID)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("expected 404, got %d", rec.Code)
			}
		})
	}
}

func TestDMHandler_GroupDetails_RejectsAMalformedConversationID(t *testing.T) {
	provider := &fakeDMProvider{}
	rec := serveGroupDetails(t, groupDetailsHandler(provider), "not-a-uuid")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	if provider.detailsCalls != 0 {
		t.Fatal("a malformed conversation id must never reach the service")
	}
}

func TestDMHandler_GroupDetails_RequiresAnAuthenticatedCaller(t *testing.T) {
	provider := &fakeDMProvider{}
	request := httptest.NewRequest(http.MethodGet, "/api/chat/dm/"+testConversationID+"/details", nil)
	request.SetPathValue("conversationID", testConversationID)

	rec := httptest.NewRecorder()
	groupDetailsHandler(provider).GroupDetails(rec, request)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	if provider.detailsCalls != 0 {
		t.Fatal("an unauthenticated request must never reach the service")
	}
}

func TestDMHandler_GroupDetails_IsUnavailableWithoutWiring(t *testing.T) {
	rec := serveGroupDetails(t, httpapi.NewDMHandler(nil, nil, nil), testConversationID)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}
