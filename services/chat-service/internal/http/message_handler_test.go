package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	httpapi "github.com/nicrepository/nchat/services/chat-service/internal/http"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// ── Fake dependencies ────────────────────────────────────────────────────────

type fakeWorkspaceResolver struct {
	workspace domain.Workspace
	err       error
}

func (f *fakeWorkspaceResolver) GetDefaultWorkspace(_ context.Context) (domain.Workspace, error) {
	return f.workspace, f.err
}

type fakeMessageProvider struct {
	channelOut    service.ListChannelMessagesOutput
	channelOutErr error
	createdMsg    domain.Message
	createChErr   error
	channelMsg    domain.Message
	channelMsgErr error
	dmOut         service.ListDMMessagesOutput
	dmOutErr      error
	createDMMsg   domain.Message
	createDMErr   error
	dmMsg         domain.Message
	dmMsgErr      error
	editedMsg     domain.Message
	editErr       error
	deletedMsg    domain.Message
	deleteErr     error
	history       []domain.MessageEditHistory
	historyErr    error

	lastCreateChannelInput service.CreateChannelMessageInput
	lastCreateDMInput      service.CreateDMMessageInput
	lastListChannelInput   service.ListChannelMessagesInput
	lastListDMInput        service.ListDMMessagesInput
	lastGetChannelInput    service.GetChannelMessageInput
	lastGetDMInput         service.GetDMMessageInput
	lastEditInput          service.EditMessageInput
	lastDeleteInput        service.DeleteMessageInput
	lastHistoryInput       service.GetMessageEditHistoryInput
}

func (f *fakeMessageProvider) EditMessage(_ context.Context, in service.EditMessageInput) (domain.Message, error) {
	f.lastEditInput = in
	f.editedMsg.BodyText = in.Body
	f.editedMsg.BodyFormat = in.BodyFormat
	return f.editedMsg, f.editErr
}

func (f *fakeMessageProvider) DeleteMessage(_ context.Context, in service.DeleteMessageInput) (domain.Message, error) {
	f.lastDeleteInput = in
	return f.deletedMsg, f.deleteErr
}

func (f *fakeMessageProvider) GetMessageEditHistory(_ context.Context, in service.GetMessageEditHistoryInput) ([]domain.MessageEditHistory, error) {
	f.lastHistoryInput = in
	return f.history, f.historyErr
}

type fakeMentionProvider struct {
	out       service.SearchMentionsOutput
	err       error
	lastInput service.SearchMentionsInput
}

type fakeEditLimiter struct {
	allowed bool
	err     error
}

func (f fakeEditLimiter) AllowAction(context.Context, string, string) (bool, error) {
	return f.allowed, f.err
}

type fakeSettingsAuthorizer struct {
	allowed bool
	err     error
}

func (f fakeSettingsAuthorizer) CanManageWorkspace(context.Context, string, string) (bool, error) {
	return f.allowed, f.err
}

type fakeWorkspaceSettingsStore struct {
	calls       int
	lastSeconds *int
	err         error
}

func (f *fakeWorkspaceSettingsStore) UpdateEditWindow(_ context.Context, workspaceID, _ string, seconds *int) (domain.Workspace, error) {
	f.calls++
	f.lastSeconds = seconds
	return domain.Workspace{ID: workspaceID, EditWindowSeconds: seconds}, f.err
}

func (f *fakeMentionProvider) SearchMentions(_ context.Context, in service.SearchMentionsInput) (service.SearchMentionsOutput, error) {
	f.lastInput = in
	return f.out, f.err
}

func (f *fakeMessageProvider) ListChannelMessages(_ context.Context, in service.ListChannelMessagesInput) (service.ListChannelMessagesOutput, error) {
	f.lastListChannelInput = in
	return f.channelOut, f.channelOutErr
}

func (f *fakeMessageProvider) CreateChannelMessage(_ context.Context, in service.CreateChannelMessageInput) (domain.Message, error) {
	f.lastCreateChannelInput = in
	return f.createdMsg, f.createChErr
}

func (f *fakeMessageProvider) ListDMMessages(_ context.Context, in service.ListDMMessagesInput) (service.ListDMMessagesOutput, error) {
	f.lastListDMInput = in
	return f.dmOut, f.dmOutErr
}

func (f *fakeMessageProvider) CreateDMMessage(_ context.Context, in service.CreateDMMessageInput) (domain.Message, error) {
	f.lastCreateDMInput = in
	return f.createDMMsg, f.createDMErr
}

func (f *fakeMessageProvider) GetChannelMessage(_ context.Context, in service.GetChannelMessageInput) (domain.Message, error) {
	f.lastGetChannelInput = in
	return f.channelMsg, f.channelMsgErr
}

func (f *fakeMessageProvider) GetDMMessage(_ context.Context, in service.GetDMMessageInput) (domain.Message, error) {
	f.lastGetDMInput = in
	return f.dmMsg, f.dmMsgErr
}

// ── Helpers ──────────────────────────────────────────────────────────────────

const (
	testWorkspaceID    = "11111111-1111-1111-1111-111111111111"
	testChannelID      = "22222222-2222-2222-2222-222222222222"
	testConversationID = "33333333-3333-3333-3333-333333333333"
	msgTestUserID      = "44444444-4444-4444-4444-444444444444"
	testMessageID      = "55555555-5555-5555-5555-555555555555"
)

func activeWorkspace() domain.Workspace {
	return domain.Workspace{ID: testWorkspaceID, Status: domain.WorkspaceStatusActive}
}

func testNow() time.Time {
	return time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)
}

func testMessage() domain.Message {
	return domain.Message{
		ID:          testMessageID,
		WorkspaceID: testWorkspaceID,
		ChannelID:   testChannelID,
		SenderID:    msgTestUserID,
		Kind:        domain.MessageKindUser,
		BodyText:    "hello",
		BodyFormat:  domain.MessageBodyFormatV1,
		Status:      domain.MessageStatusActive,
		CreatedAt:   testNow(),
		UpdatedAt:   testNow(),
		Reactions:   []domain.MessageReaction{{Emoji: "👍", Count: 2, ReactedByMe: true}},
	}
}

// makeHandlerWithUser builds a MessageHandler and wraps it so it has msgTestUserID in context.
func makeHandlerWithUser(ws *fakeWorkspaceResolver, msgs *fakeMessageProvider) *httpapi.MessageHandler {
	return httpapi.NewMessageHandler(ws, msgs, nil)
}

// requestWithUser adds the user ID to the request context as BearerAuth would.
func requestWithUser(method, target string, body io.Reader) *http.Request {
	r := httptest.NewRequest(method, target, body)
	ctx := context.WithValue(r.Context(), httpapi.ExportCtxKeyUserID, msgTestUserID)
	return r.WithContext(ctx)
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&m); err != nil {
		t.Fatalf("decode response body: %v", err)
	}
	return m
}

func TestMessageHandler_EditMessage_RejectsMalformedAndUnknownFields(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
	}{
		{name: "unknown fields", body: `{"body":"edited","body_format":"v1","author_id":"spoof","created_at":"2020-01-01T00:00:00Z"}`},
		{name: "malformed JSON", body: `{"body":"edited","body_format":`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			handler := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{}).
				WithEditing(nil, nil, fakeEditLimiter{allowed: true})
			request := requestWithUser(http.MethodPatch, "/api/chat/messages/"+testMessageID, strings.NewReader(tt.body))
			request.SetPathValue("messageID", testMessageID)
			recorder := httptest.NewRecorder()

			handler.EditMessage(recorder, request)

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d", recorder.Code)
			}
		})
	}
}

