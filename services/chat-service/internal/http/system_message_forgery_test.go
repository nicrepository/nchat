package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	httpapi "github.com/nicrepository/nchat/services/chat-service/internal/http"
)

// A client must not be able to mint a system message (issue #527).
//
// The database CHECK is the backstop, but a CHECK alone would not be enough if
// the ordinary send endpoint let a caller choose the kind — the row would be
// well-formed and still a forgery. These prove the accepted body simply has no
// field for it: the strict decoder answers 400 to every one of them, so a system
// message can only ever come from the mutation that owns the event.
func TestMessageHandler_CreateMessage_RejectsClientSuppliedSystemFields(t *testing.T) {
	for _, body := range []string{
		`{"body_text":"olá","kind":"system"}`,
		`{"body_text":"olá","event_type":"conversation_renamed"}`,
		`{"body_text":"olá","event_payload":{"old_name":"a","new_name":"b"}}`,
		`{"body_text":"olá","sender_id":"11111111-1111-4111-8111-111111111111"}`,
		`{"body_text":"olá","kind":"system","event_type":"conversation_member_left","event_payload":{}}`,
	} {
		provider := &fakeMessageProvider{}
		request := requestWithUser(
			http.MethodPost,
			"/api/chat/channels/"+testChannelID+"/messages",
			strings.NewReader(body),
		)
		request.Header.Set("Content-Type", "application/json")
		request.SetPathValue("channelID", testChannelID)

		recorder := httptest.NewRecorder()
		makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, provider).
			CreateChannelMessage(recorder, request)

		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d for %s, want 400", recorder.Code, body)
		}
		// The service was never reached: a rejected body leaves the recorded
		// input untouched.
		if provider.lastCreateChannelInput.BodyText != "" {
			t.Fatalf("service reached with a body claiming a system message: %s", body)
		}
	}
	// The route constant is referenced so this test fails to compile if the send
	// surface is ever moved or renamed out from under it.
	_ = httpapi.RouteChannelMessages
}
