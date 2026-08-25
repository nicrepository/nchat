package httpapi_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// The two self-service conversation endpoints (issue #527): renaming a group and
// leaving a group or a channel.
//
// Three properties carry the weight, and all three are about what the request
// is *not* allowed to say: the actor comes from the session, the workspace comes
// from the server-side sidebar context, and there is no target user anywhere —
// so neither endpoint can be aimed at somebody else. The fourth is ordering: the
// realtime signal fires only after the write returned successfully.

func groupRenameRequest(conversationID, body string) *http.Request {
	r := requestWithUser(http.MethodPatch, "/api/chat/dm/"+conversationID, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.SetPathValue("conversationID", conversationID)
	return r
}

func groupLeaveRequest(conversationID string) *http.Request {
	r := requestWithUser(http.MethodDelete, "/api/chat/dm/"+conversationID+"/membership", nil)
	r.SetPathValue("conversationID", conversationID)
	return r
}

// The channel write path admits every mutation the same way, content type
// included, so the request carries the header the web client always sends.
func channelLeaveRequest(channelID string) *http.Request {
	r := requestWithUser(http.MethodDelete, "/api/chat/channels/"+channelID+"/membership", nil)
	r.Header.Set("Content-Type", "application/json")
	r.SetPathValue("channelID", channelID)
	return r
}

func renamedGroup(title string) storage.RenameGroupResult {
	return storage.RenameGroupResult{
		Conversation: domain.DMConversation{
			ID: dmConversationID, WorkspaceID: testWorkspaceID,
			Type: domain.DMConversationTypeGroup, Title: title,
		},
		Event: domain.Message{ID: "event-" + dmConversationID, Kind: domain.MessageKindSystem},
	}
}

// ── Group rename ────────────────────────────────────────────────────────────

func TestDMHandler_RenameGroup_DerivesActorAndWorkspaceServerSide(t *testing.T) {
	provider := &fakeDMProvider{renameGroupResult: renamedGroup("Piloto")}
	recorder := httptest.NewRecorder()

	dmTestHandler(provider).RenameGroup(recorder, groupRenameRequest(dmConversationID, `{"title":"Piloto"}`))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body)
	}
	if provider.lastRenameGroup.CallerID != msgTestUserID {
		t.Fatalf("caller = %q, want the session's user", provider.lastRenameGroup.CallerID)
	}
	if provider.lastRenameGroup.WorkspaceID != testWorkspaceID {
		t.Fatalf("workspace = %q, want the resolved one", provider.lastRenameGroup.WorkspaceID)
	}
	if provider.lastRenameGroup.ConversationID != dmConversationID || provider.lastRenameGroup.Title != "Piloto" {
		t.Fatalf("forwarded %+v", provider.lastRenameGroup)
	}
	data, ok := decodeBody(t, recorder)["data"].(map[string]any)
	if !ok || data["id"] != dmConversationID || data["title"] != "Piloto" {
		t.Fatalf("body = %v, want the conversation id and its new title", recorder.Body)
	}
}

// The body is one field. A request that also names a workspace, an actor or a
// participant list is refused outright rather than having those fields ignored:
// silently dropping them is how a client comes to believe it set them.
func TestDMHandler_RenameGroup_RefusesAnyFieldBeyondTheTitle(t *testing.T) {
	for _, body := range []string{
		`{"title":"Piloto","workspace_id":"` + testWorkspaceID + `"}`,
		`{"title":"Piloto","caller_id":"someone-else"}`,
		`{"title":"Piloto","type":"group"}`,
	} {
		t.Run(body, func(t *testing.T) {
			provider := &fakeDMProvider{}
			recorder := httptest.NewRecorder()

			dmTestHandler(provider).RenameGroup(recorder, groupRenameRequest(dmConversationID, body))

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", recorder.Code)
			}
			if provider.renameGroupCalls != 0 {
				t.Fatal("a rejected body must not reach the service")
			}
		})
	}
}