func TestMessageHandler_EditMessage_MapsInvalidBodyFormatToBadRequest(t *testing.T) {
	handler := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{editErr: domain.ErrInvalidInput}).
		WithEditing(nil, nil, fakeEditLimiter{allowed: true})
	request := requestWithUser(http.MethodPatch, "/api/chat/messages/"+testMessageID,
		strings.NewReader(`{"body":"edited","body_format":"v4"}`))
	request.SetPathValue("messageID", testMessageID)
	recorder := httptest.NewRecorder()

	handler.EditMessage(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", recorder.Code)
	}
}

func TestMessageHandler_EditMessage_ReturnsUpdatedMessage(t *testing.T) {
	editedAt := testNow().Add(time.Minute)
	messages := &fakeMessageProvider{editedMsg: domain.Message{
		ID: testMessageID, SenderID: msgTestUserID, Kind: domain.MessageKindUser,
		Status: domain.MessageStatusActive, EditedAt: editedAt, EditCount: 1,
	}}
	handler := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, messages).
		WithEditing(nil, nil, fakeEditLimiter{allowed: true})
	request := requestWithUser(http.MethodPatch, "/api/chat/messages/"+testMessageID,
		strings.NewReader(`{"body":"edited","body_format":"v3"}`))
	request.SetPathValue("messageID", testMessageID)
	recorder := httptest.NewRecorder()

	handler.EditMessage(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	data := decodeBody(t, recorder)["data"].(map[string]any)
	if data["id"] != testMessageID || data["body_text"] != "edited" || data["body_format"] != "v3" || data["is_edited"] != true || data["edit_count"] != float64(1) {
		t.Fatalf("unexpected edit response: %#v", data)
	}
	if messages.lastEditInput.EditorID != msgTestUserID || messages.lastEditInput.Body != "edited" || messages.lastEditInput.BodyFormat != domain.MessageBodyFormatV3 {
		t.Fatalf("unexpected service input: %+v", messages.lastEditInput)
	}
}

func TestMessageHandler_DeleteMessage_ReturnsSanitizedPlaceholder(t *testing.T) {
	deletedAt := testNow().Add(time.Minute)
	messages := &fakeMessageProvider{deletedMsg: domain.Message{
		ID: testMessageID, WorkspaceID: testWorkspaceID, ChannelID: testChannelID,
		SenderID: msgTestUserID, Kind: domain.MessageKindUser, BodyText: "secret body",
		BodyFormat: domain.MessageBodyFormatV3, Status: domain.MessageStatusDeleted,
		DeletedAt: deletedAt, CreatedAt: testNow(), UpdatedAt: deletedAt,
		Quoted: &domain.QuotedMessage{ID: "parent", BodyText: "quoted secret"},
	}}
	request := requestWithUser(http.MethodDelete, "/api/chat/messages/"+testMessageID, nil)
	request.SetPathValue("messageID", testMessageID)
	recorder := httptest.NewRecorder()

	makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, messages).DeleteMessage(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	body := decodeBody(t, recorder)
	data := body["data"].(map[string]any)
	if data["status"] != "deleted" || data["is_removed"] != true || data["deleted_at"] == nil {
		t.Fatalf("unexpected placeholder: %#v", data)
	}
	if _, ok := data["body_text"]; ok {
		t.Fatalf("deleted response leaked body_text: %#v", data)
	}
	if _, ok := data["quoted"]; ok {
		t.Fatalf("deleted response leaked quote: %#v", data)
	}
	if messages.lastDeleteInput != (service.DeleteMessageInput{WorkspaceID: testWorkspaceID, MessageID: testMessageID, RequesterID: msgTestUserID}) {
		t.Fatalf("unexpected delete input: %+v", messages.lastDeleteInput)
	}
}

func TestMessageHandler_DeleteMessage_ValidatesAuthIDAndAuthorization(t *testing.T) {
	for _, tt := range []struct {
		name       string
		request    *http.Request
		provider   *fakeMessageProvider
		wantStatus int
	}{
		{
			name: "invalid id", request: requestWithUser(http.MethodDelete, "/api/chat/messages/not-a-uuid", nil),
			provider: &fakeMessageProvider{}, wantStatus: http.StatusBadRequest,
		},
		{
			name: "unauthenticated", request: httptest.NewRequest(http.MethodDelete, "/api/chat/messages/"+testMessageID, nil),
			provider: &fakeMessageProvider{}, wantStatus: http.StatusUnauthorized,
		},
		{
			name: "other author", request: requestWithUser(http.MethodDelete, "/api/chat/messages/"+testMessageID, nil),
			provider: &fakeMessageProvider{deleteErr: domain.ErrNotFound}, wantStatus: http.StatusNotFound,
		},
		{
			name: "inaccessible or missing", request: requestWithUser(http.MethodDelete, "/api/chat/messages/"+testMessageID, nil),
			provider: &fakeMessageProvider{deleteErr: domain.ErrNotFound}, wantStatus: http.StatusNotFound,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tt.request.SetPathValue("messageID", map[bool]string{true: "not-a-uuid", false: testMessageID}[tt.name == "invalid id"])
			recorder := httptest.NewRecorder()
			makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, tt.provider).DeleteMessage(recorder, tt.request)
			if recorder.Code != tt.wantStatus {
				t.Fatalf("expected %d, got %d: %s", tt.wantStatus, recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestMessageHandler_EditMessage_EnforcesAuthenticationAndRateLimit(t *testing.T) {
	t.Run("unauthenticated", func(t *testing.T) {
		handler := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{}).
			WithEditing(nil, nil, fakeEditLimiter{allowed: true})
		request := httptest.NewRequest(http.MethodPatch, "/api/chat/messages/"+testMessageID, strings.NewReader(`{"body":"edited","body_format":"v1"}`))
		request.SetPathValue("messageID", testMessageID)
		recorder := httptest.NewRecorder()
		handler.EditMessage(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", recorder.Code)
		}
	})

	for _, tt := range []struct {
		name    string
		limiter interface {
			AllowAction(context.Context, string, string) (bool, error)
		}
		wantCode  int
		wantRetry string
	}{
		{name: "limiter unavailable", wantCode: http.StatusServiceUnavailable},
		{name: "limiter error", limiter: fakeEditLimiter{err: errors.New("valkey unavailable")}, wantCode: http.StatusServiceUnavailable},
		{name: "rate limited", limiter: fakeEditLimiter{allowed: false}, wantCode: http.StatusTooManyRequests, wantRetry: "60"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			handler := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{}).
				WithEditing(nil, nil, tt.limiter)
			request := requestWithUser(http.MethodPatch, "/api/chat/messages/"+testMessageID, strings.NewReader(`{"body":"edited","body_format":"v1"}`))
			request.SetPathValue("messageID", testMessageID)
			recorder := httptest.NewRecorder()
			handler.EditMessage(recorder, request)
			if recorder.Code != tt.wantCode || recorder.Header().Get("Retry-After") != tt.wantRetry {
				t.Fatalf("status=%d retry-after=%q", recorder.Code, recorder.Header().Get("Retry-After"))
			}
		})
	}
}

func TestMessageEditingHandlers_RejectInvalidTargetIDs(t *testing.T) {
	for _, tt := range []struct {
		name   string
		invoke func(*httpapi.MessageHandler, http.ResponseWriter, *http.Request)
		path   string
		param  string
	}{
		{name: "edit message", path: "/api/chat/messages/not-a-uuid", param: "messageID", invoke: (*httpapi.MessageHandler).EditMessage},
		{name: "message history", path: "/api/chat/messages/not-a-uuid/history", param: "messageID", invoke: (*httpapi.MessageHandler).GetMessageEditHistory},
		{name: "workspace settings", path: "/api/v1/workspaces/not-a-uuid/settings", param: "workspaceID", invoke: (*httpapi.MessageHandler).UpdateWorkspaceEditWindow},
	} {
		t.Run(tt.name, func(t *testing.T) {
			handler := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{}).
				WithEditing(&fakeWorkspaceSettingsStore{}, fakeSettingsAuthorizer{allowed: true}, fakeEditLimiter{allowed: true})
			request := requestWithUser(http.MethodPatch, tt.path, nil)
			request.SetPathValue(tt.param, "not-a-uuid")
			recorder := httptest.NewRecorder()

			tt.invoke(handler, recorder, request)

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d", recorder.Code)
			}
		})
	}
}

