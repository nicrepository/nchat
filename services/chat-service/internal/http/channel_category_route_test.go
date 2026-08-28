package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	httpapi "github.com/nicrepository/nchat/services/chat-service/internal/http"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Route-level tests for the RF-17 category endpoints.
//
// Everything here goes through the real router — BearerAuth, RequireActiveSession
// — and the real ChannelCategoryService over in-memory stores. The handler tests
// call the methods directly and so cannot see the middleware at all; a rejection
// only the middleware performs is visible only from here, and so is the
// end-to-end authorization decision for each role.

// ── in-memory stores ──────────────────────────────────────────────────────────

// routeCategoryStore re-applies the predicate the SQL backstop applies, so a route
// test sees the same denial. The atomicity of the real statements is proved in the
// storage package, not here.
type routeCategoryStore struct {
	workspace  domain.Workspace
	member     domain.WorkspaceMember
	present    bool
	categories []domain.ChannelCategory
	writes     int
}

func (s *routeCategoryStore) authorized(workspaceID, callerID string) bool {
	if s.workspace.ID != workspaceID || s.workspace.Status != domain.WorkspaceStatusActive {
		return false
	}
	if !s.present || s.member.WorkspaceID != workspaceID || s.member.UserID != callerID {
		return false
	}
	return domain.CanManageChannelCategories(&s.member)
}

func (s *routeCategoryStore) ListChannelCategories(_ context.Context, workspaceID string) ([]domain.ChannelCategory, error) {
	if s.workspace.ID != workspaceID {
		return nil, nil
	}
	return s.categories, nil
}

func (s *routeCategoryStore) CountChannelCategories(_ context.Context, _ string) (int, error) {
	return len(s.categories), nil
}

func (s *routeCategoryStore) CreateChannelCategoryForManager(_ context.Context, input storage.CreateChannelCategoryInput) (domain.ChannelCategory, error) {
	if !s.authorized(input.WorkspaceID, input.CallerID) {
		return domain.ChannelCategory{}, domain.ErrForbidden
	}
	for _, existing := range s.categories {
		if strings.EqualFold(existing.Name, input.Name) {
			return domain.ChannelCategory{}, domain.ErrDuplicateChannelCategoryName
		}
	}
	s.writes++
	created := domain.ChannelCategory{
		ID: testCategoryID, WorkspaceID: input.WorkspaceID, Name: input.Name, Position: len(s.categories),
	}
	s.categories = append(s.categories, created)
	return created, nil
}

func (s *routeCategoryStore) RenameChannelCategoryForManager(_ context.Context, input storage.RenameChannelCategoryInput) (domain.ChannelCategory, error) {
	if !s.authorized(input.WorkspaceID, input.CallerID) {
		return domain.ChannelCategory{}, domain.ErrForbidden
	}
	for i := range s.categories {
		if s.categories[i].ID == input.CategoryID {
			s.writes++
			s.categories[i].Name = input.Name
			return s.categories[i], nil
		}
	}
	return domain.ChannelCategory{}, domain.ErrNotFound
}

func (s *routeCategoryStore) ReorderChannelCategoriesForManager(_ context.Context, input storage.ReorderChannelCategoriesInput) ([]domain.ChannelCategory, error) {
	if !s.authorized(input.WorkspaceID, input.CallerID) {
		return nil, domain.ErrForbidden
	}
	if len(input.OrderedIDs) != len(s.categories) {
		return nil, domain.ErrInvalidChannelCategoryOrder
	}
	byID := make(map[string]domain.ChannelCategory, len(s.categories))
	for _, category := range s.categories {
		byID[category.ID] = category
	}
	reordered := make([]domain.ChannelCategory, 0, len(input.OrderedIDs))
	for i, id := range input.OrderedIDs {
		category, ok := byID[id]
		if !ok {
			return nil, domain.ErrInvalidChannelCategoryOrder
		}
		delete(byID, id)
		category.Position = i
		reordered = append(reordered, category)
	}
	s.writes++
	s.categories = reordered
	return reordered, nil
}

func (s *routeCategoryStore) DeleteChannelCategoryForManager(_ context.Context, workspaceID, categoryID, callerID string) error {
	if !s.authorized(workspaceID, callerID) {
		return domain.ErrForbidden
	}
	for i, category := range s.categories {
		if category.ID == categoryID {
			s.writes++
			s.categories = append(s.categories[:i], s.categories[i+1:]...)
			return nil
		}
	}
	return domain.ErrNotFound
}

