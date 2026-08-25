package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	httpapi "github.com/nicrepository/nchat/services/chat-service/internal/http"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// recordingChannelUpdates captures the post-commit realtime signal so the tests
// can assert it fires exactly when the write succeeded and never otherwise.
type recordingChannelUpdates struct {
	workspaceIDs []string
	channelIDs   []string
	// Persisted system messages announced after commit (issue #527).
	eventMessageIDs []string
}

func (r *recordingChannelUpdates) PublishConversationUpdated(_ context.Context, workspaceID, _, targetID string) {
	r.workspaceIDs = append(r.workspaceIDs, workspaceID)
	r.channelIDs = append(r.channelIDs, targetID)
}

func (r *recordingChannelUpdates) PublishConversationEvent(_ context.Context, _, _, _, messageID string) {
	r.eventMessageIDs = append(r.eventMessageIDs, messageID)
}

func renameHandler(provider *fakeChannelProvider, updates *recordingChannelUpdates) *httpapi.ChannelHandler {
	handler := httpapi.NewChannelHandler(
		&fakeWorkspaceResolver{workspace: activeWorkspace()}, provider, &fakeDMRateLimiter{},
	)
	if updates != nil {
		handler = handler.WithChannelUpdates(updates)
	}
	return handler
}

func renameRequest(channelID, body string) *http.Request {
	r := requestWithUser(http.MethodPatch, "/api/chat/channels/"+channelID, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.SetPathValue("channelID", channelID)
	return r
}

// renamed models what a committed rename returns: the channel plus the system
// message the same transaction wrote (issue #527).
func renamed(name string) storage.UpdateChannelResult {
	return storage.UpdateChannelResult{
		Channel: domain.Channel{
			ID: createdChannelID, Slug: "infra", DisplayName: name, Type: domain.ChannelTypePublic,
		},
		Event: domain.Message{ID: "event-" + createdChannelID, Kind: domain.MessageKindSystem},
	}
}

// renamedWithoutEvent models an update that changed no name, so the transaction
// wrote no event and nothing must be announced for it.
func renamedWithoutEvent(name string) storage.UpdateChannelResult {
	result := renamed(name)
	result.Event = domain.Message{}
	return result
}

// The caller and the workspace come from the session. A body that tries to name
// either is refused outright by the strict decoder, so there is no field through
// which a request could aim a rename at another workspace or another actor.
func TestChannelHandler_Rename_DerivesCallerAndWorkspaceServerSide(t *testing.T) {
	provider := &fakeChannelProvider{updated: renamed("Plataforma")}
	updates := &recordingChannelUpdates{}

	recorder := httptest.NewRecorder()
	renameHandler(provider, updates).Rename(recorder, renameRequest(createdChannelID, `{"display_name":"Plataforma"}`))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	want := service.UpdateChannelInput{
		WorkspaceID: testWorkspaceID,
		CallerID:    msgTestUserID,
		ChannelID:   createdChannelID,
		DisplayName: "Plataforma",
	}
	if provider.lastUpdateInput != want {
		t.Fatalf("input = %+v, want %+v", provider.lastUpdateInput, want)
	}
	body := decodeBody(t, recorder)
	data, _ := body["data"].(map[string]any)
	if data["id"] != createdChannelID {
		t.Fatalf("response id = %v, want the unchanged channel id %s", data["id"], createdChannelID)
	}
	if data["display_name"] != "Plataforma" {
		t.Fatalf("response display_name = %v, want Plataforma", data["display_name"])
	}
	if len(updates.channelIDs) != 1 || updates.channelIDs[0] != createdChannelID {
		t.Fatalf("conversation.updated published for %v, want exactly [%s]", updates.channelIDs, createdChannelID)
	}
	// The system message the transaction wrote is announced too, and only after
	// the service returned — that is, only after the commit (issue #527).
	if len(updates.eventMessageIDs) != 1 || updates.eventMessageIDs[0] != "event-"+createdChannelID {
		t.Fatalf("conversation.event published for %v, want the persisted message id", updates.eventMessageIDs)
	}
}

// An update that renamed nothing has no event to announce, and must not invent
// one: a timeline entry for "nothing changed" would be visible to everyone.
func TestChannelHandler_Rename_PublishesNoEventWhenNothingWasRenamed(t *testing.T) {
	provider := &fakeChannelProvider{updated: renamedWithoutEvent("Infra")}
	updates := &recordingChannelUpdates{}

	recorder := httptest.NewRecorder()
	renameHandler(provider, updates).Rename(recorder, renameRequest(createdChannelID, `{"display_name":"Infra"}`))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if len(updates.eventMessageIDs) != 0 {
		t.Fatalf("published %v for an update that renamed nothing", updates.eventMessageIDs)
	}
}

// Slug, type, category and position are not renameable through this route: the
// service reads their zero values as "unchanged", so a rename can never become a
// visibility change or move a channel between categories.
func TestChannelHandler_Rename_ForwardsOnlyTheDisplayName(t *testing.T) {
	provider := &fakeChannelProvider{updated: renamed("Plataforma")}

	recorder := httptest.NewRecorder()
	renameHandler(provider, nil).Rename(recorder, renameRequest(createdChannelID, `{"display_name":"Plataforma"}`))

	input := provider.lastUpdateInput
	if input.Slug != "" || input.Type != nil || input.CategoryID != nil || input.Position != nil {
		t.Fatalf("input carried more than the display name: %+v", input)
	}
}

func TestChannelHandler_Rename_RejectsUnknownBodyFields(t *testing.T) {
	for _, body := range []string{
		`{"display_name":"X","workspace_id":"11111111-1111-1111-1111-111111111111"}`,
		`{"display_name":"X","type":"private"}`,
		`{"display_name":"X","slug":"outro"}`,
		`{"display_name":"X","caller_id":"someone-else"}`,
		`{"display_name":"X","can_rename":true}`,
	} {
		provider := &fakeChannelProvider{updated: renamed("X")}
		recorder := httptest.NewRecorder()
		renameHandler(provider, nil).Rename(recorder, renameRequest(createdChannelID, body))

		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d for %s, want 400", recorder.Code, body)
		}
		if provider.updateCalls != 0 {
			t.Fatalf("service reached with a body claiming a privilege: %s", body)
		}
	}
}

