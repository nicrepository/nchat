package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	platformlog "github.com/nicrepository/nchat/libs/go/platform/log"
	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/service"
)

// POST /auth/admin/invites is the browser-callable, session-scoped invite
// contract. The guard below is what makes its workspace and actor
// server-derived rather than client-supplied.

const (
	adminActorID     = "3f1c2d4e-5a6b-4c8d-9e0f-1a2b3c4d5e6f"
	adminSessionID   = "22222222-2222-4222-8222-222222222222"
	adminWorkspaceID = "9a8b7c6d-5e4f-4a3b-8c2d-1e0f9a8b7c6d"
	otherWorkspaceID = "11111111-2222-4333-8444-555555555555"
)

// workspaceAdminStub is a service.UserAdmin whose workspace methods are
// scriptable. Every other method is inherited unused from routerUsersUnused.
type workspaceAdminStub struct {
	routerUsersUnused
	stubProfileReader

	workspaceID  string
	workspaceErr error
	gotActorID   string
}

func (s *workspaceAdminStub) GetAdminWorkspaceID(_ context.Context, userID string) (string, error) {
	s.gotActorID = userID
	return s.workspaceID, s.workspaceErr
}

// inviteStub records what the invite manager was asked to create.
type inviteStub struct {
	result domain.InviteResult
	err    error
	got    domain.AdminInviteInput
	calls  int

	bootstrapGot   domain.BootstrapInviteInput
	bootstrapCalls int

	// emailHandoffOff models a service booted without the outbox key, which
	// both invite routes must refuse rather than call into.
	emailHandoffOff bool
}

func (s *inviteStub) CreateInvite(_ context.Context, input domain.AdminInviteInput) (domain.InviteResult, error) {
	s.got = input
	s.calls++
	if s.err != nil {
		return domain.InviteResult{}, s.err
	}
	return s.result, nil
}

// The bootstrap command is a distinct entry point, recorded separately so a
// test can assert which of the two a route actually reached.
func (s *inviteStub) CreateBootstrapInvite(_ context.Context, input domain.BootstrapInviteInput) (domain.InviteResult, error) {
	s.bootstrapGot = input
	s.bootstrapCalls++
	if s.err != nil {
		return domain.InviteResult{}, s.err
	}
	return s.result, nil
}

func (s *inviteStub) AcceptInvite(_ context.Context, _ domain.AcceptInviteInput) (domain.AcceptInviteResult, error) {
	return domain.AcceptInviteResult{}, nil
}

func (s *inviteStub) EmailHandoffAvailable() bool { return !s.emailHandoffOff }

func workspaceAdminRouter(t *testing.T, users *workspaceAdminStub, invites service.InviteManager) http.Handler {
	t.Helper()
	return NewRouter(jwtTestConfig(), platformlog.New("auth-service", "test"),
		users, nil, nil, nil, invites, nil, routerSessionStub{}, nil, nil, nil)
}

func adminRequest(t *testing.T, method, path, body string) *http.Request {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+mustAccessTokenForRouter(t, adminActorID, adminSessionID))
	return req
}

// ── Route registration and methods ─────────────────────────────────────────

func TestAdminInvites_RejectsWrongMethod(t *testing.T) {
	users := &workspaceAdminStub{workspaceID: adminWorkspaceID}
	rec := httptest.NewRecorder()

	workspaceAdminRouter(t, users, &inviteStub{}).ServeHTTP(rec, adminRequest(t, http.MethodGet, RouteAuthAdminInvites, ""))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for GET on the invite route, got %d", rec.Code)
	}
}

// ── Authentication ─────────────────────────────────────────────────────────

// ── Authorization ──────────────────────────────────────────────────────────

func TestAdminInvites_NonAdminReturns403(t *testing.T) {
	users := &workspaceAdminStub{workspaceErr: domain.ErrForbidden}
	invites := &inviteStub{}
	rec := httptest.NewRecorder()

	workspaceAdminRouter(t, users, invites).ServeHTTP(rec,
		adminRequest(t, http.MethodPost, RouteAuthAdminInvites, `{"email":"new@example.com","display_name":"New"}`))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for a non-admin creating an invite, got %d", rec.Code)
	}
	if invites.calls != 0 {
		t.Fatalf("unauthorized caller must not create an invite")
	}
}

// ── Tenant isolation ───────────────────────────────────────────────────────

