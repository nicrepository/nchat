package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
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
func sidebarRouter(v *httpapi.TokenValidator, svc *stubSidebarProvider) http.Handler {
	return httpapi.NewRouter(sidebarTestConfig(), nil, v, allowAllSessionValidator{}, httpapi.NewSidebarHandler(svc), httpapi.NewMessageHandler(nil, nil, nil), nil, nil)
}

// authGet returns an authenticated GET request to RouteSidebar.
func authGet(t *testing.T) *http.Request {
	t.Helper()
	tok := makeTestToken(t, testUserID, testHMACSecret, testIssuer, testAudience, time.Hour)
	req := httptest.NewRequest(http.MethodGet, httpapi.RouteSidebar, nil)
	setBearerToken(req, tok)
	return req
}

// ── SidebarHandler tests ──────────────────────────────────────────────────────

func TestSidebarHandler_NilService_Returns503(t *testing.T) {
	v := makeTestValidator(t)
	router := httpapi.NewRouter(sidebarTestConfig(), nil, v, allowAllSessionValidator{}, httpapi.NewSidebarHandler(nil), httpapi.NewMessageHandler(nil, nil, nil), nil, nil)
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

func TestSidebarHandler_ValidAuth_ReturnsSidebar(t *testing.T) {
	v := makeTestValidator(t)
	svc := &stubSidebarProvider{data: service.SidebarData{
		Workspace: domain.Workspace{ID: "ws-1", Name: "NIC Labs", Slug: "default", Status: domain.WorkspaceStatusActive},
		Channels: []domain.Channel{
			{ID: "ch-1", Slug: "geral", DisplayName: "geral", Type: domain.ChannelTypePublic, IsGeneral: true, Status: domain.ChannelStatusActive},
		},
		DMs: []domain.DMConversationWithParticipantIDs{
			{
				DMConversation: domain.DMConversation{ID: "dm-1", Type: domain.DMConversationTypeDirect},
				ParticipantIDs: []string{testUserID, "other-user"},
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
				ID        string `json:"id"`
				Slug      string `json:"slug"`
				IsGeneral bool   `json:"is_general"`
			} `json:"channels"`
			DMs []struct {
				ID   string `json:"id"`
				Type string `json:"type"`
				Name string `json:"name"`
			} `json:"dm_conversations"`
		} `json:"data"`
	}
	mustDecode(t, rr, &envelope)
	if envelope.Data.Workspace.ID != "ws-1" {
		t.Fatalf("expected workspace ws-1, got %q", envelope.Data.Workspace.ID)
	}
	if len(envelope.Data.Channels) != 1 {
		t.Fatalf("expected 1 channel, got %d", len(envelope.Data.Channels))
	}
	if !envelope.Data.Channels[0].IsGeneral {
		t.Fatal("expected is_general=true for geral channel")
	}
	if len(envelope.Data.DMs) != 1 {
		t.Fatalf("expected 1 DM, got %d", len(envelope.Data.DMs))
	}
	if envelope.Data.DMs[0].Type != "direct" {
		t.Fatalf("expected DM type 'direct', got %q", envelope.Data.DMs[0].Type)
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

func TestSidebarHandler_DM_DirectName_UsesNeutralFallback(t *testing.T) {
	// Direct DMs must not expose the other participant's user ID as display name.
	// A neutral placeholder is used until a profile-safe display source exists.
	v := makeTestValidator(t)
	svc := &stubSidebarProvider{data: service.SidebarData{
		Workspace: domain.Workspace{ID: "ws-1", Name: "NIC Labs", Slug: "default", Status: domain.WorkspaceStatusActive},
		DMs: []domain.DMConversationWithParticipantIDs{
			{
				DMConversation: domain.DMConversation{ID: "dm-1", Type: domain.DMConversationTypeDirect},
				ParticipantIDs: []string{testUserID, "other-user-id"},
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
			} `json:"dm_conversations"`
		} `json:"data"`
	}
	mustDecode(t, rr, &envelope)
	if len(envelope.Data.DMs) == 0 {
		t.Fatal("expected at least 1 DM")
	}
	// Must NOT expose the other participant's ID; neutral fallback expected.
	if envelope.Data.DMs[0].Name == "other-user-id" {
		t.Fatal("direct DM name must not leak participant user ID")
	}
	if envelope.Data.DMs[0].Name == "" {
		t.Fatal("direct DM name must not be empty")
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
		Channels: []domain.Channel{
			{ID: "ch-priv", Slug: "secreto", DisplayName: "secreto", Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive},
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
				DMConversation: domain.DMConversation{ID: "dm-direct", Type: domain.DMConversationTypeDirect},
				ParticipantIDs: []string{testUserID, "other-user"},
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
}

// mustDecode decodes the recorder body into v, failing the test on error.
func mustDecode(t *testing.T, rr *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.NewDecoder(rr.Body).Decode(v); err != nil {
		t.Fatalf("decode response: %v; body: %s", err, rr.Body.String())
	}
}