// routeVisibleChannelStore returns what the SQL read policy would return for this
// caller. Channels the policy excludes are simply absent, the way the query leaves
// them out.
type routeVisibleChannelStore struct {
	accesses []storage.VisibleChannelAccess
}

func (s *routeVisibleChannelStore) ListVisibleChannelAccessByUser(_ context.Context, _, _ string) ([]storage.VisibleChannelAccess, error) {
	return s.accesses, nil
}

// ── harness ───────────────────────────────────────────────────────────────────

type categoryRouteEnv struct {
	router     http.Handler
	categories *routeCategoryStore
}

func newCategoryRouteEnv(
	t *testing.T,
	sessions httpapi.SessionValidator,
	workspace domain.Workspace,
	member domain.WorkspaceMember,
	memberPresent bool,
	seeded []domain.ChannelCategory,
	accesses []storage.VisibleChannelAccess,
) categoryRouteEnv {
	t.Helper()
	workspaces := &routeWorkspaceStore{workspace: workspace}
	members := &routeMemberStore{member: member, present: memberPresent}
	categories := &routeCategoryStore{
		workspace: workspace, member: member, present: memberPresent, categories: seeded,
	}
	handler := httpapi.NewChannelCategoryHandler(
		workspaces,
		service.NewChannelCategoryService(workspaces, members, categories, &routeVisibleChannelStore{accesses: accesses}),
		&fakeDMRateLimiter{},
	)
	return categoryRouteEnv{
		router: httpapi.NewRouter(
			sidebarTestConfig(), nil, httpapi.ReadinessState{}, makeTestValidator(t), sessions,
			httpapi.NewSidebarHandler(nil), httpapi.NewMessageHandler(nil, nil, nil), nil, nil, nil, handler, nil,
		),
		categories: categories,
	}
}

func categoryRouteToken(t *testing.T) string {
	t.Helper()
	return bearer(makeTestToken(t, testUserID, testHMACSecret, testIssuer, testAudience, time.Hour))
}

func categoryRequest(method, target, authorization, body string) *http.Request {
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	} else {
		reader = strings.NewReader("")
	}
	request := httptest.NewRequest(method, target, reader)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	return request
}

func routeCategory(id, name string, position int) domain.ChannelCategory {
	return domain.ChannelCategory{ID: id, WorkspaceID: testWorkspaceID, Name: name, Position: position}
}

// ── every route needs a usable session ────────────────────────────────────────

func TestChannelCategoryRoute_RejectsRequestsWithoutASession(t *testing.T) {
	valid := makeTestToken(t, testUserID, testHMACSecret, testIssuer, testAudience, time.Hour)
	expired := makeTestToken(t, testUserID, testHMACSecret, testIssuer, testAudience, -time.Hour)

	for _, route := range []struct {
		name   string
		method string
		target string
		body   string
	}{
		{name: "list", method: http.MethodGet, target: httpapi.RouteChannelCategories},
		{name: "create", method: http.MethodPost, target: httpapi.RouteChannelCategories, body: `{"name":"Projetos"}`},
		{name: "reorder", method: http.MethodPut, target: httpapi.RouteChannelCategoriesOrder, body: `{"category_ids":["` + testCategoryID + `"]}`},
		{name: "rename", method: http.MethodPatch, target: "/api/chat/channel-categories/" + testCategoryID, body: `{"name":"Projetos"}`},
		{name: "delete", method: http.MethodDelete, target: "/api/chat/channel-categories/" + testCategoryID},
	} {
		for _, session := range []struct {
			name          string
			authorization string
			sessions      httpapi.SessionValidator
		}{
			{name: "no Authorization header", sessions: allowAllSessionValidator{}},
			{name: "malformed token", authorization: bearer("not-a-jwt"), sessions: allowAllSessionValidator{}},
			{name: "expired token", authorization: bearer(expired), sessions: allowAllSessionValidator{}},
			{name: "session revoked", authorization: bearer(valid), sessions: denySessionValidator{err: domain.ErrInvalidToken}},
			{name: "session absent", authorization: bearer(valid), sessions: denySessionValidator{err: domain.ErrNotFound}},
		} {
			t.Run(route.name+"/"+session.name, func(t *testing.T) {
				env := newCategoryRouteEnv(t, session.sessions, routeActiveWorkspace(),
					routeMember(domain.WorkspaceRoleAdmin, domain.MemberStatusActive), true, nil, nil)

				recorder := httptest.NewRecorder()
				env.router.ServeHTTP(recorder, categoryRequest(route.method, route.target, session.authorization, route.body))

				if recorder.Code != http.StatusUnauthorized {
					t.Fatalf("status = %d, want 401", recorder.Code)
				}
				if code := errorCode(t, recorder); code != "unauthorized" {
					t.Fatalf("error code = %q, want unauthorized", code)
				}
				if env.categories.writes != 0 {
					t.Fatal("an unauthenticated request must not write")
				}
			})
		}
	}
}