func TestDMHandler_RenameGroup_RequiresJSONAndAnAuthenticatedActor(t *testing.T) {
	t.Run("no content type", func(t *testing.T) {
		provider := &fakeDMProvider{}
		recorder := httptest.NewRecorder()
		r := requestWithUser(http.MethodPatch, "/api/chat/dm/"+dmConversationID, strings.NewReader(`{"title":"x"}`))
		r.SetPathValue("conversationID", dmConversationID)

		dmTestHandler(provider).RenameGroup(recorder, r)

		if recorder.Code != http.StatusUnsupportedMediaType || provider.renameGroupCalls != 0 {
			t.Fatalf("status = %d, calls = %d", recorder.Code, provider.renameGroupCalls)
		}
	})

	t.Run("anonymous", func(t *testing.T) {
		provider := &fakeDMProvider{}
		recorder := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPatch, "/api/chat/dm/"+dmConversationID, strings.NewReader(`{"title":"x"}`))
		r.Header.Set("Content-Type", "application/json")
		r.SetPathValue("conversationID", dmConversationID)

		dmTestHandler(provider).RenameGroup(recorder, r)

		if recorder.Code != http.StatusUnauthorized || provider.renameGroupCalls != 0 {
			t.Fatalf("status = %d, calls = %d", recorder.Code, provider.renameGroupCalls)
		}
	})

	t.Run("malformed conversation id", func(t *testing.T) {
		provider := &fakeDMProvider{}
		recorder := httptest.NewRecorder()

		dmTestHandler(provider).RenameGroup(recorder, groupRenameRequest("not-a-uuid", `{"title":"x"}`))

		if recorder.Code != http.StatusBadRequest || provider.renameGroupCalls != 0 {
			t.Fatalf("status = %d, calls = %d", recorder.Code, provider.renameGroupCalls)
		}
	})
}

// Both mutations share one budget: a caller must not get a separate allowance
// for renaming and another for leaving.
func TestDMHandler_GroupAdmin_SharesOneRateBudget(t *testing.T) {
	provider := &fakeDMProvider{renameGroupResult: renamedGroup("Piloto")}
	// One short of the budget, under the key both endpoints use.
	limiter := &fakeDMRateLimiter{counts: map[string]int{"group_admin:" + msgTestUserID: 19}}
	handler := dmTestHandlerWithLimiter(provider, limiter)

	renameRecorder := httptest.NewRecorder()
	handler.RenameGroup(renameRecorder, groupRenameRequest(dmConversationID, `{"title":"Piloto"}`))
	if renameRecorder.Code != http.StatusOK {
		t.Fatalf("rename status = %d, want the last of the budget to be spent on it", renameRecorder.Code)
	}

	// The leave finds the budget already spent — by the rename, not by a leave.
	leaveRecorder := httptest.NewRecorder()
	handler.LeaveGroup(leaveRecorder, groupLeaveRequest(dmConversationID))
	if leaveRecorder.Code != http.StatusTooManyRequests {
		t.Fatalf("leave status = %d, want 429", leaveRecorder.Code)
	}
	if provider.leaveGroupCalls != 0 {
		t.Fatal("a throttled request must not reach the service")
	}
}

func TestDMHandler_GroupAdmin_MapsRefusalsWithoutDescribingState(t *testing.T) {
	for _, test := range []struct {
		name       string
		err        error
		wantStatus int
	}{
		// A revoked membership, a non-participant and a workspace admin who is
		// not in the group are all the same 403.
		{name: "forbidden", err: domain.ErrForbidden, wantStatus: http.StatusForbidden},
		// A 1:1, an archived conversation and one that never existed are all 404.
		{name: "not found", err: domain.ErrNotFound, wantStatus: http.StatusNotFound},
		{name: "invalid input", err: domain.ErrInvalidInput, wantStatus: http.StatusBadRequest},
		{name: "conflict", err: domain.ErrConflict, wantStatus: http.StatusConflict},
		// Anything unrecognised is a generic 500 — never a database detail.
		{name: "unexpected", err: errors.New("pq: deadlock detected (SQLSTATE 40P01)"), wantStatus: http.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := &fakeDMProvider{renameGroupErr: test.err, leaveGroupErr: test.err}

			renameRecorder := httptest.NewRecorder()
			dmTestHandler(provider).RenameGroup(renameRecorder, groupRenameRequest(dmConversationID, `{"title":"Piloto"}`))
			if renameRecorder.Code != test.wantStatus {
				t.Fatalf("rename status = %d, want %d", renameRecorder.Code, test.wantStatus)
			}

			leaveRecorder := httptest.NewRecorder()
			dmTestHandler(provider).LeaveGroup(leaveRecorder, groupLeaveRequest(dmConversationID))
			if leaveRecorder.Code != test.wantStatus {
				t.Fatalf("leave status = %d, want %d", leaveRecorder.Code, test.wantStatus)
			}
			// Nothing about the failure reaches the client but its category.
			for _, recorder := range []*httptest.ResponseRecorder{renameRecorder, leaveRecorder} {
				if strings.Contains(recorder.Body.String(), "SQLSTATE") ||
					strings.Contains(recorder.Body.String(), "deadlock") {
					t.Fatalf("the response leaks database detail: %s", recorder.Body)
				}
			}
		})
	}
}