func TestMessageEditingHandlers_ReturnNotFoundWhenWorkspaceCannotBeResolved(t *testing.T) {
	for _, tt := range []struct {
		name   string
		path   string
		invoke func(*httpapi.MessageHandler, http.ResponseWriter, *http.Request)
	}{
		{name: "edit message", path: "/api/chat/messages/" + testMessageID, invoke: (*httpapi.MessageHandler).EditMessage},
		{name: "message history", path: "/api/chat/messages/" + testMessageID + "/history", invoke: (*httpapi.MessageHandler).GetMessageEditHistory},
	} {
		t.Run(tt.name, func(t *testing.T) {
			handler := makeHandlerWithUser(&fakeWorkspaceResolver{err: domain.ErrNotFound}, &fakeMessageProvider{}).
				WithEditing(nil, nil, fakeEditLimiter{allowed: true})
			request := requestWithUser(http.MethodPatch, tt.path, strings.NewReader(`{"body":"edited","body_format":"v1"}`))
			request.SetPathValue("messageID", testMessageID)
			recorder := httptest.NewRecorder()

			tt.invoke(handler, recorder, request)

			if recorder.Code != http.StatusNotFound {
				t.Fatalf("expected 404, got %d", recorder.Code)
			}
		})
	}
}

func TestMessageHandler_GetMessageEditHistory_ReturnsPageAndEmptyList(t *testing.T) {
	newest := testNow()
	for _, tt := range []struct {
		name    string
		history []domain.MessageEditHistory
		wantLen int
	}{
		{name: "page", history: []domain.MessageEditHistory{
			{ID: "hist-3", MessageID: testMessageID, Body: "third", BodyFormat: domain.MessageBodyFormatV2, EditorUserID: msgTestUserID, VersionedAt: newest},
			{ID: "hist-2", MessageID: testMessageID, Body: "second", BodyFormat: domain.MessageBodyFormatV1, EditorUserID: msgTestUserID, VersionedAt: newest.Add(-time.Minute)},
		}, wantLen: 2},
		{name: "no edits", history: []domain.MessageEditHistory{}, wantLen: 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			messages := &fakeMessageProvider{history: tt.history}
			handler := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, messages)
			request := requestWithUser(http.MethodGet, "/api/chat/messages/"+testMessageID+"/history?limit=2&offset=1", nil)
			request.SetPathValue("messageID", testMessageID)
			recorder := httptest.NewRecorder()

			handler.GetMessageEditHistory(recorder, request)

			if recorder.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
			}
			data := decodeBody(t, recorder)["data"].(map[string]any)
			history := data["history"].([]any)
			if len(history) != tt.wantLen || data["offset"] != float64(1) {
				t.Fatalf("unexpected history response: %#v", data)
			}
			if tt.wantLen > 0 && history[0].(map[string]any)["id"] != "hist-3" {
				t.Fatalf("history order changed: %#v", history)
			}
			if messages.lastHistoryInput.Limit != 2 || messages.lastHistoryInput.Offset != 1 || messages.lastHistoryInput.CallerID != msgTestUserID {
				t.Fatalf("unexpected service input: %+v", messages.lastHistoryInput)
			}
		})
	}
}

func TestMessageHandler_GetMessageEditHistory_NonMemberAndMissingReturnNotFound(t *testing.T) {
	for _, name := range []string{"non-member", "missing-message"} {
		t.Run(name, func(t *testing.T) {
			handler := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{historyErr: domain.ErrNotFound})
			request := requestWithUser(http.MethodGet, "/api/chat/messages/"+testMessageID+"/history", nil)
			request.SetPathValue("messageID", testMessageID)
			recorder := httptest.NewRecorder()

			handler.GetMessageEditHistory(recorder, request)

			if recorder.Code != http.StatusNotFound {
				t.Fatalf("expected 404, got %d", recorder.Code)
			}
		})
	}
}

func TestMessageHandler_GetMessageEditHistory_RejectsUnauthenticatedAndInvalidOffset(t *testing.T) {
	t.Run("unauthenticated", func(t *testing.T) {
		handler := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{})
		request := httptest.NewRequest(http.MethodGet, "/api/chat/messages/"+testMessageID+"/history", nil)
		request.SetPathValue("messageID", testMessageID)
		recorder := httptest.NewRecorder()
		handler.GetMessageEditHistory(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", recorder.Code)
		}
	})

	for _, offset := range []string{"invalid", "-1", "10001"} {
		t.Run(offset, func(t *testing.T) {
			handler := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{})
			request := requestWithUser(http.MethodGet, "/api/chat/messages/"+testMessageID+"/history?offset="+offset, nil)
			request.SetPathValue("messageID", testMessageID)
			recorder := httptest.NewRecorder()
			handler.GetMessageEditHistory(recorder, request)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d", recorder.Code)
			}
		})
	}
}

func TestMessageHandler_EditMessage_MapsNonAuthorToForbidden(t *testing.T) {
	messages := &fakeMessageProvider{editErr: domain.ErrEditForbidden}
	handler := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, messages).
		WithEditing(nil, nil, fakeEditLimiter{allowed: true})
	request := requestWithUser(http.MethodPatch, "/api/chat/messages/"+testMessageID,
		strings.NewReader(`{"body":"edited","body_format":"v1"}`))
	request.SetPathValue("messageID", testMessageID)
	recorder := httptest.NewRecorder()

	handler.EditMessage(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", recorder.Code)
	}
}

func TestMessageHandler_UpdateWorkspaceEditWindow_RequiresAdmin(t *testing.T) {
	settings := &fakeWorkspaceSettingsStore{}
	handler := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{}).
		WithEditing(settings, fakeSettingsAuthorizer{allowed: false}, fakeEditLimiter{allowed: true})
	request := requestWithUser(http.MethodPatch, "/api/v1/workspaces/"+testWorkspaceID+"/settings",
		strings.NewReader(`{"edit_window_seconds":900}`))
	request.SetPathValue("workspaceID", testWorkspaceID)
	recorder := httptest.NewRecorder()

	handler.UpdateWorkspaceEditWindow(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", recorder.Code)
	}
	if settings.calls != 0 {
		t.Fatalf("unauthorized settings update reached storage %d times", settings.calls)
	}
}

func TestMessageHandler_UpdateWorkspaceEditWindow_ValidatesBoundsAndAllowsNull(t *testing.T) {
	settings := &fakeWorkspaceSettingsStore{}
	handler := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{}).
		WithEditing(settings, fakeSettingsAuthorizer{allowed: true}, fakeEditLimiter{allowed: true})

	for _, value := range []int{-1, 0, 86401} {
		request := requestWithUser(http.MethodPatch, "/api/v1/workspaces/"+testWorkspaceID+"/settings",
			strings.NewReader(`{"edit_window_seconds":`+fmt.Sprint(value)+`}`))
		request.SetPathValue("workspaceID", testWorkspaceID)
		recorder := httptest.NewRecorder()
		handler.UpdateWorkspaceEditWindow(recorder, request)
		if recorder.Code != http.StatusBadRequest || settings.calls != 0 {
			t.Fatalf("value %d: status=%d storage calls=%d", value, recorder.Code, settings.calls)
		}
	}
	for _, value := range []int{30, 86400} {
		request := requestWithUser(http.MethodPatch, "/api/v1/workspaces/"+testWorkspaceID+"/settings",
			strings.NewReader(`{"edit_window_seconds":`+fmt.Sprint(value)+`}`))
		request.SetPathValue("workspaceID", testWorkspaceID)
		recorder := httptest.NewRecorder()
		handler.UpdateWorkspaceEditWindow(recorder, request)
		if recorder.Code != http.StatusOK || settings.lastSeconds == nil || *settings.lastSeconds != value {
			t.Fatalf("value %d: status=%d stored=%v", value, recorder.Code, settings.lastSeconds)
		}
	}

	unlimited := requestWithUser(http.MethodPatch, "/api/v1/workspaces/"+testWorkspaceID+"/settings",
		strings.NewReader(`{"edit_window_seconds":null}`))
	unlimited.SetPathValue("workspaceID", testWorkspaceID)
	unlimitedRecorder := httptest.NewRecorder()
	handler.UpdateWorkspaceEditWindow(unlimitedRecorder, unlimited)
	if unlimitedRecorder.Code != http.StatusOK || settings.calls != 3 || settings.lastSeconds != nil {
		t.Fatalf("null window status=%d storage calls=%d", unlimitedRecorder.Code, settings.calls)
	}
}

