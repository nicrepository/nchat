package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/uploadpolicy"
	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
	"github.com/nicrepository/nchat/services/admin-service/internal/service"
)

const (
	targetUserID    = "44444444-4444-4444-4444-444444444444"
	testChannelID   = "55555555-5555-5555-5555-555555555555"
	testWorkspaceID = "66666666-6666-6666-6666-666666666666"
)

// managementStub stands in for the three management services. It records what
// it was asked and answers with whatever a spec set up, so the specs below are
// about the router, the guards and the contract rather than about SQL.
type managementStub struct {
	userFilter    domain.AdminUserFilter
	channelFilter domain.AdminChannelFilter
	convFilter    domain.AdminConversationFilter
	calls         []string

	users          []domain.AdminUserSummary
	nextCursor     string
	detail         domain.AdminUserDetail
	change         domain.UserStatusChange
	revoked        int
	channels       []domain.AdminChannelSummary
	channelDetail  domain.AdminChannelDetail
	channel        domain.AdminChannelSummary
	membership     domain.ChannelMembershipChange
	addedUserIDs   []string
	candidates     []domain.ChannelMemberCandidate
	candidateQuery string
	conversations  []domain.AdminConversationSummary
	antiSpam       []domain.AntiSpamPolicy
	uploads        []domain.UploadPolicy

	err error
}

func (m *managementStub) List(_ context.Context, filter domain.AdminUserFilter) (domain.Page[domain.AdminUserSummary], error) {
	m.calls = append(m.calls, "users.list")
	m.userFilter = filter
	return domain.Page[domain.AdminUserSummary]{Items: m.users, NextCursor: m.nextCursor}, m.err
}

func (m *managementStub) Get(_ context.Context, userID string) (domain.AdminUserDetail, error) {
	m.calls = append(m.calls, "users.get:"+userID)
	return m.detail, m.err
}

func (m *managementStub) SetStatus(_ context.Context, _ service.Actor, userID, status string) (domain.UserStatusChange, error) {
	m.calls = append(m.calls, "users.status:"+userID+":"+status)
	if m.err != nil {
		return domain.UserStatusChange{}, m.err
	}
	change := m.change
	change.TargetUserID = userID
	change.ToStatus = status
	return change, nil
}

func (m *managementStub) RevokeSessions(_ context.Context, _ service.Actor, userID string) (int, error) {
	m.calls = append(m.calls, "users.revoke:"+userID)
	return m.revoked, m.err
}

func (m *managementStub) GrantRole(_ context.Context, _ service.Actor, userID, slug string) error {
	m.calls = append(m.calls, "users.grant:"+userID+":"+slug)
	return m.err
}

func (m *managementStub) RevokeRole(_ context.Context, _ service.Actor, userID, slug string) error {
	m.calls = append(m.calls, "users.revokeRole:"+userID+":"+slug)
	return m.err
}

func (m *managementStub) ListChannels(_ context.Context, filter domain.AdminChannelFilter) (domain.Page[domain.AdminChannelSummary], error) {
	m.calls = append(m.calls, "channels.list")
	m.channelFilter = filter
	return domain.Page[domain.AdminChannelSummary]{Items: m.channels, NextCursor: m.nextCursor}, m.err
}

func (m *managementStub) GetChannel(_ context.Context, id string) (domain.AdminChannelDetail, error) {
	m.calls = append(m.calls, "channels.get:"+id)
	return m.channelDetail, m.err
}

func (m *managementStub) SetChannelStatus(_ context.Context, _ service.Actor, id, status string) (domain.AdminChannelSummary, error) {
	m.calls = append(m.calls, "channels.status:"+id+":"+status)
	return m.channel, m.err
}

func (m *managementStub) MemberCandidates(_ context.Context, channelID, query string) ([]domain.ChannelMemberCandidate, error) {
	m.calls = append(m.calls, "channels.candidates:"+channelID)
	m.candidateQuery = query
	return m.candidates, m.err
}

func (m *managementStub) AddMembers(_ context.Context, _ service.Actor, channelID string, userIDs []string) (domain.ChannelMembershipChange, error) {
	m.calls = append(m.calls, "channels.addMembers:"+channelID)
	m.addedUserIDs = userIDs
	if m.err != nil {
		return domain.ChannelMembershipChange{}, m.err
	}
	change := m.membership
	change.ChannelID = channelID
	change.Added = len(userIDs)
	return change, nil
}

func (m *managementStub) RemoveMember(_ context.Context, _ service.Actor, channelID, userID string) (domain.ChannelMembershipChange, error) {
	m.calls = append(m.calls, "channels.removeMember:"+channelID+":"+userID)
	if m.err != nil {
		return domain.ChannelMembershipChange{}, m.err
	}
	change := m.membership
	change.ChannelID = channelID
	change.Removed = true
	return change, nil
}

func (m *managementStub) ListConversations(_ context.Context, filter domain.AdminConversationFilter) (domain.Page[domain.AdminConversationSummary], error) {
	m.calls = append(m.calls, "conversations.list")
	m.convFilter = filter
	return domain.Page[domain.AdminConversationSummary]{Items: m.conversations, NextCursor: m.nextCursor}, m.err
}

