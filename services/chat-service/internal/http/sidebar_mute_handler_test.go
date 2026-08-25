package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
)

// The mute routes (issue #527).
//
// Four verbs across two handlers, differing only in the target kind, the path
// parameter and the muted flag — the shape a copy-paste gets wrong silently. The
// same properties the pin routes hold apply here: the actor is the authenticated
// principal, the target is a validated path segment, and there is no body at all
// through which a client could name a user, a workspace or a role.

type mutingSidebarProvider struct {
	stubSidebarProvider
	mutedArgs   []string
	unmutedArgs []string
	err         error
}

func (s *mutingSidebarProvider) MuteConversation(_ context.Context, userID, targetType, targetID string) error {
	s.mutedArgs = []string{userID, targetType, targetID}
	return s.err
}

func (s *mutingSidebarProvider) UnmuteConversation(_ context.Context, userID, targetType, targetID string) error {
	s.unmutedArgs = []string{userID, targetType, targetID}
	return s.err
}

func TestSidebarHandler_MuteAndUnmuteChannelUseAuthenticatedUser(t *testing.T) {
	svc := &mutingSidebarProvider{}
	router := sidebarRouter(makeTestValidator(t), svc)
	channelID := "11111111-1111-1111-1111-111111111111"
	path := "/api/chat/channels/" + channelID + "/mute"

	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, authSidebarPinRequest(t, method, path))
		if rr.Code != http.StatusNoContent {
			t.Fatalf("%s expected 204, got %d: %s", method, rr.Code, rr.Body.String())
		}
	}
	// The user is the session's, never a field of the request.
	if got, want := strings.Join(svc.mutedArgs, ","),
		strings.Join([]string{testUserID, service.ReadTargetChannel, channelID}, ","); got != want {
		t.Fatalf("mute args = %q, want %q", got, want)
	}
	if got, want := strings.Join(svc.unmutedArgs, ","),
		strings.Join([]string{testUserID, service.ReadTargetChannel, channelID}, ","); got != want {
		t.Fatalf("unmute args = %q, want %q", got, want)
	}
}

// The DM pair is the one no other test reaches: a handler that read {channelID}
// from a route that only carries {conversationID}, or that muted a *channel*
// from the DM route, would ship without a failing test.
func TestSidebarHandler_MuteAndUnmuteDMTargetTheConversation(t *testing.T) {
	svc := &mutingSidebarProvider{}
	router := sidebarRouter(makeTestValidator(t), svc)
	conversationID := "22222222-2222-2222-2222-222222222222"
	path := "/api/chat/dm/" + conversationID + "/mute"

	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, authSidebarPinRequest(t, method, path))
		if rr.Code != http.StatusNoContent {
			t.Fatalf("%s expected 204, got %d: %s", method, rr.Code, rr.Body.String())
		}
	}
	if got, want := strings.Join(svc.mutedArgs, ","),
		strings.Join([]string{testUserID, service.ReadTargetDM, conversationID}, ","); got != want {
		t.Fatalf("mute args = %q, want %q", got, want)
	}
	if got, want := strings.Join(svc.unmutedArgs, ","),
		strings.Join([]string{testUserID, service.ReadTargetDM, conversationID}, ","); got != want {
		t.Fatalf("unmute args = %q, want %q", got, want)
	}
}

// A target that is not a UUID is refused before the service is reached: the
// route parameter is data, and the endpoint must not become a way to hand
// arbitrary strings to the database.
func TestSidebarHandler_MuteRejectsAMalformedTarget(t *testing.T) {
	svc := &mutingSidebarProvider{}
	router := sidebarRouter(makeTestValidator(t), svc)

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authSidebarPinRequest(t, http.MethodPost, "/api/chat/channels/not-a-uuid/mute"))

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rr.Code, rr.Body.String())
	}
	if svc.mutedArgs != nil {
		t.Fatalf("the service was reached with %v", svc.mutedArgs)
	}
}

// The general channel, a conversation the caller cannot see and one that does
// not exist are all the same non-enumerating 404 — the endpoint must not become
// a way to probe which IDs exist.
func TestSidebarHandler_MuteRefusalIsANonEnumeratingNotFound(t *testing.T) {
	svc := &mutingSidebarProvider{err: domain.ErrNotFound}
	router := sidebarRouter(makeTestValidator(t), svc)

	for _, path := range []string{
		"/api/chat/channels/11111111-1111-1111-1111-111111111111/mute",
		"/api/chat/dm/22222222-2222-2222-2222-222222222222/mute",
	} {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, authSidebarPinRequest(t, http.MethodPost, path))
		if rr.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404: %s", path, rr.Code, rr.Body.String())
		}
		if strings.Contains(rr.Body.String(), "geral") || strings.Contains(rr.Body.String(), "general") {
			t.Fatalf("the refusal names the reason: %s", rr.Body)
		}
	}
}

// A build whose sidebar service does not offer preferences answers 503 rather
// than pretending the mute landed.
func TestSidebarHandler_MuteWithoutAProviderIsUnavailable(t *testing.T) {
	router := sidebarRouter(makeTestValidator(t), &stubSidebarProvider{})

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authSidebarPinRequest(
		t, http.MethodPost, "/api/chat/channels/11111111-1111-1111-1111-111111111111/mute"))

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rr.Code, rr.Body.String())
	}
}

func TestSidebarHandler_MuteRequiresAuthentication(t *testing.T) {
	svc := &mutingSidebarProvider{}
	router := sidebarRouter(makeTestValidator(t), svc)

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(
		http.MethodPost, "/api/chat/channels/11111111-1111-1111-1111-111111111111/mute", nil))

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
	if svc.mutedArgs != nil {
		t.Fatalf("the service was reached by an anonymous caller: %v", svc.mutedArgs)
	}
}
