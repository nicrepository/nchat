package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/chat-service/internal/config"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	httpapi "github.com/nicrepository/nchat/services/chat-service/internal/http"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func sidebarTestConfig() config.Config {
	return config.Config{ServiceName: "chat-service", Env: "test", Port: 8082, ReadHeaderTimeoutSeconds: 5}
}

// ── stub sidebar provider ─────────────────────────────────────────────────────

type stubSidebarProvider struct {
	data service.SidebarData
	err  error
}

func (s *stubSidebarProvider) GetSidebar(_ context.Context, _ string) (service.SidebarData, error) {
	return s.data, s.err
}

// sidebarRouter builds a test router wired with the given validator and stub.
// allowAllSessionValidator accepts all sessions so tests focus on sidebar logic.
func sidebarRouter(v *httpapi.TokenValidator, svc interface {
	GetSidebar(context.Context, string) (service.SidebarData, error)
}) http.Handler {
	return httpapi.NewRouter(sidebarTestConfig(), nil, httpapi.ReadinessState{}, v, allowAllSessionValidator{}, httpapi.NewSidebarHandler(svc), httpapi.NewMessageHandler(nil, nil, nil), nil, nil, nil, nil, nil)
}

type pinningSidebarProvider struct {
	stubSidebarProvider
	pinArgs   []string
	unpinArgs []string
	err       error
}

type readingSidebarProvider struct {
	stubSidebarProvider
	args    []string
	message *string
	err     error
}

func (s *readingSidebarProvider) MarkConversationRead(_ context.Context, userID, targetType, targetID string, lastReadMessageID *string) error {
	s.args = []string{userID, targetType, targetID}
	s.message = lastReadMessageID
	return s.err
}

func (s *pinningSidebarProvider) PinConversation(_ context.Context, userID, targetType, targetID string) error {
	s.pinArgs = []string{userID, targetType, targetID}
	return s.err
}

func (s *pinningSidebarProvider) UnpinConversation(_ context.Context, userID, targetType, targetID string) error {
	s.unpinArgs = []string{userID, targetType, targetID}
	return s.err
}

// authGet returns an authenticated GET request to RouteSidebar.
func authGet(t *testing.T) *http.Request {
	t.Helper()
	tok := makeTestToken(t, testUserID, testHMACSecret, testIssuer, testAudience, time.Hour)
	req := httptest.NewRequest(http.MethodGet, httpapi.RouteSidebar, nil)
	setBearerToken(req, tok)
	return req
}

func authSidebarPinRequest(t *testing.T, method, path string) *http.Request {
	t.Helper()
	tok := makeTestToken(t, testUserID, testHMACSecret, testIssuer, testAudience, time.Hour)
	req := httptest.NewRequest(method, path, nil)
	setBearerToken(req, tok)
	return req
}

// ── SidebarHandler tests ──────────────────────────────────────────────────────

func TestSidebarHandler_NilService_Returns503(t *testing.T) {
	v := makeTestValidator(t)
	router := httpapi.NewRouter(sidebarTestConfig(), nil, httpapi.ReadinessState{}, v, allowAllSessionValidator{}, httpapi.NewSidebarHandler(nil), httpapi.NewMessageHandler(nil, nil, nil), nil, nil, nil, nil, nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authGet(t))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rr.Code)
	}
}

func TestSidebarHandler_Unauthenticated_Returns401(t *testing.T) {
	v := makeTestValidator(t)
	router := sidebarRouter(v, &stubSidebarProvider{})
	req := httptest.NewRequest(http.MethodGet, httpapi.RouteSidebar, nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestSidebarHandler_PinAndUnpinUseAuthenticatedUser(t *testing.T) {
	v := makeTestValidator(t)
	svc := &pinningSidebarProvider{}
	router := sidebarRouter(v, svc)
	channelID := "11111111-1111-1111-1111-111111111111"
	path := "/api/chat/channels/" + channelID + "/sidebar-pin"

	for _, method := range []string{http.MethodPost, http.MethodPost, http.MethodDelete, http.MethodDelete} {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, authSidebarPinRequest(t, method, path))
		if rr.Code != http.StatusNoContent {
			t.Fatalf("%s expected 204, got %d: %s", method, rr.Code, rr.Body.String())
		}
	}
	if got, want := strings.Join(svc.pinArgs, ","), strings.Join([]string{testUserID, service.PinTargetChannel, channelID}, ","); got != want {
		t.Fatalf("pin args = %q, want %q", got, want)
	}
	if got, want := strings.Join(svc.unpinArgs, ","), strings.Join([]string{testUserID, service.PinTargetChannel, channelID}, ","); got != want {
		t.Fatalf("unpin args = %q, want %q", got, want)
	}
}

// The DM pin route has four verbs across two handlers and they differ only in
// the target kind, the path parameter and the pinned flag — the shape a
// copy-paste gets wrong silently. UnpinDM is the one no other test reaches, so
// a body that unpinned a *channel*, or that read {channelID} from a route that
// only carries {conversationID}, would ship without a failing test.
func TestSidebarHandler_UnpinDMTargetsTheConversationAndUnpins(t *testing.T) {
	v := makeTestValidator(t)
	svc := &pinningSidebarProvider{}
	router := sidebarRouter(v, svc)
	conversationID := "22222222-2222-2222-2222-222222222222"
	path := "/api/chat/dm/" + conversationID + "/sidebar-pin"

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authSidebarPinRequest(t, http.MethodDelete, path))

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rr.Code, rr.Body.String())
	}
	if got, want := strings.Join(svc.unpinArgs, ","), strings.Join([]string{testUserID, service.PinTargetDM, conversationID}, ","); got != want {
		t.Fatalf("unpin args = %q, want %q", got, want)
	}
	// Unpinning must not reach the pin path at all: a flipped flag would leave
	// the conversation pinned while answering 204.
	if svc.pinArgs != nil {
		t.Fatalf("DELETE reached PinConversation with %v", svc.pinArgs)
	}
}