func TestMessageHandler_UpdateWorkspaceEditWindow_HandlesBoundaryFailures(t *testing.T) {
	t.Run("dependencies unavailable", func(t *testing.T) {
		handler := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{})
		request := requestWithUser(http.MethodPatch, "/api/v1/workspaces/"+testWorkspaceID+"/settings", strings.NewReader(`{"edit_window_seconds":900}`))
		request.SetPathValue("workspaceID", testWorkspaceID)
		recorder := httptest.NewRecorder()
		handler.UpdateWorkspaceEditWindow(recorder, request)
		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected 503, got %d", recorder.Code)
		}
	})

	t.Run("unauthenticated", func(t *testing.T) {
		handler := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{}).
			WithEditing(&fakeWorkspaceSettingsStore{}, fakeSettingsAuthorizer{allowed: true}, fakeEditLimiter{allowed: true})
		request := httptest.NewRequest(http.MethodPatch, "/api/v1/workspaces/"+testWorkspaceID+"/settings", strings.NewReader(`{"edit_window_seconds":900}`))
		request.SetPathValue("workspaceID", testWorkspaceID)
		recorder := httptest.NewRecorder()
		handler.UpdateWorkspaceEditWindow(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", recorder.Code)
		}
	})

	t.Run("missing setting", func(t *testing.T) {
		handler := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{}).
			WithEditing(&fakeWorkspaceSettingsStore{}, fakeSettingsAuthorizer{allowed: true}, fakeEditLimiter{allowed: true})
		request := requestWithUser(http.MethodPatch, "/api/v1/workspaces/"+testWorkspaceID+"/settings", strings.NewReader(`{}`))
		request.SetPathValue("workspaceID", testWorkspaceID)
		recorder := httptest.NewRecorder()
		handler.UpdateWorkspaceEditWindow(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d", recorder.Code)
		}
	})

	t.Run("authorization lookup error", func(t *testing.T) {
		handler := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{}).
			WithEditing(&fakeWorkspaceSettingsStore{}, fakeSettingsAuthorizer{err: domain.ErrNotFound}, fakeEditLimiter{allowed: true})
		request := requestWithUser(http.MethodPatch, "/api/v1/workspaces/"+testWorkspaceID+"/settings", strings.NewReader(`{"edit_window_seconds":900}`))
		request.SetPathValue("workspaceID", testWorkspaceID)
		recorder := httptest.NewRecorder()
		handler.UpdateWorkspaceEditWindow(recorder, request)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d", recorder.Code)
		}
	})

	t.Run("atomic storage backstop", func(t *testing.T) {
		settings := &fakeWorkspaceSettingsStore{err: domain.ErrForbidden}
		handler := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{}).
			WithEditing(settings, fakeSettingsAuthorizer{allowed: true}, fakeEditLimiter{allowed: true})
		request := requestWithUser(http.MethodPatch, "/api/v1/workspaces/"+testWorkspaceID+"/settings", strings.NewReader(`{"edit_window_seconds":900}`))
		request.SetPathValue("workspaceID", testWorkspaceID)
		recorder := httptest.NewRecorder()
		handler.UpdateWorkspaceEditWindow(recorder, request)
		if recorder.Code != http.StatusForbidden || settings.calls != 1 {
			t.Fatalf("status=%d storage calls=%d", recorder.Code, settings.calls)
		}
	})
}

func TestMessageHandler_ListAllowedReactionEmojis(t *testing.T) {
	h := httpapi.NewMessageHandler(nil, nil, nil)
	rec := httptest.NewRecorder()
	h.ListAllowedReactionEmojis(rec, httptest.NewRequest(http.MethodGet, httpapi.RouteAllowedReactionEmojis, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	emojis, ok := body["data"].(map[string]any)["emojis"].([]any)
	if !ok || len(emojis) < 16 || len(emojis) > 20 {
		t.Fatalf("unexpected allowlist response: %#v", body)
	}
}

// ── ListChannelMessages ──────────────────────────────────────────────────────

func TestMessageHandler_ListChannelMessages_UnauthenticatedReturns401(t *testing.T) {
	h := httpapi.NewMessageHandler(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{}, nil)
	rec := httptest.NewRecorder()
	// No user in context (no BearerAuth applied)
	r := httptest.NewRequest(http.MethodGet, "/api/chat/channels/"+testChannelID+"/messages", nil)
	r.SetPathValue("channelID", testChannelID)
	h.ListChannelMessages(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestMessageHandler_ListChannelMessages_InvalidChannelIDReturns400(t *testing.T) {
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{})
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodGet, "/api/chat/channels/not-a-uuid/messages", nil)
	r.SetPathValue("channelID", "not-a-uuid")
	h.ListChannelMessages(rec, r)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestMessageHandler_ListChannelMessages_InvalidCursorReturns400(t *testing.T) {
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{})
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodGet, "/api/chat/channels/"+testChannelID+"/messages?before=!!!invalid!!!", nil)
	r.SetPathValue("channelID", testChannelID)
	h.ListChannelMessages(rec, r)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid cursor, got %d", rec.Code)
	}
}

func TestMessageHandler_ListChannelMessages_WorkspaceNotFoundReturns404(t *testing.T) {
	h := makeHandlerWithUser(&fakeWorkspaceResolver{err: domain.ErrNotFound}, &fakeMessageProvider{})
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodGet, "/api/chat/channels/"+testChannelID+"/messages", nil)
	r.SetPathValue("channelID", testChannelID)
	h.ListChannelMessages(rec, r)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestMessageHandler_ListChannelMessages_SuccessReturnsMessages(t *testing.T) {
	msgs := &fakeMessageProvider{channelOut: service.ListChannelMessagesOutput{
		Messages: []domain.Message{testMessage()},
	}}
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, msgs)
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodGet, "/api/chat/channels/"+testChannelID+"/messages", nil)
	r.SetPathValue("channelID", testChannelID)
	h.ListChannelMessages(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	msgsArr, ok := body["data"].(map[string]any)["messages"].([]any)
	if !ok {
		t.Fatalf("expected messages array, got %T", body["data"].(map[string]any)["messages"])
	}
	if len(msgsArr) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgsArr))
	}
	if got := msgsArr[0].(map[string]any)["body_format"]; got != "v1" {
		t.Fatalf("expected legacy body_format v1, got %v", got)
	}
	reactions, ok := msgsArr[0].(map[string]any)["reactions"].([]any)
	if !ok || len(reactions) != 1 || reactions[0].(map[string]any)["reacted_by_me"] != true {
		t.Fatalf("expected reaction aggregate, got %#v", msgsArr[0].(map[string]any)["reactions"])
	}
}