func (m *managementStub) ListAntiSpam(_ context.Context, _ domain.Cursor, _ int) (domain.Page[domain.AntiSpamPolicy], error) {
	m.calls = append(m.calls, "policies.antispam.list")
	return domain.Page[domain.AntiSpamPolicy]{Items: m.antiSpam, NextCursor: m.nextCursor}, m.err
}

func (m *managementStub) ListUpload(_ context.Context, _ domain.Cursor, _ int) (domain.Page[domain.UploadPolicy], error) {
	m.calls = append(m.calls, "policies.upload.list")
	return domain.Page[domain.UploadPolicy]{Items: m.uploads, NextCursor: m.nextCursor}, m.err
}

func (m *managementStub) UpdateAntiSpam(_ context.Context, _ service.Actor, workspaceID string, value int) (domain.AntiSpamPolicy, error) {
	m.calls = append(m.calls, "policies.antispam.update:"+workspaceID)
	if m.err != nil {
		return domain.AntiSpamPolicy{}, m.err
	}
	return domain.AntiSpamPolicy{
		Workspace:                 domain.WorkspaceRef{ID: workspaceID, Slug: "default", Name: "NChat", Status: "active"},
		MessageRateLimitPerMinute: value,
	}, nil
}

func (m *managementStub) UpdateUpload(_ context.Context, _ service.Actor, workspaceID string, value int64) (domain.UploadPolicy, error) {
	m.calls = append(m.calls, "policies.upload.update:"+workspaceID)
	if m.err != nil {
		return domain.UploadPolicy{}, m.err
	}
	return domain.UploadPolicy{
		Workspace:      domain.WorkspaceRef{ID: workspaceID, Slug: "default", Name: "NChat", Status: "active"},
		MaxUploadBytes: value,
	}, nil
}

// channelAdapter bridges the stub's SetChannelStatus onto the ChannelAdmin
// interface, whose method is called SetStatus like UserAdmin's. One stub cannot
// carry two methods of the same name, so the adapter renames one of them here
// rather than splitting the recorder into two objects a spec would have to
// assert against separately.
type channelAdapter struct{ *managementStub }

func (c channelAdapter) SetStatus(ctx context.Context, actor service.Actor, id, status string) (domain.AdminChannelSummary, error) {
	return c.SetChannelStatus(ctx, actor, id, status)
}

func (c channelAdapter) List(ctx context.Context, filter domain.AdminChannelFilter) (domain.Page[domain.AdminChannelSummary], error) {
	return c.ListChannels(ctx, filter)
}

func (c channelAdapter) Get(ctx context.Context, id string) (domain.AdminChannelDetail, error) {
	return c.GetChannel(ctx, id)
}

func (c channelAdapter) MemberCandidates(ctx context.Context, id, query string) ([]domain.ChannelMemberCandidate, error) {
	return c.managementStub.MemberCandidates(ctx, id, query)
}

func (c channelAdapter) AddMembers(ctx context.Context, actor service.Actor, id string, userIDs []string) (domain.ChannelMembershipChange, error) {
	return c.managementStub.AddMembers(ctx, actor, id, userIDs)
}

func (c channelAdapter) RemoveMember(ctx context.Context, actor service.Actor, id, userID string) (domain.ChannelMembershipChange, error) {
	return c.managementStub.RemoveMember(ctx, actor, id, userID)
}

func managementPorts(stub *managementStub) *ManagementPorts {
	return NewManagementPorts(stub, channelAdapter{stub}, stub)
}

// managed builds a harness whose management surface is wired, with the
// capabilities a spec wants the principal to hold.
func managed(t *testing.T, stub *managementStub, capabilities ...domain.Capability) *testHarness {
	t.Helper()
	return newHarness(t, adminStore(capabilities...), withManagement(managementPorts(stub)))
}

func (h *testHarness) request(t *testing.T, method, path string, cookie *http.Cookie, csrf, body string) *http.Request {
	t.Helper()
	var request *http.Request
	if body == "" {
		request = httptest.NewRequest(method, path, nil)
	} else {
		request = httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
	}
	request.AddCookie(cookie)
	request.Header.Set("Origin", testOrigin)
	if csrf != "" {
		request.Header.Set(csrfHeaderName, csrf)
	}
	return request
}

func decodeData(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v (body %s)", err, response.Body.String())
	}
	return envelope.Data
}

// ---------------------------------------------------------------------------
// Capability enforcement
// ---------------------------------------------------------------------------

