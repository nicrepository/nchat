package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	platformlog "github.com/nicrepository/nchat/libs/go/platform/log"
	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

// GET /auth/admin/users is the browser-callable workspace user listing. It is
// reachable at /api/auth/admin/users through the gateway, which rewrites
// /api/auth/* to /auth/*. The guard below is what makes its workspace
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

	users          []domain.WorkspaceUser
	usersErr       error
	nextCursor     string
	gotWorkspaceID string
	gotLimit       int
	gotCursor      string
	listCalls      int
}

func (s *workspaceAdminStub) ListWorkspaceUsers(_ context.Context, workspaceID string, limit int, cursor string) ([]domain.WorkspaceUser, string, error) {
	s.gotWorkspaceID = workspaceID
	s.gotLimit = limit
	s.gotCursor = cursor
	s.listCalls++
	if s.usersErr != nil {
		return nil, "", s.usersErr
	}
	return s.users, s.nextCursor, nil
}

func sampleWorkspaceUsers() []domain.WorkspaceUser {
	created := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	return []domain.WorkspaceUser{
		{ID: "u1", Email: "alice@example.com", DisplayName: "Alice", FullName: "Alice Andrade",
			Status: "active", AuthSource: "manual", CreatedAt: created, SortKey: "alice"},
		{ID: "u2", Email: "bob@example.com", DisplayName: "Bob",
			Status: "suspended", AuthSource: "oidc", CreatedAt: created, SortKey: "bob"},
	}
}

func (s *workspaceAdminStub) GetAdminWorkspaceID(_ context.Context, userID string) (string, error) {
	s.gotActorID = userID
	return s.workspaceID, s.workspaceErr
}

func workspaceAdminRouter(t *testing.T, users *workspaceAdminStub) http.Handler {
	t.Helper()
	return NewRouter(jwtTestConfig(), platformlog.New("auth-service", "test"),
		users, nil, nil, nil, nil, nil, routerSessionStub{}, nil, nil, nil)
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

// ── Authentication ─────────────────────────────────────────────────────────

// ── Authorization ──────────────────────────────────────────────────────────

// ── Tenant isolation ───────────────────────────────────────────────────────

// ── Response shape ─────────────────────────────────────────────────────────

// ── Failure mapping ────────────────────────────────────────────────────────

// ── Guard, exercised directly ──────────────────────────────────────────────

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

	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, RouteAuthAdminUsers, nil))

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
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, RouteAuthAdminUsers, nil))

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
	req := httptest.NewRequest(http.MethodGet, RouteAuthAdminUsers, nil)
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

// ── Workspace user listing and pagination (issues #425, #433) ──────────────
//
// The listing is paginated because an unbounded one lets any admin force the
// service to materialise and serialise every member of a workspace (CWE-400).

func listRequest(t *testing.T, query string) *http.Request {
	t.Helper()
	path := RouteAuthAdminUsers
	if query != "" {
		path += "?" + query
	}
	return adminRequest(t, http.MethodGet, path, "")
}