// The DM route carries no channelID, so a handler reading the wrong parameter
// would send an empty target through to the service instead of being refused.
func TestSidebarHandler_UnpinDMRejectsAMalformedConversationID(t *testing.T) {
	v := makeTestValidator(t)
	svc := &pinningSidebarProvider{}
	router := sidebarRouter(v, svc)

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authSidebarPinRequest(t, http.MethodDelete, "/api/chat/dm/not-a-uuid/sidebar-pin"))

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
	if svc.unpinArgs != nil {
		t.Fatalf("a malformed id still reached the service: %v", svc.unpinArgs)
	}
}

// Unauthenticated unpin must be refused before the service is consulted.
func TestSidebarHandler_UnpinDMUnauthenticatedDoesNotReachTheService(t *testing.T) {
	v := makeTestValidator(t)
	svc := &pinningSidebarProvider{}
	router := sidebarRouter(v, svc)
	path := "/api/chat/dm/22222222-2222-2222-2222-222222222222/sidebar-pin"

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodDelete, path, nil))

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rr.Code, rr.Body.String())
	}
	if svc.unpinArgs != nil {
		t.Fatalf("an unauthenticated request reached the service: %v", svc.unpinArgs)
	}
}

func TestSidebarHandler_PinInaccessibleConversationReturnsGenericNotFound(t *testing.T) {
	v := makeTestValidator(t)
	svc := &pinningSidebarProvider{err: domain.ErrNotFound}
	router := sidebarRouter(v, svc)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authSidebarPinRequest(t, http.MethodPost, "/api/chat/dm/11111111-1111-1111-1111-111111111111/sidebar-pin"))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected non-enumerating 404, got %d: %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(strings.ToLower(rr.Body.String()), "private") {
		t.Fatalf("response leaks access detail: %s", rr.Body.String())
	}
}