// ── Response shape ─────────────────────────────────────────────────────────

// ── Failure mapping ────────────────────────────────────────────────────────

// ── Invite ─────────────────────────────────────────────────────────────────

func TestAdminInvites_AuthorizedAdminCreatesInvite(t *testing.T) {
	users := &workspaceAdminStub{workspaceID: adminWorkspaceID}
	invites := &inviteStub{result: domain.InviteResult{ID: "inv-1", Email: "new@example.com", CreatedAt: time.Now()}}
	rec := httptest.NewRecorder()

	workspaceAdminRouter(t, users, invites).ServeHTTP(rec,
		adminRequest(t, http.MethodPost, RouteAuthAdminInvites, `{"email":"new@example.com","display_name":"New User"}`))

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if invites.got.Email != "new@example.com" || invites.got.DisplayName != "New User" {
		t.Fatalf("unexpected invite input: %+v", invites.got)
	}
}

func TestAdminInvites_InvalidPayloadReturns400(t *testing.T) {
	users := &workspaceAdminStub{workspaceID: adminWorkspaceID}
	invites := &inviteStub{}
	rec := httptest.NewRecorder()

	workspaceAdminRouter(t, users, invites).ServeHTTP(rec,
		adminRequest(t, http.MethodPost, RouteAuthAdminInvites, `{"email":`))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for malformed JSON, got %d", rec.Code)
	}
	if invites.calls != 0 {
		t.Fatalf("malformed payload must not reach the invite service")
	}
}

func TestAdminInvites_InvalidEmailReturns400(t *testing.T) {
	users := &workspaceAdminStub{workspaceID: adminWorkspaceID}
	invites := &inviteStub{err: domain.ErrInvalidInput}
	rec := httptest.NewRecorder()

	workspaceAdminRouter(t, users, invites).ServeHTTP(rec,
		adminRequest(t, http.MethodPost, RouteAuthAdminInvites, `{"email":"not-an-email","display_name":"X"}`))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an invalid email, got %d", rec.Code)
	}
}

// A role sent by the browser must not be honoured: the payload has no such
// field, so it is discarded at decode time.
func TestAdminInvites_RoleInPayloadIsIgnored(t *testing.T) {
	users := &workspaceAdminStub{workspaceID: adminWorkspaceID}
	invites := &inviteStub{result: domain.InviteResult{ID: "inv-2", Email: "new@example.com", CreatedAt: time.Now()}}
	rec := httptest.NewRecorder()

	workspaceAdminRouter(t, users, invites).ServeHTTP(rec,
		adminRequest(t, http.MethodPost, RouteAuthAdminInvites,
			`{"email":"new@example.com","display_name":"New","role":"owner","workspace_id":"`+otherWorkspaceID+`"}`))

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(strings.ToLower(rec.Body.String()), "owner") {
		t.Fatalf("privileged role echoed back: %s", rec.Body.String())
	}
}

func TestAdminInvites_DuplicateReturns409(t *testing.T) {
	users := &workspaceAdminStub{workspaceID: adminWorkspaceID}
	invites := &inviteStub{err: domain.ErrInviteAlreadyPending}
	rec := httptest.NewRecorder()

	workspaceAdminRouter(t, users, invites).ServeHTTP(rec,
		adminRequest(t, http.MethodPost, RouteAuthAdminInvites, `{"email":"new@example.com","display_name":"New"}`))

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 for a repeated invite, got %d", rec.Code)
	}
}

func TestAdminInvites_ServiceFailureReturns500WithoutDetail(t *testing.T) {
	users := &workspaceAdminStub{workspaceID: adminWorkspaceID}
	invites := &inviteStub{err: errors.New("dial tcp 10.0.0.5:5432: connect: connection refused")}
	rec := httptest.NewRecorder()

	workspaceAdminRouter(t, users, invites).ServeHTTP(rec,
		adminRequest(t, http.MethodPost, RouteAuthAdminInvites, `{"email":"new@example.com","display_name":"New"}`))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "10.0.0.5") {
		t.Fatalf("internal error leaked topology: %s", rec.Body.String())
	}
}

// ── Fail-closed branches, exercised directly ───────────────────────────────
//
// The router substitutes a refusal handler when a dependency is missing, so
// these guards are unreachable through it. They are the backstop that holds if
// that wiring is ever changed, and they must fail closed on their own.

