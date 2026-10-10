package httpapi_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
)

// The participants a group is created for are not subscribed to a conversation
// that did not exist a moment ago, so no room broadcast reaches them (issue
// #1103). These tests pin who is told about a new group, and who is not.

func serveCreateGroup(provider *fakeDMProvider, broadcast *recordingBroadcaster) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	directDMHandler(provider, broadcast).CreateGroup(recorder, groupRequest(
		`{"participant_user_ids":["`+dmOtherUserID+`","`+dmSecondUserID+`"],"title":"Infra"}`,
	))
	return recorder
}

func createdGroupOutput(invited ...string) service.CreateGroupConversationOutput {
	return service.CreateGroupConversationOutput{
		Conversation:   domain.DMConversation{ID: dmConversationID, WorkspaceID: testWorkspaceID},
		InvitedUserIDs: invited,
	}
}

func TestCreateGroup_AnnouncesTheNewGroupToEveryInvitee(t *testing.T) {
	provider := &fakeDMProvider{groupOutput: createdGroupOutput(dmOtherUserID, dmSecondUserID)}
	broadcast := &recordingBroadcaster{}

	if rec := serveCreateGroup(provider, broadcast); rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}

	if len(broadcast.available) != 1 {
		t.Fatalf("conversation.available published %d times, want 1", len(broadcast.available))
	}
	got := broadcast.available[0]
	if got.WorkspaceID != testWorkspaceID || got.TargetType != "dm" || got.TargetID != dmConversationID ||
		!slices.Equal(got.UserIDs, []string{dmOtherUserID, dmSecondUserID}) {
		t.Fatalf("published %+v", got)
	}
}

// The creator already holds the group; nobody outside it may learn of it.
// The recipients are the service's, not the body's: the service below resolved
// an outsider-free set that differs from the request, and the signal follows it.
func TestCreateGroup_AnnouncesOnlyToTheServiceResolvedInvitees(t *testing.T) {
	provider := &fakeDMProvider{groupOutput: createdGroupOutput(dmResolvedOtherUserID)}
	broadcast := &recordingBroadcaster{}

	serveCreateGroup(provider, broadcast)

	// Exact equality: neither the creator (msgTestUserID) nor either body ID.
	if len(broadcast.available) != 1 || !slices.Equal(broadcast.available[0].UserIDs, []string{dmResolvedOtherUserID}) {
		t.Fatalf("published %+v, want only the service-resolved invitee", broadcast.available)
	}
}

// Creating a group adds nobody to anything they could already see, so none of
// the room-scoped membership signals fire.
func TestCreateGroup_EmitsNoRoomScopedSignal(t *testing.T) {
	provider := &fakeDMProvider{groupOutput: createdGroupOutput(dmOtherUserID, dmSecondUserID)}
	broadcast := &recordingBroadcaster{}

	serveCreateGroup(provider, broadcast)

	if len(broadcast.calls) != 0 || len(broadcast.conversationUpdates) != 0 || len(broadcast.conversationEvents) != 0 {
		t.Fatalf("unexpected room-scoped publishes: %+v %+v %+v",
			broadcast.calls, broadcast.conversationUpdates, broadcast.conversationEvents)
	}
}

// Nothing committed, nothing announced — whichever status the failure maps to.
func TestCreateGroup_AnnouncesNothingWhenTheCreateFails(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want int
	}{
		{name: "forbidden", err: domain.ErrForbidden, want: http.StatusNotFound},
		{name: "invalid", err: domain.ErrInvalidInput, want: http.StatusBadRequest},
		{name: "rollback", err: errors.New("commit create group conversation: boom"), want: http.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := &fakeDMProvider{groupErr: test.err}
			broadcast := &recordingBroadcaster{}

			rec := serveCreateGroup(provider, broadcast)

			if rec.Code != test.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, test.want, rec.Body.String())
			}
			if len(broadcast.available) != 0 {
				t.Fatalf("published %+v after a failed create, want nothing", broadcast.available)
			}
		})
	}
}

// A service that reports nobody besides the creator has nobody to address.
func TestCreateGroup_AnnouncesNothingWithoutInvitees(t *testing.T) {
	provider := &fakeDMProvider{groupOutput: createdGroupOutput()}
	broadcast := &recordingBroadcaster{}

	if rec := serveCreateGroup(provider, broadcast); rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}
	if len(broadcast.available) != 0 {
		t.Fatalf("published %+v without invitees, want nothing", broadcast.available)
	}
}

// The broadcaster is optional wiring: without a hub the group is still created.
func TestCreateGroup_CreatesTheGroupWithoutABroadcaster(t *testing.T) {
	provider := &fakeDMProvider{groupOutput: createdGroupOutput(dmOtherUserID)}

	rec := serveCreateGroup(provider, nil)

	if rec.Code != http.StatusCreated || provider.groupCreateCalls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", rec.Code, provider.groupCreateCalls, rec.Body.String())
	}
}
