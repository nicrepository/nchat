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
	"unicode/utf8"

	"github.com/golang-jwt/jwt/v5"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	httpapi "github.com/nicrepository/nchat/services/chat-service/internal/http"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Route-level tests for POST /api/chat/channels.
//
// Everything here goes through the real router: BearerAuth, RequireActiveSession
// and the real ChannelService over in-memory stores. The handler-level tests in
// channel_handler_test.go call Create directly and so cannot see the middleware
// at all; a rejection that only the middleware performs — a revoked session, an
// expired token — is only visible from here.

// ── in-memory stores ──────────────────────────────────────────────────────────
//
// Each embeds its storage interface so an unimplemented method panics rather
// than quietly answering. Channel creation must touch only what is overridden.

type routeWorkspaceStore struct {
	storage.WorkspaceStore
	workspace domain.Workspace
}

func (s *routeWorkspaceStore) GetDefaultWorkspace(context.Context) (domain.Workspace, error) {
	return s.workspace, nil
}

func (s *routeWorkspaceStore) GetWorkspaceByID(_ context.Context, id string) (domain.Workspace, error) {
	if s.workspace.ID != id {
		return domain.Workspace{}, domain.ErrNotFound
	}
	return s.workspace, nil
}

type routeMemberStore struct {
	storage.MemberStore
	member  domain.WorkspaceMember
	present bool
}

func (s *routeMemberStore) GetWorkspaceMember(_ context.Context, workspaceID, userID string) (domain.WorkspaceMember, error) {
	if !s.present || s.member.WorkspaceID != workspaceID || s.member.UserID != userID {
		return domain.WorkspaceMember{}, domain.ErrNotFound
	}
	return s.member, nil
}

// routeChannelStore stands in for the pgx store's atomic creation path. It
// re-applies the same predicate the SQL does, so a route test sees the same
// denial; the atomicity itself is proved against PostgreSQL in the storage
// package, not here.
type routeChannelStore struct {
	storage.ChannelStore
	workspace domain.Workspace
	member    domain.WorkspaceMember
	present   bool
	created   []storage.CreateChannelInput
}

func (s *routeChannelStore) CreateChannelForActiveMember(_ context.Context, input storage.CreateChannelInput) (domain.Channel, error) {
	if s.workspace.ID != input.WorkspaceID || s.workspace.Status != domain.WorkspaceStatusActive {
		return domain.Channel{}, domain.ErrForbidden
	}
	if !s.present || s.member.Status != domain.MemberStatusActive ||
		s.member.WorkspaceID != input.WorkspaceID || s.member.UserID != input.CreatedBy {
		return domain.Channel{}, domain.ErrForbidden
	}
	s.created = append(s.created, input)
	return domain.Channel{
		ID: createdChannelID, WorkspaceID: input.WorkspaceID, Slug: input.Slug,
		DisplayName: input.DisplayName, Type: input.Type, Status: domain.ChannelStatusActive,
		CreatedBy: input.CreatedBy,
	}, nil
}

// ── harness ───────────────────────────────────────────────────────────────────

type channelRouteEnv struct {
	router  http.Handler
	created *routeChannelStore
}

// newChannelRouteEnv wires the real router around the real ChannelService.
func newChannelRouteEnv(t *testing.T, sessions httpapi.SessionValidator, workspace domain.Workspace, member domain.WorkspaceMember, memberPresent bool) channelRouteEnv {
	t.Helper()
	workspaces := &routeWorkspaceStore{workspace: workspace}
	members := &routeMemberStore{member: member, present: memberPresent}
	channels := &routeChannelStore{workspace: workspace, member: member, present: memberPresent}

	handler := httpapi.NewChannelHandler(
		workspaces,
		service.NewChannelService(workspaces, channels, members),
		&fakeDMRateLimiter{},
	)
	return channelRouteEnv{
		router: httpapi.NewRouter(
			sidebarTestConfig(), nil, httpapi.ReadinessState{}, makeTestValidator(t), sessions,
			httpapi.NewSidebarHandler(nil), httpapi.NewMessageHandler(nil, nil, nil), nil, nil, handler, nil, nil,
		),
		created: channels,
	}
}

