package httpapi_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

func callParticipantsRequest(channelID, body string) *http.Request {
	r := requestWithUser(http.MethodPost, "/api/chat/channels/"+channelID+"/call-participants", bytes.NewBufferString(body))
	r.Header.Set("Content-Type", "application/json")
	r.SetPathValue("channelID", channelID)
	return r
}

func decodeCallParticipantProfiles(t *testing.T, recorder *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	var decoded struct {
		Data struct {
			Profiles []map[string]any `json:"profiles"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode: %v, body=%s", err, recorder.Body.String())
	}
	return decoded.Data.Profiles
}

func TestChannelHandler_CallParticipants_ReturnsResolvedProfiles(t *testing.T) {
	provider := &fakeChannelProvider{
		callParticipantProfiles: []domain.CallParticipantProfile{
			{UserID: "user-a", DisplayName: "Ana Souza", AvatarURL: "https://x/a.png"},
		},
	}
	handler := channelTestHandler(provider)
	req := callParticipantsRequest(createdChannelID, `{"user_ids":["user-a"]}`)
	recorder := httptest.NewRecorder()

	handler.CallParticipants(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	profiles := decodeCallParticipantProfiles(t, recorder)
	if len(profiles) != 1 || profiles[0]["user_id"] != "user-a" || profiles[0]["display_name"] != "Ana Souza" {
		t.Fatalf("unexpected body: %s", recorder.Body.String())
	}
	if provider.lastCallParticipantsInput.ChannelID != createdChannelID {
		t.Fatalf("channel id not threaded through: %+v", provider.lastCallParticipantsInput)
	}
	if provider.lastCallParticipantsInput.CallerID != msgTestUserID {
		t.Fatalf("caller id must come from context, got %+v", provider.lastCallParticipantsInput)
	}
}

func TestChannelHandler_CallParticipants_RequiresAuthenticatedUser(t *testing.T) {
	handler := channelTestHandler(&fakeChannelProvider{})
	req := httptest.NewRequest(http.MethodPost, "/api/chat/channels/"+createdChannelID+"/call-participants", bytes.NewBufferString(`{"user_ids":[]}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("channelID", createdChannelID)
	recorder := httptest.NewRecorder()

	handler.CallParticipants(recorder, req)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
}

func TestChannelHandler_CallParticipants_PropagatesServiceErrorAsNotFound(t *testing.T) {
	provider := &fakeChannelProvider{callParticipantProfilesErr: domain.ErrNotFound}
	handler := channelTestHandler(provider)
	req := callParticipantsRequest(createdChannelID, `{"user_ids":["user-a"]}`)
	recorder := httptest.NewRecorder()

	handler.CallParticipants(recorder, req)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body=%s", recorder.Code, recorder.Body.String())
	}
}

// TestChannelHandler_CallParticipants_RejectsCallerIDInBody proves the caller
// identity cannot be spoofed through the request body: the strict decoder
// rejects any field beyond user_ids, so a body naming "caller_id" (or
// anything else) never reaches the service at all — the only identity that
// ever reaches ChannelCallParticipantProfilesInput.CallerID is the one
// GetContextUserID resolved from the authenticated session.
func TestChannelHandler_CallParticipants_RejectsCallerIDInBody(t *testing.T) {
	provider := &fakeChannelProvider{}
	handler := channelTestHandler(provider)
	req := callParticipantsRequest(createdChannelID, `{"user_ids":["user-a"],"caller_id":"someone-else"}`)
	recorder := httptest.NewRecorder()

	handler.CallParticipants(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an unknown field, body=%s", recorder.Code, recorder.Body.String())
	}
	if provider.callParticipantsCalls != 0 {
		t.Fatalf("service must never be called for a rejected body, calls=%d", provider.callParticipantsCalls)
	}
}

// TestChannelHandler_CallParticipants_ResponseCarriesOnlyPresentationFields
// proves the response envelope exposes exactly the three fields issue #612
// asks for — user_id, display_name, avatar_url — and nothing else (no email,
// no username, no role, no presence).
func TestChannelHandler_CallParticipants_ResponseCarriesOnlyPresentationFields(t *testing.T) {
	provider := &fakeChannelProvider{
		callParticipantProfiles: []domain.CallParticipantProfile{
			{UserID: "user-a", DisplayName: "Ana Souza", AvatarURL: "https://x/a.png"},
		},
	}
	handler := channelTestHandler(provider)
	req := callParticipantsRequest(createdChannelID, `{"user_ids":["user-a"]}`)
	recorder := httptest.NewRecorder()

	handler.CallParticipants(recorder, req)

	profiles := decodeCallParticipantProfiles(t, recorder)
	if len(profiles) != 1 {
		t.Fatalf("unexpected body: %s", recorder.Body.String())
	}
	if len(profiles[0]) != 3 {
		t.Fatalf("profile must carry exactly user_id/display_name/avatar_url, got %#v", profiles[0])
	}
	for _, key := range []string{"user_id", "display_name", "avatar_url"} {
		if _, ok := profiles[0][key]; !ok {
			t.Fatalf("missing expected field %q in %#v", key, profiles[0])
		}
	}
}

// TestChannelHandler_CallParticipants_RejectsOversizedBatch proves the HTTP
// layer surfaces the service's cap as a 400, not a 500 or a silently
// truncated request.
func TestChannelHandler_CallParticipants_RejectsOversizedBatch(t *testing.T) {
	provider := &fakeChannelProvider{callParticipantProfilesErr: domain.ErrTooManyCallParticipantsRequested}
	handler := channelTestHandler(provider)
	req := callParticipantsRequest(createdChannelID, `{"user_ids":["user-a"]}`)
	recorder := httptest.NewRecorder()

	handler.CallParticipants(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", recorder.Code, recorder.Body.String())
	}
}