// managementRoutes is every route the issue #579 surface adds, with the one
// capability it requires. The table drives the specs below, so a route added
// without an entry here has no capability coverage and a route whose guard is
// changed fails immediately.
var managementRoutes = []struct {
	name       string
	method     string
	path       string
	body       string
	capability domain.Capability
}{
	{"list users", http.MethodGet, "/users", "", domain.CapabilityUsersRead},
	{"get user", http.MethodGet, "/users/" + targetUserID, "", domain.CapabilityUsersRead},
	{"suspend user", http.MethodPatch, "/users/" + targetUserID + "/status", `{"status":"suspended"}`, domain.CapabilityUsersManage},
	{"revoke sessions", http.MethodDelete, "/users/" + targetUserID + "/sessions", "", domain.CapabilityUsersManage},
	{"grant role", http.MethodPost, "/users/" + targetUserID + "/admin-roles", `{"role_slug":"platform-auditor"}`, domain.CapabilitySuperuser},
	{"revoke role", http.MethodDelete, "/users/" + targetUserID + "/admin-roles/platform-auditor", "", domain.CapabilitySuperuser},
	{"list channels", http.MethodGet, "/channels", "", domain.CapabilityChannelsRead},
	{"get channel", http.MethodGet, "/channels/" + testChannelID, "", domain.CapabilityChannelsRead},
	{"archive channel", http.MethodPatch, "/channels/" + testChannelID + "/status", `{"status":"archived"}`, domain.CapabilityChannelsManage},
	{"search member candidates", http.MethodGet, "/channels/" + testChannelID + "/member-candidates", "", domain.CapabilityChannelsManage},
	{"add channel members", http.MethodPost, "/channels/" + testChannelID + "/members", `{"user_ids":["` + targetUserID + `"]}`, domain.CapabilityChannelsManage},
	{"remove channel member", http.MethodDelete, "/channels/" + testChannelID + "/members/" + targetUserID, "", domain.CapabilityChannelsManage},
	{"list conversations", http.MethodGet, "/conversations", "", domain.CapabilityChannelsRead},
	{"list anti-spam", http.MethodGet, "/policies/anti-spam", "", domain.CapabilitySecurityRead},
	{"update anti-spam", http.MethodPatch, "/policies/anti-spam/" + testWorkspaceID, `{"message_rate_limit_per_minute":30}`, domain.CapabilitySecurityManage},
	{"list upload policy", http.MethodGet, "/policies/upload", "", domain.CapabilityInfrastructureRead},
	{"update upload policy", http.MethodPatch, "/policies/upload/" + testWorkspaceID, `{"max_upload_bytes":104857600}`, domain.CapabilityInfrastructureManage},
}

// Every management route refuses a principal that does not hold its capability,
// and records the refusal. admin.audit.read is held here precisely because
// holding *a* capability is not holding *this* one.
func TestManagement_EveryRouteRequiresItsCapability(t *testing.T) {
	for _, route := range managementRoutes {
		t.Run(route.name, func(t *testing.T) {
			stub := &managementStub{}
			harness := managed(t, stub, domain.CapabilityAuditRead)
			cookie, csrf := harness.establish(t)

			response := harness.do(harness.request(t, route.method, route.path, cookie, csrf, route.body))
			if response.Code != http.StatusForbidden {
				t.Fatalf("expected 403, got %d (%s)", response.Code, response.Body.String())
			}
			if len(stub.calls) != 0 {
				t.Fatalf("a refused request must not reach the service, got %v", stub.calls)
			}
			if !containsAction(harness.store.recordedActions(), domain.AuditActionAuthorizationDeny+":denied") {
				t.Fatalf("the denial must be recorded, got %v", harness.store.recordedActions())
			}
		})
	}
}

// The same routes succeed for a superuser, which is what proves the refusals
// above were about the capability and not about the route being broken.
func TestManagement_EveryRouteAcceptsASuperuser(t *testing.T) {
	for _, route := range managementRoutes {
		t.Run(route.name, func(t *testing.T) {
			stub := &managementStub{}
			harness := managed(t, stub, domain.CapabilitySuperuser)
			cookie, csrf := harness.establish(t)

			response := harness.do(harness.request(t, route.method, route.path, cookie, csrf, route.body))
			if response.Code >= http.StatusBadRequest {
				t.Fatalf("expected success, got %d (%s)", response.Code, response.Body.String())
			}
			if len(stub.calls) == 0 {
				t.Fatal("the request should have reached the service")
			}
		})
	}
}

// Holding admin.users.manage does not let somebody decide who administers the
// platform. Conferring authority is reserved to a principal that holds all of
// it, so this is the horizontal escalation guard, enforced at the route.
func TestManagement_RoleGrantRequiresSuperuserNotUsersManage(t *testing.T) {
	stub := &managementStub{}
	harness := managed(t, stub, domain.CapabilityUsersRead, domain.CapabilityUsersManage)
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.request(t, http.MethodPost,
		"/users/"+targetUserID+"/admin-roles", cookie, csrf, `{"role_slug":"platform-superuser"}`))
	if response.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d (%s)", response.Code, response.Body.String())
	}
	if len(stub.calls) != 0 {
		t.Fatalf("the service must not be reached, got %v", stub.calls)
	}
}

// No management route is reachable without an administrative session. The
// credential is the HttpOnly cookie and nothing else.
func TestManagement_EveryRouteRequiresASession(t *testing.T) {
	for _, route := range managementRoutes {
		t.Run(route.name, func(t *testing.T) {
			stub := &managementStub{}
			harness := managed(t, stub, domain.CapabilitySuperuser)

			request := httptest.NewRequest(route.method, route.path, strings.NewReader(route.body))
			request.Header.Set("Origin", testOrigin)
			response := harness.do(request)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d (%s)", response.Code, response.Body.String())
			}
			if len(stub.calls) != 0 {
				t.Fatalf("the service must not be reached, got %v", stub.calls)
			}
		})
	}
}