func TestRequireWorkspaceAdmin_NilResolverReturns503(t *testing.T) {
	var reached bool
	handler := RequireWorkspaceAdmin(nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reached = true
	}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, RouteAuthAdminInvites, nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 without a resolver, got %d", rec.Code)
	}
	if reached {
		t.Fatalf("an unwired guard must not call through")
	}
}

func TestRequireWorkspaceAdmin_MissingUserIDReturns401(t *testing.T) {
	users := &workspaceAdminStub{workspaceID: adminWorkspaceID}
	handler := RequireWorkspaceAdmin(users)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatalf("guard must not call through without an authenticated user")
	}))
	rec := httptest.NewRecorder()

	// No BearerAuth ran, so the context carries no user ID.
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, RouteAuthAdminInvites, nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with no user in context, got %d", rec.Code)
	}
}

// A subject that is not a UUID cannot identify a membership row. Rejecting it
// keeps a malformed identity from reaching the query.
func TestRequireWorkspaceAdmin_NonUUIDSubjectReturns401(t *testing.T) {
	users := &workspaceAdminStub{workspaceID: adminWorkspaceID}
	handler := RequireWorkspaceAdmin(users)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatalf("guard must not call through for a malformed subject")
	}))
	req := httptest.NewRequest(http.MethodGet, RouteAuthAdminInvites, nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxKeyUserID, "not-a-uuid"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for a non-UUID subject, got %d", rec.Code)
	}
	if users.gotActorID != "" {
		t.Fatalf("a malformed subject must not reach the resolver")
	}
}

// ── Invite authority and rate limit (issue #425) ───────────────────────────

// The workspace and actor reaching the service are the ones the guard
// resolved, not anything the browser supplied.
func TestAdminInvites_WorkspaceAndActorComeFromSession(t *testing.T) {
	users := &workspaceAdminStub{workspaceID: adminWorkspaceID}
	invites := &inviteStub{result: domain.InviteResult{ID: "inv-1", Email: "new@example.com", CreatedAt: time.Now()}}
	rec := httptest.NewRecorder()

	workspaceAdminRouter(t, users, invites).ServeHTTP(rec,
		adminRequest(t, http.MethodPost, RouteAuthAdminInvites, `{"email":"new@example.com","display_name":"New"}`))

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if invites.got.WorkspaceID != adminWorkspaceID {
		t.Fatalf("expected workspace %q from the session, got %q", adminWorkspaceID, invites.got.WorkspaceID)
	}
	if invites.got.ActorID != adminActorID {
		t.Fatalf("expected actor %q from the session, got %q", adminActorID, invites.got.ActorID)
	}
}

// A workspace_id in the body is discarded at decode time: createInviteRequest
// has no such field, so it cannot displace the resolved one.
func TestAdminInvites_PayloadWorkspaceIsIgnored(t *testing.T) {
	users := &workspaceAdminStub{workspaceID: adminWorkspaceID}
	invites := &inviteStub{result: domain.InviteResult{ID: "inv-1", CreatedAt: time.Now()}}
	rec := httptest.NewRecorder()

	body := `{"email":"new@example.com","display_name":"New","workspace_id":"` + otherWorkspaceID +
		`","actor_id":"attacker","invited_by_user_id":"attacker","role":"owner"}`
	workspaceAdminRouter(t, users, invites).ServeHTTP(rec,
		adminRequest(t, http.MethodPost, RouteAuthAdminInvites, body))

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if invites.got.WorkspaceID != adminWorkspaceID {
		t.Fatalf("payload workspace leaked through: %q", invites.got.WorkspaceID)
	}
	if invites.got.ActorID != adminActorID {
		t.Fatalf("payload actor leaked through: %q", invites.got.ActorID)
	}
}

// Two workspaces inviting the same address are independent: the second
// request carries its own workspace and is not blocked by the first.
func TestAdminInvites_DifferentWorkspacesInviteSameAddressIndependently(t *testing.T) {
	for _, workspaceID := range []string{adminWorkspaceID, otherWorkspaceID} {
		users := &workspaceAdminStub{workspaceID: workspaceID}
		invites := &inviteStub{result: domain.InviteResult{ID: "inv", CreatedAt: time.Now()}}
		rec := httptest.NewRecorder()

		workspaceAdminRouter(t, users, invites).ServeHTTP(rec,
			adminRequest(t, http.MethodPost, RouteAuthAdminInvites, `{"email":"shared@example.com","display_name":"Shared"}`))

		if rec.Code != http.StatusCreated {
			t.Fatalf("workspace %s: expected 201, got %d", workspaceID, rec.Code)
		}
		if invites.got.WorkspaceID != workspaceID {
			t.Fatalf("expected workspace %q, got %q", workspaceID, invites.got.WorkspaceID)
		}
	}
}