// ── role matrix, end to end ───────────────────────────────────────────────────

// The management gate through the whole stack. Owner and admin may mutate; a plain
// member, a guest, a suspended admin and a non-member may not, and none of them
// reaches a write.
func TestChannelCategoryRoute_MutationsAreRestrictedToWorkspaceManagement(t *testing.T) {
	for _, caller := range []struct {
		name    string
		role    domain.WorkspaceRole
		status  domain.MemberStatus
		present bool
		allowed bool
	}{
		{name: "owner", role: domain.WorkspaceRoleOwner, status: domain.MemberStatusActive, present: true, allowed: true},
		{name: "admin", role: domain.WorkspaceRoleAdmin, status: domain.MemberStatusActive, present: true, allowed: true},
		{name: "member", role: domain.WorkspaceRoleMember, status: domain.MemberStatusActive, present: true},
		{name: "guest", role: domain.WorkspaceRoleGuest, status: domain.MemberStatusActive, present: true},
		{name: "suspended admin", role: domain.WorkspaceRoleAdmin, status: domain.MemberStatusSuspended, present: true},
		{name: "left owner", role: domain.WorkspaceRoleOwner, status: domain.MemberStatusLeft, present: true},
		{name: "not a member", role: domain.WorkspaceRoleAdmin, status: domain.MemberStatusActive},
	} {
		t.Run(caller.name, func(t *testing.T) {
			for _, operation := range []struct {
				name       string
				method     string
				target     string
				body       string
				wantStatus int
			}{
				{
					name: "create", method: http.MethodPost, target: httpapi.RouteChannelCategories,
					body: `{"name":"Projetos"}`, wantStatus: http.StatusCreated,
				},
				{
					name: "rename", method: http.MethodPatch, target: "/api/chat/channel-categories/" + testCategoryID,
					body: `{"name":"Renomeada"}`, wantStatus: http.StatusOK,
				},
				{
					name: "reorder", method: http.MethodPut, target: httpapi.RouteChannelCategoriesOrder,
					body: `{"category_ids":["` + testCategoryID + `"]}`, wantStatus: http.StatusOK,
				},
				{
					name: "delete", method: http.MethodDelete, target: "/api/chat/channel-categories/" + testCategoryID,
					wantStatus: http.StatusNoContent,
				},
			} {
				t.Run(operation.name, func(t *testing.T) {
					env := newCategoryRouteEnv(t, allowAllSessionValidator{}, routeActiveWorkspace(),
						routeMember(caller.role, caller.status), caller.present,
						[]domain.ChannelCategory{routeCategory(testCategoryID, "Alfa", 0)}, nil)

					recorder := httptest.NewRecorder()
					env.router.ServeHTTP(recorder, categoryRequest(
						operation.method, operation.target, categoryRouteToken(t), operation.body,
					))

					if caller.allowed {
						if recorder.Code != operation.wantStatus {
							t.Fatalf("status = %d, want %d: %s", recorder.Code, operation.wantStatus, recorder.Body.String())
						}
						if env.categories.writes != 1 {
							t.Fatalf("writes = %d, want 1", env.categories.writes)
						}
						return
					}
					if recorder.Code != http.StatusForbidden {
						t.Fatalf("status = %d, want 403: %s", recorder.Code, recorder.Body.String())
					}
					if code := errorCode(t, recorder); code != "forbidden" {
						t.Fatalf("error code = %q, want forbidden", code)
					}
					if env.categories.writes != 0 {
						t.Fatal("a denied caller must not write")
					}
				})
			}
		})
	}
}