// ── Group self-leave ────────────────────────────────────────────────────────

func TestDMHandler_LeaveGroup_RemovesOnlyTheSessionsOwnMembership(t *testing.T) {
	provider := &fakeDMProvider{}
	recorder := httptest.NewRecorder()

	dmTestHandler(provider).LeaveGroup(recorder, groupLeaveRequest(dmConversationID))

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", recorder.Code, recorder.Body)
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("body = %q, want none", recorder.Body)
	}
	// There is no target user in the path, in a body (there is none) or in what
	// the handler forwards.
	if provider.lastLeaveGroup.CallerID != msgTestUserID ||
		provider.lastLeaveGroup.ConversationID != dmConversationID ||
		provider.lastLeaveGroup.WorkspaceID != testWorkspaceID {
		t.Fatalf("forwarded %+v", provider.lastLeaveGroup)
	}
}

func TestDMHandler_LeaveGroup_RequiresAnAuthenticatedActorAndAWellFormedTarget(t *testing.T) {
	t.Run("anonymous", func(t *testing.T) {
		provider := &fakeDMProvider{}
		recorder := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodDelete, "/api/chat/dm/"+dmConversationID+"/membership", nil)
		r.SetPathValue("conversationID", dmConversationID)

		dmTestHandler(provider).LeaveGroup(recorder, r)

		if recorder.Code != http.StatusUnauthorized || provider.leaveGroupCalls != 0 {
			t.Fatalf("status = %d, calls = %d", recorder.Code, provider.leaveGroupCalls)
		}
	})

	t.Run("malformed conversation id", func(t *testing.T) {
		provider := &fakeDMProvider{}
		recorder := httptest.NewRecorder()

		dmTestHandler(provider).LeaveGroup(recorder, groupLeaveRequest("not-a-uuid"))

		if recorder.Code != http.StatusBadRequest || provider.leaveGroupCalls != 0 {
			t.Fatalf("status = %d, calls = %d", recorder.Code, provider.leaveGroupCalls)
		}
	})
}

// ── Channel self-leave ──────────────────────────────────────────────────────

func TestChannelHandler_Leave_RemovesTheSessionsOwnMembershipAndAnnouncesAfterwards(t *testing.T) {
	provider := &fakeChannelProvider{}
	updates := &recordingChannelUpdates{}
	recorder := httptest.NewRecorder()

	renameHandler(provider, updates).Leave(recorder, channelLeaveRequest(createdChannelID))

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", recorder.Code, recorder.Body)
	}
	if provider.lastLeave != [3]string{activeWorkspace().ID, createdChannelID, msgTestUserID} {
		t.Fatalf("forwarded %v, want the resolved workspace, the channel and the session's user", provider.lastLeave)
	}
	// The departure's system message is announced, and only after the write
	// returned successfully.
	if len(updates.eventMessageIDs) != 1 || updates.eventMessageIDs[0] != "event-"+createdChannelID {
		t.Fatalf("announced %v, want exactly the persisted event", updates.eventMessageIDs)
	}
}