func TestAdminInvites_AlreadyMemberReturns409(t *testing.T) {
	users := &workspaceAdminStub{workspaceID: adminWorkspaceID}
	invites := &inviteStub{err: domain.ErrAlreadyMember}
	rec := httptest.NewRecorder()

	workspaceAdminRouter(t, users, invites).ServeHTTP(rec,
		adminRequest(t, http.MethodPost, RouteAuthAdminInvites, `{"email":"member@example.com","display_name":"Member"}`))

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", rec.Code)
	}
	// The body must not reveal which of the conflict causes applied: that
	// would report presence in a workspace the caller may not administer.
	if strings.Contains(strings.ToLower(rec.Body.String()), "member") {
		t.Fatalf("conflict body must stay generic: %s", rec.Body.String())
	}
}

func TestAdminInvites_RateLimitedReturns429WithRetryAfter(t *testing.T) {
	users := &workspaceAdminStub{workspaceID: adminWorkspaceID}
	invites := &inviteStub{err: domain.ErrInviteRateLimited}
	rec := httptest.NewRecorder()

	workspaceAdminRouter(t, users, invites).ServeHTTP(rec,
		adminRequest(t, http.MethodPost, RouteAuthAdminInvites, `{"email":"new@example.com","display_name":"New"}`))

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("expected a Retry-After header on 429")
	}
	if !strings.Contains(rec.Body.String(), "rate_limited") {
		t.Fatalf("expected the canonical rate_limited envelope, got %s", rec.Body.String())
	}
	// Nothing about the budget, the actor or the address leaks into the body.
	for _, forbidden := range []string{"new@example.com", adminActorID, adminWorkspaceID} {
		if strings.Contains(rec.Body.String(), forbidden) {
			t.Fatalf("429 body must not echo %q: %s", forbidden, rec.Body.String())
		}
	}
}

// The limiter runs after authorization, so an unauthorized caller is rejected
// without spending anyone's budget, and a 403 is never converted into a 429.
func TestAdminInvites_UnauthorizedCallerDoesNotReachRateLimiter(t *testing.T) {
	users := &workspaceAdminStub{workspaceErr: domain.ErrForbidden}
	invites := &inviteStub{err: domain.ErrInviteRateLimited}
	rec := httptest.NewRecorder()

	workspaceAdminRouter(t, users, invites).ServeHTTP(rec,
		adminRequest(t, http.MethodPost, RouteAuthAdminInvites, `{"email":"new@example.com","display_name":"New"}`))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
	if invites.calls != 0 {
		t.Fatal("unauthorized caller must not reach the invite service")
	}
}

// The bootstrap route is a separate entry point with a separate guard. It must
// not become a way around the session-scoped one: without the bootstrap token
// it refuses, and it never reads the session context.
func TestAdminInvites_BootstrapRouteIsNotABypass(t *testing.T) {
	users := &workspaceAdminStub{workspaceID: adminWorkspaceID}
	invites := &inviteStub{result: domain.InviteResult{ID: "inv-1", CreatedAt: time.Now()}}
	rec := httptest.NewRecorder()

	// A valid browser session, aimed at the bootstrap path.
	workspaceAdminRouter(t, users, invites).ServeHTTP(rec,
		adminRequest(t, http.MethodPost, RouteAdminInvites, `{"email":"new@example.com","display_name":"New"}`))

	if rec.Code == http.StatusCreated {
		t.Fatalf("a session must not create an invite through the bootstrap route: %d", rec.Code)
	}
	if invites.calls != 0 {
		t.Fatal("bootstrap route must not reach the invite service without its token")
	}
}

// ── Bootstrap invite route (issue #425) ────────────────────────────────────
//
// POST /admin/invites is the initialization-only sibling of the session-scoped
// route. It previously shared AdminCreateInvite, which reads a workspace and an
// actor from the request context — values AdminBootstrapGuard never injects —
// so it answered 403 unconditionally. These tests fail if that regression
// returns.

