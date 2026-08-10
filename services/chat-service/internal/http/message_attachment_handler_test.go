package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// RF-32 HTTP contract: attachment_ids in, attachment metadata out.

const handlerAttachmentID = "11111111-2222-4333-8444-555555555551"

func messageWithAttachment() domain.Message {
	msg := testMessage()
	msg.Attachments = []domain.MessageAttachment{{
		ID: handlerAttachmentID, Filename: "relatorio.pdf", ContentType: "application/pdf",
		SizeBytes: 2048, Status: "pending_scan", PreviewStatus: "pending",
	}}
	return msg
}

func TestMessageHandler_CreateChannelMessage_ForwardsAttachmentIDs(t *testing.T) {
	msgs := &fakeMessageProvider{createdMsg: messageWithAttachment()}
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, msgs)
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodPost, "/api/chat/channels/"+testChannelID+"/messages",
		strings.NewReader(`{"body_text":"","attachment_ids":["`+handlerAttachmentID+`"]}`))
	r.SetPathValue("channelID", testChannelID)

	h.CreateChannelMessage(rec, r)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	got := msgs.lastCreateChannelInput.AttachmentIDs
	if len(got) != 1 || got[0] != handlerAttachmentID {
		t.Fatalf("attachment ids must reach the service unchanged: %+v", got)
	}
}

func TestMessageHandler_CreateDMMessage_ForwardsAttachmentIDs(t *testing.T) {
	msgs := &fakeMessageProvider{createDMMsg: messageWithAttachment()}
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, msgs)
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodPost, "/api/chat/dm/"+testConversationID+"/messages",
		strings.NewReader(`{"body_text":"veja","attachment_ids":["`+handlerAttachmentID+`"]}`))
	r.SetPathValue("conversationID", testConversationID)

	h.CreateDMMessage(rec, r)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	got := msgs.lastCreateDMInput.AttachmentIDs
	if len(got) != 1 || got[0] != handlerAttachmentID {
		t.Fatalf("attachment ids must reach the service unchanged: %+v", got)
	}
}

// A non-array value is a malformed body, not an empty list.
func TestMessageHandler_CreateChannelMessage_RejectsNonArrayAttachmentIDs(t *testing.T) {
	for _, body := range []string{
		`{"body_text":"x","attachment_ids":"` + handlerAttachmentID + `"}`,
		`{"body_text":"x","attachment_ids":{"id":"` + handlerAttachmentID + `"}}`,
		`{"body_text":"x","attachment_ids":1}`,
	} {
		msgs := &fakeMessageProvider{createdMsg: testMessage()}
		h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, msgs)
		rec := httptest.NewRecorder()
		r := requestWithUser(http.MethodPost, "/api/chat/channels/"+testChannelID+"/messages",
			strings.NewReader(body))
		r.SetPathValue("channelID", testChannelID)

		h.CreateChannelMessage(rec, r)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %s: expected 400, got %d", body, rec.Code)
		}
	}
}

// Adding a field must not weaken the strict decoder.
func TestMessageHandler_CreateChannelMessage_StillRejectsUnknownFieldsAlongsideAttachments(t *testing.T) {
	msgs := &fakeMessageProvider{createdMsg: testMessage()}
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, msgs)
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodPost, "/api/chat/channels/"+testChannelID+"/messages",
		strings.NewReader(`{"attachment_ids":["`+handlerAttachmentID+`"],"attachment_status":"clean"}`))
	r.SetPathValue("channelID", testChannelID)

	h.CreateChannelMessage(rec, r)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown field, got %d", rec.Code)
	}
	if len(msgs.lastCreateChannelInput.AttachmentIDs) != 0 {
		t.Fatalf("nothing may reach the service: %+v", msgs.lastCreateChannelInput)
	}
}

// The response carries what the UI needs and nothing file-service keeps private.
func TestMessageHandler_MessageJSON_ExposesOnlySafeAttachmentMetadata(t *testing.T) {
	msgs := &fakeMessageProvider{createdMsg: messageWithAttachment()}
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, msgs)
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodPost, "/api/chat/channels/"+testChannelID+"/messages",
		strings.NewReader(`{"body_text":"veja","attachment_ids":["`+handlerAttachmentID+`"]}`))
	r.SetPathValue("channelID", testChannelID)

	h.CreateChannelMessage(rec, r)

	var payload struct {
		Data struct {
			Attachments []map[string]any `json:"attachments"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Data.Attachments) != 1 {
		t.Fatalf("expected one attachment, got %s", rec.Body.String())
	}
	attachment := payload.Data.Attachments[0]
	for key, want := range map[string]any{
		"id": handlerAttachmentID, "filename": "relatorio.pdf",
		"content_type": "application/pdf", "size": float64(2048),
		"status": "pending_scan", "preview_status": "pending",
	} {
		if attachment[key] != want {
			t.Fatalf("attachment.%s = %v, want %v", key, attachment[key], want)
		}
	}
	if len(attachment) != 6 {
		t.Fatalf("attachment must expose exactly the six safe fields: %+v", attachment)
	}
	// Not a whitelist restated: these are the names file-service must never
	// publish, asserted against the whole serialized message.
	for _, forbidden := range []string{
		"storage_object_key", "wrapped_dek", "kek_key_id", "envelope_version",
		"dek_wrap_version", "uploader_id", "scan_attempts", "preview_object_id",
		"storage_provider", "failure_code",
	} {
		if strings.Contains(rec.Body.String(), forbidden) {
			t.Fatalf("response leaks %q: %s", forbidden, rec.Body.String())
		}
	}
}

// A removed message says only that it was removed.
func TestMessageHandler_MessageJSON_WithholdsAttachmentsOnRemovedMessage(t *testing.T) {
	removed := messageWithAttachment()
	removed.Status = domain.MessageStatusDeleted
	removed.DeletedAt = testNow()
	msgs := &fakeMessageProvider{channelMsg: removed}
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, msgs)
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodGet,
		"/api/chat/channels/"+testChannelID+"/messages/"+testMessageID, nil)
	r.SetPathValue("channelID", testChannelID)
	r.SetPathValue("messageID", testMessageID)

	h.GetChannelMessage(rec, r)

	if strings.Contains(rec.Body.String(), "relatorio.pdf") ||
		strings.Contains(rec.Body.String(), handlerAttachmentID) {
		t.Fatalf("removed message must not describe its attachments: %s", rec.Body.String())
	}
}

// A text-only message's JSON is exactly what it was before RF-32.
func TestMessageHandler_MessageJSON_OmitsAttachmentsWhenThereAreNone(t *testing.T) {
	msgs := &fakeMessageProvider{createdMsg: testMessage()}
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, msgs)
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodPost, "/api/chat/channels/"+testChannelID+"/messages",
		strings.NewReader(`{"body_text":"hello"}`))
	r.SetPathValue("channelID", testChannelID)

	h.CreateChannelMessage(rec, r)

	if strings.Contains(rec.Body.String(), "attachments") {
		t.Fatalf("text-only message must not grow an attachments field: %s", rec.Body.String())
	}
}
