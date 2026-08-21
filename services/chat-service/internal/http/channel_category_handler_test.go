package httpapi_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	httpapi "github.com/nicrepository/nchat/services/chat-service/internal/http"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
)

const (
	testCategoryID      = "88888888-8888-8888-8888-888888888888"
	otherTestCategoryID = "99999999-9999-9999-9999-999999999999"
	// Only ever sent in a request body, to prove the body cannot redirect an
	// operation at another workspace.
	otherWorkspaceIDForTest = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
)

// fakeChannelCategoryProvider records what reached the service and returns what
// the test wants back. Every test also asserts the call count, so "the handler
// rejected it" is distinguishable from "the service rejected it".
type fakeChannelCategoryProvider struct {
	groups     []service.ChannelCategoryGroup
	category   domain.ChannelCategory
	categories []domain.ChannelCategory

	listErr    error
	createErr  error
	renameErr  error
	reorderErr error
	deleteErr  error

	lastCreate  service.CreateChannelCategoryInput
	lastRename  service.RenameChannelCategoryInput
	lastReorder service.ReorderChannelCategoriesInput
	lastDelete  [3]string
	calls       int
}

func (f *fakeChannelCategoryProvider) ListGroupedChannels(_ context.Context, _, _ string) ([]service.ChannelCategoryGroup, error) {
	f.calls++
	return f.groups, f.listErr
}

func (f *fakeChannelCategoryProvider) CreateChannelCategory(_ context.Context, input service.CreateChannelCategoryInput) (domain.ChannelCategory, error) {
	f.calls++
	f.lastCreate = input
	return f.category, f.createErr
}

func (f *fakeChannelCategoryProvider) RenameChannelCategory(_ context.Context, input service.RenameChannelCategoryInput) (domain.ChannelCategory, error) {
	f.calls++
	f.lastRename = input
	return f.category, f.renameErr
}

func (f *fakeChannelCategoryProvider) ReorderChannelCategories(_ context.Context, input service.ReorderChannelCategoriesInput) ([]domain.ChannelCategory, error) {
	f.calls++
	f.lastReorder = input
	return f.categories, f.reorderErr
}

func (f *fakeChannelCategoryProvider) DeleteChannelCategory(_ context.Context, workspaceID, categoryID, callerID string) error {
	f.calls++
	f.lastDelete = [3]string{workspaceID, categoryID, callerID}
	return f.deleteErr
}

func categoryHandler(provider *fakeChannelCategoryProvider) *httpapi.ChannelCategoryHandler {
	return httpapi.NewChannelCategoryHandler(
		&fakeWorkspaceResolver{workspace: activeWorkspace()}, provider, &fakeDMRateLimiter{},
	)
}

func jsonRequest(method, target, body string) *http.Request {
	r := requestWithUser(method, target, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r
}

// categoryPathRequest builds a request with {categoryID} already resolved, the way
// the router's pattern would.
func categoryPathRequest(method, categoryID, body string) *http.Request {
	r := jsonRequest(method, "/api/chat/channel-categories/"+categoryID, body)
	r.SetPathValue("categoryID", categoryID)
	return r
}

func serveCategory(handler http.HandlerFunc, r *http.Request) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	handler(recorder, r)
	return recorder
}

// ── authentication and wiring ─────────────────────────────────────────────────