// A role claimed by the client must change nothing: the decision comes from the
// membership the server reads, and an unknown body field is refused outright.
func TestChannelCategoryRoute_ClientSuppliedRoleIsIgnored(t *testing.T) {
	for _, body := range []string{
		`{"name":"Projetos","role":"admin"}`,
		`{"name":"Projetos","workspace_role":"owner"}`,
	} {
		env := newCategoryRouteEnv(t, allowAllSessionValidator{}, routeActiveWorkspace(),
			routeMember(domain.WorkspaceRoleMember, domain.MemberStatusActive), true, nil, nil)

		recorder := httptest.NewRecorder()
		env.router.ServeHTTP(recorder, categoryRequest(
			http.MethodPost, httpapi.RouteChannelCategories, categoryRouteToken(t), body,
		))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("body %q: status = %d, want 400", body, recorder.Code)
		}
		if env.categories.writes != 0 {
			t.Fatal("a claimed role must not produce a write")
		}
	}
}

// A header claiming a role or a workspace is not read by anything.
func TestChannelCategoryRoute_ClientSuppliedHeadersAndQueryAreIgnored(t *testing.T) {
	env := newCategoryRouteEnv(t, allowAllSessionValidator{}, routeActiveWorkspace(),
		routeMember(domain.WorkspaceRoleMember, domain.MemberStatusActive), true, nil, nil)

	request := categoryRequest(http.MethodPost,
		httpapi.RouteChannelCategories+"?workspace_id="+otherWorkspaceIDForTest+"&role=admin",
		categoryRouteToken(t), `{"name":"Projetos"}`)
	request.Header.Set("X-Workspace-Role", "owner")
	request.Header.Set("X-Workspace-Id", otherWorkspaceIDForTest)

	recorder := httptest.NewRecorder()
	env.router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", recorder.Code, recorder.Body.String())
	}
	if env.categories.writes != 0 {
		t.Fatal("a claimed role must not produce a write")
	}
}

// A disabled workspace denies everything, read included.
func TestChannelCategoryRoute_DisabledWorkspaceDeniesEverything(t *testing.T) {
	disabled := domain.Workspace{ID: testWorkspaceID, Slug: "default", Status: domain.WorkspaceStatusDisabled}
	env := newCategoryRouteEnv(t, allowAllSessionValidator{}, disabled,
		routeMember(domain.WorkspaceRoleOwner, domain.MemberStatusActive), true, nil, nil)

	for _, route := range []struct {
		method string
		target string
		body   string
	}{
		{method: http.MethodGet, target: httpapi.RouteChannelCategories},
		{method: http.MethodPost, target: httpapi.RouteChannelCategories, body: `{"name":"Projetos"}`},
		{method: http.MethodDelete, target: "/api/chat/channel-categories/" + testCategoryID},
	} {
		recorder := httptest.NewRecorder()
		env.router.ServeHTTP(recorder, categoryRequest(route.method, route.target, categoryRouteToken(t), route.body))
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("%s %s: status = %d, want 403", route.method, route.target, recorder.Code)
		}
	}
	if env.categories.writes != 0 {
		t.Fatal("a disabled workspace must not write")
	}
}

// ── listing, end to end ───────────────────────────────────────────────────────

