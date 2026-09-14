package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// Issue #821. The priority field's contract is a presence question before it is
// a value question, so these tests are split the way the contract is: one
// behaviour per test, and the two create paths asserted separately because they
// are two endpoints even though they share one parser.

func priorityMessage(priority domain.MessagePriority) domain.Message {
	msg := testMessage()
	msg.Priority = priority
	return msg
}

// postCreateChannel sends a raw create body to the channel endpoint and returns
// what the handler wrote, together with the fake it would have called.
func postCreateChannel(t *testing.T, body string, created domain.Message) (*httptest.ResponseRecorder, *fakeMessageProvider) {
	t.Helper()
	msgs := &fakeMessageProvider{createdMsg: created}
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, msgs)
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodPost, "/api/chat/channels/"+testChannelID+"/messages", strings.NewReader(body))
	r.SetPathValue("channelID", testChannelID)
	h.CreateChannelMessage(rec, r)
	return rec, msgs
}

// postCreateDM is postCreateChannel for a DM conversation.
func postCreateDM(t *testing.T, body string, created domain.Message) (*httptest.ResponseRecorder, *fakeMessageProvider) {
	t.Helper()
	msgs := &fakeMessageProvider{createDMMsg: created}
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, msgs)
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodPost, "/api/chat/dm/"+testConversationID+"/messages", strings.NewReader(body))
	r.SetPathValue("conversationID", testConversationID)
	h.CreateDMMessage(rec, r)
	return rec, msgs
}

func assertCreatedWithPriority(t *testing.T, rec *httptest.ResponseRecorder, want domain.MessagePriority) {
	t.Helper()
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d — body: %s", rec.Code, rec.Body.String())
	}
	data := decodeBody(t, rec)["data"].(map[string]any)
	if data["priority"] != string(want) {
		t.Fatalf("response priority = %#v, want %q", data["priority"], want)
	}
}

// assertRejectedBeforeSending proves both halves of a refusal: the client is
// told, and nothing happened. senderSeen is the SenderID the fake recorded —
// the handler always fills it from the auth context, so an empty one is proof
// the service was never reached, and therefore that no row was written and no
// event published.
func assertRejectedBeforeSending(t *testing.T, rec *httptest.ResponseRecorder, senderSeen string) {
	t.Helper()
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d — body: %s", rec.Code, rec.Body.String())
	}
	if senderSeen != "" {
		t.Fatalf("a refused priority reached the service (sender %q); nothing must be sent", senderSeen)
	}
}

// statedButNotAPriority are the bodies that put the field in and fill it with
// something that is not one of the three. Every one of them is a statement, so
// none of them may be answered with the default.
var statedButNotAPriority = map[string]string{
	"empty string":        `{"body_text":"hello","priority":""}`,
	"null":                `{"body_text":"hello","priority":null}`,
	"unknown word":        `{"body_text":"hello","priority":"critical"}`,
	"wrong case":          `{"body_text":"hello","priority":"URGENT"}`,
	"notification vocab":  `{"body_text":"hello","priority":"high"}`,
	"number":              `{"body_text":"hello","priority":123}`,
	"boolean":             `{"body_text":"hello","priority":true}`,
	"array":               `{"body_text":"hello","priority":[]}`,
	"object":              `{"body_text":"hello","priority":{}}`,
	"trailing whitespace": `{"body_text":"hello","priority":"urgent "}`,
}

// ── channel ─────────────────────────────────────────────────────────────────

// The backward-compatible case: a client that predates the field sends no
// priority and gets a standard message, with no 400 and nothing to change.
func TestMessageHandler_CreateChannelMessage_OmittedPriorityIsStandard(t *testing.T) {
	rec, msgs := postCreateChannel(t, `{"body_text":"hello"}`, priorityMessage(domain.MessagePriorityStandard))
	assertCreatedWithPriority(t, rec, domain.MessagePriorityStandard)
	if msgs.lastCreateChannelInput.Priority != domain.MessagePriorityStandard {
		t.Fatalf("service received %q, want standard resolved at the boundary", msgs.lastCreateChannelInput.Priority)
	}
}

func TestMessageHandler_CreateChannelMessage_AcceptsTheThreePriorities(t *testing.T) {
	for _, priority := range []domain.MessagePriority{
		domain.MessagePriorityStandard,
		domain.MessagePriorityImportant,
		domain.MessagePriorityUrgent,
	} {
		t.Run(string(priority), func(t *testing.T) {
			rec, msgs := postCreateChannel(t,
				`{"body_text":"hello","priority":"`+string(priority)+`"}`, priorityMessage(priority))
			assertCreatedWithPriority(t, rec, priority)
			if msgs.lastCreateChannelInput.Priority != priority {
				t.Fatalf("service received %q, want %q", msgs.lastCreateChannelInput.Priority, priority)
			}
		})
	}
}