// Every mutation passes the CSRF guard. A request carrying the session cookie
// but no token — what a cross-site page could produce if SameSite were ever
// relaxed — changes nothing.
func TestManagement_MutationsRequireCSRF(t *testing.T) {
	for _, route := range managementRoutes {
		if isSafeMethod(route.method) {
			continue
		}
		t.Run(route.name+" without a token", func(t *testing.T) {
			stub := &managementStub{}
			harness := managed(t, stub, domain.CapabilitySuperuser)
			cookie, _ := harness.establish(t)

			response := harness.do(harness.request(t, route.method, route.path, cookie, "", route.body))
			if response.Code != http.StatusForbidden {
				t.Fatalf("expected 403, got %d (%s)", response.Code, response.Body.String())
			}
			if len(stub.calls) != 0 {
				t.Fatalf("the service must not be reached, got %v", stub.calls)
			}
		})
		t.Run(route.name+" from a foreign origin", func(t *testing.T) {
			stub := &managementStub{}
			harness := managed(t, stub, domain.CapabilitySuperuser)
			cookie, csrf := harness.establish(t)

			request := harness.request(t, route.method, route.path, cookie, csrf, route.body)
			request.Header.Set("Origin", "https://evil.example")
			if response := harness.do(request); response.Code != http.StatusForbidden {
				t.Fatalf("expected 403, got %d (%s)", response.Code, response.Body.String())
			}
			if len(stub.calls) != 0 {
				t.Fatalf("the service must not be reached, got %v", stub.calls)
			}
		})
	}
}

// A pod without the management services wired serves those paths as
// unavailable rather than serving one of them unguarded.
func TestManagement_UnwiredServesUnavailable(t *testing.T) {
	harness := newHarness(t, adminStore(domain.CapabilitySuperuser))
	cookie, csrf := harness.establish(t)
	for _, route := range managementRoutes {
		t.Run(route.name, func(t *testing.T) {
			response := harness.do(harness.request(t, route.method, route.path, cookie, csrf, route.body))
			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("expected 503, got %d (%s)", response.Code, response.Body.String())
			}
		})
	}
}

func TestManagement_WrongMethodIsRefused(t *testing.T) {
	stub := &managementStub{}
	harness := managed(t, stub, domain.CapabilitySuperuser)
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.request(t, http.MethodPost, "/users", cookie, csrf, `{}`))
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", response.Code)
	}
	if allow := response.Header().Get("Allow"); allow != http.MethodGet {
		t.Fatalf("expected an Allow header naming GET, got %q", allow)
	}
}

// ---------------------------------------------------------------------------
// Mass assignment
// ---------------------------------------------------------------------------