func TestChannelHandler_Rename_RequiresAuthenticatedUser(t *testing.T) {
	provider := &fakeChannelProvider{updated: renamed("X")}
	request := httptest.NewRequest(http.MethodPatch, "/api/chat/channels/"+createdChannelID,
		strings.NewReader(`{"display_name":"X"}`))
	request.Header.Set("Content-Type", "application/json")
	request.SetPathValue("channelID", createdChannelID)

	recorder := httptest.NewRecorder()
	renameHandler(provider, nil).Rename(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
	if provider.updateCalls != 0 {
		t.Fatalf("service called %d times for an unauthenticated request", provider.updateCalls)
	}
}

// A path segment that is not a UUID never reaches the service: it cannot be an
// ID this deployment issued, so there is nothing to look up and nothing to leak.
func TestChannelHandler_Rename_RejectsMalformedChannelID(t *testing.T) {
	for _, id := range []string{"nao-e-uuid", "../../etc/passwd", "00000000-0000-0000-0000-000000000000"} {
		provider := &fakeChannelProvider{updated: renamed("X")}
		recorder := httptest.NewRecorder()
		renameHandler(provider, nil).Rename(recorder, renameRequest(id, `{"display_name":"X"}`))

		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d for id %q, want 400", recorder.Code, id)
		}
		if provider.updateCalls != 0 {
			t.Fatalf("service reached with malformed id %q", id)
		}
	}
}

// Every denial the domain can produce, and the status it is allowed to reveal.
//
// ErrForbidden (member, guest, moderator — CanManageWorkspace admits none of
// them) stays 403 and ErrNotFound stays 404, both without a body that describes
// state: the authorization check runs before the channel is read, so neither a
// non-manager nor a manager of another workspace can use this route to learn
// whether a channel ID exists.
func TestChannelHandler_Rename_MapsDomainErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"member/guest/moderator refused", domain.ErrForbidden, http.StatusForbidden},
		{"channel of another workspace", domain.ErrNotFound, http.StatusNotFound},
		{"empty name", domain.ErrChannelDisplayNameRequired, http.StatusBadRequest},
		{"name over the cap", domain.ErrChannelDisplayNameTooLong, http.StatusBadRequest},
		{"geral is immutable", domain.ErrInvalidInput, http.StatusBadRequest},
		{"concurrent write", domain.ErrConflict, http.StatusConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := &fakeChannelProvider{updateErr: test.err}
			updates := &recordingChannelUpdates{}
			recorder := httptest.NewRecorder()
			renameHandler(provider, updates).Rename(recorder, renameRequest(createdChannelID, `{"display_name":"X"}`))

			if recorder.Code != test.want {
				t.Fatalf("status = %d, want %d", recorder.Code, test.want)
			}
			// The rejected name never comes back. It is caller-controlled text
			// that would otherwise reach every error body and every log line.
			if body := recorder.Body.String(); strings.Contains(body, "X") {
				t.Fatalf("response echoed the submitted name: %s", body)
			}
			if len(updates.channelIDs) != 0 || len(updates.eventMessageIDs) != 0 {
				t.Fatalf("a refused rename published %d updates and %d events",
					len(updates.channelIDs), len(updates.eventMessageIDs))
			}
		})
	}
}

func TestChannelHandler_Rename_RequiresJSONContentType(t *testing.T) {
	provider := &fakeChannelProvider{updated: renamed("X")}
	request := requestWithUser(http.MethodPatch, "/api/chat/channels/"+createdChannelID,
		strings.NewReader(`{"display_name":"X"}`))
	request.SetPathValue("channelID", createdChannelID)

	recorder := httptest.NewRecorder()
	renameHandler(provider, nil).Rename(recorder, request)

	if recorder.Code == http.StatusOK {
		t.Fatal("a request without a JSON content type was accepted")
	}
	if provider.updateCalls != 0 {
		t.Fatal("service reached without a JSON content type")
	}
}

// The rename persists and answers even when no hub is wired: the write is
// authoritative on its own, and a missing broadcaster costs a stale name in
// other sessions until their next refetch — never the rename.
func TestChannelHandler_Rename_SucceedsWithoutABroadcaster(t *testing.T) {
	provider := &fakeChannelProvider{updated: renamed("Plataforma")}

	recorder := httptest.NewRecorder()
	renameHandler(provider, nil).Rename(recorder, renameRequest(createdChannelID, `{"display_name":"Plataforma"}`))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
}