func routeActiveWorkspace() domain.Workspace {
	return domain.Workspace{ID: testWorkspaceID, Slug: "default", Name: "NIC", Status: domain.WorkspaceStatusActive}
}

func routeMember(role domain.WorkspaceRole, status domain.MemberStatus) domain.WorkspaceMember {
	return domain.WorkspaceMember{WorkspaceID: testWorkspaceID, UserID: testUserID, Role: role, Status: status}
}

// postChannel builds a POST with the given Authorization header value, or none
// when authorization is empty.
func postChannel(authorization, body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, httpapi.RouteChannels, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	return request
}

func bearer(token string) string { return httpapi.ExportBearerScheme + token }

// errorCode reads the error envelope's code, and fails if the payload carries a
// data object instead.
func errorCode(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error envelope: %v; body: %s", err, recorder.Body.String())
	}
	if body.Error == nil {
		t.Fatalf("no error envelope in a failed response: %s", recorder.Body.String())
	}
	if len(body.Data) != 0 && string(body.Data) != "null" {
		t.Fatalf("failed response carries data: %s", recorder.Body.String())
	}
	return body.Error.Code
}

// ── session and token rejection ───────────────────────────────────────────────

// Every way a request can arrive without a usable session must be refused by the
// middleware, with the same envelope and without the service ever running.
func TestChannelRoute_Create_RejectsUnusableSessions(t *testing.T) {
	expired := makeTestToken(t, testUserID, testHMACSecret, testIssuer, testAudience, -time.Hour)
	valid := makeTestToken(t, testUserID, testHMACSecret, testIssuer, testAudience, time.Hour)
	foreignSecret := makeTestToken(t, testUserID, "another-secret-that-is-long-enough-here!!", testIssuer, testAudience, time.Hour)
	noSession := makeTokenWithClaims(t, jwt.MapClaims{
		"sub": testUserID, "iss": testIssuer, "aud": jwt.ClaimStrings{testAudience},
		"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
	}, testHMACSecret)

	for _, test := range []struct {
		name          string
		authorization string
		sessions      httpapi.SessionValidator
	}{
		{name: "no Authorization header", sessions: allowAllSessionValidator{}},
		{name: "malformed token", authorization: bearer("not-a-jwt"), sessions: allowAllSessionValidator{}},
		{name: "wrong scheme", authorization: "Basic " + valid, sessions: allowAllSessionValidator{}},
		{name: "signed by another key", authorization: bearer(foreignSecret), sessions: allowAllSessionValidator{}},
		{name: "expired token", authorization: bearer(expired), sessions: allowAllSessionValidator{}},
		{name: "token without a session claim", authorization: bearer(noSession), sessions: allowAllSessionValidator{}},
		// A structurally valid token whose session the store refuses: revoked,
		// expired server-side, or belonging to a user that no longer exists. The
		// validator collapses them into ErrInvalidToken/ErrNotFound by design.
		{name: "session revoked or gone", authorization: bearer(valid), sessions: denySessionValidator{err: domain.ErrInvalidToken}},
		{name: "session absent", authorization: bearer(valid), sessions: denySessionValidator{err: domain.ErrNotFound}},
	} {
		t.Run(test.name, func(t *testing.T) {
			env := newChannelRouteEnv(t, test.sessions, routeActiveWorkspace(),
				routeMember(domain.WorkspaceRoleOwner, domain.MemberStatusActive), true)

			recorder := httptest.NewRecorder()
			env.router.ServeHTTP(recorder, postChannel(test.authorization, validCreateBody))

			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401: %s", recorder.Code, recorder.Body.String())
			}
			if code := errorCode(t, recorder); code != "unauthorized" {
				t.Fatalf("error code = %q, want unauthorized", code)
			}
			// One generic message for all of them: the caller must not learn
			// which part of their credential the server disliked.
			if body := recorder.Body.String(); strings.Contains(body, testUserID) || strings.Contains(body, "session") {
				t.Fatalf("response leaks credential detail: %s", body)
			}
			if len(env.created.created) != 0 {
				t.Fatalf("a rejected request created %d channel(s)", len(env.created.created))
			}
		})
	}
}