func TestSidebarHandler_ValidAuth_ReturnsSidebar(t *testing.T) {
	v := makeTestValidator(t)
	svc := &stubSidebarProvider{data: service.SidebarData{
		Workspace: domain.Workspace{ID: "ws-1", Name: "NIC Labs", Slug: "default", Status: domain.WorkspaceStatusActive},
		Channels: []service.SidebarChannel{
			{Channel: domain.Channel{ID: "ch-1", Slug: "geral", DisplayName: "geral", Type: domain.ChannelTypePublic, IsGeneral: true, Status: domain.ChannelStatusActive}, CanWrite: true, UnreadCount: 3},
			{Channel: domain.Channel{ID: "ch-2", Slug: "arquivo", DisplayName: "arquivo", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusArchived}, CanWrite: false},
		},
		DMs: []domain.DMConversationWithParticipantIDs{
			{
				DMConversation: domain.DMConversation{ID: "dm-1", Type: domain.DMConversationTypeDirect},
				ParticipantIDs: []string{testUserID, "other-user"},
				UnreadCount:    2,
			},
		},
	}}
	router := sidebarRouter(v, svc)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authGet(t))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rr.Code, rr.Body.String())
	}
	var envelope struct {
		Data struct {
			Workspace struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"workspace"`
			Channels []struct {
				ID          string `json:"id"`
				Slug        string `json:"slug"`
				IsGeneral   bool   `json:"is_general"`
				CanWrite    bool   `json:"can_write"`
				UnreadCount int    `json:"unread_count"`
			} `json:"channels"`
			DMs []struct {
				ID          string `json:"id"`
				Type        string `json:"type"`
				Name        string `json:"name"`
				UnreadCount int    `json:"unread_count"`
			} `json:"dm_conversations"`
		} `json:"data"`
	}
	mustDecode(t, rr, &envelope)
	if envelope.Data.Workspace.ID != "ws-1" {
		t.Fatalf("expected workspace ws-1, got %q", envelope.Data.Workspace.ID)
	}
	if len(envelope.Data.Channels) != 2 {
		t.Fatalf("expected 2 channels, got %d", len(envelope.Data.Channels))
	}
	if !envelope.Data.Channels[0].IsGeneral {
		t.Fatal("expected is_general=true for geral channel")
	}
	if !envelope.Data.Channels[0].CanWrite {
		t.Fatal("expected server-derived can_write=true")
	}
	if envelope.Data.Channels[0].UnreadCount != 3 || envelope.Data.DMs[0].UnreadCount != 2 {
		t.Fatalf("unexpected unread counts: %+v", envelope.Data)
	}
	if envelope.Data.Channels[1].CanWrite {
		t.Fatal("expected server-derived can_write=false")
	}
	if len(envelope.Data.DMs) != 1 {
		t.Fatalf("expected 1 DM, got %d", len(envelope.Data.DMs))
	}
	if envelope.Data.DMs[0].Type != "direct" {
		t.Fatalf("expected DM type 'direct', got %q", envelope.Data.DMs[0].Type)
	}
}

func TestSidebarHandler_MarkReadUsesAuthenticatedUserAndOptionalMessageID(t *testing.T) {
	v := makeTestValidator(t)
	svc := &readingSidebarProvider{}
	router := sidebarRouter(v, svc)
	channelID := "11111111-1111-4111-8111-111111111111"
	messageID := "22222222-2222-4222-8222-222222222222"
	req := authSidebarPinRequest(t, http.MethodPost, "/api/chat/channels/"+channelID+"/read")
	req.Body = http.NoBody
	req.ContentLength = 0
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("empty body expected 204, got %d: %s", rr.Code, rr.Body.String())
	}

	req = authSidebarPinRequest(t, http.MethodPost, "/api/chat/dm/33333333-3333-4333-8333-333333333333/read")
	req.Body = io.NopCloser(strings.NewReader(`{"last_read_message_id":"` + messageID + `"}`))
	req.ContentLength = int64(len(`{"last_read_message_id":"` + messageID + `"}`))
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("body expected 204, got %d: %s", rr.Code, rr.Body.String())
	}
	if got, want := strings.Join(svc.args, ","), strings.Join([]string{testUserID, service.ReadTargetDM, "33333333-3333-4333-8333-333333333333"}, ","); got != want {
		t.Fatalf("read args = %q, want %q", got, want)
	}
	if svc.message == nil || *svc.message != messageID {
		t.Fatalf("message = %v, want %q", svc.message, messageID)
	}
}

func TestSidebarHandler_MarkReadRejectsUnauthorizedInvalidAndInaccessibleRequests(t *testing.T) {
	v := makeTestValidator(t)
	channelID := "11111111-1111-4111-8111-111111111111"
	path := "/api/chat/channels/" + channelID + "/read"

	t.Run("unauthenticated", func(t *testing.T) {
		rr := httptest.NewRecorder()
		sidebarRouter(v, &readingSidebarProvider{}).ServeHTTP(rr, httptest.NewRequest(http.MethodPost, path, nil))
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d: %s", rr.Code, rr.Body.String())
		}
	})

	t.Run("strict body", func(t *testing.T) {
		req := authSidebarPinRequest(t, http.MethodPost, path)
		body := `{"unexpected":true}`
		req.Body = io.NopCloser(strings.NewReader(body))
		req.ContentLength = int64(len(body))
		rr := httptest.NewRecorder()
		sidebarRouter(v, &readingSidebarProvider{}).ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
		}
	})

	t.Run("inaccessible target", func(t *testing.T) {
		rr := httptest.NewRecorder()
		sidebarRouter(v, &readingSidebarProvider{err: domain.ErrNotFound}).ServeHTTP(rr, authSidebarPinRequest(t, http.MethodPost, path))
		if rr.Code != http.StatusNotFound || strings.Contains(strings.ToLower(rr.Body.String()), "access") {
			t.Fatalf("expected non-enumerating 404, got %d: %s", rr.Code, rr.Body.String())
		}
	})
}

// ISSUE #414 — the sidebar publishes two ordering keys per item, and nothing
// else about the message it points at.
// last_read_message_id is caller-supplied and ends up in a read-state write, so
// it is validated like any other id rather than forwarded on trust.
func TestSidebarHandler_MarkReadRejectsAMalformedLastReadMessageID(t *testing.T) {
	v := makeTestValidator(t)
	svc := &readingSidebarProvider{}
	channelID := "11111111-1111-4111-8111-111111111111"
	req := authSidebarPinRequest(t, http.MethodPost, "/api/chat/channels/"+channelID+"/read")
	body := `{"last_read_message_id":"not-a-uuid"}`
	req.Body = io.NopCloser(strings.NewReader(body))
	req.ContentLength = int64(len(body))

	rr := httptest.NewRecorder()
	sidebarRouter(v, svc).ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "last_read_message_id") {
		t.Fatalf("error does not name the offending field: %s", rr.Body.String())
	}
	if svc.args != nil {
		t.Fatalf("a malformed message id reached the service: %v", svc.args)
	}
}

func TestSidebarHandler_MarkReadRejectsAMalformedTargetID(t *testing.T) {
	v := makeTestValidator(t)
	svc := &readingSidebarProvider{}

	rr := httptest.NewRecorder()
	sidebarRouter(v, svc).ServeHTTP(rr, authSidebarPinRequest(t, http.MethodPost, "/api/chat/dm/not-a-uuid/read"))

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
	if svc.args != nil {
		t.Fatalf("a malformed target id reached the service: %v", svc.args)
	}
}

// The read and pin routes reach the sidebar service through an optional
// interface. A deployment wiring only the plain sidebar provider must answer
// "unavailable" rather than dereference an interface it never received.
func TestSidebarHandler_ReadAndPinRoutesWithoutTheirProviderReturn503(t *testing.T) {
	v := makeTestValidator(t)
	channelID := "11111111-1111-4111-8111-111111111111"
	for _, test := range []struct {
		name   string
		method string
		path   string
	}{
		{name: "mark channel read", method: http.MethodPost, path: "/api/chat/channels/" + channelID + "/read"},
		{name: "mark dm read", method: http.MethodPost, path: "/api/chat/dm/" + channelID + "/read"},
		{name: "pin channel", method: http.MethodPost, path: "/api/chat/channels/" + channelID + "/sidebar-pin"},
		{name: "unpin dm", method: http.MethodDelete, path: "/api/chat/dm/" + channelID + "/sidebar-pin"},
	} {
		t.Run(test.name, func(t *testing.T) {
			// stubSidebarProvider serves GetSidebar only — it implements
			// neither the read nor the pin interface.
			rr := httptest.NewRecorder()
			sidebarRouter(v, &stubSidebarProvider{}).ServeHTTP(rr, authSidebarPinRequest(t, test.method, test.path))

			if rr.Code != http.StatusServiceUnavailable {
				t.Fatalf("expected 503, got %d: %s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestSidebarHandler_SerializesActivityTimestamps(t *testing.T) {
	v := makeTestValidator(t)
	// Microseconds, because chat.messages.created_at is a TIMESTAMPTZ and holds
	// them: the sidebar orders by this value, so a fraction dropped on the wire
	// is an ordering decision silently handed to the name/id tie-breakers.
	created := time.Date(2026, 8, 4, 12, 0, 0, 900123000, time.UTC)
	// Deliberately in a non-UTC zone: the wire format must not depend on where
	// the server happens to be, and converting must not cost the fraction.
	lastMessage := time.Date(2026, 8, 4, 9, 0, 0, 900123000, time.FixedZone("BRT", -3*60*60))
	svc := &stubSidebarProvider{data: service.SidebarData{
		Workspace: domain.Workspace{ID: "ws-1", Name: "NIC Labs", Slug: "default", Status: domain.WorkspaceStatusActive},
		Channels: []service.SidebarChannel{
			{
				Channel:       domain.Channel{ID: "ch-1", Slug: "geral", DisplayName: "geral", Type: domain.ChannelTypePublic, CreatedAt: created},
				CanWrite:      true,
				LastMessageAt: &lastMessage,
			},
			{
				Channel:  domain.Channel{ID: "ch-2", Slug: "vazio", DisplayName: "vazio", Type: domain.ChannelTypePublic, CreatedAt: created},
				CanWrite: true,
			},
		},
		DMs: []domain.DMConversationWithParticipantIDs{
			{
				DMConversation: domain.DMConversation{ID: "dm-1", Type: domain.DMConversationTypeDirect, CreatedAt: created},
				LastMessageAt:  &lastMessage,
			},
			{
				DMConversation: domain.DMConversation{ID: "dm-2", Type: domain.DMConversationTypeGroup, Title: "Equipe", CreatedAt: created},
			},
		},
	}}
	router := sidebarRouter(v, svc)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authGet(t))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rr.Code, rr.Body.String())
	}
	// Captured before decoding, which drains the recorder's buffer.
	raw := rr.Body.String()
	type activityJSON struct {
		ID            string  `json:"id"`
		CreatedAt     string  `json:"created_at"`
		LastMessageAt *string `json:"last_message_at"`
	}
	var envelope struct {
		Data struct {
			Channels []activityJSON `json:"channels"`
			DMs      []activityJSON `json:"dm_conversations"`
		} `json:"data"`
	}
	mustDecode(t, rr, &envelope)

	items := append(append([]activityJSON{}, envelope.Data.Channels...), envelope.Data.DMs...)
	if len(items) != 4 {
		t.Fatalf("expected 4 sidebar items, got %d", len(items))
	}
	for _, item := range items {
		if item.CreatedAt != "2026-08-04T12:00:00.900123Z" {
			t.Fatalf("%s: expected an RFC 3339 UTC created_at keeping its fraction, got %q", item.ID, item.CreatedAt)
		}
	}
	for _, item := range []activityJSON{items[0], items[2]} {
		if item.LastMessageAt == nil {
			t.Fatalf("%s: expected a last_message_at", item.ID)
		}
		// 09:00.900123-03:00 rendered in UTC, so a client parses one shape and
		// the microseconds survive the conversion.
		if *item.LastMessageAt != "2026-08-04T12:00:00.900123Z" {
			t.Fatalf("%s: expected an RFC 3339 UTC last_message_at keeping its fraction, got %q", item.ID, *item.LastMessageAt)
		}
	}
	for _, item := range []activityJSON{items[1], items[3]} {
		if item.LastMessageAt != nil {
			t.Fatalf("%s: expected a null last_message_at, got %q", item.ID, *item.LastMessageAt)
		}
	}
	// The key must be present-and-null rather than omitted: a client has to be
	// able to tell "no activity" from "this server does not report activity".
	if !strings.Contains(raw, `"last_message_at":null`) {
		t.Fatalf("expected an explicit null last_message_at in the payload: %s", raw)
	}
}

// Two messages written inside the same second are two different instants that
// the query already distinguishes, and the payload has to keep saying so. The
// whole-second cases are here too: RFC3339Nano drops trailing zeros, so the
// same instant is written several ways and none of them may lose information.
func TestSidebarHandler_ActivityFractionSurvivesSerialization(t *testing.T) {
	v := makeTestValidator(t)
	for _, test := range []struct {
		name  string
		value time.Time
		want  string
	}{
		{name: "microseconds", value: time.Date(2026, 8, 4, 12, 0, 0, 900123000, time.UTC), want: "2026-08-04T12:00:00.900123Z"},
		{name: "same second, earlier", value: time.Date(2026, 8, 4, 12, 0, 0, 100456000, time.UTC), want: "2026-08-04T12:00:00.100456Z"},
		{name: "sub millisecond apart", value: time.Date(2026, 8, 4, 12, 0, 0, 900045000, time.UTC), want: "2026-08-04T12:00:00.900045Z"},
		{name: "nanoseconds", value: time.Date(2026, 8, 4, 12, 0, 0, 100123456, time.UTC), want: "2026-08-04T12:00:00.100123456Z"},
		// Trailing zeros are dropped; .900000000 and .9 are the same instant.
		{name: "trailing zeros dropped", value: time.Date(2026, 8, 4, 12, 0, 0, 900000000, time.UTC), want: "2026-08-04T12:00:00.9Z"},
		{name: "whole second carries no fraction", value: time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC), want: "2026-08-04T12:00:00Z"},
		{name: "offset normalized to UTC", value: time.Date(2026, 8, 4, 9, 0, 0, 900123000, time.FixedZone("BRT", -3*60*60)), want: "2026-08-04T12:00:00.900123Z"},
	} {
		t.Run(test.name, func(t *testing.T) {
			svc := &stubSidebarProvider{data: service.SidebarData{
				Workspace: domain.Workspace{ID: "ws-1", Name: "NIC Labs", Slug: "default", Status: domain.WorkspaceStatusActive},
				Channels: []service.SidebarChannel{
					{
						Channel:       domain.Channel{ID: "ch-1", Slug: "geral", DisplayName: "geral", Type: domain.ChannelTypePublic, CreatedAt: test.value},
						CanWrite:      true,
						LastMessageAt: &test.value,
					},
				},
				DMs: []domain.DMConversationWithParticipantIDs{
					{
						DMConversation: domain.DMConversation{ID: "dm-1", Type: domain.DMConversationTypeDirect, CreatedAt: test.value},
						LastMessageAt:  &test.value,
					},
				},
			}}
			rr := httptest.NewRecorder()
			sidebarRouter(v, svc).ServeHTTP(rr, authGet(t))
			if rr.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d", rr.Code)
			}
			var envelope struct {
				Data struct {
					Channels []struct {
						CreatedAt     string  `json:"created_at"`
						LastMessageAt *string `json:"last_message_at"`
					} `json:"channels"`
					DMs []struct {
						CreatedAt     string  `json:"created_at"`
						LastMessageAt *string `json:"last_message_at"`
					} `json:"dm_conversations"`
				} `json:"data"`
			}
			mustDecode(t, rr, &envelope)

			channel, dm := envelope.Data.Channels[0], envelope.Data.DMs[0]
			for field, got := range map[string]string{
				"channel created_at": channel.CreatedAt,
				"dm created_at":      dm.CreatedAt,
			} {
				if got != test.want {
					t.Fatalf("%s: expected %q, got %q", field, test.want, got)
				}
			}
			for field, got := range map[string]*string{
				"channel last_message_at": channel.LastMessageAt,
				"dm last_message_at":      dm.LastMessageAt,
			} {
				if got == nil {
					t.Fatalf("%s: expected a value", field)
				}
				if *got != test.want {
					t.Fatalf("%s: expected %q, got %q", field, test.want, *got)
				}
			}
		})
	}
}

// The activity metadata must not become a message preview by the back door.
func TestSidebarHandler_CarriesNoMessageContent(t *testing.T) {
	v := makeTestValidator(t)
	lastMessage := time.Date(2026, 7, 30, 18, 0, 0, 0, time.UTC)
	svc := &stubSidebarProvider{data: service.SidebarData{
		Workspace: domain.Workspace{ID: "ws-1", Name: "NIC Labs", Slug: "default", Status: domain.WorkspaceStatusActive},
		Channels: []service.SidebarChannel{
			{Channel: domain.Channel{ID: "ch-1", Slug: "geral", DisplayName: "geral", Type: domain.ChannelTypePublic}, CanWrite: true, LastMessageAt: &lastMessage},
		},
		DMs: []domain.DMConversationWithParticipantIDs{
			{DMConversation: domain.DMConversation{ID: "dm-1", Type: domain.DMConversationTypeDirect}, LastMessageAt: &lastMessage},
		},
	}}
	router := sidebarRouter(v, svc)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authGet(t))

	body := rr.Body.String()
	for _, forbidden := range []string{
		"body_text", "body_format", "sender_id", "sender_display_name", "message_id",
		"last_message", "preview", "kind", "is_removed",
	} {
		if strings.Contains(body, `"`+forbidden+`"`) {
			t.Fatalf("sidebar payload must not carry %q: %s", forbidden, body)
		}
	}
}

func TestSidebarHandler_EmptyData_ReturnsEmptyArrays(t *testing.T) {
	v := makeTestValidator(t)
	svc := &stubSidebarProvider{data: service.SidebarData{
		Workspace: domain.Workspace{ID: "ws-1", Name: "NIC Labs", Slug: "default", Status: domain.WorkspaceStatusActive},
	}}
	router := sidebarRouter(v, svc)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authGet(t))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var envelope struct {
		Data struct {
			Channels json.RawMessage `json:"channels"`
			DMs      json.RawMessage `json:"dm_conversations"`
		} `json:"data"`
	}
	mustDecode(t, rr, &envelope)
	if string(envelope.Data.Channels) != "[]" {
		t.Fatalf("expected empty channels array [], got %s", envelope.Data.Channels)
	}
	if string(envelope.Data.DMs) != "[]" {
		t.Fatalf("expected empty DMs array [], got %s", envelope.Data.DMs)
	}
}

func TestSidebarHandler_Forbidden_Returns403(t *testing.T) {
	v := makeTestValidator(t)
	router := sidebarRouter(v, &stubSidebarProvider{err: domain.ErrForbidden})
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authGet(t))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rr.Code)
	}
}

func TestSidebarHandler_PostMethod_Returns405(t *testing.T) {
	v := makeTestValidator(t)
	router := sidebarRouter(v, &stubSidebarProvider{})

	tok := makeTestToken(t, testUserID, testHMACSecret, testIssuer, testAudience, time.Hour)
	req := httptest.NewRequest(http.MethodPost, httpapi.RouteSidebar, nil)
	setBearerToken(req, tok)

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rr.Code)
	}
}

// decodeDMNames returns the dm_conversations names from a sidebar response.
func decodeDMNames(t *testing.T, rr *httptest.ResponseRecorder) []string {
	t.Helper()
	var envelope struct {
		Data struct {
			DMs []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"dm_conversations"`
		} `json:"data"`
	}
	mustDecode(t, rr, &envelope)
	names := make([]string, 0, len(envelope.Data.DMs))
	for _, dm := range envelope.Data.DMs {
		names = append(names, dm.Name)
	}
	return names
}