func TestMessageHandler_ListChannelMessages_EmptyListReturnsEmptyArray(t *testing.T) {
	msgs := &fakeMessageProvider{channelOut: service.ListChannelMessagesOutput{Messages: nil}}
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, msgs)
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodGet, "/api/chat/channels/"+testChannelID+"/messages", nil)
	r.SetPathValue("channelID", testChannelID)
	h.ListChannelMessages(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := decodeBody(t, rec)
	msgsArr, ok := body["data"].(map[string]any)["messages"].([]any)
	if !ok || msgsArr == nil {
		t.Fatalf("expected non-null empty array, got %v", body["data"].(map[string]any)["messages"])
	}
}

func TestMessageHandler_ListChannelMessages_DeletedMessageBodyWithheld(t *testing.T) {
	deleted := testMessage()
	deleted.Status = domain.MessageStatusDeleted
	deleted.BodyText = "this should not appear"
	msgs := &fakeMessageProvider{channelOut: service.ListChannelMessagesOutput{
		Messages: []domain.Message{deleted},
	}}
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, msgs)
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodGet, "/api/chat/channels/"+testChannelID+"/messages", nil)
	r.SetPathValue("channelID", testChannelID)
	h.ListChannelMessages(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "this should not appear") {
		t.Fatal("deleted message body_text must not appear in response")
	}
}

func TestMessageHandler_ListChannelMessages_QuotedDeletedParentBodyWithheld(t *testing.T) {
	deletedAt := testNow().Add(-time.Minute)
	msg := testMessage()
	msg.ParentMessageID = "66666666-6666-6666-6666-666666666666"
	msg.Quoted = &domain.QuotedMessage{
		ID:         msg.ParentMessageID,
		AuthorID:   msgTestUserID,
		BodyText:   "quoted secret",
		BodyFormat: domain.MessageBodyFormatV1,
		Status:     domain.MessageStatusDeleted,
		DeletedAt:  deletedAt,
		CreatedAt:  testNow().Add(-time.Hour),
	}
	msgs := &fakeMessageProvider{channelOut: service.ListChannelMessagesOutput{
		Messages: []domain.Message{msg},
	}}
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, msgs)
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodGet, "/api/chat/channels/"+testChannelID+"/messages", nil)
	r.SetPathValue("channelID", testChannelID)
	h.ListChannelMessages(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "quoted secret") {
		t.Fatal("deleted quoted parent body must not appear in response")
	}
	body := decodeBody(t, rec)
	first := body["data"].(map[string]any)["messages"].([]any)[0].(map[string]any)
	quoted := first["quoted"].(map[string]any)
	if quoted["is_removed"] != true {
		t.Fatalf("expected removed quoted parent, got %#v", quoted)
	}
	if _, ok := quoted["body"]; ok {
		t.Fatalf("deleted quoted parent must omit body: %#v", quoted)
	}
}

func TestMessageHandler_ListChannelMessages_InaccessibleChannelReturnsNotFound(t *testing.T) {
	msgs := &fakeMessageProvider{channelOutErr: domain.ErrNotFound}
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, msgs)
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodGet, "/api/chat/channels/"+testChannelID+"/messages", nil)
	r.SetPathValue("channelID", testChannelID)
	h.ListChannelMessages(rec, r)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for inaccessible channel, got %d", rec.Code)
	}
}

// ── SearchMentions ───────────────────────────────────────────────────────────

func TestMessageHandler_SearchMentions_RejectsInvalidOrUnauthenticatedRequests(t *testing.T) {
	t.Run("invalid channel ID", func(t *testing.T) {
		h := httpapi.NewMessageHandler(
			&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{}, &fakeMentionProvider{},
		)
		rec := httptest.NewRecorder()
		r := requestWithUser(http.MethodGet, "/api/chat/channels/not-a-uuid/mentions", nil)
		r.SetPathValue("channelID", "not-a-uuid")
		h.SearchMentions(rec, r)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d", rec.Code)
		}
	})

	t.Run("missing authentication", func(t *testing.T) {
		h := httpapi.NewMessageHandler(
			&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{}, &fakeMentionProvider{},
		)
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/api/chat/channels/"+testChannelID+"/mentions", nil)
		r.SetPathValue("channelID", testChannelID)
		h.SearchMentions(rec, r)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", rec.Code)
		}
	})

	t.Run("missing mention service", func(t *testing.T) {
		h := httpapi.NewMessageHandler(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{}, nil)
		rec := httptest.NewRecorder()
		h.SearchMentions(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected 503, got %d", rec.Code)
		}
	})
}

func TestMessageHandler_SearchMentions_ReturnsAuthorizedCandidates(t *testing.T) {
	mentions := &fakeMentionProvider{out: service.SearchMentionsOutput{
		Users:    []domain.MentionCandidate{{Type: domain.MentionTypeUser, ID: msgTestUserID, Label: "Alice"}},
		Channels: []domain.MentionCandidate{{Type: domain.MentionTypeChannel, ID: testChannelID, Label: "geral"}},
	}}
	h := httpapi.NewMessageHandler(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{}, mentions)
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodGet, "/api/chat/channels/"+testChannelID+"/mentions?q=al", nil)
	r.SetPathValue("channelID", testChannelID)

	h.SearchMentions(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if mentions.lastInput.WorkspaceID != testWorkspaceID || mentions.lastInput.ChannelID != testChannelID || mentions.lastInput.CallerID != msgTestUserID || mentions.lastInput.Query != "al" {
		t.Fatalf("unexpected service input: %+v", mentions.lastInput)
	}
	body := decodeBody(t, rec)["data"].(map[string]any)
	if len(body["users"].([]any)) != 1 || len(body["channels"].([]any)) != 1 {
		t.Fatalf("unexpected candidates: %v", body)
	}
}

func TestMessageHandler_SearchMentions_PrivateChannelOutsiderGets404(t *testing.T) {
	h := httpapi.NewMessageHandler(
		&fakeWorkspaceResolver{workspace: activeWorkspace()},
		&fakeMessageProvider{},
		&fakeMentionProvider{err: domain.ErrNotFound},
	)
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodGet, "/api/chat/channels/"+testChannelID+"/mentions?q=a", nil)
	r.SetPathValue("channelID", testChannelID)

	h.SearchMentions(rec, r)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

// ── CreateChannelMessage ─────────────────────────────────────────────────────

func TestMessageHandler_CreateChannelMessage_UnauthenticatedReturns401(t *testing.T) {
	h := httpapi.NewMessageHandler(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{}, nil)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/chat/channels/"+testChannelID+"/messages",
		strings.NewReader(`{"body_text":"hello"}`))
	r.SetPathValue("channelID", testChannelID)
	h.CreateChannelMessage(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestMessageHandler_CreateChannelMessage_Success(t *testing.T) {
	msgs := &fakeMessageProvider{createdMsg: testMessage()}
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, msgs)
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodPost, "/api/chat/channels/"+testChannelID+"/messages",
		strings.NewReader(`{"body_text":"hello"}`))
	r.SetPathValue("channelID", testChannelID)
	h.CreateChannelMessage(rec, r)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d — body: %s", rec.Code, rec.Body.String())
	}
}

func TestMessageHandler_CreateChannelMessage_AcceptsParentMessageID(t *testing.T) {
	const parentID = "66666666-6666-6666-6666-666666666666"
	msgs := &fakeMessageProvider{createdMsg: testMessage()}
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, msgs)
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodPost, "/api/chat/channels/"+testChannelID+"/messages",
		strings.NewReader(`{"body_text":"hello","parent_message_id":"`+parentID+`"}`))
	r.SetPathValue("channelID", testChannelID)
	h.CreateChannelMessage(rec, r)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d — body: %s", rec.Code, rec.Body.String())
	}
	if msgs.lastCreateChannelInput.ParentMessageID != parentID {
		t.Fatalf("parent_message_id not forwarded: %+v", msgs.lastCreateChannelInput)
	}
}