func bootstrapRouter(t *testing.T, invites service.InviteManager, token string) http.Handler {
	t.Helper()
	cfg := jwtTestConfig()
	cfg.AdminBootstrapToken = token
	return NewRouter(cfg, platformlog.New("auth-service", "test"),
		&workspaceAdminStub{workspaceID: adminWorkspaceID}, nil, nil, nil, invites, nil, routerSessionStub{}, nil, nil, nil)
}

func bootstrapRequest(t *testing.T, token, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, RouteAdminInvites, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-NChat-Admin-Token", token)
	}
	return req
}

const bootstrapToken = "bootstrap-credential-for-tests" //nolint:gosec // test fixture

// The regression guard: with a valid credential the route must create an
// invite, not answer 403.
func TestBootstrapInvites_ValidCredentialCreatesInvite(t *testing.T) {
	invites := &inviteStub{result: domain.InviteResult{ID: "inv-1", Email: "first@example.com", CreatedAt: time.Now()}}
	rec := httptest.NewRecorder()

	bootstrapRouter(t, invites, bootstrapToken).ServeHTTP(rec,
		bootstrapRequest(t, bootstrapToken, `{"email":"first@example.com","display_name":"First"}`))

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	// It must reach the bootstrap command, never the session-scoped one.
	if invites.bootstrapCalls != 1 {
		t.Fatalf("expected the bootstrap command to run once, got %d", invites.bootstrapCalls)
	}
	if invites.calls != 0 {
		t.Fatal("the bootstrap route must not reach the session-scoped command")
	}
	if invites.bootstrapGot.Email != "first@example.com" || invites.bootstrapGot.DisplayName != "First" {
		t.Fatalf("unexpected invitee: %+v", invites.bootstrapGot)
	}
}

func TestBootstrapInvites_MissingOrWrongCredentialIsRejected(t *testing.T) {
	for _, tc := range []struct {
		name  string
		token string
		want  int
	}{
		{"missing", "", http.StatusUnauthorized},
		{"wrong", "not-the-credential", http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			invites := &inviteStub{result: domain.InviteResult{ID: "inv-1"}}
			rec := httptest.NewRecorder()

			bootstrapRouter(t, invites, bootstrapToken).ServeHTTP(rec,
				bootstrapRequest(t, tc.token, `{"email":"first@example.com","display_name":"First"}`))

			if rec.Code != tc.want {
				t.Fatalf("expected %d, got %d", tc.want, rec.Code)
			}
			if invites.bootstrapCalls != 0 {
				t.Fatal("a rejected credential must not create an invite")
			}
		})
	}
}

// An unconfigured bootstrap token disables the route entirely.
func TestBootstrapInvites_UnconfiguredTokenDisablesRoute(t *testing.T) {
	invites := &inviteStub{}
	rec := httptest.NewRecorder()

	bootstrapRouter(t, invites, "").ServeHTTP(rec,
		bootstrapRequest(t, "anything", `{"email":"first@example.com","display_name":"First"}`))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
	if invites.bootstrapCalls != 0 {
		t.Fatal("a disabled route must not create an invite")
	}
}

// A session, however valid, is not authority on this route: only the
// credential is. This is the other half of the separation — the two routes
// cannot be used interchangeably.
func TestBootstrapInvites_SessionIsNotAcceptedAsCredential(t *testing.T) {
	invites := &inviteStub{result: domain.InviteResult{ID: "inv-1"}}
	req := adminRequest(t, http.MethodPost, RouteAdminInvites, `{"email":"first@example.com","display_name":"First"}`)
	rec := httptest.NewRecorder()

	bootstrapRouter(t, invites, bootstrapToken).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for a bearer token on the bootstrap route, got %d", rec.Code)
	}
	if invites.bootstrapCalls != 0 || invites.calls != 0 {
		t.Fatal("a session must not create an invite through the bootstrap route")
	}
}