// A session-store outage is a 500, never a silent success and never a 401 that
// would send the user to log in again for no reason.
func TestChannelRoute_Create_SessionStoreFailureIs500(t *testing.T) {
	env := newChannelRouteEnv(t, denySessionValidator{err: errors.New("database down")},
		routeActiveWorkspace(), routeMember(domain.WorkspaceRoleMember, domain.MemberStatusActive), true)

	recorder := httptest.NewRecorder()
	env.router.ServeHTTP(recorder, postChannel(
		bearer(makeTestToken(t, testUserID, testHMACSecret, testIssuer, testAudience, time.Hour)), validCreateBody))

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}
	if body := recorder.Body.String(); strings.Contains(body, "database down") {
		t.Fatalf("response leaks the internal failure: %s", body)
	}
	if len(env.created.created) != 0 {
		t.Fatal("a failed request created a channel")
	}
}

// ── workspace and membership rejection ────────────────────────────────────────

func TestChannelRoute_Create_MembershipAndWorkspaceState(t *testing.T) {
	disabled := routeActiveWorkspace()
	disabled.Status = domain.WorkspaceStatusDisabled

	for _, test := range []struct {
		name       string
		workspace  domain.Workspace
		member     domain.WorkspaceMember
		hasMember  bool
		wantStatus int
	}{
		// Every active role creates: the rule is membership, not rank (BUG #393).
		{name: "active owner", workspace: routeActiveWorkspace(), member: routeMember(domain.WorkspaceRoleOwner, domain.MemberStatusActive), hasMember: true, wantStatus: http.StatusCreated},
		{name: "active admin", workspace: routeActiveWorkspace(), member: routeMember(domain.WorkspaceRoleAdmin, domain.MemberStatusActive), hasMember: true, wantStatus: http.StatusCreated},
		{name: "active member", workspace: routeActiveWorkspace(), member: routeMember(domain.WorkspaceRoleMember, domain.MemberStatusActive), hasMember: true, wantStatus: http.StatusCreated},
		{name: "active guest", workspace: routeActiveWorkspace(), member: routeMember(domain.WorkspaceRoleGuest, domain.MemberStatusActive), hasMember: true, wantStatus: http.StatusCreated},
		{name: "suspended membership", workspace: routeActiveWorkspace(), member: routeMember(domain.WorkspaceRoleOwner, domain.MemberStatusSuspended), hasMember: true, wantStatus: http.StatusForbidden},
		{name: "no membership", workspace: routeActiveWorkspace(), wantStatus: http.StatusForbidden},
		{name: "disabled workspace", workspace: disabled, member: routeMember(domain.WorkspaceRoleOwner, domain.MemberStatusActive), hasMember: true, wantStatus: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			env := newChannelRouteEnv(t, allowAllSessionValidator{}, test.workspace, test.member, test.hasMember)

			recorder := httptest.NewRecorder()
			env.router.ServeHTTP(recorder, postChannel(
				bearer(makeTestToken(t, testUserID, testHMACSecret, testIssuer, testAudience, time.Hour)),
				validCreateBody))

			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, test.wantStatus, recorder.Body.String())
			}
			if test.wantStatus != http.StatusCreated {
				if len(env.created.created) != 0 {
					t.Fatalf("a denied request created %d channel(s)", len(env.created.created))
				}
				return
			}
			if len(env.created.created) != 1 {
				t.Fatalf("created %d channel(s), want 1", len(env.created.created))
			}
			// The actor written is the session's subject, not anything the body
			// could have carried, and the role never reaches storage.
			if got := env.created.created[0].CreatedBy; got != testUserID {
				t.Fatalf("created_by = %q, want the session subject %q", got, testUserID)
			}
			if got := env.created.created[0].WorkspaceID; got != testWorkspaceID {
				t.Fatalf("workspace_id = %q, want the server-resolved %q", got, testWorkspaceID)
			}
		})
	}
}

