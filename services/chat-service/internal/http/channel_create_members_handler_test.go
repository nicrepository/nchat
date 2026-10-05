package httpapi_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	httpapi "github.com/nicrepository/nchat/services/chat-service/internal/http"
)

// Issue #1025: the HTTP contract of private-channel creation with initial
// members and Idempotency-Key.

const privateCreateBody = `{"slug":"infra","display_name":"Infra","type":"private",` +
	`"initial_member_ids":["22222222-2222-4222-8222-222222222222"]}`

func createHandlerWithBroadcast(provider *fakeChannelProvider) (*httpapi.ChannelHandler, *recordingBroadcaster) {
	broadcast := &recordingBroadcaster{}
	return httpapi.NewChannelHandler(
		&fakeWorkspaceResolver{workspace: activeWorkspace()}, provider, &fakeDMRateLimiter{},
	).WithMembers(&fakeMemberManager{}, broadcast), broadcast
}

func privateChannel() domain.Channel {
	return domain.Channel{ID: createdChannelID, Slug: "infra", DisplayName: "Infra", Type: domain.ChannelTypePrivate}
}

func TestChannelHandler_Create_ForwardsInitialMembersAndKey(t *testing.T) {
	provider := &fakeChannelProvider{
		channel: privateChannel(), createdMemberIDs: []string{"22222222-2222-4222-8222-222222222222"},
	}
	handler, broadcast := createHandlerWithBroadcast(provider)
	request := createChannelRequest(privateCreateBody)
	request.Header.Set("Idempotency-Key", "intent-123")

	recorder := httptest.NewRecorder()
	handler.Create(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if provider.lastInput.IdempotencyKey != "intent-123" ||
		!slices.Equal(provider.lastInput.InitialMemberIDs, []string{"22222222-2222-4222-8222-222222222222"}) ||
		provider.lastInput.CallerID != msgTestUserID || provider.lastInput.WorkspaceID != testWorkspaceID {
		t.Fatalf("input = %+v", provider.lastInput)
	}
	// The invitees learn about the channel only after the committed creation,
	// through the existing user-scoped signal.
	if len(broadcast.available) != 1 || broadcast.available[0].TargetID != createdChannelID ||
		!slices.Equal(broadcast.available[0].UserIDs, provider.createdMemberIDs) {
		t.Fatalf("available = %+v", broadcast.available)
	}
}

func TestChannelHandler_Create_ReplayIs200AndAnnouncesNothing(t *testing.T) {
	provider := &fakeChannelProvider{channel: privateChannel(), createReplayed: true}
	handler, broadcast := createHandlerWithBroadcast(provider)
	request := createChannelRequest(privateCreateBody)
	request.Header.Set("Idempotency-Key", "intent-123")

	recorder := httptest.NewRecorder()
	handler.Create(recorder, request)

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), createdChannelID) {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if len(broadcast.available) != 0 || len(broadcast.calls) != 0 {
		t.Fatalf("a replay announced: %+v", broadcast)
	}
}

func TestChannelHandler_Create_NoInviteesAnnouncesNothing(t *testing.T) {
	provider := &fakeChannelProvider{channel: privateChannel()}
	handler, broadcast := createHandlerWithBroadcast(provider)
	recorder := httptest.NewRecorder()
	handler.Create(recorder, createChannelRequest(validCreateBody))
	if recorder.Code != http.StatusCreated || len(broadcast.available) != 0 {
		t.Fatalf("status = %d, available = %+v", recorder.Code, broadcast.available)
	}
}

func TestChannelHandler_Create_RefusedCreationAnnouncesNothing(t *testing.T) {
	provider := &fakeChannelProvider{err: domain.ErrForbidden, createdMemberIDs: []string{"x"}}
	handler, broadcast := createHandlerWithBroadcast(provider)
	recorder := httptest.NewRecorder()
	handler.Create(recorder, createChannelRequest(privateCreateBody))
	if recorder.Code != http.StatusForbidden || len(broadcast.available) != 0 {
		t.Fatalf("status = %d, available = %+v", recorder.Code, broadcast.available)
	}
}