// The body names only the invitee. A workspace_id or actor in it is discarded
// at decode time — the bootstrap payload type has no field for either.
func TestBootstrapInvites_PayloadCannotChooseWorkspaceOrActor(t *testing.T) {
	invites := &inviteStub{result: domain.InviteResult{ID: "inv-1"}}
	rec := httptest.NewRecorder()

	body := `{"email":"first@example.com","display_name":"First","workspace_id":"` + otherWorkspaceID +
		`","actor_id":"attacker","invited_by_user_id":"attacker","role":"owner"}`
	bootstrapRouter(t, invites, bootstrapToken).ServeHTTP(rec, bootstrapRequest(t, bootstrapToken, body))

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	// domain.BootstrapInviteInput carries only the invitee, so there is nothing
	// for a client-supplied workspace or actor to land in.
	if invites.bootstrapGot != (domain.BootstrapInviteInput{Email: "first@example.com", DisplayName: "First"}) {
		t.Fatalf("payload fields leaked into the command: %+v", invites.bootstrapGot)
	}
	if strings.Contains(strings.ToLower(rec.Body.String()), "owner") {
		t.Fatalf("privileged role echoed back: %s", rec.Body.String())
	}
}

func TestBootstrapInvites_OutsideInitializationWindowReturns503(t *testing.T) {
	invites := &inviteStub{err: domain.ErrBootstrapUnavailable}
	rec := httptest.NewRecorder()

	bootstrapRouter(t, invites, bootstrapToken).ServeHTTP(rec,
		bootstrapRequest(t, bootstrapToken, `{"email":"first@example.com","display_name":"First"}`))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", rec.Code, rec.Body.String())
	}
	// The body must not say which workspace, or who already administers it.
	for _, forbidden := range []string{adminWorkspaceID, otherWorkspaceID, "owner", "admin"} {
		if strings.Contains(rec.Body.String(), forbidden) {
			t.Fatalf("503 body must not reveal %q: %s", forbidden, rec.Body.String())
		}
	}
}

func TestBootstrapInvites_RateLimitedReturns429WithRetryAfter(t *testing.T) {
	invites := &inviteStub{err: domain.ErrInviteRateLimited}
	rec := httptest.NewRecorder()

	bootstrapRouter(t, invites, bootstrapToken).ServeHTTP(rec,
		bootstrapRequest(t, bootstrapToken, `{"email":"first@example.com","display_name":"First"}`))

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("expected a Retry-After header on 429")
	}
}

func TestBootstrapInvites_InvalidPayloadReturns400(t *testing.T) {
	invites := &inviteStub{}
	rec := httptest.NewRecorder()

	bootstrapRouter(t, invites, bootstrapToken).ServeHTTP(rec,
		bootstrapRequest(t, bootstrapToken, `{"email":`))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	if invites.bootstrapCalls != 0 {
		t.Fatal("malformed JSON must not reach the invite service")
	}
}

func TestBootstrapInvites_RejectsWrongMethod(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, RouteAdminInvites, nil)
	req.Header.Set("X-NChat-Admin-Token", bootstrapToken)

	bootstrapRouter(t, &inviteStub{}, bootstrapToken).ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

// The response must never carry the invite token or its hash: the token
// reaches the invitee only through the encrypted outbox handoff.
func TestBootstrapInvites_ResponseCarriesNoToken(t *testing.T) {
	invites := &inviteStub{result: domain.InviteResult{ID: "inv-1", Email: "first@example.com", CreatedAt: time.Now()}}
	rec := httptest.NewRecorder()

	bootstrapRouter(t, invites, bootstrapToken).ServeHTTP(rec,
		bootstrapRequest(t, bootstrapToken, `{"email":"first@example.com","display_name":"First"}`))

	body := strings.ToLower(rec.Body.String())
	for _, forbidden := range []string{"token", "hash", "password", "secret", bootstrapToken} {
		if strings.Contains(body, strings.ToLower(forbidden)) {
			t.Fatalf("bootstrap invite response must not expose %q: %s", forbidden, rec.Body.String())
		}
	}
}

// A pod that booted without a database, or without the e-mail handoff, must
// refuse rather than reach a half-wired service.
func TestBootstrapInvites_UnwiredDependenciesReturn503(t *testing.T) {
	for _, tc := range []struct {
		name    string
		invites service.InviteManager
	}{
		{"no invite service", nil},
		{"email handoff disabled", &inviteStub{emailHandoffOff: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()

			BootstrapCreateInvite(tc.invites, 600).ServeHTTP(rec,
				bootstrapRequest(t, "", `{"email":"first@example.com","display_name":"First"}`))

			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("expected 503, got %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}