// A field the endpoint never agreed to accept is refused outright rather than
// ignored. Ignoring is what lets a request "work" while carrying something a
// future decoder change would start honouring.
func TestManagement_UnknownBodyFieldsAreRefused(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"status change carrying a role", http.MethodPatch, "/users/" + targetUserID + "/status",
			`{"status":"suspended","role_slug":"platform-superuser"}`},
		{"status change carrying platform_admin", http.MethodPatch, "/users/" + targetUserID + "/status",
			`{"status":"active","platform_admin":true}`},
		{"status change carrying capabilities", http.MethodPatch, "/users/" + targetUserID + "/status",
			`{"status":"active","capabilities":["admin.superuser"]}`},
		{"role grant carrying a target", http.MethodPost, "/users/" + targetUserID + "/admin-roles",
			`{"role_slug":"platform-auditor","user_id":"` + testUserID + `"}`},
		{"channel status carrying an id", http.MethodPatch, "/channels/" + testChannelID + "/status",
			`{"status":"archived","channel_id":"` + targetUserID + `"}`},
		{"member add carrying a role", http.MethodPost, "/channels/" + testChannelID + "/members",
			`{"user_ids":["` + targetUserID + `"],"role":"moderator"}`},
		{"member add carrying a workspace", http.MethodPost, "/channels/" + testChannelID + "/members",
			`{"user_ids":["` + targetUserID + `"],"workspace_id":"` + testWorkspaceID + `"}`},
		{"anti-spam carrying a second policy", http.MethodPatch, "/policies/anti-spam/" + testWorkspaceID,
			`{"message_rate_limit_per_minute":30,"max_upload_bytes":1}`},
		{"upload policy carrying a workspace", http.MethodPatch, "/policies/upload/" + testWorkspaceID,
			`{"max_upload_bytes":104857600,"workspace_id":"` + targetUserID + `"}`},
		{"a second json document", http.MethodPatch, "/users/" + targetUserID + "/status",
			`{"status":"suspended"}{"status":"active"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &managementStub{}
			harness := managed(t, stub, domain.CapabilitySuperuser)
			cookie, csrf := harness.establish(t)

			response := harness.do(harness.request(t, tc.method, tc.path, cookie, csrf, tc.body))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d (%s)", response.Code, response.Body.String())
			}
			if len(stub.calls) != 0 {
				t.Fatalf("nothing must reach the service, got %v", stub.calls)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Filters and pagination
// ---------------------------------------------------------------------------

func TestListUsers_ParsesTheAllowlistedFilters(t *testing.T) {
	stub := &managementStub{}
	harness := managed(t, stub, domain.CapabilityUsersRead)
	cookie, csrf := harness.establish(t)

	path := "/users?limit=10&q=ana&status=suspended&auth_source=oidc&platform_admin=true&inactivity=30d"
	if response := harness.do(harness.request(t, http.MethodGet, path, cookie, csrf, "")); response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", response.Code, response.Body.String())
	}
	filter := stub.userFilter
	if filter.Limit != 10 || filter.Query != "ana" || filter.Status != "suspended" ||
		filter.AuthSource != "oidc" || filter.Inactivity != "30d" {
		t.Fatalf("unexpected filter %+v", filter)
	}
	if filter.PlatformAdmin == nil || !*filter.PlatformAdmin {
		t.Fatalf("platform_admin must be honoured, got %v", filter.PlatformAdmin)
	}
}

// A value outside the allowlist is a 400, not a filter that silently matches
// nothing — and the parameter never reaches the query as an arbitrary string.
func TestListUsers_RefusesValuesOutsideTheAllowlists(t *testing.T) {
	cases := map[string]string{
		"unknown status":             "/users?status=deleted_forever",
		"unknown auth source":        "/users?auth_source=ldap",
		"unknown inactivity bucket":  "/users?inactivity=42d",
		"non-boolean platform_admin": "/users?platform_admin=maybe",
		"non-numeric limit":          "/users?limit=abc",
		"zero limit":                 "/users?limit=0",
		"negative limit":             "/users?limit=-1",
		"forged cursor":              "/users?cursor=%21%21%21",
		"sql in a status filter":     "/users?status=active'%20OR%20'1'='1",
	}
	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			stub := &managementStub{}
			harness := managed(t, stub, domain.CapabilityUsersRead)
			cookie, csrf := harness.establish(t)

			response := harness.do(harness.request(t, http.MethodGet, path, cookie, csrf, ""))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d (%s)", response.Code, response.Body.String())
			}
			if len(stub.calls) != 0 {
				t.Fatalf("nothing must reach the service, got %v", stub.calls)
			}
		})
	}
}

func TestListChannels_RefusesValuesOutsideTheAllowlists(t *testing.T) {
	cases := map[string]string{
		"unknown type":            "/channels?type=secret",
		"unknown status":          "/channels?status=deleted",
		"unknown activity bucket": "/channels?active_within=1y",
		"workspace is not a uuid": "/channels?workspace_id=not-a-uuid",
		"negative member filter":  "/channels?min_members=-1",
	}
	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			stub := &managementStub{}
			harness := managed(t, stub, domain.CapabilityChannelsRead)
			cookie, csrf := harness.establish(t)

			if response := harness.do(harness.request(t, http.MethodGet, path, cookie, csrf, "")); response.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d (%s)", response.Code, response.Body.String())
			}
			if len(stub.calls) != 0 {
				t.Fatalf("nothing must reach the service, got %v", stub.calls)
			}
		})
	}
}

// Every listing pages the same way, so every listing has to say so the same
// way. The five are asserted together because one helper builds all five
// blocks: a change that reshapes one and not the others is the failure this
// catches.
func listingPaths() []struct {
	path       string
	capability domain.Capability
} {
	return []struct {
		path       string
		capability domain.Capability
	}{
		{RouteAdminUsers, domain.CapabilityUsersRead},
		{RouteAdminChannels, domain.CapabilityChannelsRead},
		{RouteAdminConversations, domain.CapabilityChannelsRead},
		{RouteAdminAntiSpam, domain.CapabilitySecurityRead},
		{RouteAdminUploadPolicy, domain.CapabilityInfrastructureRead},
	}
}

func populatedStub(nextCursor string) *managementStub {
	now := time.Now().UTC()
	return &managementStub{
		users:         []domain.AdminUserSummary{{ID: targetUserID, Email: "a@example.test", CreatedAt: now}},
		channels:      []domain.AdminChannelSummary{{ID: testChannelID, Slug: "geral", DisplayName: "geral", CreatedAt: now}},
		conversations: []domain.AdminConversationSummary{{ID: testChannelID, UpdatedAt: now}},
		antiSpam:      []domain.AntiSpamPolicy{{Workspace: domain.WorkspaceRef{ID: testWorkspaceID}}},
		uploads:       []domain.UploadPolicy{{Workspace: domain.WorkspaceRef{ID: testWorkspaceID}}},
		nextCursor:    nextCursor,
	}
}

func paginationBlock(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", response.Code, response.Body.String())
	}
	pagination, ok := decodeData(t, response)["pagination"].(map[string]any)
	if !ok {
		t.Fatalf("missing pagination in %s", response.Body.String())
	}
	return pagination
}

func TestListings_PublishPaginationThatAgreesWithItself(t *testing.T) {
	for _, listing := range listingPaths() {
		t.Run(listing.path, func(t *testing.T) {
			harness := managed(t, populatedStub("cursor-token"), listing.capability)
			cookie, csrf := harness.establish(t)

			pagination := paginationBlock(t, harness.do(harness.request(t, http.MethodGet, listing.path, cookie, csrf, "")))
			if pagination["next_cursor"] != "cursor-token" || pagination["has_more"] != true {
				t.Fatalf("unexpected pagination %v", pagination)
			}
		})
	}
}

// The documented contract is that the last page carries next_cursor: null.
// An empty string is a different value to every client that checks
// truthiness in one place and equality in another, and it is indistinguishable
// from a cursor the server failed to build. Encoding it as JSON null — present,
// and explicitly nothing — is what makes "there is no next page" a fact the
// response states rather than one the client infers.
func TestListings_PublishNullCursorOnTheLastPage(t *testing.T) {
	for _, listing := range listingPaths() {
		t.Run(listing.path, func(t *testing.T) {
			harness := managed(t, populatedStub(""), listing.capability)
			cookie, csrf := harness.establish(t)
			response := harness.do(harness.request(t, http.MethodGet, listing.path, cookie, csrf, ""))

			pagination := paginationBlock(t, response)
			cursor, present := pagination["next_cursor"]
			if !present {
				t.Fatalf("next_cursor must always be present, got %s", response.Body.String())
			}
			if cursor != nil {
				t.Fatalf("last page must carry next_cursor: null, got %#v", cursor)
			}
			if pagination["has_more"] != false {
				t.Fatalf("unexpected has_more %v", pagination["has_more"])
			}
			// Read as bytes too: a map cannot tell "" from null after decoding
			// through any, and the string is what the browser receives.
			if body := response.Body.String(); !strings.Contains(body, `"next_cursor":null`) {
				t.Fatalf("expected a literal null cursor, got %s", body)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Contract
// ---------------------------------------------------------------------------

// The directory publishes what the console renders and nothing more. The
// absent fields matter more than the present ones: auth.users carries a
// soft-delete timestamp, an anonymization timestamp and the subject identifier
// an identity provider knows the person by, and none of them is the console's
// business.
func TestListUsers_PublishesAnAllowlistAndNoIdentityProviderSubject(t *testing.T) {
	stub := &managementStub{users: []domain.AdminUserSummary{{
		ID: targetUserID, Email: "ana@example.test", DisplayName: "Ana",
		Status: "active", AuthSource: "oidc", ExternalProvider: "keycloak",
		AdminRoles: []string{}, WorkspaceRoles: []domain.WorkspaceRoleRef{},
	}}}
	harness := managed(t, stub, domain.CapabilityUsersRead)
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.request(t, http.MethodGet, "/users", cookie, csrf, ""))
	users, _ := decodeData(t, response)["users"].([]any)
	if len(users) != 1 {
		t.Fatalf("expected one user, got %s", response.Body.String())
	}
	user, _ := users[0].(map[string]any)

	expected := map[string]struct{}{
		"id": {}, "email": {}, "display_name": {}, "full_name": {}, "avatar_url": {},
		"status": {}, "auth_source": {}, "external_provider": {},
		"identity_managed_externally": {}, "last_login_at": {}, "created_at": {},
		"platform_admin": {}, "admin_roles": {}, "workspace_roles": {}, "active_sessions": {},
	}
	for key := range user {
		if _, ok := expected[key]; !ok {
			t.Fatalf("the directory published an unexpected field %q", key)
		}
	}
	for key := range expected {
		if _, ok := user[key]; !ok {
			t.Fatalf("the directory is missing %q", key)
		}
	}
	// The console must be told the identity is somebody else's to change, and
	// this API offers no way to change it here.
	if user["identity_managed_externally"] != true {
		t.Fatalf("an OIDC identity must be flagged as externally managed, got %v", user)
	}
	body := response.Body.String()
	for _, forbidden := range []string{"external_subject", "deleted_at", "anonymized_at", "password"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("the response must not carry %q: %s", forbidden, body)
		}
	}
}

// Conversation metadata, and only metadata. This is the Slice C contract: an
// administrator sees that a private conversation exists and how busy it is, and
// never who is in it or what was said.
func TestListConversations_ExposesNoContentAndNoParticipants(t *testing.T) {
	stub := &managementStub{conversations: []domain.AdminConversationSummary{{
		ID: testChannelID, WorkspaceID: testWorkspaceID, WorkspaceName: "NChat",
		Type: "group", Status: "active", ParticipantCount: 4, MessageCount: 120,
	}}}
	harness := managed(t, stub, domain.CapabilityChannelsRead)
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.request(t, http.MethodGet, "/conversations", cookie, csrf, ""))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
	conversations, _ := decodeData(t, response)["conversations"].([]any)
	conversation, _ := conversations[0].(map[string]any)

	expected := map[string]struct{}{
		"id": {}, "workspace_id": {}, "workspace_name": {}, "type": {}, "status": {},
		"participant_count": {}, "message_count": {}, "created_at": {}, "updated_at": {},
		"last_activity_at": {},
	}
	for key := range conversation {
		if _, ok := expected[key]; !ok {
			t.Fatalf("conversation metadata published an unexpected field %q", key)
		}
	}
	body := response.Body.String()
	for _, forbidden := range []string{
		"body", "title", "participants\"", "last_message", "preview", "attachment", "reaction", "quote",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("the response must not carry %q: %s", forbidden, body)
		}
	}
}

// There is no administrative endpoint that reads a message, in any shape. This
// asserts the surface, not one handler: a future route that added one would
// have to be registered, and would be found here.
func TestManagement_NoRouteReadsMessages(t *testing.T) {
	stub := &managementStub{}
	harness := managed(t, stub, domain.CapabilitySuperuser)
	cookie, csrf := harness.establish(t)

	for _, path := range []string{
		"/messages",
		"/conversations/" + testChannelID,
		"/conversations/" + testChannelID + "/messages",
		"/channels/" + testChannelID + "/messages",
		"/users/" + targetUserID + "/messages",
	} {
		response := harness.do(harness.request(t, http.MethodGet, path, cookie, csrf, ""))
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s must not exist, got %d (%s)", path, response.Code, response.Body.String())
		}
	}
}

// ---------------------------------------------------------------------------
// Mutations
// ---------------------------------------------------------------------------

func TestUpdateUserStatus_ReportsTheTransitionAndItsEffect(t *testing.T) {
	stub := &managementStub{change: domain.UserStatusChange{FromStatus: "active", RevokedSessions: 2}}
	harness := managed(t, stub, domain.CapabilityUsersManage)
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.request(t, http.MethodPatch,
		"/users/"+targetUserID+"/status", cookie, csrf, `{"status":"suspended"}`))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", response.Code, response.Body.String())
	}
	data := decodeData(t, response)
	if data["from_status"] != "active" || data["to_status"] != "suspended" || data["revoked_sessions"] != float64(2) {
		t.Fatalf("unexpected body %v", data)
	}
}

// A refusal from the service reaches the client as the status it means: a
// self-suspension is a 403, a lost race is a 409, an unknown target is a 404.
func TestManagement_DomainRefusalsMapToTheirStatus(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		expected int
	}{
		{"self mutation is forbidden", domain.ErrForbidden, http.StatusForbidden},
		{"unknown target", domain.ErrNotFound, http.StatusNotFound},
		{"lost race", domain.ErrConflict, http.StatusConflict},
		{"malformed", domain.ErrInvalidInput, http.StatusBadRequest},
		{"not wired", domain.ErrUnavailable, http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &managementStub{err: tc.err}
			harness := managed(t, stub, domain.CapabilityUsersManage)
			cookie, csrf := harness.establish(t)

			response := harness.do(harness.request(t, http.MethodPatch,
				"/users/"+targetUserID+"/status", cookie, csrf, `{"status":"suspended"}`))
			if response.Code != tc.expected {
				t.Fatalf("expected %d, got %d (%s)", tc.expected, response.Code, response.Body.String())
			}
		})
	}
}

// An internal failure never describes itself: a wrapped database error can name
// a table, a column or a constraint.
func TestManagement_InternalFailureLeaksNothing(t *testing.T) {
	stub := &managementStub{err: errNamedConstraint}
	harness := managed(t, stub, domain.CapabilityUsersManage)
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.request(t, http.MethodPatch,
		"/users/"+targetUserID+"/status", cookie, csrf, `{"status":"suspended"}`))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", response.Code)
	}
	if strings.Contains(response.Body.String(), "admin_principal_roles") {
		t.Fatalf("the body must not name a database object: %s", response.Body.String())
	}
}

func TestRevokeUserSessions_ReportsHowManyEnded(t *testing.T) {
	stub := &managementStub{revoked: 3}
	harness := managed(t, stub, domain.CapabilityUsersManage)
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.request(t, http.MethodDelete,
		"/users/"+targetUserID+"/sessions", cookie, csrf, ""))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", response.Code, response.Body.String())
	}
	if decodeData(t, response)["revoked_sessions"] != float64(3) {
		t.Fatalf("unexpected body %s", response.Body.String())
	}
}

func TestUpdateChannelStatus_ArchivesThroughTheGuards(t *testing.T) {
	stub := &managementStub{channel: domain.AdminChannelSummary{ID: testChannelID, Status: "archived"}}
	harness := managed(t, stub, domain.CapabilityChannelsManage)
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.request(t, http.MethodPatch,
		"/channels/"+testChannelID+"/status", cookie, csrf, `{"status":"archived"}`))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", response.Code, response.Body.String())
	}
	if decodeData(t, response)["status"] != "archived" {
		t.Fatalf("unexpected body %s", response.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Policy guardrails
// ---------------------------------------------------------------------------

// Every one of these is refused at the HTTP boundary, before the service, and
// none of them is corrected into a value the operator did not type.
func TestUpdatePolicies_RefuseValuesOutsideTheContract(t *testing.T) {
	cases := []struct {
		name string
		path string
		body string
	}{
		{"anti-spam zero", "/policies/anti-spam/" + testWorkspaceID, `{"message_rate_limit_per_minute":0}`},
		{"anti-spam negative", "/policies/anti-spam/" + testWorkspaceID, `{"message_rate_limit_per_minute":-5}`},
		{"anti-spam above the ceiling", "/policies/anti-spam/" + testWorkspaceID, `{"message_rate_limit_per_minute":601}`},
		{"anti-spam as a decimal", "/policies/anti-spam/" + testWorkspaceID, `{"message_rate_limit_per_minute":1.5}`},
		{"anti-spam as a string", "/policies/anti-spam/" + testWorkspaceID, `{"message_rate_limit_per_minute":"30"}`},
		{"anti-spam null", "/policies/anti-spam/" + testWorkspaceID, `{"message_rate_limit_per_minute":null}`},
		{"anti-spam absent", "/policies/anti-spam/" + testWorkspaceID, `{}`},
		{"anti-spam overflowing int64", "/policies/anti-spam/" + testWorkspaceID,
			`{"message_rate_limit_per_minute":99999999999999999999}`},
		{"upload zero", "/policies/upload/" + testWorkspaceID, `{"max_upload_bytes":0}`},
		{"upload negative", "/policies/upload/" + testWorkspaceID, `{"max_upload_bytes":-1048576}`},
		{"upload below the floor", "/policies/upload/" + testWorkspaceID, `{"max_upload_bytes":1048575}`},
		{"upload above the ceiling", "/policies/upload/" + testWorkspaceID, `{"max_upload_bytes":536870913}`},
		{"upload not a whole MiB", "/policies/upload/" + testWorkspaceID, `{"max_upload_bytes":1572864}`},
		{"upload in exponent notation", "/policies/upload/" + testWorkspaceID, `{"max_upload_bytes":1e8}`},
		{"upload overflowing int64", "/policies/upload/" + testWorkspaceID,
			`{"max_upload_bytes":99999999999999999999}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &managementStub{}
			harness := managed(t, stub, domain.CapabilitySuperuser)
			cookie, csrf := harness.establish(t)

			response := harness.do(harness.request(t, http.MethodPatch, tc.path, cookie, csrf, tc.body))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d (%s)", response.Code, response.Body.String())
			}
			if len(stub.calls) != 0 {
				t.Fatalf("a refused value must not reach the service, got %v", stub.calls)
			}
		})
	}
}

