package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

type ownershipStub struct {
	err        error
	input      storage.OwnershipMutation
	calls      int
	disabled   bool
	enabledErr error
	result     storage.OwnershipMutationResult
}

func (s *ownershipStub) PrivateEnabled(context.Context, storage.OwnershipScope) (bool, error) {
	return !s.disabled, s.enabledErr
}
func (s *ownershipStub) Details(context.Context, storage.OwnershipScope) (storage.OwnershipDetails, error) {
	return storage.OwnershipDetails{}, s.err
}
func (s *ownershipStub) Mutate(_ context.Context, input storage.OwnershipMutation) (storage.OwnershipMutationResult, error) {
	s.calls++
	s.input = input
	return s.result, s.err
}

type ownershipWorkspaceStub struct{}

func (ownershipWorkspaceStub) GetDefaultWorkspace(context.Context) (domain.Workspace, error) {
	return domain.Workspace{ID: "95300000-0000-4000-8000-000000000001"}, nil
}

type ownershipLimiterStub struct{}

func (ownershipLimiterStub) AllowActionWithLimit(context.Context, string, string, int, int) (bool, error) {
	return true, nil
}

type ownershipDMDependencies struct{ dmProvider }
type ownershipChannelDependencies struct{ channelProvider }

func ownershipRequest(method, body string) *http.Request {
	r := httptest.NewRequest(method, "/ownership", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.SetPathValue("conversationID", "95300000-0000-4000-8000-000000000002")
	r.SetPathValue("channelID", "95300000-0000-4000-8000-000000000002")
	r.SetPathValue("userID", "95300000-0000-4000-8000-00000000000b")
	return r.WithContext(context.WithValue(r.Context(), ctxKeyUserID, "95300000-0000-4000-8000-00000000000a"))
}

// Real wrappers must switch authority only after activation, preserve legacy
// fallback and pass the server-resolved workspace/actor into every mutation.
func TestOwnershipLegacyWrappers(t *testing.T) {
	for _, kind := range []string{"dm", "channel"} {
		for _, operation := range []string{"leave", "remove", "rename"} {
			t.Run(kind+"/"+operation, func(t *testing.T) { testOwnershipLegacyWrapper(t, kind, operation) })
		}
	}
}

func testOwnershipLegacyWrapper(t *testing.T, kind, operation string) {
	t.Helper()
	provider := &ownershipStub{}
	legacyCalled := false
	legacy := func(w http.ResponseWriter, _ *http.Request) { legacyCalled = true; w.WriteHeader(202) }
	dm := NewDMHandler(ownershipWorkspaceStub{}, ownershipDMDependencies{}, ownershipLimiterStub{}).WithOwnership(provider)
	channel := NewChannelHandler(ownershipWorkspaceStub{}, ownershipChannelDependencies{}, ownershipLimiterStub{}).WithOwnership(provider)
	handler := dm.ownershipAware(operation, legacy)
	if kind == "channel" {
		handler = channel.ownershipAware(operation, legacy)
	}
	response := httptest.NewRecorder()
	handler(response, ownershipRequest(http.MethodPatch, `{"title":"  Grupo  ","display_name":"  Canal  "}`))
	assertOwnershipLegacyResponse(t, response, provider, kind, operation)
	provider.disabled = true
	response = httptest.NewRecorder()
	handler(response, ownershipRequest(http.MethodDelete, ""))
	if response.Code != 202 || !legacyCalled || provider.calls != 1 {
		t.Fatal("disabled ownership bypassed legacy or mutated ownership")
	}
	provider.enabledErr = domain.ErrNotFound
	response = httptest.NewRecorder()
	handler(response, ownershipRequest(http.MethodDelete, ""))
	if response.Code != 404 {
		t.Fatalf("availability error=%d", response.Code)
	}
}

func assertOwnershipLegacyResponse(t *testing.T, response *httptest.ResponseRecorder, provider *ownershipStub, kind, operation string) {
	t.Helper()
	want := 204
	if operation == "rename" {
		want = 200
	}
	if response.Code != want || provider.calls != 1 || provider.input.Scope.Kind != kind {
		t.Fatalf("status=%d calls=%d scope=%+v", response.Code, provider.calls, provider.input.Scope)
	}
	if operation == "rename" && strings.Contains(response.Body.String(), "  ") {
		t.Fatal("response did not normalize name")
	}
}

func TestOwnershipReadRoutesAndProjection(t *testing.T) {
	provider := &ownershipStub{}
	dm := NewDMHandler(ownershipWorkspaceStub{}, ownershipDMDependencies{}, ownershipLimiterStub{}).WithOwnership(provider)
	channel := NewChannelHandler(ownershipWorkspaceStub{}, ownershipChannelDependencies{}, ownershipLimiterStub{}).WithOwnership(provider)
	for _, handler := range []http.HandlerFunc{dm.Ownership, channel.Ownership} {
		response := httptest.NewRecorder()
		handler(response, ownershipRequest(http.MethodGet, ""))
		if response.Code != 200 {
			t.Fatalf("read status=%d body=%s", response.Code, response.Body.String())
		}
	}
	projection, err := ownershipProjection(t.Context(), provider, storage.OwnershipScope{})
	if err != nil || projection == nil {
		t.Fatalf("projection=%v error=%v", projection, err)
	}
	provider.err = domain.ErrNotFound
	if _, err := ownershipProjection(t.Context(), provider, storage.OwnershipScope{}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("projection swallowed revoked access")
	}
	provider.disabled = true
	if projection, err := ownershipProjection(t.Context(), provider, storage.OwnershipScope{}); err != nil || projection != nil {
		t.Fatal("disabled projection exposed ownership")
	}
	if projection, err := ownershipProjection(t.Context(), nil, storage.OwnershipScope{}); err != nil || projection != nil {
		t.Fatal("unwired projection exposed ownership")
	}
}

func TestOwnershipReadFailureContract(t *testing.T) {
	for _, tc := range []struct {
		provider ownershipProvider
		want     int
	}{
		{nil, 503},
		{&ownershipStub{err: domain.ErrInvalidInput}, 400},
		{&ownershipStub{err: errors.New("database unavailable")}, 500},
		{&ownershipStub{err: domain.ErrNotFound}, 404},
	} {
		response := httptest.NewRecorder()
		handleOwnership(response, ownershipRequest(http.MethodGet, ""), tc.provider, storage.OwnershipScope{}, nil)
		if response.Code != tc.want {
			t.Fatalf("status=%d want=%d", response.Code, tc.want)
		}
	}
}

type ownershipBroadcastStub struct {
	events                                 int
	workspace, kind, conversation, message string
}

func (*ownershipBroadcastStub) PublishConversationUpdated(context.Context, string, string, string) {}
func (s *ownershipBroadcastStub) PublishConversationEvent(_ context.Context, workspace, kind, conversation, message string) {
	s.events++
	s.workspace = workspace
	s.kind = kind
	s.conversation = conversation
	s.message = message
}

func TestOwnershipReplayDoesNotDuplicatePersistedEvent(t *testing.T) {
	broadcast := &ownershipBroadcastStub{}
	scope := storage.OwnershipScope{WorkspaceID: "workspace", Kind: "dm", ConversationID: "group"}
	result := storage.OwnershipMutationResult{EventID: "event"}
	publishOwnershipResult(t.Context(), broadcast, scope, result)
	result.Replayed = true
	publishOwnershipResult(t.Context(), broadcast, scope, result)
	if broadcast.events != 1 || broadcast.workspace != "workspace" || broadcast.kind != "dm" || broadcast.conversation != "group" || broadcast.message != "event" {
		t.Fatalf("broadcast=%+v", broadcast)
	}
}

func TestOwnershipOptionalWiringRetainsCompatibility(t *testing.T) {
	var dm *DMHandler
	var channel *ChannelHandler
	if dm.WithOwnership(nil) != nil || channel.WithOwnership(nil) != nil {
		t.Fatal("nil handler was initialized")
	}
	legacy := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(202) }
	for _, handler := range []http.HandlerFunc{NewDMHandler(nil, nil, nil).ownershipAware("leave", legacy), NewChannelHandler(nil, nil, nil).ownershipAware("leave", legacy)} {
		response := httptest.NewRecorder()
		handler(response, ownershipRequest(http.MethodDelete, ""))
		if response.Code != 202 {
			t.Fatal("compatibility fallback refused")
		}
	}
}

