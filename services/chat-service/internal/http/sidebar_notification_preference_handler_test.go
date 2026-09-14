package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
)

// The canonical notification-preference route (issue #136).
//
// The mute routes beside it keep their own suite; what these add is the whole
// preference: a closed set of modes, a strictly decoded body, and the same
// authority rules — the actor is the session, the target is a validated path
// segment, and there is nothing in the payload through which a client could
// name a user, a workspace or a role.

type preferringSidebarProvider struct {
	stubSidebarProvider
	args []string
	err  error
}

func (s *preferringSidebarProvider) SetConversationNotificationPreference(
	_ context.Context, userID, targetType, targetID, mode string,
) error {
	s.args = []string{userID, targetType, targetID, mode}
	return s.err
}

// preferenceRequest is authSidebarPinRequest with a body, which the mute routes
// never have.
func preferenceRequest(t *testing.T, path, body string) *http.Request {
	t.Helper()
	tok := makeTestToken(t, testUserID, testHMACSecret, testIssuer, testAudience, time.Hour)
	req := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	setBearerToken(req, tok)
	return req
}

// Every mode the first version offers, on both target kinds, with the actor
// taken from the session rather than from the request.
func TestSidebarHandler_SetNotificationPreferenceAcceptsEveryMode(t *testing.T) {
	channelID := "11111111-1111-1111-1111-111111111111"
	conversationID := "22222222-2222-2222-2222-222222222222"

	for _, target := range []struct {
		name       string
		path       string
		targetType string
		targetID   string
	}{
		{name: "channel", path: "/api/chat/channels/" + channelID + "/notification-preference",
			targetType: service.ReadTargetChannel, targetID: channelID},
		{name: "dm", path: "/api/chat/dm/" + conversationID + "/notification-preference",
			targetType: service.ReadTargetDM, targetID: conversationID},
	} {
		for _, mode := range []string{
			service.NotificationModeAll,
			service.NotificationModeMentionsReplies,
			service.NotificationModeMuted,
		} {
			t.Run(target.name+"/"+mode, func(t *testing.T) {
				svc := &preferringSidebarProvider{}
				router := sidebarRouter(makeTestValidator(t), svc)

				rr := httptest.NewRecorder()
				router.ServeHTTP(rr, preferenceRequest(t, target.path, `{"mode":"`+mode+`"}`))

				if rr.Code != http.StatusNoContent {
					t.Fatalf("status = %d, want 204: %s", rr.Code, rr.Body.String())
				}
				want := strings.Join([]string{testUserID, target.targetType, target.targetID, mode}, ",")
				if got := strings.Join(svc.args, ","); got != want {
					t.Fatalf("service args = %q, want %q", got, want)
				}
			})
		}
	}
}

// The closed set is closed. A mode outside it is a 400 and never a write that
// guesses at what the caller meant.
func TestSidebarHandler_SetNotificationPreferenceRefusesAnUnknownMode(t *testing.T) {
	for _, body := range []string{
		`{"mode":"mentions"}`,
		`{"mode":""}`,
		`{"mode":"ALL"}`,
		`{"mode":"muted "}`,
		`{}`,
	} {
		svc := &preferringSidebarProvider{}
		router := sidebarRouter(makeTestValidator(t), svc)

		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, preferenceRequest(
			t, "/api/chat/channels/11111111-1111-1111-1111-111111111111/notification-preference", body))

		if rr.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d, want 400: %s", body, rr.Code, rr.Body.String())
		}
		if svc.args != nil {
			t.Fatalf("%s reached the service with %v", body, svc.args)
		}
	}
}

// Nothing in the payload may name an actor or a tenant, and the decoder is what
// enforces it: an unknown field is refused rather than ignored, so a request
// that tried to carry a user_id cannot be silently accepted as though it had
// not.
func TestSidebarHandler_SetNotificationPreferenceRefusesAnExtendedPayload(t *testing.T) {
	for _, body := range []string{
		`{"mode":"muted","user_id":"33333333-3333-3333-3333-333333333333"}`,
		`{"mode":"muted","workspace_id":"44444444-4444-4444-4444-444444444444"}`,
		`{"mode":"muted"} {"mode":"all"}`,
		`not json`,
	} {
		svc := &preferringSidebarProvider{}
		router := sidebarRouter(makeTestValidator(t), svc)

		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, preferenceRequest(
			t, "/api/chat/channels/11111111-1111-1111-1111-111111111111/notification-preference", body))

		if rr.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d, want 400: %s", body, rr.Code, rr.Body.String())
		}
		if svc.args != nil {
			t.Fatalf("%s reached the service with %v", body, svc.args)
		}
	}
}

