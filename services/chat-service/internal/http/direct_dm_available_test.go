package httpapi_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	httpapi "github.com/nicrepository/nchat/services/chat-service/internal/http"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
)

// The counterpart of a 1:1 is never subscribed to a conversation that did not
// exist a moment ago, so the room broadcast cannot reach them (issue #721).
// These tests pin who is told, and — the part that matters — who is not.

// dmResolvedOtherUserID stands in for a counterpart id the service resolved to
// something other than the spelling the body carried — which the real service
// does on every call, since it canonicalises the UUID before the eligibility
// lookup. Deliberately different from dmOtherUserID so a handler that addressed
// the request payload instead of the resolved state fails loudly.
const dmResolvedOtherUserID = "77777777-7777-4777-8777-777777777777"

func directDMHandler(provider *fakeDMProvider, broadcast *recordingBroadcaster) *httpapi.DMHandler {
	handler := httpapi.NewDMHandler(
		&fakeWorkspaceResolver{workspace: activeWorkspace()}, provider, &fakeDMRateLimiter{},
	)
	if broadcast == nil {
		return handler
	}
	return handler.WithMembersBroadcast(broadcast)
}

func serveGetOrCreateDirect(handler *httpapi.DMHandler, otherUserID string) *httptest.ResponseRecorder {
	request := requestWithUser(
		http.MethodPost, httpapi.RouteDMConversations,
		strings.NewReader(`{"other_user_id":"`+otherUserID+`"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.GetOrCreateDirect(recorder, request)
	return recorder
}

func createdDirectOutput() service.CreateDirectConversationOutput {
	return service.CreateDirectConversationOutput{
		Conversation: domain.DMConversation{ID: dmConversationID, WorkspaceID: testWorkspaceID},
		Created:      true,
		OtherUserID:  dmOtherUserID,
	}
}

func TestGetOrCreateDirect_AnnouncesANewDMToTheCounterpart(t *testing.T) {
	provider := &fakeDMProvider{createOutput: createdDirectOutput()}
	broadcast := &recordingBroadcaster{}

	if rec := serveGetOrCreateDirect(directDMHandler(provider, broadcast), dmOtherUserID); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	if len(broadcast.available) != 1 {
		t.Fatalf("conversation.available published %d times, want 1", len(broadcast.available))
	}
	got := broadcast.available[0]
	want := availableRecord{
		WorkspaceID: testWorkspaceID, TargetType: "dm", TargetID: dmConversationID,
		UserIDs: []string{dmOtherUserID},
	}
	if got.WorkspaceID != want.WorkspaceID || got.TargetType != want.TargetType ||
		got.TargetID != want.TargetID || len(got.UserIDs) != 1 || got.UserIDs[0] != want.UserIDs[0] {
		t.Fatalf("published %+v, want %+v", got, want)
	}
}

// The actor already has the conversation open; announcing it to them would be a
// second, contradictory sidebar refetch and would leak nothing useful.
func TestGetOrCreateDirect_NeverAnnouncesToTheActorOrAnyoneElse(t *testing.T) {
	provider := &fakeDMProvider{createOutput: createdDirectOutput()}
	broadcast := &recordingBroadcaster{}

	serveGetOrCreateDirect(directDMHandler(provider, broadcast), dmOtherUserID)

	for _, record := range broadcast.available {
		for _, userID := range record.UserIDs {
			if userID == msgTestUserID {
				t.Fatalf("announced to the actor %q", userID)
			}
			if userID != dmOtherUserID {
				t.Fatalf("announced to an unexpected user %q", userID)
			}
		}
	}
}

// The recipient comes from the service's eligibility lookup, not from the body.
// The request below names one id and the service resolves another; the signal
// must follow the service.
func TestGetOrCreateDirect_AddressesTheServerResolvedCounterpart(t *testing.T) {
	output := createdDirectOutput()
	output.OtherUserID = dmResolvedOtherUserID
	provider := &fakeDMProvider{createOutput: output}
	broadcast := &recordingBroadcaster{}

	serveGetOrCreateDirect(directDMHandler(provider, broadcast), dmOtherUserID)

	if len(broadcast.available) != 1 || broadcast.available[0].UserIDs[0] != dmResolvedOtherUserID {
		t.Fatalf("published %+v, want the service-resolved counterpart", broadcast.available)
	}
}

// The room-scoped signals belong to membership changes on an existing target.
// Creating a 1:1 emits neither: nobody was added to anything they can see yet.
func TestGetOrCreateDirect_EmitsNoRoomScopedSignal(t *testing.T) {
	provider := &fakeDMProvider{createOutput: createdDirectOutput()}
	broadcast := &recordingBroadcaster{}

	serveGetOrCreateDirect(directDMHandler(provider, broadcast), dmOtherUserID)

	if len(broadcast.calls) != 0 || len(broadcast.conversationUpdates) != 0 || len(broadcast.conversationEvents) != 0 {
		t.Fatalf("unexpected room-scoped publishes: %+v %+v %+v",
			broadcast.calls, broadcast.conversationUpdates, broadcast.conversationEvents)
	}
}

// An existing DM is already in both sidebars. Re-opening it must stay silent,
// or every visit to a colleague's DM would refetch their sidebar.
func TestGetOrCreateDirect_AnnouncesNothingForAnExistingDM(t *testing.T) {
	output := createdDirectOutput()
	output.Created = false
	provider := &fakeDMProvider{createOutput: output}
	broadcast := &recordingBroadcaster{}

	if rec := serveGetOrCreateDirect(directDMHandler(provider, broadcast), dmOtherUserID); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if len(broadcast.available) != 0 {
		t.Fatalf("published %+v for an existing DM, want nothing", broadcast.available)
	}
}

// Nothing persisted, so there is nothing to announce — for any failure the
// service reports, whichever status it maps to.
func TestGetOrCreateDirect_AnnouncesNothingWhenTheCreateFails(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want int
	}{
		{name: "forbidden", err: domain.ErrForbidden, want: http.StatusNotFound},
		{name: "invalid", err: domain.ErrInvalidInput, want: http.StatusBadRequest},
		{name: "internal", err: errors.New("boom"), want: http.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := &fakeDMProvider{createErr: test.err}
			broadcast := &recordingBroadcaster{}

			rec := serveGetOrCreateDirect(directDMHandler(provider, broadcast), dmOtherUserID)

			if rec.Code != test.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, test.want, rec.Body.String())
			}
			if len(broadcast.available) != 0 {
				t.Fatalf("published %+v after a failed create, want nothing", broadcast.available)
			}
		})
	}
}

// A rejected request never reaches the service, so it never reaches the bus.
func TestGetOrCreateDirect_AnnouncesNothingForARejectedRequest(t *testing.T) {
	provider := &fakeDMProvider{createOutput: createdDirectOutput()}
	broadcast := &recordingBroadcaster{}

	rec := serveGetOrCreateDirect(directDMHandler(provider, broadcast), "not-a-uuid")

	if rec.Code != http.StatusBadRequest || provider.createCalls != 0 || len(broadcast.available) != 0 {
		t.Fatalf("status=%d calls=%d published=%+v", rec.Code, provider.createCalls, broadcast.available)
	}
}

// The broadcaster is optional wiring, exactly as it is for the other DM routes:
// without a hub the DM is still created and still answered 200.
func TestGetOrCreateDirect_CreatesTheDMWithoutABroadcaster(t *testing.T) {
	provider := &fakeDMProvider{createOutput: createdDirectOutput()}

	rec := serveGetOrCreateDirect(directDMHandler(provider, nil), dmOtherUserID)

	if rec.Code != http.StatusOK || provider.createCalls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", rec.Code, provider.createCalls, rec.Body.String())
	}
}

// A service that reports Created without naming the counterpart has nobody to
// address; publishing to an empty id would be a signal delivered to no one.
func TestGetOrCreateDirect_AnnouncesNothingWithoutACounterpart(t *testing.T) {
	output := createdDirectOutput()
	output.OtherUserID = ""
	provider := &fakeDMProvider{createOutput: output}
	broadcast := &recordingBroadcaster{}

	if rec := serveGetOrCreateDirect(directDMHandler(provider, broadcast), dmOtherUserID); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if len(broadcast.available) != 0 {
		t.Fatalf("published %+v without a counterpart, want nothing", broadcast.available)
	}
}