// The general channel answers 403 and says so, because the caller can plainly
// see the channel and a "not found" would be a lie they cannot act on.
func TestChannelHandler_Leave_MapsRefusalsAndAnnouncesNothing(t *testing.T) {
	for _, test := range []struct {
		name       string
		err        error
		wantStatus int
		wantBody   string
	}{
		{
			name: "general channel", err: domain.ErrGeneralChannelImmutable,
			wantStatus: http.StatusForbidden, wantBody: "general channel cannot be left",
		},
		{name: "forbidden", err: domain.ErrForbidden, wantStatus: http.StatusForbidden},
		{name: "not found", err: domain.ErrNotFound, wantStatus: http.StatusNotFound},
		{name: "invalid input", err: domain.ErrInvalidInput, wantStatus: http.StatusBadRequest},
		{name: "unexpected", err: errors.New("pq: relation does not exist"), wantStatus: http.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := &fakeChannelProvider{leaveErr: test.err}
			updates := &recordingChannelUpdates{}
			recorder := httptest.NewRecorder()

			renameHandler(provider, updates).Leave(recorder, channelLeaveRequest(createdChannelID))

			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, test.wantStatus)
			}
			if test.wantBody != "" && !strings.Contains(recorder.Body.String(), test.wantBody) {
				t.Fatalf("body = %s, want it to mention %q", recorder.Body, test.wantBody)
			}
			if strings.Contains(recorder.Body.String(), "relation does not exist") {
				t.Fatalf("the response leaks database detail: %s", recorder.Body)
			}
			// A refused departure announces nothing: the membership is still there.
			if len(updates.eventMessageIDs) != 0 {
				t.Fatalf("announced %v after a refusal", updates.eventMessageIDs)
			}
		})
	}
}

func TestChannelHandler_Leave_RequiresAnAuthenticatedActorAndAWellFormedTarget(t *testing.T) {
	t.Run("anonymous", func(t *testing.T) {
		provider := &fakeChannelProvider{}
		recorder := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodDelete, "/api/chat/channels/"+createdChannelID+"/membership", nil)
		r.SetPathValue("channelID", createdChannelID)

		renameHandler(provider, nil).Leave(recorder, r)

		if recorder.Code != http.StatusUnauthorized || provider.leaveCalls != 0 {
			t.Fatalf("status = %d, calls = %d", recorder.Code, provider.leaveCalls)
		}
	})

	t.Run("malformed channel id", func(t *testing.T) {
		provider := &fakeChannelProvider{}
		recorder := httptest.NewRecorder()

		renameHandler(provider, nil).Leave(recorder, channelLeaveRequest("not-a-uuid"))

		if recorder.Code != http.StatusBadRequest || provider.leaveCalls != 0 {
			t.Fatalf("status = %d, calls = %d", recorder.Code, provider.leaveCalls)
		}
	})

	// Every channel mutation is admitted the same way, so this one answers 415
	// to a request that does not declare JSON — the same answer a create or a
	// rename gives.
	t.Run("without the json content type", func(t *testing.T) {
		provider := &fakeChannelProvider{}
		recorder := httptest.NewRecorder()
		r := requestWithUser(http.MethodDelete, "/api/chat/channels/"+createdChannelID+"/membership", nil)
		r.SetPathValue("channelID", createdChannelID)

		renameHandler(provider, nil).Leave(recorder, r)

		if recorder.Code != http.StatusUnsupportedMediaType || provider.leaveCalls != 0 {
			t.Fatalf("status = %d, calls = %d", recorder.Code, provider.leaveCalls)
		}
	})
}

// Without the realtime dependency wired in, the departure still commits and the
// endpoint still answers: the announcement is an optional side channel, not part
// of the write.
func TestChannelHandler_Leave_WorksWithoutTheRealtimeDependency(t *testing.T) {
	provider := &fakeChannelProvider{}
	recorder := httptest.NewRecorder()

	renameHandler(provider, nil).Leave(recorder, channelLeaveRequest(createdChannelID))

	if recorder.Code != http.StatusNoContent || provider.leaveCalls != 1 {
		t.Fatalf("status = %d, calls = %d", recorder.Code, provider.leaveCalls)
	}
}
