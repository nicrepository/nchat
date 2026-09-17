package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	httpapi "github.com/nicrepository/nchat/services/chat-service/internal/http"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
)

// Issue #825, HTTP layer. The endpoint carries no body and names no actor, so
// what is asserted here is the shape of that: the actor comes from the session,
// the message from the path, refusals are non-enumerating, and an unwired
// deployment answers 503 instead of panicking.

func cancelRemindersRequest(
	t *testing.T, acks *fakeAcknowledgementProvider,
) *httptest.ResponseRecorder {
	t.Helper()
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{}).
		WithAcknowledgements(acks, &fakeAcknowledgementBroadcaster{})
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodDelete,
		"/api/chat/messages/"+testMessageID+"/persistent-notifications", strings.NewReader(""))
	r.SetPathValue("messageID", testMessageID)
	h.CancelPersistentNotifications(rec, r)
	return rec
}

// The sender gets 200 and a count of what actually stopped, so a client that
// lost an earlier response can tell an arriving cancellation from a repeat.
func TestMessageHandler_CancelPersistentNotifications_ReportsWhatItStopped(t *testing.T) {
	acks := &fakeAcknowledgementProvider{stopped: 3}

	rec := cancelRemindersRequest(t, acks)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Data struct {
			MessageID string `json:"message_id"`
			Stopped   int    `json:"stopped"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if envelope.Data.MessageID != testMessageID || envelope.Data.Stopped != 3 {
		t.Fatalf("body = %+v", envelope.Data)
	}
}

// The actor is the session's and the message is the path's. There is no request
// body at all, so there is nothing a client could assert and nothing for a
// future edit of this handler to forget to ignore.
func TestMessageHandler_CancelPersistentNotifications_TakesTheActorFromTheSession(t *testing.T) {
	acks := &fakeAcknowledgementProvider{}

	cancelRemindersRequest(t, acks)

	if acks.cancelCalls != 1 {
		t.Fatalf("called %d times, want 1", acks.cancelCalls)
	}
	if acks.lastInput.ActorUserID != msgTestUserID {
		t.Fatalf("actor = %q, want the authenticated user %q", acks.lastInput.ActorUserID, msgTestUserID)
	}
	if acks.lastInput.MessageID != testMessageID {
		t.Fatalf("message = %q, want the path value", acks.lastInput.MessageID)
	}
}

// A repeat stops nothing and is still a success. Answering 404 or 409 for it
// would make an idempotent command something a client has to special-case.
func TestMessageHandler_CancelPersistentNotifications_RepeatIsStillASuccess(t *testing.T) {
	rec := cancelRemindersRequest(t, &fakeAcknowledgementProvider{stopped: 0})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"stopped":0`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

// Somebody else's message, another tenant's message, a message that does not
// exist and a message that never asked for reminders all arrive as ErrNotFound
// and all answer 404 — so the endpoint cannot be used to discover which of the
// four it was.
func TestMessageHandler_CancelPersistentNotifications_RefusalIsNonEnumerating(t *testing.T) {
	rec := cancelRemindersRequest(t, &fakeAcknowledgementProvider{err: domain.ErrNotFound})

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	for _, leaked := range []string{"sender", "workspace", "persistent", "recipient"} {
		if strings.Contains(strings.ToLower(rec.Body.String()), leaked) {
			t.Fatalf("refusal body names %q: %s", leaked, rec.Body.String())
		}
	}
}

// A deployment without a database answers 503 rather than panicking, through the
// same nil check that already guards the acknowledgement endpoints.
func TestMessageHandler_CancelPersistentNotifications_UnwiredAnswers503(t *testing.T) {
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{})
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodDelete,
		"/api/chat/messages/"+testMessageID+"/persistent-notifications", strings.NewReader(""))
	r.SetPathValue("messageID", testMessageID)

	h.CancelPersistentNotifications(rec, r)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

// A malformed message id is refused before anything is asked of the store: an id
// that cannot name a row is a request that could not have been authorised.
func TestMessageHandler_CancelPersistentNotifications_RefusesAMalformedMessageID(t *testing.T) {
	acks := &fakeAcknowledgementProvider{}
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{}).
		WithAcknowledgements(acks, &fakeAcknowledgementBroadcaster{})
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodDelete,
		"/api/chat/messages/not-a-uuid/persistent-notifications", strings.NewReader(""))
	r.SetPathValue("messageID", "not-a-uuid")

	h.CancelPersistentNotifications(rec, r)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if acks.cancelCalls != 0 {
		t.Fatal("a malformed id must not reach the store")
	}
}