// directDM builds a direct conversation whose counterpart was already resolved
// for the requesting user by the storage layer. A resolved name always comes
// with a resolved user ID, mirroring what the single sidebar query produces.
func directDM(id, counterpart string, participantIDs ...string) domain.DMConversationWithParticipantIDs {
	dm := domain.DMConversationWithParticipantIDs{
		DMConversation:         domain.DMConversation{ID: id, Type: domain.DMConversationTypeDirect},
		ParticipantIDs:         participantIDs,
		CounterpartDisplayName: counterpart,
	}
	if counterpart != "" {
		dm.CounterpartUserID = "counterpart-of-" + id
	}
	return dm
}

// dmJSON mirrors the wire shape of one sidebar DM, including the optional
// counterpart object, so tests can assert on absence as well as on values.
type dmJSON struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Name        string `json:"name"`
	Counterpart *struct {
		UserID      string `json:"user_id"`
		DisplayName string `json:"display_name"`
		AvatarURL   string `json:"avatar_url"`
	} `json:"counterpart"`
}

func decodeDMs(t *testing.T, rr *httptest.ResponseRecorder) []dmJSON {
	t.Helper()
	var envelope struct {
		Data struct {
			DMs []dmJSON `json:"dm_conversations"`
		} `json:"data"`
	}
	mustDecode(t, rr, &envelope)
	return envelope.Data.DMs
}

