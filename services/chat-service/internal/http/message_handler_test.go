package httpapi_test

import (
	"context"
	"encoding/json"
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
	dmOut         service.ListDMMessagesOutput
	dmOutErr      error
	createDMMsg   domain.Message
	createDMErr   error

	lastCreateChannelInput service.CreateChannelMessageInput
	lastCreateDMInput      service.CreateDMMessageInput
}

func (f *fakeMessageProvider) ListChannelMessages(_ context.Context, in service.ListChannelMessagesInput) (service.ListChannelMessagesOutput, error) {
	_ = in
	return f.channelOut, f.channelOutErr
}

func (f *fakeMessageProvider) CreateChannelMessage(_ context.Context, in service.CreateChannelMessageInput) (domain.Message, error) {
	f.lastCreateChannelInput = in
	return f.createdMsg, f.createChErr
}

func (f *fakeMessageProvider) ListDMMessages(_ context.Context, in service.ListDMMessagesInput) (service.ListDMMessagesOutput, error) {
	_ = in
	return f.dmOut, f.dmOutErr
}

func (f *fakeMessageProvider) CreateDMMessage(_ context.Context, in service.CreateDMMessageInput) (domain.Message, error) {
	f.lastCreateDMInput = in
	return f.createDMMsg, f.createDMErr
}

// ── Helpers ──────────────────────────────────────────────────────────────────

const (
	testWorkspaceID    = "11111111-1111-1111-1111-111111111111"
	testChannelID      = "22222222-2222-2222-2222-222222222222"
	testConversationID = "33333333-3333-3333-3333-333333333333"
	msgTestUserID         = "44444444-4444-4444-4444-444444444444"
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
		ID:        testMessageID,
		WorkspaceID: testWorkspaceID,
		ChannelID: testChannelID,
		SenderID:  msgTestUserID,
		Kind:      domain.MessageKindUser,
		BodyText:  "hello",
		Status:    domain.MessageStatusActive,
		CreatedAt: testNow(),
		UpdatedAt: testNow(),
	}
}

// makeHandlerWithUser builds a MessageHandler and wraps it so it has msgTestUserID in context.
func makeHandlerWithUser(ws *fakeWorkspaceResolver, msgs *fakeMessageProvider) *httpapi.MessageHandler {
	return httpapi.NewMessageHandler(ws, msgs)
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

// ── ListChannelMessages ──────────────────────────────────────────────────────

func TestMessageHandler_ListChannelMessages_UnauthenticatedReturns401(t *testing.T) {
	h := httpapi.NewMessageHandler(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{})
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

// ── CreateChannelMessage ─────────────────────────────────────────────────────

func TestMessageHandler_CreateChannelMessage_UnauthenticatedReturns401(t *testing.T) {
	h := httpapi.NewMessageHandler(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{})
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
	h := httpapi.NewMessageHandler(nil, nil)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/chat/channels/"+testChannelID+"/messages", nil)
	r.SetPathValue("channelID", testChannelID)
	h.ListChannelMessages(rec, r)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}
