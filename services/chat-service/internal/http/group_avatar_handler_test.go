package httpapi_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	httpapi "github.com/nicrepository/nchat/services/chat-service/internal/http"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
)

// Group identity (issue #1026): PUT sets the emoji, DELETE returns the group to
// Automático. The handler decides nothing about the emoji or the authority —
// those are the service's and the store's — so what is pinned here is what the
// request may not say, the refusals it maps, and that the sidebar signal fires
// only after a successful write.

func groupAvatarRequest(method, conversationID string, body io.Reader) *http.Request {
	r := requestWithUser(method, "/api/chat/dm/"+conversationID+"/avatar", body)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	r.SetPathValue("conversationID", conversationID)
	return r
}

// recordingAvatarHandler wires a handler whose service and realtime fan-out
// are both observable.
func recordingAvatarHandler(provider *fakeDMProvider) (*httpapi.DMHandler, *recordingBroadcaster) {
	broadcast := &recordingBroadcaster{}
	return dmTestHandlerWithLimiter(provider, &fakeDMRateLimiter{}).WithMembersBroadcast(broadcast), broadcast
}

func TestDMHandler_SetGroupAvatar_DerivesActorAndWorkspaceServerSide(t *testing.T) {
	provider := &fakeDMProvider{}
	handler, _ := recordingAvatarHandler(provider)

	handler.SetGroupAvatar(httptest.NewRecorder(), groupAvatarRequest(http.MethodPut, dmConversationID, strings.NewReader(`{"emoji":"👩‍💻"}`)))

	want := service.GroupAvatarInput{
		WorkspaceID: testWorkspaceID, CallerID: msgTestUserID, ConversationID: dmConversationID, AvatarEmoji: "👩‍💻",
	}
	if provider.lastAvatar != want {
		t.Fatalf("forwarded %+v, want %+v (session actor, resolved workspace, ZWJ untouched)", provider.lastAvatar, want)
	}
}

func TestDMHandler_SetGroupAvatar_AnswersTheStoredIdentityAndAnnouncesIt(t *testing.T) {
	handler, broadcast := recordingAvatarHandler(&fakeDMProvider{})
	recorder := httptest.NewRecorder()

	handler.SetGroupAvatar(recorder, groupAvatarRequest(http.MethodPut, dmConversationID, strings.NewReader(`{"emoji":"👩‍💻"}`)))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body)
	}
	data, _ := decodeBody(t, recorder)["data"].(map[string]any)
	if data["id"] != dmConversationID || data["avatar_emoji"] != "👩‍💻" {
		t.Fatalf("body = %v", recorder.Body)
	}
	want := [][3]string{{testWorkspaceID, "dm", dmConversationID}}
	if !reflect.DeepEqual(broadcast.conversationUpdates, want) {
		t.Fatalf("conversationUpdates = %v, want %v", broadcast.conversationUpdates, want)
	}
}

func TestDMHandler_SetGroupAvatar_RefusesAnyFieldBeyondTheEmoji(t *testing.T) {
	for _, body := range []string{
		`{"emoji":"🎉","workspace_id":"` + testWorkspaceID + `"}`,
		`{"emoji":"🎉","caller_id":"someone-else"}`,
		`{"emoji":"🎉","initials":"AB"}`,
		`{"emoji":"🎉","color":"#fff"}`,
		`not json`,
	} {
		t.Run(body, func(t *testing.T) {
			provider := &fakeDMProvider{}
			recorder := httptest.NewRecorder()

			dmTestHandler(provider).SetGroupAvatar(recorder, groupAvatarRequest(http.MethodPut, dmConversationID, strings.NewReader(body)))

			if recorder.Code != http.StatusBadRequest || provider.setAvatarCalls != 0 {
				t.Fatalf("status = %d, calls = %d, want 400 and no service call", recorder.Code, provider.setAvatarCalls)
			}
		})
	}
}

func TestDMHandler_SetGroupAvatar_RequiresJSONAndAnAuthenticatedActor(t *testing.T) {
	t.Run("no content type", func(t *testing.T) {
		provider := &fakeDMProvider{}
		recorder := httptest.NewRecorder()
		r := requestWithUser(http.MethodPut, "/api/chat/dm/"+dmConversationID+"/avatar", strings.NewReader(`{"emoji":"🎉"}`))
		r.SetPathValue("conversationID", dmConversationID)

		dmTestHandler(provider).SetGroupAvatar(recorder, r)

		if recorder.Code != http.StatusUnsupportedMediaType || provider.setAvatarCalls != 0 {
			t.Fatalf("status = %d, calls = %d", recorder.Code, provider.setAvatarCalls)
		}
	})

	t.Run("anonymous", func(t *testing.T) {
		provider := &fakeDMProvider{}
		recorder := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodDelete, "/api/chat/dm/"+dmConversationID+"/avatar", nil)
		r.SetPathValue("conversationID", dmConversationID)

		dmTestHandler(provider).ClearGroupAvatar(recorder, r)

		if recorder.Code != http.StatusUnauthorized || provider.clearAvatarCall != 0 {
			t.Fatalf("status = %d, calls = %d", recorder.Code, provider.clearAvatarCall)
		}
	})
}