func sidebarWithDMs(dms ...domain.DMConversationWithParticipantIDs) service.SidebarData {
	return service.SidebarData{
		Workspace: domain.Workspace{ID: "ws-1", Name: "NIC Labs", Slug: "default", Status: domain.WorkspaceStatusActive},
		DMs:       dms,
	}
}

func TestSidebarHandler_DM_DirectName_UsesOtherParticipant(t *testing.T) {
	v := makeTestValidator(t)
	svc := &stubSidebarProvider{data: sidebarWithDMs(
		directDM("dm-1", "Juliane Lino", testUserID, "other-user-id"),
	)}
	router := sidebarRouter(v, svc)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authGet(t))

	names := decodeDMNames(t, rr)
	if len(names) != 1 {
		t.Fatalf("expected 1 DM, got %d", len(names))
	}
	if names[0] != "Juliane Lino" {
		t.Fatalf("expected other participant name, got %q", names[0])
	}
}

// TestSidebarHandler_DM_DirectName_IsViewerScoped asserts the same conversation
// renders as B for A and as A for B: the name is never persisted per-conversation.
func TestSidebarHandler_DM_DirectName_IsViewerScoped(t *testing.T) {
	const conversationID = "dm-shared"
	v := makeTestValidator(t)

	for _, test := range []struct {
		viewer      string
		counterpart string
	}{
		{viewer: "user-a", counterpart: "User B"},
		{viewer: "user-b", counterpart: "User A"},
	} {
		t.Run(test.viewer, func(t *testing.T) {
			svc := &stubSidebarProvider{data: sidebarWithDMs(
				directDM(conversationID, test.counterpart, "user-a", "user-b"),
			)}
			router := sidebarRouter(v, svc)
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, httpapi.RouteSidebar, nil)
			setBearerToken(req, makeTestToken(t, test.viewer, testHMACSecret, testIssuer, testAudience, time.Hour))
			router.ServeHTTP(rr, req)

			names := decodeDMNames(t, rr)
			if len(names) != 1 || names[0] != test.counterpart {
				t.Fatalf("viewer %s: expected %q, got %v", test.viewer, test.counterpart, names)
			}
			if names[0] == test.viewer {
				t.Fatalf("viewer %s must never be the title of their own DM", test.viewer)
			}
		})
	}
}