// The grouped listing must show exactly the channels the read policy returned,
// with the uncategorized ones under the virtual group — including #geral.
func TestChannelCategoryRoute_ListGroupsOnlyVisibleChannels(t *testing.T) {
	env := newCategoryRouteEnv(t, allowAllSessionValidator{}, routeActiveWorkspace(),
		routeMember(domain.WorkspaceRoleMember, domain.MemberStatusActive), true,
		[]domain.ChannelCategory{
			routeCategory(testCategoryID, "Projetos", 0),
			routeCategory(otherTestCategoryID, "Suporte", 1),
		},
		[]storage.VisibleChannelAccess{
			{Channel: domain.Channel{
				ID: "ch-geral", WorkspaceID: testWorkspaceID, Slug: "geral", DisplayName: "Geral",
				Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive, IsGeneral: true,
			}},
			{Channel: domain.Channel{
				ID: "ch-infra", WorkspaceID: testWorkspaceID, CategoryID: testCategoryID, Slug: "infra",
				DisplayName: "Infra", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive,
			}},
			{
				Channel: domain.Channel{
					ID: "ch-privado", WorkspaceID: testWorkspaceID, CategoryID: testCategoryID, Slug: "privado",
					DisplayName: "Privado", Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive,
				},
				ChannelMember: &domain.ChannelMember{ChannelID: "ch-privado", UserID: testUserID, Role: domain.ChannelRoleMember},
			},
		})

	recorder := httptest.NewRecorder()
	env.router.ServeHTTP(recorder, categoryRequest(http.MethodGet, httpapi.RouteChannelCategories, categoryRouteToken(t), ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}

	var body struct {
		Data struct {
			Groups []struct {
				Kind     string `json:"kind"`
				ID       string `json:"id"`
				Name     string `json:"name"`
				Channels []struct {
					ID string `json:"id"`
				} `json:"channels"`
			} `json:"groups"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode grouped listing: %v; body: %s", err, recorder.Body.String())
	}
	if len(body.Data.Groups) != 3 {
		t.Fatalf("groups = %d, want 3: %s", len(body.Data.Groups), recorder.Body.String())
	}

	virtual := body.Data.Groups[0]
	if virtual.Kind != "uncategorized" || virtual.ID != "" || virtual.Name != domain.UncategorizedGroupName {
		t.Fatalf("virtual group = %+v", virtual)
	}
	if len(virtual.Channels) != 1 || virtual.Channels[0].ID != "ch-geral" {
		t.Fatalf("#geral must be in the virtual group, got %+v", virtual.Channels)
	}
	if body.Data.Groups[1].ID != testCategoryID || len(body.Data.Groups[1].Channels) != 2 {
		t.Fatalf("first category = %+v", body.Data.Groups[1])
	}
	if body.Data.Groups[2].ID != otherTestCategoryID || len(body.Data.Groups[2].Channels) != 0 {
		t.Fatalf("second category = %+v", body.Data.Groups[2])
	}

	// A private channel the caller is not a member of is not in the response at
	// all — not inside a category and not in the virtual group.
	if strings.Contains(recorder.Body.String(), "ch-oculto") {
		t.Fatalf("an invisible channel leaked: %s", recorder.Body.String())
	}
}

// Deleting a category must leave its channels listed, under the virtual group.
func TestChannelCategoryRoute_DeleteKeepsChannelsUnderTheVirtualGroup(t *testing.T) {
	// The channel starts uncategorized here because the SET NULL that moves it is
	// the database's job; what this asserts is the API-visible consequence.
	env := newCategoryRouteEnv(t, allowAllSessionValidator{}, routeActiveWorkspace(),
		routeMember(domain.WorkspaceRoleAdmin, domain.MemberStatusActive), true,
		[]domain.ChannelCategory{routeCategory(testCategoryID, "Projetos", 0)},
		[]storage.VisibleChannelAccess{{Channel: domain.Channel{
			ID: "ch-infra", WorkspaceID: testWorkspaceID, Slug: "infra", DisplayName: "Infra",
			Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive,
		}}})

	deleteRecorder := httptest.NewRecorder()
	env.router.ServeHTTP(deleteRecorder, categoryRequest(
		http.MethodDelete, "/api/chat/channel-categories/"+testCategoryID, categoryRouteToken(t), "",
	))
	if deleteRecorder.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204: %s", deleteRecorder.Code, deleteRecorder.Body.String())
	}

	listRecorder := httptest.NewRecorder()
	env.router.ServeHTTP(listRecorder, categoryRequest(http.MethodGet, httpapi.RouteChannelCategories, categoryRouteToken(t), ""))
	if listRecorder.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200", listRecorder.Code)
	}
	if !strings.Contains(listRecorder.Body.String(), "ch-infra") {
		t.Fatalf("the channel must survive its category: %s", listRecorder.Body.String())
	}
	if strings.Contains(listRecorder.Body.String(), testCategoryID) {
		t.Fatalf("the deleted category must be gone: %s", listRecorder.Body.String())
	}
	if !strings.Contains(listRecorder.Body.String(), `"kind":"uncategorized"`) {
		t.Fatalf("the virtual group must remain: %s", listRecorder.Body.String())
	}
}

// ── conflicts and missing resources, end to end ───────────────────────────────

func TestChannelCategoryRoute_DuplicateNameIsConflict(t *testing.T) {
	env := newCategoryRouteEnv(t, allowAllSessionValidator{}, routeActiveWorkspace(),
		routeMember(domain.WorkspaceRoleAdmin, domain.MemberStatusActive), true,
		[]domain.ChannelCategory{routeCategory(otherTestCategoryID, "Projetos", 0)}, nil)

	// The collision is case-insensitive and it is never silenced.
	recorder := httptest.NewRecorder()
	env.router.ServeHTTP(recorder, categoryRequest(
		http.MethodPost, httpapi.RouteChannelCategories, categoryRouteToken(t), `{"name":"projetos"}`,
	))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", recorder.Code, recorder.Body.String())
	}
	if code := errorCode(t, recorder); code != "conflict" {
		t.Fatalf("error code = %q, want conflict", code)
	}
	if env.categories.writes != 0 {
		t.Fatal("a duplicate name must not write")
	}
}

func TestChannelCategoryRoute_ReservedNameIsRejected(t *testing.T) {
	for _, name := range []string{"Geral", "geral", " GERAL "} {
		env := newCategoryRouteEnv(t, allowAllSessionValidator{}, routeActiveWorkspace(),
			routeMember(domain.WorkspaceRoleAdmin, domain.MemberStatusActive), true, nil, nil)

		recorder := httptest.NewRecorder()
		env.router.ServeHTTP(recorder, categoryRequest(
			http.MethodPost, httpapi.RouteChannelCategories, categoryRouteToken(t), `{"name":"`+name+`"}`,
		))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("name %q: status = %d, want 400: %s", name, recorder.Code, recorder.Body.String())
		}
		if env.categories.writes != 0 {
			t.Fatal("the reserved name must not write")
		}
	}
}

// A category ID that is not in the caller's workspace answers 404, the same as one
// that does not exist anywhere. The status cannot be used to tell them apart.
func TestChannelCategoryRoute_UnknownCategoryIsNotFound(t *testing.T) {
	env := newCategoryRouteEnv(t, allowAllSessionValidator{}, routeActiveWorkspace(),
		routeMember(domain.WorkspaceRoleAdmin, domain.MemberStatusActive), true, nil, nil)

	for _, route := range []struct {
		method string
		body   string
	}{
		{method: http.MethodPatch, body: `{"name":"Renomeada"}`},
		{method: http.MethodDelete},
	} {
		recorder := httptest.NewRecorder()
		env.router.ServeHTTP(recorder, categoryRequest(
			route.method, "/api/chat/channel-categories/"+otherTestCategoryID, categoryRouteToken(t), route.body,
		))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s: status = %d, want 404: %s", route.method, recorder.Code, recorder.Body.String())
		}
		if code := errorCode(t, recorder); code != "not_found" {
			t.Fatalf("%s: error code = %q, want not_found", route.method, code)
		}
	}
}

func TestChannelCategoryRoute_ReorderRejectsAPartialSet(t *testing.T) {
	env := newCategoryRouteEnv(t, allowAllSessionValidator{}, routeActiveWorkspace(),
		routeMember(domain.WorkspaceRoleAdmin, domain.MemberStatusActive), true,
		[]domain.ChannelCategory{
			routeCategory(testCategoryID, "Alfa", 0),
			routeCategory(otherTestCategoryID, "Beta", 1),
		}, nil)

	for _, test := range []struct {
		name string
		body string
	}{
		{name: "subset", body: `{"category_ids":["` + testCategoryID + `"]}`},
		{name: "duplicate", body: `{"category_ids":["` + testCategoryID + `","` + testCategoryID + `"]}`},
		{name: "foreign id", body: `{"category_ids":["` + testCategoryID + `","` + otherWorkspaceIDForTest + `"]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			env.router.ServeHTTP(recorder, categoryRequest(
				http.MethodPut, httpapi.RouteChannelCategoriesOrder, categoryRouteToken(t), test.body,
			))
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", recorder.Code, recorder.Body.String())
			}
			if env.categories.writes != 0 {
				t.Fatal("an invalid order must not write")
			}
		})
	}
}