// groupAvatarRefusals are the store's and service's refusals and the status
// each becomes. They describe no state: an invalid emoji is 400, a
// non-participant 403, and a 1:1, a foreign workspace or an unknown id are one
// indistinguishable 404.
var groupAvatarRefusals = []struct {
	name       string
	err        error
	wantStatus int
}{
	{name: "invalid emoji", err: domain.ErrInvalidInput, wantStatus: http.StatusBadRequest},
	{name: "not a participant", err: domain.ErrForbidden, wantStatus: http.StatusForbidden},
	{name: "not a visible group", err: domain.ErrNotFound, wantStatus: http.StatusNotFound},
}

func TestDMHandler_SetGroupAvatar_MapsRefusalsWithoutEchoOrAnnouncement(t *testing.T) {
	for _, test := range groupAvatarRefusals {
		t.Run(test.name, func(t *testing.T) {
			handler, broadcast := recordingAvatarHandler(&fakeDMProvider{avatarErr: test.err})
			recorder := httptest.NewRecorder()

			handler.SetGroupAvatar(recorder, groupAvatarRequest(http.MethodPut, dmConversationID, strings.NewReader(`{"emoji":"<b>x</b>"}`)))

			if recorder.Code != test.wantStatus || strings.Contains(recorder.Body.String(), "<b>") {
				t.Fatalf("status = %d, body = %s; want %d without the refused value", recorder.Code, recorder.Body, test.wantStatus)
			}
			if len(broadcast.conversationUpdates) != 0 {
				t.Fatalf("a refusal published %v", broadcast.conversationUpdates)
			}
		})
	}
}

func TestDMHandler_ClearGroupAvatar_MapsRefusalsWithoutAnnouncement(t *testing.T) {
	for _, test := range groupAvatarRefusals {
		t.Run(test.name, func(t *testing.T) {
			handler, broadcast := recordingAvatarHandler(&fakeDMProvider{avatarErr: test.err})
			recorder := httptest.NewRecorder()

			handler.ClearGroupAvatar(recorder, groupAvatarRequest(http.MethodDelete, dmConversationID, nil))

			if recorder.Code != test.wantStatus || len(broadcast.conversationUpdates) != 0 {
				t.Fatalf("status = %d, updates = %v; want %d and none", recorder.Code, broadcast.conversationUpdates, test.wantStatus)
			}
		})
	}
}

func TestDMHandler_ClearGroupAvatar_ReturnsToAutomaticAndAnnounces(t *testing.T) {
	provider := &fakeDMProvider{}
	broadcast := &recordingBroadcaster{}
	handler := dmTestHandlerWithLimiter(provider, &fakeDMRateLimiter{}).WithMembersBroadcast(broadcast)
	recorder := httptest.NewRecorder()

	handler.ClearGroupAvatar(recorder, groupAvatarRequest(http.MethodDelete, dmConversationID, nil))

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", recorder.Code, recorder.Body)
	}
	if provider.clearAvatarCall != 1 || provider.lastAvatar.CallerID != msgTestUserID || provider.lastAvatar.AvatarEmoji != "" {
		t.Fatalf("calls = %d, forwarded %+v", provider.clearAvatarCall, provider.lastAvatar)
	}
	if len(broadcast.conversationUpdates) != 1 {
		t.Fatalf("conversationUpdates = %v, want exactly one", broadcast.conversationUpdates)
	}
}

func TestDMHandler_CreateGroup_ForwardsTheOptionalAvatarEmoji(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		want string
	}{
		{name: "automatic", body: `{"participant_user_ids":["a","b"],"title":"Infra"}`, want: ""},
		{name: "emoji", body: `{"participant_user_ids":["a","b"],"title":"Infra","avatar_emoji":"🚀"}`, want: "🚀"},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := &fakeDMProvider{groupOutput: service.CreateGroupConversationOutput{Conversation: domain.DMConversation{ID: dmConversationID}}}
			recorder := httptest.NewRecorder()
			r := requestWithUser(http.MethodPost, "/api/chat/dms/group", strings.NewReader(test.body))
			r.Header.Set("Content-Type", "application/json")

			dmTestHandler(provider).CreateGroup(recorder, r)

			if recorder.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201: %s", recorder.Code, recorder.Body)
			}
			if provider.lastGroupInput.AvatarEmoji != test.want {
				t.Fatalf("avatar emoji = %q, want %q", provider.lastGroupInput.AvatarEmoji, test.want)
			}
		})
	}
}

// Strict decoding is unchanged: the identity adds one field, never a way to
// state initials, a colour, or who the caller is.
func TestDMHandler_CreateGroup_StillRefusesUnknownIdentityFields(t *testing.T) {
	for _, body := range []string{
		`{"participant_user_ids":["a","b"],"avatar_initials":"AB"}`,
		`{"participant_user_ids":["a","b"],"avatar_color":"rose"}`,
		`{"participant_user_ids":["a","b"],"avatar_emoji":"🚀","created_by":"x"}`,
	} {
		t.Run(body, func(t *testing.T) {
			provider := &fakeDMProvider{}
			recorder := httptest.NewRecorder()
			r := requestWithUser(http.MethodPost, "/api/chat/dms/group", strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")

			dmTestHandler(provider).CreateGroup(recorder, r)

			if recorder.Code != http.StatusBadRequest || provider.groupCreateCalls != 0 {
				t.Fatalf("status = %d, calls = %d", recorder.Code, provider.groupCreateCalls)
			}
		})
	}
}