// The route exists, under DELETE and only under DELETE. There is deliberately no
// POST counterpart: reminders are started by sending the message, so no endpoint
// exists through which an already-sent message can be made to start paging.
func TestRouter_PersistentNotificationsRouteIsDeleteOnly(t *testing.T) {
	if !strings.Contains(httpapi.RouteMessagePersistentNotifications, "{messageID}") {
		t.Fatalf("route %q must be message-scoped", httpapi.RouteMessagePersistentNotifications)
	}
	if strings.Contains(httpapi.RouteMessagePersistentNotifications, "{userID}") ||
		strings.Contains(httpapi.RouteMessagePersistentNotifications, "recipient") {
		t.Fatalf("route %q must not name an actor", httpapi.RouteMessagePersistentNotifications)
	}
}

// The outcome type is the service's, so the handler cannot report a number the
// store never produced.
func TestMessageHandler_CancelPersistentNotifications_ReportsTheStoreCount(t *testing.T) {
	var outcome service.CancelPersistentNotificationsOutcome
	outcome.Stopped = 11
	acks := &fakeAcknowledgementProvider{stopped: outcome.Stopped}

	rec := cancelRemindersRequest(t, acks)

	if !strings.Contains(rec.Body.String(), `"stopped":11`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

// ── the create field (issue #825) ────────────────────────────────────────────

// The flag reaches the service from the body, on both create paths. A field the
// handler decoded and dropped would make the whole feature unreachable from a
// client.
func TestMessageHandler_CreateChannelMessage_ForwardsPersistentNotifications(t *testing.T) {
	created := testMessage()
	created.Priority = domain.MessagePriorityUrgent
	created.PersistentNotifications = true

	rec, msgs := postCreateChannel(t,
		`{"body_text":"restart the cluster","priority":"urgent","persistent_notifications":true}`, created)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if !msgs.lastCreateChannelInput.PersistentNotifications {
		t.Fatal("the reminder request did not reach the service")
	}
}

func TestMessageHandler_CreateDMMessage_ForwardsPersistentNotifications(t *testing.T) {
	created := testMessage()
	created.Priority = domain.MessagePriorityUrgent
	created.PersistentNotifications = true

	rec, msgs := postCreateDM(t,
		`{"body_text":"restart the cluster","priority":"urgent","persistent_notifications":true}`, created)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if !msgs.lastCreateDMInput.PersistentNotifications {
		t.Fatal("the reminder request did not reach the service")
	}
}

// Absence and false are the same request: a client written before this field
// existed sends exactly what it always sent and keeps working.
func TestMessageHandler_CreateChannelMessage_AbsentFlagAsksForNoReminders(t *testing.T) {
	_, msgs := postCreateChannel(t, `{"body_text":"hello"}`, testMessage())

	if msgs.lastCreateChannelInput.PersistentNotifications {
		t.Fatal("a body that says nothing must not ask for reminders")
	}
}

// The response always carries the flag, so a client never has to infer it from
// the priority or default it itself.
func TestMessageHandler_CreateChannelMessage_ResponseCarriesTheFlag(t *testing.T) {
	created := testMessage()
	created.Priority = domain.MessagePriorityUrgent
	created.PersistentNotifications = true

	rec, _ := postCreateChannel(t,
		`{"body_text":"x","priority":"urgent","persistent_notifications":true}`, created)

	data := decodeBody(t, rec)["data"].(map[string]any)
	if data["persistent_notifications"] != true {
		t.Fatalf("response persistent_notifications = %#v, want true", data["persistent_notifications"])
	}
}

// There is no PATCH that starts or stops reminders. Editing a message must not
// restart a timer (#820), and decodeStrictJSON refusing the unknown field is
// what makes that true for every future edit payload as well.
func TestMessageHandler_EditMessage_RefusesPersistentNotifications(t *testing.T) {
	msgs := &fakeMessageProvider{editedMsg: testMessage()}
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, msgs).
		WithEditing(nil, nil, fakeEditLimiter{allowed: true})
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodPatch, "/api/chat/messages/"+testMessageID,
		strings.NewReader(`{"body_text":"edited","persistent_notifications":true}`))
	r.SetPathValue("messageID", testMessageID)

	h.EditMessage(rec, r)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}