func TestChannelCategoryHandler_RequiresAuthenticatedUser(t *testing.T) {
	for _, test := range []struct {
		name    string
		invoke  func(h *httpapi.ChannelCategoryHandler) http.HandlerFunc
		request func() *http.Request
	}{
		{name: "list", invoke: func(h *httpapi.ChannelCategoryHandler) http.HandlerFunc { return h.List },
			request: func() *http.Request {
				return httptest.NewRequest(http.MethodGet, httpapi.RouteChannelCategories, nil)
			}},
		{name: "create", invoke: func(h *httpapi.ChannelCategoryHandler) http.HandlerFunc { return h.Create },
			request: func() *http.Request {
				r := httptest.NewRequest(http.MethodPost, httpapi.RouteChannelCategories, strings.NewReader(`{"name":"X"}`))
				r.Header.Set("Content-Type", "application/json")
				return r
			}},
		{name: "reorder", invoke: func(h *httpapi.ChannelCategoryHandler) http.HandlerFunc { return h.Reorder },
			request: func() *http.Request {
				r := httptest.NewRequest(http.MethodPut, httpapi.RouteChannelCategoriesOrder, strings.NewReader(`{"category_ids":["`+testCategoryID+`"]}`))
				r.Header.Set("Content-Type", "application/json")
				return r
			}},
		{name: "rename", invoke: func(h *httpapi.ChannelCategoryHandler) http.HandlerFunc { return h.Rename },
			request: func() *http.Request {
				r := httptest.NewRequest(http.MethodPatch, "/api/chat/channel-categories/"+testCategoryID, strings.NewReader(`{"name":"X"}`))
				r.Header.Set("Content-Type", "application/json")
				r.SetPathValue("categoryID", testCategoryID)
				return r
			}},
		{name: "delete", invoke: func(h *httpapi.ChannelCategoryHandler) http.HandlerFunc { return h.Delete },
			request: func() *http.Request {
				r := httptest.NewRequest(http.MethodDelete, "/api/chat/channel-categories/"+testCategoryID, nil)
				r.SetPathValue("categoryID", testCategoryID)
				return r
			}},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := &fakeChannelCategoryProvider{}
			recorder := serveCategory(test.invoke(categoryHandler(provider)), test.request())
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", recorder.Code)
			}
			if provider.calls != 0 {
				t.Fatal("an unauthenticated request must not reach the service")
			}
		})
	}
}

func TestChannelCategoryHandler_UnwiredDependenciesAnswer503(t *testing.T) {
	handler := httpapi.NewChannelCategoryHandler(nil, nil, nil)
	for name, invoke := range map[string]http.HandlerFunc{
		"list":   handler.List,
		"create": handler.Create,
	} {
		t.Run(name, func(t *testing.T) {
			recorder := serveCategory(invoke, jsonRequest(http.MethodGet, httpapi.RouteChannelCategories, ""))
			if recorder.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503", recorder.Code)
			}
		})
	}
}

// ── listing ───────────────────────────────────────────────────────────────────