func TestMessageHandler_CreateChannelMessage_InvalidParentReferenceReturns404(t *testing.T) {
	msgs := &fakeMessageProvider{createChErr: domain.ErrInvalidMessageReference}
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, msgs)
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodPost, "/api/chat/channels/"+testChannelID+"/messages",
		strings.NewReader(`{"body_text":"hello","parent_message_id":"66666666-6666-6666-6666-666666666666"}`))
	r.SetPathValue("channelID", testChannelID)
	h.CreateChannelMessage(rec, r)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected generic 404 for invalid parent, got %d — body: %s", rec.Code, rec.Body.String())
	}
}

func TestMessageHandler_CreateChannelMessage_AcceptsV2Format(t *testing.T) {
	msg := testMessage()
	msg.BodyFormat = domain.MessageBodyFormatV2
	msgs := &fakeMessageProvider{createdMsg: msg}
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, msgs)
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodPost, "/api/chat/channels/"+testChannelID+"/messages",
		strings.NewReader(`{"body_text":"hello","body_format":"v2"}`))
	r.SetPathValue("channelID", testChannelID)
	h.CreateChannelMessage(rec, r)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 for v2, got %d — body: %s", rec.Code, rec.Body.String())
	}
	if msgs.lastCreateChannelInput.BodyFormat != domain.MessageBodyFormatV2 {
		t.Fatalf("expected v2 forwarded to service, got %q", msgs.lastCreateChannelInput.BodyFormat)
	}
}

func TestMessageHandler_CreateChannelMessage_RejectsUnknownFormat(t *testing.T) {
	msgs := &fakeMessageProvider{createChErr: domain.ErrInvalidInput}
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, msgs)
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodPost, "/api/chat/channels/"+testChannelID+"/messages",
		strings.NewReader(`{"body_text":"hello","body_format":"v3"}`))
	r.SetPathValue("channelID", testChannelID)
	h.CreateChannelMessage(rec, r)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown body format, got %d", rec.Code)
	}
}

func TestMessageHandler_CreateChannelMessage_AuthorIDCannotBeSpoofed(t *testing.T) {
	msgs := &fakeMessageProvider{createdMsg: testMessage()}
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, msgs)
	rec := httptest.NewRecorder()
	// Attempt to inject author_id via unknown field — should be rejected (DisallowUnknownFields).
	r := requestWithUser(http.MethodPost, "/api/chat/channels/"+testChannelID+"/messages",
		strings.NewReader(`{"body_text":"hello","author_id":"attacker-id"}`))
	r.SetPathValue("channelID", testChannelID)
	h.CreateChannelMessage(rec, r)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown field author_id, got %d", rec.Code)
	}
}

func TestMessageHandler_CreateChannelMessage_SenderIDFromContextNotBody(t *testing.T) {
	msgs := &fakeMessageProvider{createdMsg: testMessage()}
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, msgs)
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodPost, "/api/chat/channels/"+testChannelID+"/messages",
		strings.NewReader(`{"body_text":"hello"}`))
	r.SetPathValue("channelID", testChannelID)
	h.CreateChannelMessage(rec, r)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rec.Code)
	}
	if msgs.lastCreateChannelInput.SenderID != msgTestUserID {
		t.Fatalf("expected SenderID from context (%s), got %s", msgTestUserID, msgs.lastCreateChannelInput.SenderID)
	}
}

func TestMessageHandler_CreateChannelMessage_NotFoundReturns404(t *testing.T) {
	msgs := &fakeMessageProvider{createChErr: domain.ErrNotFound}
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, msgs)
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodPost, "/api/chat/channels/"+testChannelID+"/messages",
		strings.NewReader(`{"body_text":"hello"}`))
	r.SetPathValue("channelID", testChannelID)
	h.CreateChannelMessage(rec, r)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unauthorized/missing channel, got %d", rec.Code)
	}
}

func TestMessageHandler_CreateChannelMessage_InvalidInputReturns400(t *testing.T) {
	msgs := &fakeMessageProvider{createChErr: domain.ErrInvalidInput}
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, msgs)
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodPost, "/api/chat/channels/"+testChannelID+"/messages",
		strings.NewReader(`{"body_text":""}`))
	r.SetPathValue("channelID", testChannelID)
	h.CreateChannelMessage(rec, r)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

// ── GetChannelMessage ───────────────────────────────────────────────────────

func TestMessageHandler_GetChannelMessage_Success(t *testing.T) {
	msg := testMessage()
	msg.BodyText = "channel hello"
	msgs := &fakeMessageProvider{channelMsg: msg}
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, msgs)
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodGet, "/api/chat/channels/"+testChannelID+"/messages/"+testMessageID, nil)
	r.SetPathValue("channelID", testChannelID)
	r.SetPathValue("messageID", testMessageID)

	h.GetChannelMessage(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
	}
	data := decodeBody(t, rec)["data"].(map[string]any)
	if data["id"] != testMessageID {
		t.Fatalf("expected message id %q, got %v", testMessageID, data["id"])
	}
	if data["body_text"] != "channel hello" {
		t.Fatalf("expected channel body_text, got %v", data["body_text"])
	}
	if msgs.lastGetChannelInput.WorkspaceID != testWorkspaceID ||
		msgs.lastGetChannelInput.ChannelID != testChannelID ||
		msgs.lastGetChannelInput.CallerID != msgTestUserID ||
		msgs.lastGetChannelInput.MessageID != testMessageID {
		t.Fatalf("unexpected get channel input: %+v", msgs.lastGetChannelInput)
	}
}

func TestMessageHandler_GetChannelMessage_NotFoundReturns404(t *testing.T) {
	msgs := &fakeMessageProvider{channelMsgErr: domain.ErrNotFound}
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, msgs)
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodGet, "/api/chat/channels/"+testChannelID+"/messages/"+testMessageID, nil)
	r.SetPathValue("channelID", testChannelID)
	r.SetPathValue("messageID", testMessageID)

	h.GetChannelMessage(rec, r)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d — body: %s", rec.Code, rec.Body.String())
	}
}

// ── ListDMMessages ───────────────────────────────────────────────────────────

func TestMessageHandler_ListDMMessages_Success(t *testing.T) {
	dmMsg := testMessage()
	dmMsg.ChannelID = ""
	dmMsg.DMConversationID = testConversationID
	msgs := &fakeMessageProvider{dmOut: service.ListDMMessagesOutput{Messages: []domain.Message{dmMsg}}}
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, msgs)
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodGet, "/api/chat/dm/"+testConversationID+"/messages", nil)
	r.SetPathValue("conversationID", testConversationID)
	h.ListDMMessages(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	msgsArr, ok := body["data"].(map[string]any)["messages"].([]any)
	if !ok || len(msgsArr) != 1 {
		t.Fatalf("expected 1 dm message, got %v", body["data"].(map[string]any)["messages"])
	}
}

func TestMessageHandler_ListDMMessages_NonMemberReturnsNotFound(t *testing.T) {
	msgs := &fakeMessageProvider{dmOutErr: domain.ErrNotFound}
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, msgs)
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodGet, "/api/chat/dm/"+testConversationID+"/messages", nil)
	r.SetPathValue("conversationID", testConversationID)
	h.ListDMMessages(rec, r)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for non-member DM, got %d", rec.Code)
	}
}

// ── CreateDMMessage ──────────────────────────────────────────────────────────

func TestMessageHandler_CreateDMMessage_Success(t *testing.T) {
	dmMsg := testMessage()
	dmMsg.ChannelID = ""
	dmMsg.DMConversationID = testConversationID
	msgs := &fakeMessageProvider{createDMMsg: dmMsg}
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, msgs)
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodPost, "/api/chat/dm/"+testConversationID+"/messages",
		strings.NewReader(`{"body_text":"hello"}`))
	r.SetPathValue("conversationID", testConversationID)
	h.CreateDMMessage(rec, r)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d — body: %s", rec.Code, rec.Body.String())
	}
}