func TestSidebarHandler_SetNotificationPreferenceRejectsAMalformedTarget(t *testing.T) {
	svc := &preferringSidebarProvider{}
	router := sidebarRouter(makeTestValidator(t), svc)

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, preferenceRequest(
		t, "/api/chat/channels/not-a-uuid/notification-preference", `{"mode":"muted"}`))

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rr.Code, rr.Body.String())
	}
	if svc.args != nil {
		t.Fatalf("the service was reached with %v", svc.args)
	}
}

// The general channel, a conversation the caller cannot see and one that does
// not exist are all the same non-enumerating 404 — and the body must not say
// which of the three it was.
func TestSidebarHandler_SetNotificationPreferenceRefusalIsNonEnumerating(t *testing.T) {
	for _, path := range []string{
		"/api/chat/channels/11111111-1111-1111-1111-111111111111/notification-preference",
		"/api/chat/dm/22222222-2222-2222-2222-222222222222/notification-preference",
	} {
		svc := &preferringSidebarProvider{err: domain.ErrNotFound}
		router := sidebarRouter(makeTestValidator(t), svc)

		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, preferenceRequest(t, path, `{"mode":"muted"}`))

		if rr.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404: %s", path, rr.Code, rr.Body.String())
		}
		body := rr.Body.String()
		if strings.Contains(body, "geral") || strings.Contains(body, "general") ||
			strings.Contains(body, "member") {
			t.Fatalf("the refusal names the reason: %s", body)
		}
	}
}