// A body that elects its own actor or workspace is refused outright; the ones
// the server derives are unaffected by what it contained.
func TestChannelRoute_Create_IgnoresClientSuppliedActor(t *testing.T) {
	env := newChannelRouteEnv(t, allowAllSessionValidator{}, routeActiveWorkspace(),
		routeMember(domain.WorkspaceRoleGuest, domain.MemberStatusActive), true)

	recorder := httptest.NewRecorder()
	env.router.ServeHTTP(recorder, postChannel(
		bearer(makeTestToken(t, testUserID, testHMACSecret, testIssuer, testAudience, time.Hour)),
		`{"slug":"infra","display_name":"Infra","type":"public","created_by":"someone-else","role":"owner"}`))

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", recorder.Code, recorder.Body.String())
	}
	if len(env.created.created) != 0 {
		t.Fatal("a privileged payload reached storage")
	}
}

// ── display_name over HTTP ────────────────────────────────────────────────────

// The name cap is a validation error at the transport boundary too, counted in
// code points so an emoji costs what a letter costs, and the rejected value
// never comes back in the response.
func TestChannelRoute_Create_DisplayNameValidation(t *testing.T) {
	const maxCodePoints = domain.MaxChannelDisplayNameCodePoints
	for _, test := range []struct {
		name        string
		displayName string
		wantStatus  int
	}{
		{name: "empty", displayName: "", wantStatus: http.StatusBadRequest},
		{name: "whitespace only", displayName: "   ", wantStatus: http.StatusBadRequest},
		{name: "100 ascii", displayName: strings.Repeat("a", maxCodePoints), wantStatus: http.StatusCreated},
		{name: "101 ascii", displayName: strings.Repeat("a", maxCodePoints+1), wantStatus: http.StatusBadRequest},
		{name: "100 emoji", displayName: strings.Repeat("😀", maxCodePoints), wantStatus: http.StatusCreated},
		{name: "101 emoji", displayName: strings.Repeat("😀", maxCodePoints+1), wantStatus: http.StatusBadRequest},
		{
			name:        "mixed ascii and emoji at the limit",
			displayName: strings.Repeat("a", 50) + strings.Repeat("😀", 50),
			wantStatus:  http.StatusCreated,
		},
		// Well within the 64 KiB body limit, and still refused: the body cap is
		// not what protects this field.
		{name: "kilobytes of name", displayName: strings.Repeat("x", 20000), wantStatus: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			env := newChannelRouteEnv(t, allowAllSessionValidator{}, routeActiveWorkspace(),
				routeMember(domain.WorkspaceRoleMember, domain.MemberStatusActive), true)

			body, err := json.Marshal(map[string]string{
				"slug": "infra", "display_name": test.displayName, "type": "public",
			})
			if err != nil {
				t.Fatalf("encode body: %v", err)
			}
			recorder := httptest.NewRecorder()
			env.router.ServeHTTP(recorder, postChannel(
				bearer(makeTestToken(t, testUserID, testHMACSecret, testIssuer, testAudience, time.Hour)),
				string(body)))

			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, test.wantStatus, recorder.Body.String())
			}
			if test.wantStatus == http.StatusCreated {
				if len(env.created.created) != 1 {
					t.Fatalf("created %d channel(s), want 1", len(env.created.created))
				}
				if got := env.created.created[0].DisplayName; got != test.displayName {
					t.Fatalf("stored name differs from the one sent (%d vs %d code points)",
						utf8.RuneCountInString(got), utf8.RuneCountInString(test.displayName))
				}
				return
			}
			// The status and the envelope are the ones the endpoint already
			// used for a bad payload: no new error shape, only new payloads
			// reaching it.
			if code := errorCode(t, recorder); code != "bad_request" {
				t.Fatalf("error code = %q, want bad_request", code)
			}
			if response := recorder.Body.String(); strings.Contains(response, test.displayName) && test.displayName != "" {
				t.Fatalf("response echoes the rejected name (%d bytes of body)", len(response))
			}
			if len(env.created.created) != 0 {
				t.Fatal("an invalid name reached storage")
			}
		})
	}
}