// TestSidebarHandler_DM_DistinctConversations_KeepDistinctNames guards against a
// single resolved name bleeding across conversations.
func TestSidebarHandler_DM_DistinctConversations_KeepDistinctNames(t *testing.T) {
	v := makeTestValidator(t)
	svc := &stubSidebarProvider{data: sidebarWithDMs(
		directDM("dm-1", "Juliane Lino", testUserID, "u2"),
		directDM("dm-2", "Caio Almeida", testUserID, "u3"),
	)}
	router := sidebarRouter(v, svc)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authGet(t))

	names := decodeDMNames(t, rr)
	if len(names) != 2 || names[0] != "Juliane Lino" || names[1] != "Caio Almeida" {
		t.Fatalf("expected distinct per-conversation names, got %v", names)
	}
}

// TestSidebarHandler_DM_DirectName_FallsBackWhenUnresolvable covers removed
// participants, missing user rows and blank display names.
func TestSidebarHandler_DM_DirectName_FallsBackWhenUnresolvable(t *testing.T) {
	v := makeTestValidator(t)
	for _, test := range []struct {
		name        string
		counterpart string
		title       string
		want        string
	}{
		{name: "unresolved counterpart", counterpart: "", want: "Mensagem Direta"},
		{name: "blank display name", counterpart: "   ", want: "Mensagem Direta"},
		{name: "legacy explicit title", counterpart: "", title: "Suporte", want: "Suporte"},
		{name: "counterpart wins over title", counterpart: "Juliane", title: "Suporte", want: "Juliane"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dm := directDM("dm-1", test.counterpart, testUserID, "other-user-id")
			dm.Title = test.title
			svc := &stubSidebarProvider{data: sidebarWithDMs(dm)}
			router := sidebarRouter(v, svc)
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, authGet(t))

			names := decodeDMNames(t, rr)
			if len(names) != 1 || names[0] != test.want {
				t.Fatalf("expected %q, got %v", test.want, names)
			}
			if names[0] == "other-user-id" {
				t.Fatal("direct DM name must not leak participant user ID")
			}
		})
	}
}

func TestSidebarHandler_DM_GroupName_UsesTitle(t *testing.T) {
	v := makeTestValidator(t)
	svc := &stubSidebarProvider{data: service.SidebarData{
		Workspace: domain.Workspace{ID: "ws-1", Name: "NIC Labs", Slug: "default", Status: domain.WorkspaceStatusActive},
		DMs: []domain.DMConversationWithParticipantIDs{
			{
				DMConversation: domain.DMConversation{
					ID:    "dm-grp",
					Type:  domain.DMConversationTypeGroup,
					Title: "Equipe Infra",
				},
				ParticipantIDs: []string{testUserID, "u2", "u3"},
			},
		},
	}}
	router := sidebarRouter(v, svc)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authGet(t))

	var envelope struct {
		Data struct {
			DMs []struct {
				Name string `json:"name"`
				Type string `json:"type"`
			} `json:"dm_conversations"`
		} `json:"data"`
	}
	mustDecode(t, rr, &envelope)
	if len(envelope.Data.DMs) == 0 {
		t.Fatal("expected DM in response")
	}
	if envelope.Data.DMs[0].Name != "Equipe Infra" {
		t.Fatalf("expected 'Equipe Infra', got %q", envelope.Data.DMs[0].Name)
	}
	if envelope.Data.DMs[0].Type != "group" {
		t.Fatalf("expected type 'group', got %q", envelope.Data.DMs[0].Type)
	}
}

// TestSidebarHandler_InternalError_Returns500WithoutDetails verifies that
// internal error details are not exposed in the response body.
func TestSidebarHandler_InternalError_Returns500WithoutDetails(t *testing.T) {
	v := makeTestValidator(t)
	secretMsg := "db connection failed: err_code=ERR_CONN_REFUSED"
	svc := &stubSidebarProvider{err: errors.New(secretMsg)}

	router := sidebarRouter(v, svc)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authGet(t))

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rr.Code)
	}
	var envelope httputil.Envelope
	mustDecode(t, rr, &envelope)
	if envelope.Error == nil {
		t.Fatal("expected error object in response")
	}
	if envelope.Error.Message == secretMsg {
		t.Fatal("internal error details must not leak to client")
	}
}