func TestChannelCategoryRoute_ReorderAppliesTheNewOrder(t *testing.T) {
	env := newCategoryRouteEnv(t, allowAllSessionValidator{}, routeActiveWorkspace(),
		routeMember(domain.WorkspaceRoleAdmin, domain.MemberStatusActive), true,
		[]domain.ChannelCategory{
			routeCategory(testCategoryID, "Alfa", 0),
			routeCategory(otherTestCategoryID, "Beta", 1),
		}, nil)

	recorder := httptest.NewRecorder()
	env.router.ServeHTTP(recorder, categoryRequest(
		http.MethodPut, httpapi.RouteChannelCategoriesOrder, categoryRouteToken(t),
		`{"category_ids":["`+otherTestCategoryID+`","`+testCategoryID+`"]}`,
	))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if env.categories.categories[0].ID != otherTestCategoryID || env.categories.categories[0].Position != 0 {
		t.Fatalf("stored order = %+v", env.categories.categories)
	}
	if env.categories.categories[1].ID != testCategoryID || env.categories.categories[1].Position != 1 {
		t.Fatalf("stored order = %+v", env.categories.categories)
	}
}

// ── route registration ────────────────────────────────────────────────────────

// Only the five intended method/path pairs exist.
//
// A wrong method answers 404, not 405: the router's catch-all "/" pattern matches
// before ServeMux can report a method mismatch, which is how every existing chat
// route already behaves and discloses less than a 405 would. What matters is that
// the method never reaches a handler and never writes.
func TestChannelCategoryRoute_RejectsUnsupportedMethods(t *testing.T) {
	env := newCategoryRouteEnv(t, allowAllSessionValidator{}, routeActiveWorkspace(),
		routeMember(domain.WorkspaceRoleAdmin, domain.MemberStatusActive), true, nil, nil)

	for _, route := range []struct {
		method string
		target string
	}{
		{method: http.MethodDelete, target: httpapi.RouteChannelCategories},
		{method: http.MethodPatch, target: httpapi.RouteChannelCategories},
		{method: http.MethodPost, target: httpapi.RouteChannelCategoriesOrder},
		{method: http.MethodGet, target: "/api/chat/channel-categories/" + testCategoryID},
		{method: http.MethodPost, target: "/api/chat/channel-categories/" + testCategoryID},
	} {
		t.Run(route.method+" "+route.target, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			env.router.ServeHTTP(recorder, categoryRequest(route.method, route.target, categoryRouteToken(t), ""))
			if recorder.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404: %s", recorder.Code, recorder.Body.String())
			}
			if env.categories.writes != 0 {
				t.Fatal("an unsupported method must not write")
			}
		})
	}
}

