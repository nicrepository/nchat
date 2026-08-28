package httpapi_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

func groupCallParticipantsRequest(conversationID, body string) *http.Request {
	r := requestWithUser(http.MethodPost, "/api/chat/dm/"+conversationID+"/call-participants", bytes.NewBufferString(body))
	r.Header.Set("Content-Type", "application/json")
	r.SetPathValue("conversationID", conversationID)
	return r
}

func TestDMHandler_GroupCallParticipants_ReturnsResolvedProfiles(t *testing.T) {
	provider := &fakeDMProvider{
		callParticipantProfiles: []domain.CallParticipantProfile{
			{UserID: "user-a", DisplayName: "Ana Souza", AvatarURL: ""},
		},
	}
	handler := dmTestHandler(provider)
	req := groupCallParticipantsRequest(dmConversationID, `{"user_ids":["user-a"]}`)
	recorder := httptest.NewRecorder()

	handler.GroupCallParticipants(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var decoded struct {
		Data struct {
			Profiles []map[string]any `json:"profiles"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(decoded.Data.Profiles) != 1 || decoded.Data.Profiles[0]["user_id"] != "user-a" {
		t.Fatalf("unexpected body: %s", recorder.Body.String())
	}
	if provider.lastCallParticipantsInput.ConversationID != dmConversationID {
		t.Fatalf("conversation id not threaded through: %+v", provider.lastCallParticipantsInput)
	}
}

func TestDMHandler_GroupCallParticipants_RequiresAuthenticatedUser(t *testing.T) {
	handler := dmTestHandler(&fakeDMProvider{})
	req := httptest.NewRequest(http.MethodPost, "/api/chat/dm/"+dmConversationID+"/call-participants", bytes.NewBufferString(`{"user_ids":[]}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("conversationID", dmConversationID)
	recorder := httptest.NewRecorder()

	handler.GroupCallParticipants(recorder, req)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
}

func TestDMHandler_GroupCallParticipants_PropagatesServiceErrorAsNotFound(t *testing.T) {
	provider := &fakeDMProvider{callParticipantProfilesErr: domain.ErrNotFound}
	handler := dmTestHandler(provider)
	req := groupCallParticipantsRequest(dmConversationID, `{"user_ids":["user-a"]}`)
	recorder := httptest.NewRecorder()

	handler.GroupCallParticipants(recorder, req)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body=%s", recorder.Code, recorder.Body.String())
	}
}

// TestDMHandler_GroupCallParticipants_RejectsCallerIDInBody mirrors the
// channel-side proof: identity cannot be spoofed through the body.
func TestDMHandler_GroupCallParticipants_RejectsCallerIDInBody(t *testing.T) {
	provider := &fakeDMProvider{}
	handler := dmTestHandler(provider)
	req := groupCallParticipantsRequest(dmConversationID, `{"user_ids":["user-a"],"caller_id":"someone-else"}`)
	recorder := httptest.NewRecorder()

	handler.GroupCallParticipants(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an unknown field, body=%s", recorder.Code, recorder.Body.String())
	}
	if provider.callParticipantsCalls != 0 {
		t.Fatalf("service must never be called for a rejected body, calls=%d", provider.callParticipantsCalls)
	}
}

// TestDMHandler_GroupCallParticipants_ResponseCarriesOnlyPresentationFields
// mirrors the channel-side proof: exactly user_id/display_name/avatar_url,
// nothing else.
func TestDMHandler_GroupCallParticipants_ResponseCarriesOnlyPresentationFields(t *testing.T) {
	provider := &fakeDMProvider{
		callParticipantProfiles: []domain.CallParticipantProfile{
			{UserID: "user-a", DisplayName: "Ana Souza", AvatarURL: "https://x/a.png"},
		},
	}
	handler := dmTestHandler(provider)
	req := groupCallParticipantsRequest(dmConversationID, `{"user_ids":["user-a"]}`)
	recorder := httptest.NewRecorder()

	handler.GroupCallParticipants(recorder, req)

	var decoded struct {
		Data struct {
			Profiles []map[string]any `json:"profiles"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(decoded.Data.Profiles) != 1 || len(decoded.Data.Profiles[0]) != 3 {
		t.Fatalf("profile must carry exactly user_id/display_name/avatar_url, got %#v", decoded.Data.Profiles)
	}
}
