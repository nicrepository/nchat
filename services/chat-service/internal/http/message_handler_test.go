package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
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

	lastCreateChannelInput service.CreateChannelMessageInput
	lastCreateDMInput      service.CreateDMMessageInput
	lastListChannelInput   service.ListChannelMessagesInput
	lastListDMInput        service.ListDMMessagesInput
	lastGetChannelInput    service.GetChannelMessageInput
	lastGetDMInput         service.GetDMMessageInput
}

type fakeMentionProvider struct {
	out       service.SearchMentionsOutput
	err       error
	lastInput service.SearchMentionsInput
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