// TestSidebarHandler_PrivateChannel_HasCorrectType verifies private channel type is mapped.
func TestSidebarHandler_PrivateChannel_HasCorrectType(t *testing.T) {
	v := makeTestValidator(t)
	svc := &stubSidebarProvider{data: service.SidebarData{
		Workspace: domain.Workspace{ID: "ws-1", Name: "NIC Labs", Slug: "default", Status: domain.WorkspaceStatusActive},
		Channels: []service.SidebarChannel{
			{Channel: domain.Channel{ID: "ch-priv", Slug: "secreto", DisplayName: "secreto", Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive}, CanWrite: true},
		},
	}}
	router := sidebarRouter(v, svc)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authGet(t))

	var envelope struct {
		Data struct {
			Channels []struct {
				Type string `json:"type"`
			} `json:"channels"`
		} `json:"data"`
	}
	mustDecode(t, rr, &envelope)
	if len(envelope.Data.Channels) != 1 {
		t.Fatalf("expected 1 channel, got %d", len(envelope.Data.Channels))
	}
	if envelope.Data.Channels[0].Type != "private" {
		t.Fatalf("expected type 'private', got %q", envelope.Data.Channels[0].Type)
	}
}

// TestSidebarHandler_ResponseContract_NoSensitiveFields asserts that the
// sidebar response never contains participant_ids or title in DM objects.
func TestSidebarHandler_ResponseContract_NoSensitiveFields(t *testing.T) {
	v := makeTestValidator(t)
	svc := &stubSidebarProvider{data: service.SidebarData{
		Workspace: domain.Workspace{ID: "ws-1", Name: "NIC Labs", Slug: "default", Status: domain.WorkspaceStatusActive},
		DMs: []domain.DMConversationWithParticipantIDs{
			{
				DMConversation: domain.DMConversation{
					ID:    "dm-grp",
					Type:  domain.DMConversationTypeGroup,
					Title: "Equipe Infra",
				},
				ParticipantIDs: []string{testUserID, "u2", "u3"},
			},
			{
				DMConversation:         domain.DMConversation{ID: "dm-direct", Type: domain.DMConversationTypeDirect},
				ParticipantIDs:         []string{testUserID, "other-user"},
				CounterpartUserID:      "other-user",
				CounterpartDisplayName: "Juliane Lino",
				CounterpartAvatarURL:   "/media/avatars/juliane.png",
			},
		},
	}}
	router := sidebarRouter(v, svc)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authGet(t))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body := rr.Body.String()
	if strings.Contains(body, "participant_ids") {
		t.Fatalf("response must not contain participant_ids; body: %s", body)
	}
	if strings.Contains(body, `"title"`) {
		t.Fatalf("response must not expose title field; body: %s", body)
	}

	if strings.Contains(body, "@") {
		t.Fatalf("response must not contain an e-mail address; body: %s", body)
	}

	// Exact key set: exposing the counterpart must not widen the contract with
	// e-mails, status, auth source or any other personal field. Group DMs must
	// not grow a counterpart at all.
	var raw struct {
		Data struct {
			DMs []map[string]json.RawMessage `json:"dm_conversations"`
		} `json:"data"`
	}
	mustDecode(t, rr, &raw)
	for _, dm := range raw.Data.DMs {
		// created_at and last_message_at (issue #414) are the two ordering keys;
		// pinned_at is the caller's private ordering preference and muted
		// (issue #527) is their private notification preference. All of them say
		// when or whether, never what, who or which message.
		for _, key := range []string{"id", "type", "name", "created_at", "last_message_at", "pinned_at", "unread_count", "muted"} {
			if _, ok := dm[key]; !ok {
				t.Fatalf("missing expected DM field %q in %v", key, dm)
			}
		}
		counterpart, hasCounterpart := dm["counterpart"]
		wantFields := 8
		if hasCounterpart {
			wantFields = 9
		}
		if len(dm) != wantFields {
			t.Fatalf("expected exactly %d DM fields, got %d: %v", wantFields, len(dm), dm)
		}
		if string(dm["type"]) == `"group"` && hasCounterpart {
			t.Fatalf("group DM must not carry a counterpart: %v", dm)
		}
		if !hasCounterpart {
			continue
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(counterpart, &fields); err != nil {
			t.Fatalf("decode counterpart: %v", err)
		}
		allowed := map[string]bool{"user_id": true, "display_name": true, "avatar_url": true}
		for key := range fields {
			if !allowed[key] {
				t.Fatalf("counterpart must not expose field %q: %v", key, fields)
			}
		}
	}
}

// TestSidebarHandler_DM_Counterpart_IsViewerScoped is the wire-level evidence
// for requirements 1-3: each viewer receives the other participant's identity,
// name first (already resolved from full_name) and avatar alongside it.
func TestSidebarHandler_DM_Counterpart_IsViewerScoped(t *testing.T) {
	v := makeTestValidator(t)
	for _, test := range []struct {
		viewer      string
		userID      string
		displayName string
		avatarURL   string
	}{
		{viewer: "user-a", userID: "user-b", displayName: "Bruno Lima", avatarURL: "/media/avatars/bruno.png"},
		{viewer: "user-b", userID: "user-a", displayName: "Ana Carolina Souza", avatarURL: ""},
	} {
		t.Run(test.viewer, func(t *testing.T) {
			dm := directDM("dm-shared", test.displayName, "user-a", "user-b")
			dm.CounterpartUserID = test.userID
			dm.CounterpartAvatarURL = test.avatarURL
			svc := &stubSidebarProvider{data: sidebarWithDMs(dm)}
			router := sidebarRouter(v, svc)
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, httpapi.RouteSidebar, nil)
			setBearerToken(req, makeTestToken(t, test.viewer, testHMACSecret, testIssuer, testAudience, time.Hour))
			router.ServeHTTP(rr, req)

			dms := decodeDMs(t, rr)
			if len(dms) != 1 || dms[0].Counterpart == nil {
				t.Fatalf("expected one DM carrying a counterpart, got %+v", dms)
			}
			got := dms[0].Counterpart
			if got.UserID != test.userID || got.DisplayName != test.displayName {
				t.Fatalf("expected counterpart %s/%s, got %+v", test.userID, test.displayName, got)
			}
			if got.AvatarURL != test.avatarURL {
				t.Fatalf("expected avatar %q, got %q", test.avatarURL, got.AvatarURL)
			}
			// `name` stays the resolved text for pre-counterpart clients.
			if dms[0].Name != test.displayName {
				t.Fatalf("name must stay resolved, got %q", dms[0].Name)
			}
		})
	}
}