// The virtual group must be distinguishable from a persisted category without a
// fabricated ID: it carries kind=uncategorized and no id at all.
func TestChannelCategoryHandler_List_DistinguishesTheVirtualGroup(t *testing.T) {
	provider := &fakeChannelCategoryProvider{groups: []service.ChannelCategoryGroup{
		{Channels: []service.SidebarChannel{{
			Channel:  domain.Channel{ID: "ch-geral", Slug: "geral", DisplayName: "Geral", Type: domain.ChannelTypePublic, IsGeneral: true},
			CanWrite: true,
		}}},
		{
			Category: &domain.ChannelCategory{ID: testCategoryID, Name: "Projetos", Position: 0},
			Channels: []service.SidebarChannel{{
				Channel:  domain.Channel{ID: "ch-a", Slug: "alfa", DisplayName: "Alfa", Type: domain.ChannelTypePrivate},
				CanWrite: false,
			}},
		},
	}}

	recorder := serveCategory(categoryHandler(provider).List, requestWithUser(http.MethodGet, httpapi.RouteChannelCategories, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	body := decodeBody(t, recorder)
	data, ok := body["data"].(map[string]any)
	if !ok {
		t.Fatalf("missing data envelope: %v", body)
	}
	groups, ok := data["groups"].([]any)
	if !ok || len(groups) != 2 {
		t.Fatalf("groups = %v", data["groups"])
	}

	virtual := groups[0].(map[string]any)
	if virtual["kind"] != "uncategorized" {
		t.Fatalf("virtual kind = %v, want uncategorized", virtual["kind"])
	}
	if _, present := virtual["id"]; present {
		t.Fatalf("the virtual group must carry no id, got %v", virtual["id"])
	}
	if _, present := virtual["position"]; present {
		t.Fatalf("the virtual group must carry no position, got %v", virtual["position"])
	}
	if virtual["name"] != domain.UncategorizedGroupName {
		t.Fatalf("virtual name = %v, want %q", virtual["name"], domain.UncategorizedGroupName)
	}

	persisted := groups[1].(map[string]any)
	if persisted["kind"] != "category" {
		t.Fatalf("persisted kind = %v, want category", persisted["kind"])
	}
	if persisted["id"] != testCategoryID {
		t.Fatalf("persisted id = %v", persisted["id"])
	}
	if persisted["name"] != "Projetos" {
		t.Fatalf("persisted name = %v", persisted["name"])
	}

	// The channel shape is the sidebar's, including the server-derived can_write.
	channels := persisted["channels"].([]any)
	channel := channels[0].(map[string]any)
	for _, field := range []string{"id", "slug", "display_name", "type", "is_general", "can_write"} {
		if _, present := channel[field]; !present {
			t.Fatalf("channel is missing %q: %v", field, channel)
		}
	}
	if channel["can_write"] != false {
		t.Fatalf("can_write = %v, want false", channel["can_write"])
	}
	if _, present := channel["unread_count"]; present {
		t.Fatalf("category projection must not publish a non-authoritative unread count: %v", channel)
	}
}

// A group with no channel must serialise as [] rather than null, so a client never
// has to handle both.
func TestChannelCategoryHandler_List_EmptyChannelsAreArrays(t *testing.T) {
	provider := &fakeChannelCategoryProvider{groups: []service.ChannelCategoryGroup{{}}}
	recorder := serveCategory(categoryHandler(provider).List, requestWithUser(http.MethodGet, httpapi.RouteChannelCategories, nil))
	if !strings.Contains(recorder.Body.String(), `"channels":[]`) {
		t.Fatalf("expected an empty channel array: %s", recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "null") {
		t.Fatalf("response must not contain null: %s", recorder.Body.String())
	}
}

func TestChannelCategoryHandler_List_ServiceErrorsMapToStatuses(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want int
	}{
		{name: "forbidden", err: domain.ErrForbidden, want: http.StatusForbidden},
		{name: "workspace missing", err: domain.ErrNotFound, want: http.StatusNotFound},
		{name: "unexpected", err: errors.New("boom"), want: http.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := &fakeChannelCategoryProvider{listErr: test.err}
			recorder := serveCategory(categoryHandler(provider).List, requestWithUser(http.MethodGet, httpapi.RouteChannelCategories, nil))
			if recorder.Code != test.want {
				t.Fatalf("status = %d, want %d", recorder.Code, test.want)
			}
			if strings.Contains(recorder.Body.String(), "boom") {
				t.Fatalf("internal detail leaked: %s", recorder.Body.String())
			}
		})
	}
}

// ── creation ──────────────────────────────────────────────────────────────────

// The workspace and the caller come from the server, never from the body.
func TestChannelCategoryHandler_Create_DerivesCallerAndWorkspaceServerSide(t *testing.T) {
	provider := &fakeChannelCategoryProvider{
		category: domain.ChannelCategory{ID: testCategoryID, Name: "Projetos", Position: 2},
	}
	recorder := serveCategory(categoryHandler(provider).Create,
		jsonRequest(http.MethodPost, httpapi.RouteChannelCategories, `{"name":"Projetos"}`))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", recorder.Code, recorder.Body.String())
	}
	want := service.CreateChannelCategoryInput{
		WorkspaceID: testWorkspaceID, CallerID: msgTestUserID, Name: "Projetos",
	}
	if provider.lastCreate != want {
		t.Fatalf("service input = %+v, want %+v", provider.lastCreate, want)
	}
	body := decodeBody(t, recorder)
	data := body["data"].(map[string]any)
	if data["id"] != testCategoryID || data["name"] != "Projetos" || data["position"] != float64(2) {
		t.Fatalf("response = %v", data)
	}
}