func TestOwnershipHandlerStrictInputAndErrorContract(t *testing.T) {
	cases := []struct {
		name, method, operation, body, key string
		err                                error
		want                               int
		called                             bool
	}{
		{"no-op", http.MethodPatch, "", `{"role":"member"}`, "", nil, 200, true},
		{"reject workspace", http.MethodPatch, "", `{"role":"admin","workspace_id":"forged"}`, "", nil, 400, false},
		{"reject current role", http.MethodPatch, "", `{"role":"admin","current_role":"owner"}`, "", nil, 400, false},
		{"reject owner count", http.MethodPatch, "", `{"role":"admin","owner_count":2}`, "", nil, 400, false},
		{"reject trailing JSON", http.MethodPatch, "", `{"role":"admin"}{}`, "", nil, 400, false},
		{"reject missing role", http.MethodPatch, "", `{}`, "", nil, 400, false},
		{"reject null role", http.MethodPatch, "", `{"role":null}`, "", nil, 400, false},
		{"promotion", http.MethodPatch, "", `{"role":"owner"}`, "", nil, 200, true},
		{"reject actor assignment", http.MethodPatch, "", `{"role":"owner","actor_user_id":"forged"}`, "", nil, 400, false},
		{"reject unknown role", http.MethodPatch, "", `{"role":"moderator"}`, "", nil, 400, false},
		{"transfer key required", http.MethodPost, "transfer", `{"new_owner_user_id":"95300000-0000-4000-8000-00000000000b","actor_new_role":"member"}`, "", nil, 400, false},
		{"transfer", http.MethodPost, "transfer", `{"new_owner_user_id":"95300000-0000-4000-8000-00000000000b","actor_new_role":"admin"}`, "request", nil, 200, true},
		{"unknown operation", http.MethodPost, "remove", `{}`, "request", nil, 404, false},
		{"owner conflict", http.MethodPatch, "", `{"role":"member"}`, "", domain.ErrOwnershipConflict, 409, true},
		{"private inaccessible", http.MethodPatch, "", `{"role":"owner"}`, "", domain.ErrNotFound, 404, true},
		{"member forbidden", http.MethodPatch, "", `{"role":"owner"}`, "", domain.ErrForbidden, 403, true},
	}
	scope := storage.OwnershipScope{WorkspaceID: "95300000-0000-4000-8000-000000000001", Kind: "dm", ConversationID: "95300000-0000-4000-8000-000000000002", ActorID: "95300000-0000-4000-8000-00000000000a"}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, "/ownership", strings.NewReader(tc.body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Idempotency-Key", tc.key)
			request.SetPathValue("operation", tc.operation)
			request.SetPathValue("userID", "95300000-0000-4000-8000-00000000000b")
			provider := &ownershipStub{err: tc.err}
			response := httptest.NewRecorder()
			handleOwnership(response, request, provider, scope, nil)
			if response.Code != tc.want {
				t.Fatalf("status %d body %s", response.Code, response.Body.String())
			}
			if (provider.calls > 0) != tc.called {
				t.Fatalf("store called %d", provider.calls)
			}
			if tc.called && provider.input.Scope != scope {
				t.Fatalf("actor/workspace replaced: %+v", provider.input.Scope)
			}
		})
	}
}