func TestMessageHandler_CreateDMMessage_SenderIDFromContextNotBody(t *testing.T) {
	dmMsg := testMessage()
	dmMsg.ChannelID = ""
	dmMsg.DMConversationID = testConversationID
	msgs := &fakeMessageProvider{createDMMsg: dmMsg}
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, msgs)
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodPost, "/api/chat/dm/"+testConversationID+"/messages",
		strings.NewReader(`{"body_text":"hello"}`))
	r.SetPathValue("conversationID", testConversationID)
	h.CreateDMMessage(rec, r)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rec.Code)
	}
	if msgs.lastCreateDMInput.SenderID != msgTestUserID {
		t.Fatalf("expected SenderID from context (%s), got %s", msgTestUserID, msgs.lastCreateDMInput.SenderID)
	}
}

func TestMessageHandler_CreateDMMessage_NonParticipantReturns404(t *testing.T) {
	msgs := &fakeMessageProvider{createDMErr: domain.ErrNotFound}
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, msgs)
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodPost, "/api/chat/dm/"+testConversationID+"/messages",
		strings.NewReader(`{"body_text":"hello"}`))
	r.SetPathValue("conversationID", testConversationID)
	h.CreateDMMessage(rec, r)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for non-participant DM, got %d", rec.Code)
	}
}

// ── GetDMMessage ─────────────────────────────────────────────────────────────

func TestMessageHandler_GetDMMessage_Success(t *testing.T) {
	dmMsg := testMessage()
	dmMsg.ChannelID = ""
	dmMsg.DMConversationID = testConversationID
	dmMsg.BodyText = "dm hello"
	msgs := &fakeMessageProvider{dmMsg: dmMsg}
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, msgs)
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodGet, "/api/chat/dm/"+testConversationID+"/messages/"+testMessageID, nil)
	r.SetPathValue("conversationID", testConversationID)
	r.SetPathValue("messageID", testMessageID)

	h.GetDMMessage(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
	}
	data := decodeBody(t, rec)["data"].(map[string]any)
	if data["id"] != testMessageID {
		t.Fatalf("expected dm message id %q, got %v", testMessageID, data["id"])
	}
	if data["body_text"] != "dm hello" {
		t.Fatalf("expected dm body_text, got %v", data["body_text"])
	}
	if msgs.lastGetDMInput.WorkspaceID != testWorkspaceID ||
		msgs.lastGetDMInput.ConversationID != testConversationID ||
		msgs.lastGetDMInput.CallerID != msgTestUserID ||
		msgs.lastGetDMInput.MessageID != testMessageID {
		t.Fatalf("unexpected get dm input: %+v", msgs.lastGetDMInput)
	}
}

func TestMessageHandler_GetDMMessage_NotFoundReturns404(t *testing.T) {
	msgs := &fakeMessageProvider{dmMsgErr: domain.ErrNotFound}
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, msgs)
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodGet, "/api/chat/dm/"+testConversationID+"/messages/"+testMessageID, nil)
	r.SetPathValue("conversationID", testConversationID)
	r.SetPathValue("messageID", testMessageID)

	h.GetDMMessage(rec, r)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d — body: %s", rec.Code, rec.Body.String())
	}
}

// ── Cursor encoding ──────────────────────────────────────────────────────────

func TestMessageHandler_ListChannelMessages_NextCursorPresentWhenMorePages(t *testing.T) {
	cursor := storage.EncodeCursor(storage.MessageCursor{CreatedAt: testNow(), ID: testMessageID})
	msgs := &fakeMessageProvider{channelOut: service.ListChannelMessagesOutput{
		Messages:   []domain.Message{testMessage()},
		NextCursor: cursor,
	}}
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, msgs)
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodGet, "/api/chat/channels/"+testChannelID+"/messages", nil)
	r.SetPathValue("channelID", testChannelID)
	h.ListChannelMessages(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := decodeBody(t, rec)
	if nc, _ := body["data"].(map[string]any)["next_cursor"].(string); nc == "" {
		t.Fatal("expected non-empty next_cursor when more pages available")
	}
}

// ── Nil deps ─────────────────────────────────────────────────────────────────

func TestMessageHandler_NilDeps_Returns503(t *testing.T) {
	h := httpapi.NewMessageHandler(nil, nil, nil)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/chat/channels/"+testChannelID+"/messages", nil)
	r.SetPathValue("channelID", testChannelID)
	h.ListChannelMessages(rec, r)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

// ── ListDMMessages: missing coverage tests ───────────────────────────────────

func TestMessageHandler_ListDMMessages_UnauthenticatedReturns401(t *testing.T) {
	h := httpapi.NewMessageHandler(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{}, nil)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/chat/dm/"+testConversationID+"/messages", nil)
	r.SetPathValue("conversationID", testConversationID)
	h.ListDMMessages(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestMessageHandler_ListDMMessages_InvalidConvIDReturns400(t *testing.T) {
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{})
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodGet, "/api/chat/dm/not-a-uuid/messages", nil)
	r.SetPathValue("conversationID", "not-a-uuid")
	h.ListDMMessages(rec, r)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid conv ID, got %d", rec.Code)
	}
}

func TestMessageHandler_ListDMMessages_InvalidCursorReturns400(t *testing.T) {
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{})
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodGet, "/api/chat/dm/"+testConversationID+"/messages?before=!!!bad!!!", nil)
	r.SetPathValue("conversationID", testConversationID)
	h.ListDMMessages(rec, r)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid cursor, got %d", rec.Code)
	}
}

func TestMessageHandler_ListDMMessages_WorkspaceNotFoundReturns404(t *testing.T) {
	h := makeHandlerWithUser(&fakeWorkspaceResolver{err: domain.ErrNotFound}, &fakeMessageProvider{})
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodGet, "/api/chat/dm/"+testConversationID+"/messages", nil)
	r.SetPathValue("conversationID", testConversationID)
	h.ListDMMessages(rec, r)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for workspace not found, got %d", rec.Code)
	}
}

func TestMessageHandler_ListDMMessages_InternalErrorReturns500(t *testing.T) {
	msgs := &fakeMessageProvider{dmOutErr: errors.New("some internal error")}
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, msgs)
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodGet, "/api/chat/dm/"+testConversationID+"/messages", nil)
	r.SetPathValue("conversationID", testConversationID)
	h.ListDMMessages(rec, r)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for internal error, got %d", rec.Code)
	}
}

func TestMessageHandler_ListDMMessages_EmptyListReturnsEmptyArray(t *testing.T) {
	msgs := &fakeMessageProvider{dmOut: service.ListDMMessagesOutput{Messages: nil}}
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, msgs)
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodGet, "/api/chat/dm/"+testConversationID+"/messages", nil)
	r.SetPathValue("conversationID", testConversationID)
	h.ListDMMessages(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := decodeBody(t, rec)
	msgsArr, ok := body["data"].(map[string]any)["messages"].([]any)
	if !ok || msgsArr == nil {
		t.Fatalf("expected non-null empty array, got %v", body["data"].(map[string]any)["messages"])
	}
}

func TestMessageHandler_ListDMMessages_NextCursorPresentWhenMorePages(t *testing.T) {
	cursor := storage.EncodeCursor(storage.MessageCursor{CreatedAt: testNow(), ID: testMessageID})
	msgs := &fakeMessageProvider{dmOut: service.ListDMMessagesOutput{
		Messages:   []domain.Message{testMessage()},
		NextCursor: cursor,
	}}
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, msgs)
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodGet, "/api/chat/dm/"+testConversationID+"/messages", nil)
	r.SetPathValue("conversationID", testConversationID)
	h.ListDMMessages(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := decodeBody(t, rec)
	if nc, _ := body["data"].(map[string]any)["next_cursor"].(string); nc == "" {
		t.Fatal("expected non-empty next_cursor when more DM pages available")
	}
}

// ── Cursor forwarding (pagination integration) ────────────────────────────────