// A body that carries a workspace, a position, an id or a role is rejected
// outright: the decoder disallows unknown fields, so a payload cannot claim
// anything the server derives.
func TestChannelCategoryHandler_Create_RejectsServerDerivedAndUnknownFields(t *testing.T) {
	for _, body := range []string{
		`{"name":"X","workspace_id":"` + otherWorkspaceIDForTest + `"}`,
		`{"name":"X","position":0}`,
		`{"name":"X","id":"` + testCategoryID + `"}`,
		`{"name":"X","role":"admin"}`,
		`{"name":"X","caller_id":"someone-else"}`,
		`{"name":"X","created_at":"2026-01-01T00:00:00Z"}`,
	} {
		t.Run(body, func(t *testing.T) {
			provider := &fakeChannelCategoryProvider{}
			recorder := serveCategory(categoryHandler(provider).Create,
				jsonRequest(http.MethodPost, httpapi.RouteChannelCategories, body))
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", recorder.Code)
			}
			if provider.calls != 0 {
				t.Fatal("a rejected body must not reach the service")
			}
		})
	}
}

func TestChannelCategoryHandler_Create_RejectsMalformedBodies(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "empty", body: ``},
		{name: "not json", body: `not json`},
		{name: "truncated", body: `{"name":`},
		{name: "wrong type", body: `{"name":123}`},
		{name: "array", body: `[{"name":"X"}]`},
		{name: "trailing garbage", body: `{"name":"X"}{"name":"Y"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := &fakeChannelCategoryProvider{}
			recorder := serveCategory(categoryHandler(provider).Create,
				jsonRequest(http.MethodPost, httpapi.RouteChannelCategories, test.body))
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", recorder.Code)
			}
			if provider.calls != 0 {
				t.Fatal("a malformed body must not reach the service")
			}
		})
	}
}

// A body over the shared 64 KiB cap is refused before the service sees it.
func TestChannelCategoryHandler_Create_RejectsOversizedBody(t *testing.T) {
	provider := &fakeChannelCategoryProvider{}
	oversized := fmt.Sprintf(`{"name":%q}`, strings.Repeat("a", 1<<17))
	recorder := serveCategory(categoryHandler(provider).Create,
		jsonRequest(http.MethodPost, httpapi.RouteChannelCategories, oversized))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
	if provider.calls != 0 {
		t.Fatal("an oversized body must not reach the service")
	}
}

func TestChannelCategoryHandler_Create_RequiresJSONContentType(t *testing.T) {
	for _, contentType := range []string{"", "text/plain", "application/x-www-form-urlencoded"} {
		provider := &fakeChannelCategoryProvider{}
		request := requestWithUser(http.MethodPost, httpapi.RouteChannelCategories, strings.NewReader(`{"name":"X"}`))
		if contentType != "" {
			request.Header.Set("Content-Type", contentType)
		}
		recorder := serveCategory(categoryHandler(provider).Create, request)
		if recorder.Code != http.StatusUnsupportedMediaType {
			t.Fatalf("content type %q: status = %d, want 415", contentType, recorder.Code)
		}
		if provider.calls != 0 {
			t.Fatal("a wrong content type must not reach the service")
		}
	}
}

func TestChannelCategoryHandler_Create_ServiceErrorsMapToStatuses(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want int
	}{
		{name: "empty name", err: domain.ErrChannelCategoryNameRequired, want: http.StatusBadRequest},
		{name: "name too long", err: domain.ErrChannelCategoryNameTooLong, want: http.StatusBadRequest},
		{name: "control characters", err: domain.ErrChannelCategoryNameInvalid, want: http.StatusBadRequest},
		{name: "reserved name", err: domain.ErrChannelCategoryNameReserved, want: http.StatusBadRequest},
		{name: "duplicate name", err: domain.ErrDuplicateChannelCategoryName, want: http.StatusConflict},
		{name: "limit reached", err: domain.ErrChannelCategoryLimitReached, want: http.StatusConflict},
		{name: "bare conflict", err: domain.ErrConflict, want: http.StatusConflict},
		{name: "not a manager", err: domain.ErrForbidden, want: http.StatusForbidden},
		{name: "unexpected", err: errors.New("boom"), want: http.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := &fakeChannelCategoryProvider{createErr: test.err}
			recorder := serveCategory(categoryHandler(provider).Create,
				jsonRequest(http.MethodPost, httpapi.RouteChannelCategories, `{"name":"Projetos"}`))
			if recorder.Code != test.want {
				t.Fatalf("status = %d, want %d", recorder.Code, test.want)
			}
			// No SQL text, constraint name or rejected value in any denial.
			for _, forbidden := range []string{"boom", "chat.channel_categories", "constraint", "SELECT", "INSERT"} {
				if strings.Contains(recorder.Body.String(), forbidden) {
					t.Fatalf("response leaks %q: %s", forbidden, recorder.Body.String())
				}
			}
		})
	}
}

func TestChannelCategoryHandler_Create_RateLimitReturns429WithRetryAfter(t *testing.T) {
	limiter := &fakeDMRateLimiter{}
	provider := &fakeChannelCategoryProvider{category: domain.ChannelCategory{ID: testCategoryID}}
	handler := httpapi.NewChannelCategoryHandler(
		&fakeWorkspaceResolver{workspace: activeWorkspace()}, provider, limiter,
	)
	var recorder *httptest.ResponseRecorder
	// One request past the shared write budget.
	for i := 0; i < 21; i++ {
		recorder = serveCategory(handler.Create, jsonRequest(http.MethodPost, httpapi.RouteChannelCategories, `{"name":"Projetos"}`))
	}
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", recorder.Code)
	}
	if recorder.Header().Get("Retry-After") == "" {
		t.Fatal("429 must carry Retry-After")
	}
}

func TestChannelCategoryHandler_Create_LimiterFailureIsUnavailable(t *testing.T) {
	provider := &fakeChannelCategoryProvider{}
	handler := httpapi.NewChannelCategoryHandler(
		&fakeWorkspaceResolver{workspace: activeWorkspace()},
		provider,
		&fakeDMRateLimiter{err: errors.New("valkey down")},
	)
	recorder := serveCategory(handler.Create, jsonRequest(http.MethodPost, httpapi.RouteChannelCategories, `{"name":"X"}`))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
	if provider.calls != 0 {
		t.Fatal("an unavailable limiter must not let the write through")
	}
	if strings.Contains(recorder.Body.String(), "valkey") {
		t.Fatalf("infrastructure detail leaked: %s", recorder.Body.String())
	}
}

func TestChannelCategoryHandler_WorkspaceLookupFailures(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want int
	}{
		{name: "workspace missing", err: domain.ErrNotFound, want: http.StatusNotFound},
		{name: "lookup fails", err: errors.New("boom"), want: http.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := &fakeChannelCategoryProvider{}
			handler := httpapi.NewChannelCategoryHandler(
				&fakeWorkspaceResolver{err: test.err}, provider, &fakeDMRateLimiter{},
			)
			listRecorder := serveCategory(handler.List, requestWithUser(http.MethodGet, httpapi.RouteChannelCategories, nil))
			if listRecorder.Code != test.want {
				t.Fatalf("list status = %d, want %d", listRecorder.Code, test.want)
			}
			createRecorder := serveCategory(handler.Create, jsonRequest(http.MethodPost, httpapi.RouteChannelCategories, `{"name":"X"}`))
			if createRecorder.Code != test.want {
				t.Fatalf("create status = %d, want %d", createRecorder.Code, test.want)
			}
			if provider.calls != 0 {
				t.Fatal("a failed workspace lookup must not reach the service")
			}
		})
	}
}

// ── rename ────────────────────────────────────────────────────────────────────

func TestChannelCategoryHandler_Rename_Success(t *testing.T) {
	provider := &fakeChannelCategoryProvider{
		category: domain.ChannelCategory{ID: testCategoryID, Name: "Renomeada", Position: 1},
	}
	recorder := serveCategory(categoryHandler(provider).Rename,
		categoryPathRequest(http.MethodPatch, testCategoryID, `{"name":"Renomeada"}`))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	want := service.RenameChannelCategoryInput{
		WorkspaceID: testWorkspaceID, CallerID: msgTestUserID, CategoryID: testCategoryID, Name: "Renomeada",
	}
	if provider.lastRename != want {
		t.Fatalf("service input = %+v, want %+v", provider.lastRename, want)
	}
}

func TestChannelCategoryHandler_Rename_RejectsMalformedCategoryID(t *testing.T) {
	for _, categoryID := range []string{"", "not-a-uuid", "12345", testCategoryID + "x", "../" + testCategoryID} {
		t.Run(categoryID, func(t *testing.T) {
			provider := &fakeChannelCategoryProvider{}
			recorder := serveCategory(categoryHandler(provider).Rename,
				categoryPathRequest(http.MethodPatch, categoryID, `{"name":"X"}`))
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", recorder.Code)
			}
			if provider.calls != 0 {
				t.Fatal("a malformed ID must not reach the service")
			}
		})
	}
}

// A category in another workspace is 404, exactly like one that does not exist.
func TestChannelCategoryHandler_Rename_ServiceErrorsMapToStatuses(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want int
	}{
		{name: "category of another workspace", err: domain.ErrNotFound, want: http.StatusNotFound},
		{name: "duplicate name", err: domain.ErrDuplicateChannelCategoryName, want: http.StatusConflict},
		{name: "invalid name", err: domain.ErrChannelCategoryNameRequired, want: http.StatusBadRequest},
		{name: "not a manager", err: domain.ErrForbidden, want: http.StatusForbidden},
		{name: "unexpected", err: errors.New("boom"), want: http.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := &fakeChannelCategoryProvider{renameErr: test.err}
			recorder := serveCategory(categoryHandler(provider).Rename,
				categoryPathRequest(http.MethodPatch, otherTestCategoryID, `{"name":"X"}`))
			if recorder.Code != test.want {
				t.Fatalf("status = %d, want %d", recorder.Code, test.want)
			}
		})
	}
}

// ── reorder ───────────────────────────────────────────────────────────────────

func TestChannelCategoryHandler_Reorder_Success(t *testing.T) {
	provider := &fakeChannelCategoryProvider{categories: []domain.ChannelCategory{
		{ID: otherTestCategoryID, Name: "Beta", Position: 0},
		{ID: testCategoryID, Name: "Alfa", Position: 1},
	}}
	body := `{"category_ids":["` + otherTestCategoryID + `","` + testCategoryID + `"]}`
	recorder := serveCategory(categoryHandler(provider).Reorder,
		jsonRequest(http.MethodPut, httpapi.RouteChannelCategoriesOrder, body))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if provider.lastReorder.WorkspaceID != testWorkspaceID || provider.lastReorder.CallerID != msgTestUserID {
		t.Fatalf("service input = %+v", provider.lastReorder)
	}
	if len(provider.lastReorder.OrderedIDs) != 2 || provider.lastReorder.OrderedIDs[0] != otherTestCategoryID {
		t.Fatalf("ordered IDs = %v", provider.lastReorder.OrderedIDs)
	}
	// The new order comes back so a client does not have to guess it.
	data := decodeBody(t, recorder)["data"].(map[string]any)
	categories := data["categories"].([]any)
	if len(categories) != 2 || categories[0].(map[string]any)["id"] != otherTestCategoryID {
		t.Fatalf("response = %v", categories)
	}
}

func TestChannelCategoryHandler_Reorder_RejectsInvalidPayloads(t *testing.T) {
	tooMany := make([]string, 0, domain.MaxCategoriesPerWorkspace+1)
	for i := 0; i <= domain.MaxCategoriesPerWorkspace; i++ {
		tooMany = append(tooMany, `"`+testCategoryID+`"`)
	}
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "empty list", body: `{"category_ids":[]}`},
		{name: "missing field", body: `{}`},
		{name: "null list", body: `{"category_ids":null}`},
		{name: "over the ceiling", body: `{"category_ids":[` + strings.Join(tooMany, ",") + `]}`},
		{name: "non-uuid id", body: `{"category_ids":["not-a-uuid"]}`},
		{name: "empty id", body: `{"category_ids":[""]}`},
		{name: "one bad id among good ones", body: `{"category_ids":["` + testCategoryID + `","nope"]}`},
		{name: "unknown field", body: `{"category_ids":["` + testCategoryID + `"],"workspace_id":"x"}`},
		{name: "wrong type", body: `{"category_ids":"` + testCategoryID + `"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := &fakeChannelCategoryProvider{}
			recorder := serveCategory(categoryHandler(provider).Reorder,
				jsonRequest(http.MethodPut, httpapi.RouteChannelCategoriesOrder, test.body))
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", recorder.Code, recorder.Body.String())
			}
			if provider.calls != 0 {
				t.Fatal("an invalid payload must not reach the service")
			}
		})
	}
}