// PUT /order must reach the reorder handler rather than being captured by the
// {categoryID} pattern.
func TestChannelCategoryRoute_OrderIsNotTreatedAsACategoryID(t *testing.T) {
	env := newCategoryRouteEnv(t, allowAllSessionValidator{}, routeActiveWorkspace(),
		routeMember(domain.WorkspaceRoleAdmin, domain.MemberStatusActive), true,
		[]domain.ChannelCategory{routeCategory(testCategoryID, "Alfa", 0)}, nil)

	recorder := httptest.NewRecorder()
	env.router.ServeHTTP(recorder, categoryRequest(
		http.MethodPut, httpapi.RouteChannelCategoriesOrder, categoryRouteToken(t),
		`{"category_ids":["`+testCategoryID+`"]}`,
	))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
}

// Without the handler wired the routes must not exist at all: a 404, not a
// misleading 503 on a route the build does not serve.
func TestChannelCategoryRoute_UnwiredHandlerLeavesRoutesUnregistered(t *testing.T) {
	router := httpapi.NewRouter(
		sidebarTestConfig(), nil, httpapi.ReadinessState{}, makeTestValidator(t), allowAllSessionValidator{},
		httpapi.NewSidebarHandler(nil), httpapi.NewMessageHandler(nil, nil, nil), nil, nil, nil, nil, nil,
	)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, categoryRequest(http.MethodGet, httpapi.RouteChannelCategories, categoryRouteToken(t), ""))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", recorder.Code, recorder.Body.String())
	}
}