// The bounds travel with the policy so the console validates against the
// server's numbers rather than restating them, and the unit is named so "30"
// is never ambiguous.
func TestListPolicies_PublishTheirBoundsAndUnits(t *testing.T) {
	stub := &managementStub{}
	harness := managed(t, stub, domain.CapabilitySuperuser)
	cookie, csrf := harness.establish(t)

	antiSpam := decodeData(t, harness.do(harness.request(t, http.MethodGet, "/policies/anti-spam", cookie, csrf, "")))
	bounds, _ := antiSpam["bounds"].(map[string]any)
	if bounds["min"] != float64(1) || bounds["max"] != float64(600) || bounds["unit"] != "messages_per_minute" {
		t.Fatalf("unexpected anti-spam bounds %v", bounds)
	}

	upload := decodeData(t, harness.do(harness.request(t, http.MethodGet, "/policies/upload", cookie, csrf, "")))
	bounds, _ = upload["bounds"].(map[string]any)
	if bounds["min"] != float64(uploadpolicy.MinMaxUploadBytes) ||
		bounds["max"] != float64(uploadpolicy.MaxMaxUploadBytes) ||
		bounds["step"] != float64(uploadpolicy.BytesPerMiB) || bounds["unit"] != "bytes" {
		t.Fatalf("unexpected upload bounds %v", bounds)
	}
	// The gateway ceiling is published so the console can explain why a
	// workspace limit can never exceed it, and it comes from the same constant
	// the gateway configuration does.
	if upload["gateway_hard_cap_bytes"] != float64(uploadpolicy.GatewayHardCapBytes) {
		t.Fatalf("unexpected gateway cap %v", upload["gateway_hard_cap_bytes"])
	}
	// Controls that are real but not runtime-configurable are named as such,
	// rather than offered as fields that would store a number nobody reads.
	managed, _ := upload["deployment_managed"].([]any)
	if len(managed) == 0 {
		t.Fatalf("the deployment-managed controls must be named, got %v", upload)
	}
}