// Every body that states a priority we do not accept is a 400 — including the
// two the previous revision folded into the default, "" and null.
func TestMessageHandler_CreateChannelMessage_RejectsAStatedNonPriority(t *testing.T) {
	for name, body := range statedButNotAPriority {
		t.Run(name, func(t *testing.T) {
			rec, msgs := postCreateChannel(t, body, priorityMessage(domain.MessagePriorityStandard))
			assertRejectedBeforeSending(t, rec, msgs.lastCreateChannelInput.SenderID)
		})
	}
}

// ── dm ──────────────────────────────────────────────────────────────────────

func TestMessageHandler_CreateDMMessage_OmittedPriorityIsStandard(t *testing.T) {
	rec, msgs := postCreateDM(t, `{"body_text":"hello"}`, priorityMessage(domain.MessagePriorityStandard))
	assertCreatedWithPriority(t, rec, domain.MessagePriorityStandard)
	if msgs.lastCreateDMInput.Priority != domain.MessagePriorityStandard {
		t.Fatalf("service received %q, want standard resolved at the boundary", msgs.lastCreateDMInput.Priority)
	}
}

func TestMessageHandler_CreateDMMessage_AcceptsTheThreePriorities(t *testing.T) {
	for _, priority := range []domain.MessagePriority{
		domain.MessagePriorityStandard,
		domain.MessagePriorityImportant,
		domain.MessagePriorityUrgent,
	} {
		t.Run(string(priority), func(t *testing.T) {
			rec, msgs := postCreateDM(t,
				`{"body_text":"hello","priority":"`+string(priority)+`"}`, priorityMessage(priority))
			assertCreatedWithPriority(t, rec, priority)
			if msgs.lastCreateDMInput.Priority != priority {
				t.Fatalf("service received %q, want %q", msgs.lastCreateDMInput.Priority, priority)
			}
		})
	}
}

// The DM path answers exactly as the channel path does. Asserted separately
// rather than assumed: they are two endpoints, and a parser wired into one and
// forgotten in the other is precisely the drift the shared helper exists to
// prevent.
func TestMessageHandler_CreateDMMessage_RejectsAStatedNonPriority(t *testing.T) {
	for name, body := range statedButNotAPriority {
		t.Run(name, func(t *testing.T) {
			rec, msgs := postCreateDM(t, body, priorityMessage(domain.MessagePriorityStandard))
			assertRejectedBeforeSending(t, rec, msgs.lastCreateDMInput.SenderID)
		})
	}
}

// ── edit ────────────────────────────────────────────────────────────────────

// There is no payload that re-prioritises a sent message. The edit request has
// no such field and the decoder rejects unknown ones, so this is a 400 before
// the service is reached at all.
func TestMessageHandler_EditMessage_RejectsAPriorityInThePayload(t *testing.T) {
	for _, body := range []string{
		`{"body":"edited","body_format":"v1","priority":"urgent"}`,
		`{"body":"edited","priority":"standard"}`,
	} {
		t.Run(body, func(t *testing.T) {
			msgs := &fakeMessageProvider{editedMsg: priorityMessage(domain.MessagePriorityImportant)}
			handler := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, msgs).
				WithEditing(nil, nil, fakeEditLimiter{allowed: true})
			request := requestWithUser(http.MethodPatch, "/api/chat/messages/"+testMessageID, strings.NewReader(body))
			request.SetPathValue("messageID", testMessageID)
			recorder := httptest.NewRecorder()

			handler.EditMessage(recorder, request)

			assertRejectedBeforeSending(t, recorder, msgs.lastEditInput.EditorID)
		})
	}
}

// A legitimate content edit answers with the priority the message was created
// with, unchanged.
func TestMessageHandler_EditMessage_PreservesPriorityInTheResponse(t *testing.T) {
	edited := priorityMessage(domain.MessagePriorityUrgent)
	edited.BodyText = "edited"
	edited.EditCount = 1
	handler := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{editedMsg: edited}).
		WithEditing(nil, nil, fakeEditLimiter{allowed: true})
	request := requestWithUser(http.MethodPatch, "/api/chat/messages/"+testMessageID,
		strings.NewReader(`{"body":"edited","body_format":"v1"}`))
	request.SetPathValue("messageID", testMessageID)
	recorder := httptest.NewRecorder()

	handler.EditMessage(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — body: %s", recorder.Code, recorder.Body.String())
	}
	data := decodeBody(t, recorder)["data"].(map[string]any)
	if data["priority"] != string(domain.MessagePriorityUrgent) {
		t.Fatalf("edited message priority = %#v, want \"urgent\"", data["priority"])
	}
}

// A message whose projection did not carry the column still serialises a
// priority rather than an empty string: the response contract is "always one of
// the three", and a partially-populated Message must not break it.
func TestMessageHandler_MessageResponse_NeverSerialisesAnEmptyPriority(t *testing.T) {
	rec, _ := postCreateChannel(t, `{"body_text":"hello"}`, priorityMessage(""))
	assertCreatedWithPriority(t, rec, domain.MessagePriorityStandard)
}