// A denial for the name must not weaken anything else the endpoint checks first:
// an unauthenticated or unauthorized caller is still refused as such, and never
// learns whether their name would have been acceptable.
func TestChannelRoute_Create_OversizedNameStillRequiresAuth(t *testing.T) {
	oversized, err := json.Marshal(map[string]string{
		"slug":         "infra",
		"display_name": strings.Repeat("😀", domain.MaxChannelDisplayNameCodePoints+1),
		"type":         "public",
	})
	if err != nil {
		t.Fatalf("encode body: %v", err)
	}

	for _, test := range []struct {
		name       string
		sessions   httpapi.SessionValidator
		member     domain.WorkspaceMember
		hasMember  bool
		authHeader string
		wantStatus int
	}{
		{name: "no session", sessions: allowAllSessionValidator{}, hasMember: true, wantStatus: http.StatusUnauthorized},
		{
			name:       "revoked session",
			sessions:   denySessionValidator{err: domain.ErrInvalidToken},
			member:     routeMember(domain.WorkspaceRoleOwner, domain.MemberStatusActive),
			hasMember:  true,
			authHeader: "set",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "no membership",
			sessions:   allowAllSessionValidator{},
			authHeader: "set",
			wantStatus: http.StatusForbidden,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			env := newChannelRouteEnv(t, test.sessions, routeActiveWorkspace(), test.member, test.hasMember)
			authorization := ""
			if test.authHeader != "" {
				authorization = bearer(makeTestToken(t, testUserID, testHMACSecret, testIssuer, testAudience, time.Hour))
			}

			recorder := httptest.NewRecorder()
			env.router.ServeHTTP(recorder, postChannel(authorization, string(oversized)))

			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, test.wantStatus, recorder.Body.String())
			}
			if len(env.created.created) != 0 {
				t.Fatal("a rejected request created a channel")
			}
		})
	}
}

// A body larger than the endpoint accepts is still refused by the transport.
// The field cap does not replace it, and it does not replace the field cap.
func TestChannelRoute_Create_RejectsOversizedBody(t *testing.T) {
	env := newChannelRouteEnv(t, allowAllSessionValidator{}, routeActiveWorkspace(),
		routeMember(domain.WorkspaceRoleMember, domain.MemberStatusActive), true)

	body, err := json.Marshal(map[string]string{
		"slug": "infra", "display_name": strings.Repeat("x", 128*1024), "type": "public",
	})
	if err != nil {
		t.Fatalf("encode body: %v", err)
	}
	recorder := httptest.NewRecorder()
	env.router.ServeHTTP(recorder, postChannel(
		bearer(makeTestToken(t, testUserID, testHMACSecret, testIssuer, testAudience, time.Hour)),
		string(body)))

	if recorder.Code == http.StatusCreated {
		t.Fatal("a body past the endpoint limit was accepted")
	}
	if response := recorder.Body.String(); strings.Contains(response, strings.Repeat("x", 100)) {
		t.Fatal("response echoes the rejected payload")
	}
	if len(env.created.created) != 0 {
		t.Fatal("an oversized body reached storage")
	}
}