func TestChannelCategoryHandler_Reorder_ServiceErrorsMapToStatuses(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want int
	}{
		{name: "set mismatch", err: domain.ErrInvalidChannelCategoryOrder, want: http.StatusBadRequest},
		{name: "not a manager", err: domain.ErrForbidden, want: http.StatusForbidden},
		{name: "unexpected", err: errors.New("boom"), want: http.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := &fakeChannelCategoryProvider{reorderErr: test.err}
			recorder := serveCategory(categoryHandler(provider).Reorder,
				jsonRequest(http.MethodPut, httpapi.RouteChannelCategoriesOrder, `{"category_ids":["`+testCategoryID+`"]}`))
			if recorder.Code != test.want {
				t.Fatalf("status = %d, want %d", recorder.Code, test.want)
			}
		})
	}
}

// ── delete ────────────────────────────────────────────────────────────────────

// DELETE carries no body, so it must not require a JSON content type.
func TestChannelCategoryHandler_Delete_Success(t *testing.T) {
	provider := &fakeChannelCategoryProvider{}
	request := requestWithUser(http.MethodDelete, "/api/chat/channel-categories/"+testCategoryID, nil)
	request.SetPathValue("categoryID", testCategoryID)

	recorder := serveCategory(categoryHandler(provider).Delete, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("204 must carry no body, got %q", recorder.Body.String())
	}
	if provider.lastDelete != [3]string{testWorkspaceID, testCategoryID, msgTestUserID} {
		t.Fatalf("service input = %v", provider.lastDelete)
	}
}