// TestSidebarHandler_DM_Counterpart_OmittedWhenUnresolvable asserts the client
// never has to tell a missing counterpart from a fabricated one.
func TestSidebarHandler_DM_Counterpart_OmittedWhenUnresolvable(t *testing.T) {
	v := makeTestValidator(t)
	svc := &stubSidebarProvider{data: sidebarWithDMs(
		directDM("dm-orphan", "", testUserID),
	)}
	router := sidebarRouter(v, svc)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authGet(t))

	dms := decodeDMs(t, rr)
	if len(dms) != 1 {
		t.Fatalf("expected 1 DM, got %d", len(dms))
	}
	if dms[0].Counterpart != nil {
		t.Fatalf("unresolvable counterpart must be omitted, got %+v", dms[0].Counterpart)
	}
	if dms[0].Name != "Mensagem Direta" {
		t.Fatalf("expected generic fallback name, got %q", dms[0].Name)
	}
}

// TestSidebarHandler_DM_Counterpart_NameFallsBackWithoutAvatar covers a
// resolvable user whose stored names are blank: the counterpart is still
// exposed (the avatar needs its ID), but the name is the server-side fallback.
func TestSidebarHandler_DM_Counterpart_NameFallsBackWithoutAvatar(t *testing.T) {
	v := makeTestValidator(t)
	dm := directDM("dm-blank", "   ", testUserID, "other-user-id")
	dm.CounterpartUserID = "other-user-id"
	svc := &stubSidebarProvider{data: sidebarWithDMs(dm)}
	router := sidebarRouter(v, svc)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, authGet(t))

	dms := decodeDMs(t, rr)
	if len(dms) != 1 || dms[0].Counterpart == nil {
		t.Fatalf("expected one DM carrying a counterpart, got %+v", dms)
	}
	if dms[0].Counterpart.DisplayName != "Mensagem Direta" || dms[0].Name != "Mensagem Direta" {
		t.Fatalf("counterpart name and DM name must agree on the fallback, got %+v", dms[0])
	}
	if dms[0].Counterpart.AvatarURL != "" {
		t.Fatalf("expected no avatar, got %q", dms[0].Counterpart.AvatarURL)
	}
}

// mustDecode decodes the recorder body into v, failing the test on error.
func mustDecode(t *testing.T, rr *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.NewDecoder(rr.Body).Decode(v); err != nil {
		t.Fatalf("decode response: %v; body: %s", err, rr.Body.String())
	}
}

// The deprecated can_create_channel flag must survive in the JSON contract so a
// client that predates BUG #393 keeps working during rollout. It is emitted
// unconditionally (never omitempty) and is true whenever the sidebar is
// returned, whatever role the caller holds.
func TestSidebarHandler_KeepsDeprecatedCanCreateChannelTrue(t *testing.T) {
	for _, role := range []domain.WorkspaceRole{
		domain.WorkspaceRoleOwner,
		domain.WorkspaceRoleAdmin,
		domain.WorkspaceRoleMember,
		domain.WorkspaceRoleGuest,
	} {
		t.Run(string(role), func(t *testing.T) {
			v := makeTestValidator(t)
			// The service sets the flag by construction; the role only proves the
			// handler does not reintroduce a rule of its own on top of it.
			data := service.SidebarData{
				Workspace:        domain.Workspace{ID: "ws-1", Slug: "default", Status: domain.WorkspaceStatusActive},
				CanCreateChannel: true,
			}
			rr := httptest.NewRecorder()
			sidebarRouter(v, &stubSidebarProvider{data: data}).ServeHTTP(rr, authGet(t))
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rr.Code)
			}
			var body struct {
				Data map[string]json.RawMessage `json:"data"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			raw, ok := body.Data["can_create_channel"]
			if !ok {
				t.Fatalf("can_create_channel absent from payload: %s", rr.Body.String())
			}
			if string(raw) != "true" {
				t.Fatalf("can_create_channel = %s, want true", raw)
			}
		})
	}
}

// RF-32 (issue #458): the sidebar publishes the workspace's effective
// attachment size limit, so a client can tell a user what fits before spending
// their bandwidth. It is policy, not a capability — file-service re-reads the
// same value on every upload.
func TestSidebarHandler_PublishesTheWorkspaceUploadLimit(t *testing.T) {
	v := makeTestValidator(t)

	tests := []struct {
		name   string
		stored int64
		want   int64
	}{
		{name: "configured policy", stored: 100 << 20, want: 100 << 20},
		// A row written before migration 000020 reads as zero, which is not a
		// limit: it must publish the RF-32 default rather than 0, which a client
		// would read as "nothing may be uploaded".
		{name: "unset policy reads as the default", stored: 0, want: domain.DefaultMaxUploadBytes},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &stubSidebarProvider{data: service.SidebarData{
				Workspace: domain.Workspace{
					ID: "ws-1", Name: "NIC Labs", Slug: "default",
					Status: domain.WorkspaceStatusActive, MaxUploadBytes: tt.stored,
				},
			}}
			rr := httptest.NewRecorder()
			sidebarRouter(v, svc).ServeHTTP(rr, authGet(t))

			if rr.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d; body: %s", rr.Code, rr.Body.String())
			}
			var envelope struct {
				Data struct {
					Workspace struct {
						MaxUploadBytes int64 `json:"max_upload_bytes"`
					} `json:"workspace"`
				} `json:"data"`
			}
			mustDecode(t, rr, &envelope)
			if envelope.Data.Workspace.MaxUploadBytes != tt.want {
				t.Fatalf("expected %d, got %d", tt.want, envelope.Data.Workspace.MaxUploadBytes)
			}
		})
	}
}