func TestUpdateAntiSpam_AcceptsAValueInsideTheBounds(t *testing.T) {
	stub := &managementStub{}
	harness := managed(t, stub, domain.CapabilitySecurityManage)
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.request(t, http.MethodPatch,
		"/policies/anti-spam/"+testWorkspaceID, cookie, csrf, `{"message_rate_limit_per_minute":30}`))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", response.Code, response.Body.String())
	}
	policy, _ := decodeData(t, response)["policy"].(map[string]any)
	if policy["message_rate_limit_per_minute"] != float64(30) {
		t.Fatalf("unexpected policy %v", policy)
	}
}

func TestUpdateUpload_AcceptsAWholeMiBValue(t *testing.T) {
	stub := &managementStub{}
	harness := managed(t, stub, domain.CapabilityInfrastructureManage)
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.request(t, http.MethodPatch,
		"/policies/upload/"+testWorkspaceID, cookie, csrf, `{"max_upload_bytes":104857600}`))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", response.Code, response.Body.String())
	}
	policy, _ := decodeData(t, response)["policy"].(map[string]any)
	if policy["max_upload_bytes"] != float64(104857600) {
		t.Fatalf("unexpected policy %v", policy)
	}
}

func containsAction(actions []string, wanted string) bool {
	for _, action := range actions {
		if action == wanted {
			return true
		}
	}
	return false
}

// errNamedConstraint stands in for a wrapped database error, which is exactly
// the kind of value that must never reach a response body.
var errNamedConstraint = errors.New(`insert on "auth.admin_principal_roles" violates constraint`)