func TestChannelCategoryHandler_Delete_RejectsMalformedCategoryID(t *testing.T) {
	provider := &fakeChannelCategoryProvider{}
	request := requestWithUser(http.MethodDelete, "/api/chat/channel-categories/nope", nil)
	request.SetPathValue("categoryID", "nope")

	recorder := serveCategory(categoryHandler(provider).Delete, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
	if provider.calls != 0 {
		t.Fatal("a malformed ID must not reach the service")
	}
}

func TestChannelCategoryHandler_Delete_ServiceErrorsMapToStatuses(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want int
	}{
		{name: "category of another workspace", err: domain.ErrNotFound, want: http.StatusNotFound},
		{name: "not a manager", err: domain.ErrForbidden, want: http.StatusForbidden},
		{name: "unexpected", err: errors.New("boom"), want: http.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := &fakeChannelCategoryProvider{deleteErr: test.err}
			request := requestWithUser(http.MethodDelete, "/api/chat/channel-categories/"+otherTestCategoryID, nil)
			request.SetPathValue("categoryID", otherTestCategoryID)

			recorder := serveCategory(categoryHandler(provider).Delete, request)
			if recorder.Code != test.want {
				t.Fatalf("status = %d, want %d", recorder.Code, test.want)
			}
		})
	}
}