func TestMessageHandler_ListChannelMessages_CursorForwardedToService(t *testing.T) {
	cursor := storage.EncodeCursor(storage.MessageCursor{CreatedAt: testNow(), ID: testMessageID})
	msgs := &fakeMessageProvider{channelOut: service.ListChannelMessagesOutput{
		Messages: []domain.Message{testMessage()},
	}}
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, msgs)
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodGet, "/api/chat/channels/"+testChannelID+"/messages?before="+cursor, nil)
	r.SetPathValue("channelID", testChannelID)
	h.ListChannelMessages(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if msgs.lastListChannelInput.BeforeCursor != cursor {
		t.Fatalf("cursor not forwarded: got %q, want %q", msgs.lastListChannelInput.BeforeCursor, cursor)
	}
}

func TestMessageHandler_ListDMMessages_CursorForwardedToService(t *testing.T) {
	cursor := storage.EncodeCursor(storage.MessageCursor{CreatedAt: testNow(), ID: testMessageID})
	msgs := &fakeMessageProvider{dmOut: service.ListDMMessagesOutput{
		Messages: []domain.Message{testMessage()},
	}}
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, msgs)
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodGet, "/api/chat/dm/"+testConversationID+"/messages?before="+cursor, nil)
	r.SetPathValue("conversationID", testConversationID)
	h.ListDMMessages(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if msgs.lastListDMInput.BeforeCursor != cursor {
		t.Fatalf("cursor not forwarded: got %q, want %q", msgs.lastListDMInput.BeforeCursor, cursor)
	}
}

// ── CreateDMMessage: missing coverage tests ──────────────────────────────────

func TestMessageHandler_CreateDMMessage_UnauthenticatedReturns401(t *testing.T) {
	h := httpapi.NewMessageHandler(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{}, nil)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/chat/dm/"+testConversationID+"/messages",
		strings.NewReader(`{"body_text":"hello"}`))
	r.SetPathValue("conversationID", testConversationID)
	h.CreateDMMessage(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestMessageHandler_CreateDMMessage_InvalidConvIDReturns400(t *testing.T) {
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{})
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodPost, "/api/chat/dm/not-a-uuid/messages",
		strings.NewReader(`{"body_text":"hello"}`))
	r.SetPathValue("conversationID", "not-a-uuid")
	h.CreateDMMessage(rec, r)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid conv ID, got %d", rec.Code)
	}
}

func TestMessageHandler_CreateDMMessage_AuthorIDCannotBeSpoofed(t *testing.T) {
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{})
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodPost, "/api/chat/dm/"+testConversationID+"/messages",
		strings.NewReader(`{"body_text":"hello","author_id":"attacker"}`))
	r.SetPathValue("conversationID", testConversationID)
	h.CreateDMMessage(rec, r)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown field author_id, got %d", rec.Code)
	}
}

func TestMessageHandler_CreateDMMessage_MalformedBodyReturns400(t *testing.T) {
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{})
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodPost, "/api/chat/dm/"+testConversationID+"/messages",
		strings.NewReader("not json"))
	r.SetPathValue("conversationID", testConversationID)
	h.CreateDMMessage(rec, r)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for malformed body, got %d", rec.Code)
	}
}

func TestMessageHandler_CreateDMMessage_WorkspaceNotFoundReturns404(t *testing.T) {
	h := makeHandlerWithUser(&fakeWorkspaceResolver{err: domain.ErrNotFound}, &fakeMessageProvider{})
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodPost, "/api/chat/dm/"+testConversationID+"/messages",
		strings.NewReader(`{"body_text":"hello"}`))
	r.SetPathValue("conversationID", testConversationID)
	h.CreateDMMessage(rec, r)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for workspace not found, got %d", rec.Code)
	}
}

func TestMessageHandler_CreateDMMessage_InternalErrorReturns500(t *testing.T) {
	msgs := &fakeMessageProvider{createDMErr: errors.New("some internal error")}
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, msgs)
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodPost, "/api/chat/dm/"+testConversationID+"/messages",
		strings.NewReader(`{"body_text":"hello"}`))
	r.SetPathValue("conversationID", testConversationID)
	h.CreateDMMessage(rec, r)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for internal error, got %d", rec.Code)
	}
}

// ── mapServiceError: missing cases ───────────────────────────────────────────

func TestMessageHandler_MapServiceError_ForbiddenReturns403(t *testing.T) {
	msgs := &fakeMessageProvider{channelOutErr: domain.ErrForbidden}
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, msgs)
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodGet, "/api/chat/channels/"+testChannelID+"/messages", nil)
	r.SetPathValue("channelID", testChannelID)
	h.ListChannelMessages(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for ErrForbidden, got %d", rec.Code)
	}
}

func TestMessageHandler_MapServiceError_InvalidCursorReturns400(t *testing.T) {
	msgs := &fakeMessageProvider{channelOutErr: domain.ErrInvalidCursor}
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, msgs)
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodGet, "/api/chat/channels/"+testChannelID+"/messages", nil)
	r.SetPathValue("channelID", testChannelID)
	h.ListChannelMessages(rec, r)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for ErrInvalidCursor, got %d", rec.Code)
	}
}

func TestMessageHandler_MapServiceError_UnknownErrorReturns500(t *testing.T) {
	msgs := &fakeMessageProvider{channelOutErr: errors.New("some internal error")}
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, msgs)
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodGet, "/api/chat/channels/"+testChannelID+"/messages", nil)
	r.SetPathValue("channelID", testChannelID)
	h.ListChannelMessages(rec, r)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for unknown error, got %d", rec.Code)
	}
}

// ── parseLimitParam: coverage ─────────────────────────────────────────────────

func TestMessageHandler_ListChannelMessages_LimitParamForwardedToService(t *testing.T) {
	msgs := &fakeMessageProvider{channelOut: service.ListChannelMessagesOutput{}}
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, msgs)
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodGet, "/api/chat/channels/"+testChannelID+"/messages?limit=25", nil)
	r.SetPathValue("channelID", testChannelID)
	h.ListChannelMessages(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if msgs.lastListChannelInput.Limit != 25 {
		t.Fatalf("expected Limit=25 forwarded to service, got %d", msgs.lastListChannelInput.Limit)
	}
}

func TestMessageHandler_ListChannelMessages_NegativeLimitDefaultsToZero(t *testing.T) {
	msgs := &fakeMessageProvider{channelOut: service.ListChannelMessagesOutput{}}
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, msgs)
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodGet, "/api/chat/channels/"+testChannelID+"/messages?limit=-5", nil)
	r.SetPathValue("channelID", testChannelID)
	h.ListChannelMessages(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if msgs.lastListChannelInput.Limit != 0 {
		t.Fatalf("expected Limit=0 (default) for negative limit, got %d", msgs.lastListChannelInput.Limit)
	}
}

func TestMessageHandler_ListChannelMessages_NonNumericLimitDefaultsToZero(t *testing.T) {
	msgs := &fakeMessageProvider{channelOut: service.ListChannelMessagesOutput{}}
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, msgs)
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodGet, "/api/chat/channels/"+testChannelID+"/messages?limit=abc", nil)
	r.SetPathValue("channelID", testChannelID)
	h.ListChannelMessages(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if msgs.lastListChannelInput.Limit != 0 {
		t.Fatalf("expected Limit=0 for non-numeric limit, got %d", msgs.lastListChannelInput.Limit)
	}
}

// ── resolveWorkspaceID: internal error path ──────────────────────────────────

func TestMessageHandler_ListChannelMessages_WorkspaceInternalErrorReturns500(t *testing.T) {
	h := makeHandlerWithUser(&fakeWorkspaceResolver{err: errors.New("some internal error")}, &fakeMessageProvider{})
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodGet, "/api/chat/channels/"+testChannelID+"/messages", nil)
	r.SetPathValue("channelID", testChannelID)
	h.ListChannelMessages(rec, r)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for workspace internal error, got %d", rec.Code)
	}
}