func TestChannelHandler_Create_ReusedKeyIs409WithItsOwnCode(t *testing.T) {
	provider := &fakeChannelProvider{err: domain.ErrIdempotencyKeyReused}
	recorder := httptest.NewRecorder()
	channelTestHandler(provider).Create(recorder, createChannelRequest(privateCreateBody))
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "idempotency_key_reused") {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestChannelHandler_Create_RejectsMalformedKeyBeforeTheService(t *testing.T) {
	for name, set := range map[string]func(*http.Request){
		"bad characters": func(r *http.Request) { r.Header.Set("Idempotency-Key", "has spaces!") },
		"too long":       func(r *http.Request) { r.Header.Set("Idempotency-Key", strings.Repeat("k", 129)) },
		"repeated": func(r *http.Request) {
			r.Header.Add("Idempotency-Key", "a")
			r.Header.Add("Idempotency-Key", "b")
		},
	} {
		t.Run(name, func(t *testing.T) {
			provider := &fakeChannelProvider{}
			request := createChannelRequest(privateCreateBody)
			set(request)
			recorder := httptest.NewRecorder()
			channelTestHandler(provider).Create(recorder, request)
			if recorder.Code != http.StatusBadRequest || provider.calls != 0 {
				t.Fatalf("status = %d, calls = %d", recorder.Code, provider.calls)
			}
		})
	}
}

// Strict JSON: membership privileges have no field to arrive through.
func TestChannelHandler_Create_RejectsPrivilegedFields(t *testing.T) {
	for _, field := range []string{
		`"role":"owner"`, `"owner_id":"x"`, `"created_by":"x"`, `"workspace_id":"x"`,
		`"capabilities":["manage"]`, `"members":[{"user_id":"x","role":"moderator"}]`,
	} {
		t.Run(field, func(t *testing.T) {
			provider := &fakeChannelProvider{}
			body := `{"slug":"infra","display_name":"Infra","type":"private",` + field + `}`
			recorder := httptest.NewRecorder()
			channelTestHandler(provider).Create(recorder, createChannelRequest(body))
			if recorder.Code != http.StatusBadRequest || provider.calls != 0 {
				t.Fatalf("status = %d, calls = %d", recorder.Code, provider.calls)
			}
		})
	}
}

// createStatusWith runs one creation through a handler wired with the given
// workspace resolver, limiter and service fake, and returns the HTTP status.
func createStatusWith(
	t *testing.T, resolver *fakeWorkspaceResolver, limiter *fakeDMRateLimiter, provider *fakeChannelProvider,
) int {
	t.Helper()
	recorder := httptest.NewRecorder()
	httpapi.NewChannelHandler(resolver, provider, limiter).Create(recorder, createChannelRequest(privateCreateBody))
	return recorder.Code
}

func TestChannelHandler_Create_MissingWorkspaceIs404WithoutTheService(t *testing.T) {
	provider := &fakeChannelProvider{}
	status := createStatusWith(t, &fakeWorkspaceResolver{err: domain.ErrNotFound}, &fakeDMRateLimiter{}, provider)
	if status != http.StatusNotFound || provider.calls != 0 {
		t.Fatalf("status = %d, calls = %d", status, provider.calls)
	}
}

func TestChannelHandler_Create_WorkspaceLookupFailureIs500WithoutTheService(t *testing.T) {
	provider := &fakeChannelProvider{}
	status := createStatusWith(t, &fakeWorkspaceResolver{err: errors.New("db down")}, &fakeDMRateLimiter{}, provider)
	if status != http.StatusInternalServerError || provider.calls != 0 {
		t.Fatalf("status = %d, calls = %d", status, provider.calls)
	}
}

func TestChannelHandler_Create_RateLimiterFailureIs503WithoutTheService(t *testing.T) {
	provider := &fakeChannelProvider{}
	resolver := &fakeWorkspaceResolver{workspace: activeWorkspace()}
	status := createStatusWith(t, resolver, &fakeDMRateLimiter{err: errors.New("valkey down")}, provider)
	if status != http.StatusServiceUnavailable || provider.calls != 0 {
		t.Fatalf("status = %d, calls = %d", status, provider.calls)
	}
}

func TestChannelHandler_Create_ServiceNotFoundIs404(t *testing.T) {
	resolver := &fakeWorkspaceResolver{workspace: activeWorkspace()}
	status := createStatusWith(t, resolver, &fakeDMRateLimiter{}, &fakeChannelProvider{err: domain.ErrNotFound})
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
}