func decodeUsersPage(t *testing.T, rec *httptest.ResponseRecorder) struct {
	Data       []map[string]any `json:"data"`
	Pagination struct {
		Limit      int     `json:"limit"`
		NextCursor *string `json:"next_cursor"`
		HasMore    bool    `json:"has_more"`
	} `json:"pagination"`
} {
	t.Helper()
	var env struct {
		Data struct {
			Data       []map[string]any `json:"data"`
			Pagination struct {
				Limit      int     `json:"limit"`
				NextCursor *string `json:"next_cursor"`
				HasMore    bool    `json:"has_more"`
			} `json:"pagination"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode body: %v (%s)", err, rec.Body.String())
	}
	return env.Data
}

func TestAdminUsers_CanonicalRouteIsRegistered(t *testing.T) {
	users := &workspaceAdminStub{workspaceID: adminWorkspaceID, users: sampleWorkspaceUsers()}
	rec := httptest.NewRecorder()

	workspaceAdminRouter(t, users).ServeHTTP(rec, listRequest(t, ""))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on canonical route, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAdminUsers_RejectsWrongMethod(t *testing.T) {
	users := &workspaceAdminStub{workspaceID: adminWorkspaceID}
	rec := httptest.NewRecorder()

	workspaceAdminRouter(t, users).ServeHTTP(rec, adminRequest(t, http.MethodPost, RouteAuthAdminUsers, `{}`))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

// The pre-fix path stays a 404: it is not aliased, so a client still pointing
// at it fails loudly instead of silently reading a different service.
func TestAdminUsers_LegacyAdminPathIsNotAliased(t *testing.T) {
	users := &workspaceAdminStub{workspaceID: adminWorkspaceID, users: sampleWorkspaceUsers()}
	rec := httptest.NewRecorder()

	workspaceAdminRouter(t, users).ServeHTTP(rec, adminRequest(t, http.MethodGet, "/api/admin/users", ""))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for the un-aliased legacy path, got %d", rec.Code)
	}
}

// ── Authentication and authorization ───────────────────────────────────────

func TestAdminUsers_NoSessionReturns401(t *testing.T) {
	users := &workspaceAdminStub{workspaceID: adminWorkspaceID, users: sampleWorkspaceUsers()}
	rec := httptest.NewRecorder()

	workspaceAdminRouter(t, users).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, RouteAuthAdminUsers, nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	if users.listCalls != 0 {
		t.Fatal("unauthenticated request must not reach the repository")
	}
}

func TestAdminUsers_RevokedSessionReturns401(t *testing.T) {
	users := &workspaceAdminStub{workspaceID: adminWorkspaceID, users: sampleWorkspaceUsers()}
	router := NewRouter(jwtTestConfig(), platformlog.New("auth-service", "test"),
		users, nil, nil, nil, nil, nil, routerSessionStub{validateErr: domain.ErrInvalidToken}, nil, nil, nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, listRequest(t, ""))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for a revoked session, got %d", rec.Code)
	}
	if users.listCalls != 0 {
		t.Fatal("revoked session must not reach the repository")
	}
}

// A member and a guest are both "administers no workspace" as far as the
// resolver is concerned, and both must be refused — not just the member.
func TestAdminUsers_NonAdminRolesReceive403(t *testing.T) {
	for _, role := range []string{"member", "guest"} {
		t.Run(role, func(t *testing.T) {
			users := &workspaceAdminStub{workspaceErr: domain.ErrForbidden, users: sampleWorkspaceUsers()}
			rec := httptest.NewRecorder()

			workspaceAdminRouter(t, users).ServeHTTP(rec, listRequest(t, ""))

			if rec.Code != http.StatusForbidden {
				t.Fatalf("expected 403 for %s, got %d", role, rec.Code)
			}
			if users.listCalls != 0 {
				t.Fatalf("%s must not reach the repository", role)
			}
		})
	}
}

func TestAdminUsers_OwnerAndAdminAreAuthorized(t *testing.T) {
	// The resolver returns a workspace for owner and admin alike; the handler
	// does not distinguish them, and this pins that.
	users := &workspaceAdminStub{workspaceID: adminWorkspaceID, users: sampleWorkspaceUsers()}
	rec := httptest.NewRecorder()

	workspaceAdminRouter(t, users).ServeHTTP(rec, listRequest(t, ""))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestAdminUsers_ActorIsDerivedFromSession(t *testing.T) {
	users := &workspaceAdminStub{workspaceID: adminWorkspaceID, users: sampleWorkspaceUsers()}
	req := listRequest(t, "")
	req.Header.Set("X-User-Id", "attacker")
	rec := httptest.NewRecorder()

	workspaceAdminRouter(t, users).ServeHTTP(rec, req)

	if users.gotActorID != adminActorID {
		t.Fatalf("expected actor %q from the session, got %q", adminActorID, users.gotActorID)
	}
}

func TestAdminUsers_QueriesOnlyTheResolvedWorkspace(t *testing.T) {
	users := &workspaceAdminStub{workspaceID: adminWorkspaceID, users: sampleWorkspaceUsers()}
	rec := httptest.NewRecorder()

	workspaceAdminRouter(t, users).ServeHTTP(rec, listRequest(t, ""))

	if users.gotWorkspaceID != adminWorkspaceID {
		t.Fatalf("expected workspace %q, got %q", adminWorkspaceID, users.gotWorkspaceID)
	}
}

func TestAdminUsers_ClientSuppliedWorkspaceIsIgnored(t *testing.T) {
	for _, attempt := range []struct {
		name string
		set  func(*http.Request)
	}{
		{"query parameter", func(r *http.Request) { r.URL.RawQuery = "workspace_id=" + otherWorkspaceID }},
		{"header", func(r *http.Request) { r.Header.Set("X-Workspace-Id", otherWorkspaceID) }},
	} {
		t.Run(attempt.name, func(t *testing.T) {
			users := &workspaceAdminStub{workspaceID: adminWorkspaceID, users: sampleWorkspaceUsers()}
			req := listRequest(t, "")
			attempt.set(req)
			rec := httptest.NewRecorder()

			workspaceAdminRouter(t, users).ServeHTTP(rec, req)

			if users.gotWorkspaceID != adminWorkspaceID {
				t.Fatalf("client-supplied workspace leaked through: %q", users.gotWorkspaceID)
			}
		})
	}
}

// ── Response shape ─────────────────────────────────────────────────────────

func TestAdminUsers_SuccessReturnsWorkspaceUsers(t *testing.T) {
	users := &workspaceAdminStub{workspaceID: adminWorkspaceID, users: sampleWorkspaceUsers()}
	rec := httptest.NewRecorder()

	workspaceAdminRouter(t, users).ServeHTTP(rec, listRequest(t, ""))

	page := decodeUsersPage(t, rec)
	if len(page.Data) != 2 {
		t.Fatalf("expected 2 users, got %d", len(page.Data))
	}
	if page.Data[0]["email"] != "alice@example.com" || page.Data[0]["full_name"] != "Alice Andrade" {
		t.Fatalf("unexpected first row: %v", page.Data[0])
	}
	if _, present := page.Data[1]["full_name"]; present {
		t.Fatalf("absent full_name must be omitted: %v", page.Data[1])
	}
}

func TestAdminUsers_EmptyWorkspaceReturnsEmptyArray(t *testing.T) {
	users := &workspaceAdminStub{workspaceID: adminWorkspaceID}
	rec := httptest.NewRecorder()

	workspaceAdminRouter(t, users).ServeHTTP(rec, listRequest(t, ""))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	page := decodeUsersPage(t, rec)
	if page.Data == nil || len(page.Data) != 0 {
		t.Fatalf("expected an empty array, got %v", page.Data)
	}
	if page.Pagination.HasMore || page.Pagination.NextCursor != nil {
		t.Fatalf("an empty page cannot have more: %+v", page.Pagination)
	}
}

// The sort key is an internal ordering detail and must not reach the client;
// neither may anything else beyond the table's columns.
func TestAdminUsers_ResponseCarriesNoSensitiveFields(t *testing.T) {
	users := &workspaceAdminStub{workspaceID: adminWorkspaceID, users: sampleWorkspaceUsers()}
	rec := httptest.NewRecorder()

	workspaceAdminRouter(t, users).ServeHTTP(rec, listRequest(t, ""))

	body := strings.ToLower(rec.Body.String())
	for _, forbidden := range []string{
		"password", "hash", "token", "secret", "session", "deleted_at",
		"external_subject", "avatar_url", "email_verified_at", "workspace_id", "sortkey", "sort_key",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("response must not expose %q: %s", forbidden, rec.Body.String())
		}
	}
}

// ── Limit ──────────────────────────────────────────────────────────────────

func TestAdminUsers_DefaultLimitIs50(t *testing.T) {
	users := &workspaceAdminStub{workspaceID: adminWorkspaceID}
	rec := httptest.NewRecorder()

	workspaceAdminRouter(t, users).ServeHTTP(rec, listRequest(t, ""))

	if users.gotLimit != 50 {
		t.Fatalf("expected the default limit 50, got %d", users.gotLimit)
	}
	if decodeUsersPage(t, rec).Pagination.Limit != 50 {
		t.Fatal("the effective limit must be echoed back")
	}
}

func TestAdminUsers_AcceptsLimitsInRange(t *testing.T) {
	for _, limit := range []int{1, 50, 100} {
		t.Run(strconv.Itoa(limit), func(t *testing.T) {
			users := &workspaceAdminStub{workspaceID: adminWorkspaceID}
			rec := httptest.NewRecorder()

			workspaceAdminRouter(t, users).ServeHTTP(rec, listRequest(t, "limit="+strconv.Itoa(limit)))

			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200 for limit=%d, got %d", limit, rec.Code)
			}
			if users.gotLimit != limit {
				t.Fatalf("expected limit %d to reach the service, got %d", limit, users.gotLimit)
			}
		})
	}
}

// Above the maximum the request is clamped, matching the existing
// login-attempts listing. Below it, or unparseable, it is rejected: a client
// asking for 0 or "abc" has a bug that a silent correction would hide.
func TestAdminUsers_LimitAboveMaximumIsClamped(t *testing.T) {
	users := &workspaceAdminStub{workspaceID: adminWorkspaceID}
	rec := httptest.NewRecorder()

	workspaceAdminRouter(t, users).ServeHTTP(rec, listRequest(t, "limit=5000"))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if users.gotLimit != 100 {
		t.Fatalf("expected the limit clamped to 100, got %d", users.gotLimit)
	}
}

func TestAdminUsers_InvalidLimitReturns400(t *testing.T) {
	for _, raw := range []string{"0", "-1", "abc", "1.5", " ", "1e2"} {
		t.Run(raw, func(t *testing.T) {
			users := &workspaceAdminStub{workspaceID: adminWorkspaceID}
			rec := httptest.NewRecorder()

			workspaceAdminRouter(t, users).ServeHTTP(rec, listRequest(t, "limit="+url.QueryEscape(raw)))

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for limit=%q, got %d", raw, rec.Code)
			}
			if users.listCalls != 0 {
				t.Fatalf("limit=%q must not reach the repository", raw)
			}
		})
	}
}

// ── Cursor ─────────────────────────────────────────────────────────────────

func TestAdminUsers_CursorIsPassedThroughAndEchoed(t *testing.T) {
	users := &workspaceAdminStub{workspaceID: adminWorkspaceID, users: sampleWorkspaceUsers(), nextCursor: "next-token"}
	rec := httptest.NewRecorder()

	workspaceAdminRouter(t, users).ServeHTTP(rec, listRequest(t, "cursor=abc123"))

	if users.gotCursor != "abc123" {
		t.Fatalf("expected the cursor to reach the service, got %q", users.gotCursor)
	}
	page := decodeUsersPage(t, rec)
	if page.Pagination.NextCursor == nil || *page.Pagination.NextCursor != "next-token" {
		t.Fatalf("expected next_cursor to be echoed, got %+v", page.Pagination)
	}
	if !page.Pagination.HasMore {
		t.Fatal("has_more must agree with next_cursor")
	}
}

// Every cursor rejection is the same generic 400: saying more would reveal
// whether a cursor belongs to some other workspace.
func TestAdminUsers_InvalidCursorReturnsGeneric400(t *testing.T) {
	users := &workspaceAdminStub{workspaceID: adminWorkspaceID, usersErr: domain.ErrInvalidInput}
	rec := httptest.NewRecorder()

	workspaceAdminRouter(t, users).ServeHTTP(rec, listRequest(t, "cursor=tampered"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, forbidden := range []string{adminWorkspaceID, otherWorkspaceID, "workspace", "version"} {
		if strings.Contains(strings.ToLower(body), strings.ToLower(forbidden)) {
			t.Fatalf("400 body must not reveal %q: %s", forbidden, body)
		}
	}
}

// ── Failure mapping ────────────────────────────────────────────────────────

func TestAdminUsers_RepositoryFailureReturns500WithoutDetail(t *testing.T) {
	users := &workspaceAdminStub{workspaceID: adminWorkspaceID, usersErr: errors.New("pq: relation chat.workspace_members does not exist")}
	rec := httptest.NewRecorder()

	workspaceAdminRouter(t, users).ServeHTTP(rec, listRequest(t, ""))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "workspace_members") {
		t.Fatalf("internal error leaked SQL detail: %s", rec.Body.String())
	}
}

func TestAdminUsers_ResolverFailureReturns500(t *testing.T) {
	users := &workspaceAdminStub{workspaceErr: errors.New("connection refused")}
	rec := httptest.NewRecorder()

	workspaceAdminRouter(t, users).ServeHTTP(rec, listRequest(t, ""))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "connection refused") {
		t.Fatalf("internal error leaked infrastructure detail: %s", rec.Body.String())
	}
}

func TestAdminUsers_UnwiredServiceReturns503(t *testing.T) {
	router := NewRouter(jwtTestConfig(), platformlog.New("auth-service", "test"),
		nil, nil, nil, nil, nil, nil, routerSessionStub{}, nil, nil, nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, listRequest(t, ""))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

func TestAdminListWorkspaceUsers_NilServiceReturns503(t *testing.T) {
	rec := httptest.NewRecorder()

	AdminListWorkspaceUsers(nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, RouteAuthAdminUsers, nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

// Without the guard there is no workspace in context. Listing every user would
// span workspaces, so the handler refuses.
func TestAdminListWorkspaceUsers_WithoutGuardRefuses(t *testing.T) {
	users := &workspaceAdminStub{users: sampleWorkspaceUsers()}
	rec := httptest.NewRecorder()

	AdminListWorkspaceUsers(users).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, RouteAuthAdminUsers, nil))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
	if users.listCalls != 0 {
		t.Fatal("an unscoped listing must never reach the repository")
	}
}