func TestSidebarHandler_SetNotificationPreferenceRequiresAuthentication(t *testing.T) {
	svc := &preferringSidebarProvider{}
	router := sidebarRouter(makeTestValidator(t), svc)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut,
		"/api/chat/channels/11111111-1111-1111-1111-111111111111/notification-preference",
		strings.NewReader(`{"mode":"muted"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
	if svc.args != nil {
		t.Fatalf("the service was reached by an anonymous caller: %v", svc.args)
	}
}

// A build whose sidebar service does not offer the whole preference answers 503
// rather than pretending the write landed. The mute shortcut is a separate
// surface with its own 503, so neither can stand in for the other.
func TestSidebarHandler_SetNotificationPreferenceWithoutAProviderIsUnavailable(t *testing.T) {
	router := sidebarRouter(makeTestValidator(t), &stubSidebarProvider{})

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, preferenceRequest(
		t, "/api/chat/channels/11111111-1111-1111-1111-111111111111/notification-preference",
		`{"mode":"muted"}`))

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rr.Code, rr.Body.String())
	}
}

// Repeating the same request is the same state: the settings page sends a
// complete desired state, so a double click must not become two different
// outcomes.
func TestSidebarHandler_SetNotificationPreferenceIsIdempotent(t *testing.T) {
	svc := &preferringSidebarProvider{}
	router := sidebarRouter(makeTestValidator(t), svc)
	path := "/api/chat/channels/11111111-1111-1111-1111-111111111111/notification-preference"

	for range 2 {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, preferenceRequest(t, path, `{"mode":"mentions_replies"}`))
		if rr.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204: %s", rr.Code, rr.Body.String())
		}
	}
	if len(svc.args) != 4 || svc.args[3] != service.NotificationModeMentionsReplies {
		t.Fatalf("last call = %v, want the same mode both times", svc.args)
	}
}

// The rollout gate on the wire (issue #136).
//
// The refusal is the service's, so what these prove is the contract a client
// sees: which status and code, that the mute shortcut is untouched, and that the
// capability the sidebar publishes is the same answer the write path gives.

// gatedSidebarProvider refuses the granular mode the way the real service does
// when the gate is shut, and reports the capability accordingly.
type gatedSidebarProvider struct {
	stubSidebarProvider
	enabled bool
	args    []string
	mutes   []string
}

func (s *gatedSidebarProvider) ConversationNotificationLevelsEnabled() bool { return s.enabled }

func (s *gatedSidebarProvider) SetConversationNotificationPreference(
	_ context.Context, userID, targetType, targetID, mode string,
) error {
	if mode == service.NotificationModeMentionsReplies && !s.enabled {
		return domain.ErrConversationNotificationLevelsDisabled
	}
	s.args = []string{userID, targetType, targetID, mode}
	return nil
}

func (s *gatedSidebarProvider) MuteConversation(_ context.Context, userID, targetType, targetID string) error {
	s.mutes = []string{userID, targetType, targetID}
	return nil
}

func (s *gatedSidebarProvider) UnmuteConversation(_ context.Context, userID, targetType, targetID string) error {
	s.mutes = []string{userID, targetType, targetID}
	return nil
}

// 503 and a code of its own: the mode is valid and the caller did nothing
// wrong, so this is not a 400 — the deployment has not opened the gate yet. The
// body names neither the flag nor the variable behind it.
func TestSidebarHandler_SetNotificationPreferenceRefusesTheGranularModeWhileGated(t *testing.T) {
	svc := &gatedSidebarProvider{enabled: false}
	router := sidebarRouter(makeTestValidator(t), svc)

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, preferenceRequest(
		t, "/api/chat/channels/11111111-1111-1111-1111-111111111111/notification-preference",
		`{"mode":"mentions_replies"}`))

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "notification_levels_unavailable") {
		t.Fatalf("body does not carry the code a client can act on: %s", body)
	}
	for _, leak := range []string{"CHAT_CONVERSATION", "flag", "env"} {
		if strings.Contains(body, leak) {
			t.Fatalf("the refusal names its own configuration (%q): %s", leak, body)
		}
	}
	if svc.args != nil {
		t.Fatalf("the write reached the service: %v", svc.args)
	}
}

// The two compatible modes keep working while the gate is shut, which is what
// phase one's binary UX is made of.
func TestSidebarHandler_SetNotificationPreferenceAllowsCompatibleModesWhileGated(t *testing.T) {
	for _, mode := range []string{service.NotificationModeAll, service.NotificationModeMuted} {
		svc := &gatedSidebarProvider{enabled: false}
		router := sidebarRouter(makeTestValidator(t), svc)

		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, preferenceRequest(
			t, "/api/chat/channels/11111111-1111-1111-1111-111111111111/notification-preference",
			`{"mode":"`+mode+`"}`))

		if rr.Code != http.StatusNoContent {
			t.Fatalf("%s status = %d, want 204: %s", mode, rr.Code, rr.Body.String())
		}
	}
}

// With the gate open the same request is accepted, so nothing but the
// capability decides it.
func TestSidebarHandler_SetNotificationPreferenceAcceptsTheGranularModeWhenEnabled(t *testing.T) {
	svc := &gatedSidebarProvider{enabled: true}
	router := sidebarRouter(makeTestValidator(t), svc)

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, preferenceRequest(
		t, "/api/chat/channels/11111111-1111-1111-1111-111111111111/notification-preference",
		`{"mode":"mentions_replies"}`))

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rr.Code, rr.Body.String())
	}
	if len(svc.args) != 4 || svc.args[3] != service.NotificationModeMentionsReplies {
		t.Fatalf("service args = %v, want the granular mode", svc.args)
	}
}

// The sidebar's shortcut is not gated: it is the capability this product has
// always had, and phase one depends on it still working.
func TestSidebarHandler_MuteIsNotGated(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		svc := &gatedSidebarProvider{enabled: false}
		router := sidebarRouter(makeTestValidator(t), svc)

		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, authSidebarPinRequest(t, method,
			"/api/chat/channels/11111111-1111-1111-1111-111111111111/mute"))

		if rr.Code != http.StatusNoContent {
			t.Fatalf("%s status = %d, want 204: %s", method, rr.Code, rr.Body.String())
		}
		if len(svc.mutes) != 3 {
			t.Fatalf("%s did not reach the service: %v", method, svc.mutes)
		}
	}
}

// The capability travels in the payload that already hydrates the sidebar and
// the settings page, so the client does not invent it — and a build whose
// service cannot be asked reports the compatible answer.
func TestSidebarHandler_PublishesTheNotificationLevelCapability(t *testing.T) {
	for _, test := range []struct {
		name string
		svc  interface {
			GetSidebar(context.Context, string) (service.SidebarData, error)
		}
		want bool
	}{
		{name: "gate open", svc: &gatedSidebarProvider{enabled: true}, want: true},
		{name: "gate shut", svc: &gatedSidebarProvider{enabled: false}, want: false},
		{name: "service that cannot be asked", svc: &stubSidebarProvider{}, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			router := sidebarRouter(makeTestValidator(t), test.svc)
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, authGet(t))
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
			}
			var body struct {
				Data struct {
					Enabled bool `json:"conversation_notification_levels_enabled"`
				} `json:"data"`
			}
			mustDecode(t, rr, &body)
			if body.Data.Enabled != test.want {
				t.Fatalf("capability = %v, want %v", body.Data.Enabled, test.want)
			}
		})
	}
}
