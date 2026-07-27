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
	return httpapi.NewRouter(sidebarTestConfig(), nil, httpapi.ReadinessState{}, v, allowAllSessionValidator{}, httpapi.NewSidebarHandler(svc), httpapi.NewMessageHandler(nil, nil, nil), nil, nil, nil, nil)
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
	router := httpapi.NewRouter(sidebarTestConfig(), nil, httpapi.ReadinessState{}, v, allowAllSessionValidator{}, httpapi.NewSidebarHandler(nil), httpapi.NewMessageHandler(nil, nil, nil), nil, nil, nil, nil)
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
		Channels: []service.SidebarChannel{
			{Channel: domain.Channel{ID: "ch-1", Slug: "geral", DisplayName: "geral", Type: domain.ChannelTypePublic, IsGeneral: true, Status: domain.ChannelStatusActive}, CanWrite: true},
			{Channel: domain.Channel{ID: "ch-2", Slug: "arquivo", DisplayName: "arquivo", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusArchived}, CanWrite: false},
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
				CanWrite  bool   `json:"can_write"`
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
	if len(envelope.Data.Channels) != 2 {
		t.Fatalf("expected 2 channels, got %d", len(envelope.Data.Channels))
	}
	if !envelope.Data.Channels[0].IsGeneral {
		t.Fatal("expected is_general=true for geral channel")
	}
	if !envelope.Data.Channels[0].CanWrite {
		t.Fatal("expected server-derived can_write=true")
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
		for _, key := range []string{"id", "type", "name"} {
			if _, ok := dm[key]; !ok {
				t.Fatalf("missing expected DM field %q in %v", key, dm)
			}
		}
		counterpart, hasCounterpart := dm["counterpart"]
		wantFields := 3
		if hasCounterpart {
			wantFields = 4
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
