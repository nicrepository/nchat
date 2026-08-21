package httpapi

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
)

// A handler built without its service answers 503 rather than panicking or —
// far worse — succeeding with an empty result that reads like "the platform has
// no users".
func TestManagementHandlers_WithoutTheirServiceAreUnavailable(t *testing.T) {
	handlers := map[string]http.Handler{
		"list users":          ListUsers(nil),
		"get user":            GetUser(nil),
		"update user status":  UpdateUserStatus(nil),
		"revoke sessions":     RevokeUserSessions(nil),
		"grant role":          GrantAdminRole(nil),
		"revoke role":         RevokeAdminRole(nil),
		"list channels":       ListChannels(nil),
		"get channel":         GetChannel(nil),
		"update channel":      UpdateChannelStatus(nil),
		"list conversations":  ListConversations(nil),
		"list anti-spam":      ListAntiSpamPolicies(nil),
		"update anti-spam":    UpdateAntiSpamPolicy(nil),
		"list upload policy":  ListUploadPolicies(nil),
		"update upload limit": UpdateUploadPolicy(nil),
	}
	for name, handler := range handlers {
		t.Run(name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("expected 503, got %d", response.Code)
			}
		})
	}
}

// A mutation handler reached without the session guard having run refuses
// rather than acting for an anonymous actor. The router never assembles that
// combination; this asserts the handler does not depend on it not happening.
func TestManagementMutations_WithoutAnAuthenticatedActorRefuse(t *testing.T) {
	stub := &managementStub{}
	handlers := map[string]http.Handler{
		"update user status":  UpdateUserStatus(stub),
		"revoke sessions":     RevokeUserSessions(stub),
		"grant role":          GrantAdminRole(stub),
		"revoke role":         RevokeAdminRole(stub),
		"update channel":      UpdateChannelStatus(channelAdapter{stub}),
		"update anti-spam":    UpdateAntiSpamPolicy(stub),
		"update upload limit": UpdateUploadPolicy(stub),
	}
	for name, handler := range handlers {
		t.Run(name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodPatch, "/", nil))
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d", response.Code)
			}
			if len(stub.calls) != 0 {
				t.Fatalf("nothing must reach the service, got %v", stub.calls)
			}
		})
	}
}

// A failure from any listing reaches the client as a status, not as an empty
// page: "the query failed" and "there is nothing here" must never look alike.
func TestManagementListings_PropagateFailures(t *testing.T) {
	paths := map[string]struct {
		path       string
		capability domain.Capability
	}{
		"users":         {"/users", domain.CapabilityUsersRead},
		"user":          {"/users/" + targetUserID, domain.CapabilityUsersRead},
		"channels":      {"/channels", domain.CapabilityChannelsRead},
		"channel":       {"/channels/" + testChannelID, domain.CapabilityChannelsRead},
		"conversations": {"/conversations", domain.CapabilityChannelsRead},
		"anti-spam":     {"/policies/anti-spam", domain.CapabilitySecurityRead},
		"upload":        {"/policies/upload", domain.CapabilityInfrastructureRead},
	}
	for name, tc := range paths {
		t.Run(name, func(t *testing.T) {
			stub := &managementStub{err: domain.ErrUnavailable}
			harness := managed(t, stub, tc.capability)
			cookie, csrf := harness.establish(t)

			response := harness.do(harness.request(t, http.MethodGet, tc.path, cookie, csrf, ""))
			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("expected 503, got %d (%s)", response.Code, response.Body.String())
			}
		})
	}
}

// The detail views render the aggregates and the two role lists, and keep a
// channel moderator and a workspace admin in separate fields.
func TestGetUser_RendersTheDetailAndTheRoleCatalogue(t *testing.T) {
	granted := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	stub := &managementStub{detail: domain.AdminUserDetail{
		AdminUserSummary: domain.AdminUserSummary{
			ID: targetUserID, Email: "ana@example.test", DisplayName: "Ana",
			Status: "active", AuthSource: "manual",
			AdminRoles: []string{"platform-auditor"},
			WorkspaceRoles: []domain.WorkspaceRoleRef{
				{WorkspaceID: testWorkspaceID, WorkspaceName: "NChat", Role: "member", Status: "active"},
			},
			PlatformAdmin: true,
		},
		Memberships: []domain.WorkspaceRoleRef{
			{WorkspaceID: testWorkspaceID, WorkspaceName: "NChat", Role: "member", Status: "active"},
		},
		ChannelCount: 7,
		RoleGrants: []domain.AdminRoleGrant{{
			Slug: "platform-auditor", Description: "Read-only.", GrantedAt: granted,
			GrantedBy: "root@example.test", Capabilities: []string{"admin.audit.read"},
		}},
		AvailableRoles: []domain.AdminRoleDescriptor{
			{Slug: "platform-auditor", Description: "Read-only.", Capabilities: []string{"admin.audit.read"}},
			{Slug: "platform-superuser", Description: "Everything.", Capabilities: []string{"admin.superuser"}},
		},
	}}
	harness := managed(t, stub, domain.CapabilityUsersRead)
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.request(t, http.MethodGet, "/users/"+targetUserID, cookie, csrf, ""))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", response.Code, response.Body.String())
	}
	data := decodeData(t, response)
	if data["channel_count"] != float64(7) {
		t.Fatalf("unexpected channel count %v", data["channel_count"])
	}
	grants, _ := data["role_grants"].([]any)
	if len(grants) != 1 {
		t.Fatalf("expected one grant, got %v", data["role_grants"])
	}
	// The catalogue travels with the record so the console can offer a role
	// without a second endpoint; it names roles and capabilities, never a
	// principal.
	available, _ := data["available_roles"].([]any)
	if len(available) != 2 {
		t.Fatalf("expected the catalogue, got %v", data["available_roles"])
	}
	if memberships, _ := data["memberships"].([]any); len(memberships) != 1 {
		t.Fatalf("expected the membership list, got %v", data["memberships"])
	}
}

func TestGetChannel_KeepsModeratorsAndWorkspaceAdminsApart(t *testing.T) {
	stub := &managementStub{channelDetail: domain.AdminChannelDetail{
		AdminChannelSummary: domain.AdminChannelSummary{
			ID: testChannelID, WorkspaceID: testWorkspaceID, DisplayName: "Engenharia",
			Type: "private", Status: "active", MemberCount: 12, ModeratorCount: 1,
		},
		CategoryName: "Times",
		Moderators: []domain.ChannelMemberRef{
			{UserID: targetUserID, DisplayName: "Ana", Email: "ana@example.test", Role: "moderator"},
		},
		WorkspaceAdmins: []domain.ChannelMemberRef{
			{UserID: testUserID, DisplayName: "Root", Email: "root@example.test", Role: "owner"},
		},
		MessageCount: 4200,
	}}
	harness := managed(t, stub, domain.CapabilityChannelsRead)
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.request(t, http.MethodGet, "/channels/"+testChannelID, cookie, csrf, ""))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", response.Code, response.Body.String())
	}
	data := decodeData(t, response)
	moderators, _ := data["moderators"].([]any)
	admins, _ := data["workspace_admins"].([]any)
	if len(moderators) != 1 || len(admins) != 1 {
		t.Fatalf("the two authorities must be separate lists, got %v", data)
	}
	if data["message_count"] != float64(4200) {
		t.Fatalf("unexpected message count %v", data["message_count"])
	}
	// A volume is an aggregate. It is here because deciding whether to archive
	// a channel needs it; no message is reachable from this payload.
	if _, leaked := data["messages"]; leaked {
		t.Fatalf("the detail must not carry messages: %v", data)
	}
}

func TestListConversations_AppliesTheAllowlistedFilters(t *testing.T) {
	stub := &managementStub{}
	harness := managed(t, stub, domain.CapabilityChannelsRead)
	cookie, csrf := harness.establish(t)

	path := "/conversations?workspace_id=" + testWorkspaceID + "&type=direct&status=archived&limit=5"
	if response := harness.do(harness.request(t, http.MethodGet, path, cookie, csrf, "")); response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", response.Code, response.Body.String())
	}
	filter := stub.convFilter
	if filter.WorkspaceID != testWorkspaceID || filter.Type != "direct" || filter.Status != "archived" || filter.Limit != 5 {
		t.Fatalf("unexpected filter %+v", filter)
	}
}

func TestListConversations_RefusesValuesOutsideTheAllowlists(t *testing.T) {
	cases := map[string]string{
		"unknown type":            "/conversations?type=broadcast",
		"unknown status":          "/conversations?status=purged",
		"workspace is not a uuid": "/conversations?workspace_id=1%20OR%201=1",
		"forged cursor":           "/conversations?cursor=zzz%21",
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

func TestListPolicies_RefuseAMalformedPage(t *testing.T) {
	for _, path := range []string{"/policies/anti-spam?limit=abc", "/policies/upload?cursor=%21"} {
		stub := &managementStub{}
		harness := managed(t, stub, domain.CapabilitySuperuser)
		cookie, csrf := harness.establish(t)

		if response := harness.do(harness.request(t, http.MethodGet, path, cookie, csrf, "")); response.Code != http.StatusBadRequest {
			t.Fatalf("%s: expected 400, got %d", path, response.Code)
		}
	}
}

// A search term longer than the directory is willing to hand the database is
// refused rather than truncated into a different search.
func TestListUsers_RefusesAnOversizedSearchTerm(t *testing.T) {
	stub := &managementStub{}
	harness := managed(t, stub, domain.CapabilityUsersRead)
	cookie, csrf := harness.establish(t)

	long := make([]byte, maxSearchTermLength+1)
	for i := range long {
		long[i] = 'a'
	}
	response := harness.do(harness.request(t, http.MethodGet, "/users?q="+string(long), cookie, csrf, ""))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", response.Code)
	}
}

// A refusal from the role endpoints reaches the client as its own status, and a
// successful one answers 204 with no body to leak.
func TestAdminRoleEndpoints_AnswerWithoutABody(t *testing.T) {
	stub := &managementStub{}
	harness := managed(t, stub, domain.CapabilitySuperuser)
	cookie, csrf := harness.establish(t)

	grant := harness.do(harness.request(t, http.MethodPost,
		"/users/"+targetUserID+"/admin-roles", cookie, csrf, `{"role_slug":"platform-auditor"}`))
	if grant.Code != http.StatusNoContent || grant.Body.Len() != 0 {
		t.Fatalf("expected an empty 204, got %d (%s)", grant.Code, grant.Body.String())
	}
	revoke := harness.do(harness.request(t, http.MethodDelete,
		"/users/"+targetUserID+"/admin-roles/platform-auditor", cookie, csrf, ""))
	if revoke.Code != http.StatusNoContent || revoke.Body.Len() != 0 {
		t.Fatalf("expected an empty 204, got %d (%s)", revoke.Code, revoke.Body.String())
	}
	if len(stub.calls) != 2 {
		t.Fatalf("both operations should have reached the service, got %v", stub.calls)
	}
}

// The last-administrator invariant surfaces as a 409 the console can explain,
// not as a generic failure.
func TestRevokeAdminRole_LastAdministratorIsAConflict(t *testing.T) {
	stub := &managementStub{err: domain.ErrConflict}
	harness := managed(t, stub, domain.CapabilitySuperuser)
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.request(t, http.MethodDelete,
		"/users/"+targetUserID+"/admin-roles/platform-superuser", cookie, csrf, ""))
	if response.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d (%s)", response.Code, response.Body.String())
	}
}

// A body that is not JSON at all is refused before anything reads a field.
func TestManagementMutations_RefuseANonJSONBody(t *testing.T) {
	stub := &managementStub{}
	harness := managed(t, stub, domain.CapabilitySuperuser)
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.request(t, http.MethodPatch,
		"/users/"+targetUserID+"/status", cookie, csrf, `status=suspended`))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", response.Code)
	}
	if len(stub.calls) != 0 {
		t.Fatalf("nothing must reach the service, got %v", stub.calls)
	}
}

// ---------------------------------------------------------------------------
// Channel membership (issue #579, code-quality follow-up)
// ---------------------------------------------------------------------------

func TestAddChannelMembers_AdmitsPeopleAndReportsWhatChanged(t *testing.T) {
	stub := &managementStub{membership: domain.ChannelMembershipChange{
		WorkspaceID: testWorkspaceID, AlreadyMembers: 0, MemberCount: 13,
	}}
	harness := managed(t, stub, domain.CapabilityChannelsManage)
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.request(t, http.MethodPost,
		"/channels/"+testChannelID+"/members", cookie, csrf,
		`{"user_ids":["`+targetUserID+`"]}`))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", response.Code, response.Body.String())
	}
	data := decodeData(t, response)
	if data["added"] != float64(1) || data["member_count"] != float64(13) {
		t.Fatalf("unexpected body %v", data)
	}
	if len(stub.addedUserIDs) != 1 || stub.addedUserIDs[0] != targetUserID {
		t.Fatalf("the target list must reach the service unchanged, got %v", stub.addedUserIDs)
	}
}

// A retry of the same add is a success that added nobody. Reporting it as "1
// added" would tell the operator something false.
func TestAddChannelMembers_RetryReportsNothingAdded(t *testing.T) {
	stub := &managementStub{membership: domain.ChannelMembershipChange{
		WorkspaceID: testWorkspaceID, AlreadyMembers: 1, MemberCount: 12,
	}}
	harness := managed(t, stub, domain.CapabilityChannelsManage)
	cookie, csrf := harness.establish(t)

	// The stub reports the change the store would report on a repeat: the
	// target was eligible and already present.
	stub.membership.Added = 0
	response := harness.do(harness.request(t, http.MethodPost,
		"/channels/"+testChannelID+"/members", cookie, csrf,
		`{"user_ids":["`+targetUserID+`"]}`))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
	if decodeData(t, response)["already_members"] != float64(1) {
		t.Fatalf("unexpected body %s", response.Body.String())
	}
}

// An ineligible target — not a member of the channel's workspace, suspended,
// or the channel archived — is a 409 and not a 403: the caller holds the
// capability, so telling them "forbidden" would be a lie about why it failed.
func TestAddChannelMembers_IneligibleTargetIsAConflict(t *testing.T) {
	stub := &managementStub{err: domain.ErrConflict}
	harness := managed(t, stub, domain.CapabilityChannelsManage)
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.request(t, http.MethodPost,
		"/channels/"+testChannelID+"/members", cookie, csrf,
		`{"user_ids":["`+targetUserID+`"]}`))
	if response.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d (%s)", response.Code, response.Body.String())
	}
}

func TestAddChannelMembers_UnknownChannelIsNotFound(t *testing.T) {
	stub := &managementStub{err: domain.ErrNotFound}
	harness := managed(t, stub, domain.CapabilityChannelsManage)
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.request(t, http.MethodPost,
		"/channels/"+testChannelID+"/members", cookie, csrf,
		`{"user_ids":["`+targetUserID+`"]}`))
	if response.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", response.Code)
	}
}

// The decoder refuses a body that is not the agreed shape. Semantic validation
// of the list — empty, oversized, malformed or duplicated ids — belongs to the
// service and is asserted there, against the layer that performs it.
func TestAddChannelMembers_RefusesAMalformedBody(t *testing.T) {
	cases := map[string]string{
		"list is not a list":     `{"user_ids":"` + targetUserID + `"}`,
		"target is not a string": `{"user_ids":[1]}`,
		"not json at all":        `user_ids=` + targetUserID,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			stub := &managementStub{}
			harness := managed(t, stub, domain.CapabilityChannelsManage)
			cookie, csrf := harness.establish(t)

			response := harness.do(harness.request(t, http.MethodPost,
				"/channels/"+testChannelID+"/members", cookie, csrf, body))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d (%s)", response.Code, response.Body.String())
			}
			if len(stub.calls) != 0 {
				t.Fatalf("nothing must reach the service, got %v", stub.calls)
			}
		})
	}
}

// A refusal from the service reaches the client as a 400, so a malformed target
// list is a client error rather than a server one.
func TestAddChannelMembers_ServiceRefusalIsABadRequest(t *testing.T) {
	stub := &managementStub{err: domain.ErrInvalidInput}
	harness := managed(t, stub, domain.CapabilityChannelsManage)
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.request(t, http.MethodPost,
		"/channels/"+testChannelID+"/members", cookie, csrf, `{"user_ids":["not-a-uuid"]}`))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (%s)", response.Code, response.Body.String())
	}
}

func TestRemoveChannelMember_RemovesAndIsIdempotent(t *testing.T) {
	stub := &managementStub{membership: domain.ChannelMembershipChange{
		WorkspaceID: testWorkspaceID, MemberCount: 11,
	}}
	harness := managed(t, stub, domain.CapabilityChannelsManage)
	cookie, csrf := harness.establish(t)

	path := "/channels/" + testChannelID + "/members/" + targetUserID
	first := harness.do(harness.request(t, http.MethodDelete, path, cookie, csrf, ""))
	if first.Code != http.StatusOK || decodeData(t, first)["removed"] != true {
		t.Fatalf("expected a removal, got %d (%s)", first.Code, first.Body.String())
	}
	// A repeat of the same request is a success, not a 404: the caller's intent
	// already holds, and a safe retry must not look like a failure.
	second := harness.do(harness.request(t, http.MethodDelete, path, cookie, csrf, ""))
	if second.Code != http.StatusOK {
		t.Fatalf("a retry must succeed, got %d", second.Code)
	}
}

// #geral is refused, mirroring chat-service. The console must not become a
// second way around an invariant the chat domain maintains everywhere else.
func TestRemoveChannelMember_RefusesTheGeneralChannel(t *testing.T) {
	stub := &managementStub{err: domain.ErrForbidden}
	harness := managed(t, stub, domain.CapabilityChannelsManage)
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.request(t, http.MethodDelete,
		"/channels/"+testChannelID+"/members/"+targetUserID, cookie, csrf, ""))
	if response.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", response.Code)
	}
}

func TestRemoveChannelMember_RefusesAMalformedTarget(t *testing.T) {
	stub := &managementStub{err: domain.ErrInvalidInput}
	harness := managed(t, stub, domain.CapabilityChannelsManage)
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.request(t, http.MethodDelete,
		"/channels/"+testChannelID+"/members/not-a-uuid", cookie, csrf, ""))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", response.Code)
	}
}

// Reading a channel does not authorize changing who is in it.
func TestChannelMembership_ReadCapabilityIsNotEnough(t *testing.T) {
	for _, tc := range []struct {
		method, path, body string
	}{
		{http.MethodPost, "/channels/" + testChannelID + "/members", `{"user_ids":["` + targetUserID + `"]}`},
		{http.MethodDelete, "/channels/" + testChannelID + "/members/" + targetUserID, ""},
	} {
		stub := &managementStub{}
		harness := managed(t, stub, domain.CapabilityChannelsRead)
		cookie, csrf := harness.establish(t)

		response := harness.do(harness.request(t, tc.method, tc.path, cookie, csrf, tc.body))
		if response.Code != http.StatusForbidden {
			t.Fatalf("%s %s: expected 403, got %d", tc.method, tc.path, response.Code)
		}
		if len(stub.calls) != 0 {
			t.Fatalf("nothing must reach the service, got %v", stub.calls)
		}
	}
}

// The detail view carries a bounded membership preview so the console has
// something to administer — and still no message.
func TestGetChannel_CarriesABoundedMembershipPreview(t *testing.T) {
	stub := &managementStub{channelDetail: domain.AdminChannelDetail{
		AdminChannelSummary: domain.AdminChannelSummary{ID: testChannelID, MemberCount: 900},
		Members: []domain.ChannelMemberRef{
			{UserID: targetUserID, DisplayName: "Ana", Email: "ana@example.test", Role: "member"},
		},
	}}
	harness := managed(t, stub, domain.CapabilityChannelsRead)
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.request(t, http.MethodGet, "/channels/"+testChannelID, cookie, csrf, ""))
	data := decodeData(t, response)
	members, _ := data["members"].([]any)
	if len(members) != 1 {
		t.Fatalf("expected the preview, got %v", data["members"])
	}
	// The preview is capped; the real total is the summary's count.
	if data["member_count"] != float64(900) {
		t.Fatalf("the true total must stay on the summary, got %v", data["member_count"])
	}
}

// ---------------------------------------------------------------------------
// New filters
// ---------------------------------------------------------------------------

func TestListUsers_ParsesTheWorkspaceRoleFilter(t *testing.T) {
	for _, role := range []string{"owner", "admin", "moderator", "member", "guest"} {
		t.Run(role, func(t *testing.T) {
			stub := &managementStub{}
			harness := managed(t, stub, domain.CapabilityUsersRead)
			cookie, csrf := harness.establish(t)

			response := harness.do(harness.request(t, http.MethodGet,
				"/users?workspace_role="+role, cookie, csrf, ""))
			if response.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d (%s)", response.Code, response.Body.String())
			}
			if stub.userFilter.WorkspaceRole != role {
				t.Fatalf("expected %q, got %+v", role, stub.userFilter)
			}
		})
	}
}

// A workspace role is not a platform role and not a capability. Anything that
// is not one of the five the schema allows is a 400.
func TestListUsers_RefusesAWorkspaceRoleTheSchemaDoesNotHave(t *testing.T) {
	for _, value := range []string{"superuser", "admin.superuser", "OWNER", "root", "'"} {
		stub := &managementStub{}
		harness := managed(t, stub, domain.CapabilityUsersRead)
		cookie, csrf := harness.establish(t)

		response := harness.do(harness.request(t, http.MethodGet,
			"/users?workspace_role="+url.QueryEscape(value), cookie, csrf, ""))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%q: expected 400, got %d", value, response.Code)
		}
		if len(stub.calls) != 0 {
			t.Fatalf("nothing must reach the service, got %v", stub.calls)
		}
	}
}

// The role filter and the platform_admin filter are separate questions and
// combine rather than replace each other.
func TestListUsers_CombinesWorkspaceRoleWithTheOtherFilters(t *testing.T) {
	stub := &managementStub{}
	harness := managed(t, stub, domain.CapabilityUsersRead)
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.request(t, http.MethodGet,
		"/users?workspace_role=owner&platform_admin=true&status=active&q=ana", cookie, csrf, ""))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
	filter := stub.userFilter
	if filter.WorkspaceRole != "owner" || filter.Status != "active" || filter.Query != "ana" {
		t.Fatalf("unexpected filter %+v", filter)
	}
	if filter.PlatformAdmin == nil || !*filter.PlatformAdmin {
		t.Fatalf("platform_admin must survive alongside the role filter, got %+v", filter)
	}
}

func TestListUsers_AbsentWorkspaceRoleDoesNotFilter(t *testing.T) {
	stub := &managementStub{}
	harness := managed(t, stub, domain.CapabilityUsersRead)
	cookie, csrf := harness.establish(t)

	harness.do(harness.request(t, http.MethodGet, "/users", cookie, csrf, ""))
	if stub.userFilter.WorkspaceRole != "" {
		t.Fatalf("expected no role filter, got %q", stub.userFilter.WorkspaceRole)
	}
}

func TestListChannels_ParsesTheAdministeredByFilter(t *testing.T) {
	stub := &managementStub{}
	harness := managed(t, stub, domain.CapabilityChannelsRead)
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.request(t, http.MethodGet,
		"/channels?administered_by="+targetUserID, cookie, csrf, ""))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", response.Code, response.Body.String())
	}
	if stub.channelFilter.AdministeredBy != targetUserID {
		t.Fatalf("unexpected filter %+v", stub.channelFilter)
	}
}

func TestListChannels_RefusesAMalformedAdministeredBy(t *testing.T) {
	for _, value := range []string{"not-a-uuid", "1 OR 1=1", "%"} {
		stub := &managementStub{}
		harness := managed(t, stub, domain.CapabilityChannelsRead)
		cookie, csrf := harness.establish(t)

		response := harness.do(harness.request(t, http.MethodGet,
			"/channels?administered_by="+url.QueryEscape(value), cookie, csrf, ""))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%q: expected 400, got %d", value, response.Code)
		}
		if len(stub.calls) != 0 {
			t.Fatalf("nothing must reach the service, got %v", stub.calls)
		}
	}
}

// ---------------------------------------------------------------------------
// Member candidates (second code-quality round)
// ---------------------------------------------------------------------------

// The picker returns people, identified the way a person is identified. The
// operator never has to know an identifier to use it.
func TestListMemberCandidates_ReturnsPeopleNotIdentifiers(t *testing.T) {
	stub := &managementStub{candidates: []domain.ChannelMemberCandidate{{
		UserID: targetUserID, DisplayName: "Ana", FullName: "Ana Lima",
		Email: "ana@example.test", AvatarURL: "/a.png", WorkspaceRole: "member",
	}}}
	harness := managed(t, stub, domain.CapabilityChannelsManage)
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.request(t, http.MethodGet,
		"/channels/"+testChannelID+"/member-candidates?q=ana", cookie, csrf, ""))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", response.Code, response.Body.String())
	}
	candidates, _ := decodeData(t, response)["candidates"].([]any)
	if len(candidates) != 1 {
		t.Fatalf("expected one candidate, got %s", response.Body.String())
	}
	candidate, _ := candidates[0].(map[string]any)

	expected := map[string]struct{}{
		"user_id": {}, "display_name": {}, "full_name": {}, "email": {},
		"avatar_url": {}, "workspace_role": {},
	}
	for key := range candidate {
		if _, ok := expected[key]; !ok {
			t.Fatalf("the picker published an unexpected field %q", key)
		}
	}
	// A picker behind a channel capability must not double as a second, wider
	// user directory.
	body := response.Body.String()
	for _, forbidden := range []string{
		"admin_roles", "platform_admin", "active_sessions", "workspace_roles",
		"external_provider", "auth_source", "last_login_at",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("the picker must not carry %q: %s", forbidden, body)
		}
	}
	if stub.candidateQuery != "ana" {
		t.Fatalf("the search term must reach the service, got %q", stub.candidateQuery)
	}
}

// Seeing that a channel exists must not also enumerate the people in its
// workspace: the picker feeds a mutation and carries the mutation's capability.
func TestListMemberCandidates_RequiresTheManageCapability(t *testing.T) {
	stub := &managementStub{}
	harness := managed(t, stub, domain.CapabilityChannelsRead)
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.request(t, http.MethodGet,
		"/channels/"+testChannelID+"/member-candidates", cookie, csrf, ""))
	if response.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d (%s)", response.Code, response.Body.String())
	}
	if len(stub.calls) != 0 {
		t.Fatalf("nothing must reach the service, got %v", stub.calls)
	}
}

func TestListMemberCandidates_RefusesAnOversizedTerm(t *testing.T) {
	stub := &managementStub{}
	harness := managed(t, stub, domain.CapabilityChannelsManage)
	cookie, csrf := harness.establish(t)

	long := strings.Repeat("a", maxSearchTermLength+1)
	response := harness.do(harness.request(t, http.MethodGet,
		"/channels/"+testChannelID+"/member-candidates?q="+long, cookie, csrf, ""))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", response.Code)
	}
}

func TestListMemberCandidates_MalformedChannelIsABadRequest(t *testing.T) {
	stub := &managementStub{err: domain.ErrInvalidInput}
	harness := managed(t, stub, domain.CapabilityChannelsManage)
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.request(t, http.MethodGet,
		"/channels/not-a-uuid/member-candidates", cookie, csrf, ""))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", response.Code)
	}
}

// ---------------------------------------------------------------------------
// status=deleted
// ---------------------------------------------------------------------------

// 'deleted' is a status auth.users can hold and this endpoint deliberately does
// not filter on. The directory excludes soft-deleted accounts unconditionally,
// so accepting the value would publish a filter that can never return a row.
func TestListUsers_RefusesTheDeletedStatusFilter(t *testing.T) {
	stub := &managementStub{}
	harness := managed(t, stub, domain.CapabilityUsersRead)
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.request(t, http.MethodGet, "/users?status=deleted", cookie, csrf, ""))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (%s)", response.Code, response.Body.String())
	}
	if len(stub.calls) != 0 {
		t.Fatalf("nothing must reach the service, got %v", stub.calls)
	}
}

// The statuses that remain are the ones the directory can really answer for.
func TestListUsers_AcceptsEveryStatusItStillPublishes(t *testing.T) {
	for _, status := range []string{"active", "invited", "suspended", "locked"} {
		t.Run(status, func(t *testing.T) {
			stub := &managementStub{}
			harness := managed(t, stub, domain.CapabilityUsersRead)
			cookie, csrf := harness.establish(t)

			response := harness.do(harness.request(t, http.MethodGet,
				"/users?status="+status, cookie, csrf, ""))
			if response.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d (%s)", response.Code, response.Body.String())
			}
			if stub.userFilter.Status != status {
				t.Fatalf("expected %q, got %q", status, stub.userFilter.Status)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Per-user audit history (third code-quality round)
// ---------------------------------------------------------------------------

// Absent user_id is the platform-wide trail this endpoint has always returned.
func TestListAuditEvents_WithoutAFilterStaysGlobal(t *testing.T) {
	harness := newHarness(t, adminStore(domain.CapabilityAuditRead))
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.request(t, http.MethodGet, "/audit/events?limit=50", cookie, csrf, ""))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", response.Code, response.Body.String())
	}
	if harness.store.auditFilter.Resource != "" {
		t.Fatalf("no filter must be applied, got %q", harness.store.auditFilter.Resource)
	}
	if harness.store.auditFilter.Limit != 50 {
		t.Fatalf("the limit must survive, got %d", harness.store.auditFilter.Limit)
	}
}

// A user id becomes the canonical resource key, built by the service — the
// caller never supplies the value the column is compared against.
func TestListAuditEvents_UserFilterBecomesTheCanonicalResourceKey(t *testing.T) {
	harness := newHarness(t, adminStore(domain.CapabilityAuditRead))
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.request(t, http.MethodGet,
		"/audit/events?user_id="+targetUserID, cookie, csrf, ""))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", response.Code, response.Body.String())
	}
	if want := domain.AuditUserResource(targetUserID); harness.store.auditFilter.Resource != want {
		t.Fatalf("expected %q, got %q", want, harness.store.auditFilter.Resource)
	}
}

// A malformed id is refused rather than ignored: silently dropping the filter
// would show an operator somebody else's history under this person's name.
func TestListAuditEvents_MalformedUserFilterIsABadRequest(t *testing.T) {
	for _, value := range []string{"abc", "not-a-uuid", "' OR 1=1 --", "admin.user:x", "%"} {
		harness := newHarness(t, adminStore(domain.CapabilityAuditRead))
		cookie, csrf := harness.establish(t)

		response := harness.do(harness.request(t, http.MethodGet,
			"/audit/events?user_id="+url.QueryEscape(value), cookie, csrf, ""))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%q: expected 400, got %d (%s)", value, response.Code, response.Body.String())
		}
		if harness.store.auditFilter.Resource != "" {
			t.Fatalf("%q: nothing must reach the store, got %q", value, harness.store.auditFilter.Resource)
		}
	}
}

// The filtered read carries exactly the guards the unfiltered one does. Being
// able to open somebody's record is not being able to read the audit trail.
func TestListAuditEvents_FilteredReadKeepsTheAuditCapability(t *testing.T) {
	harness := newHarness(t, adminStore(domain.CapabilityUsersRead, domain.CapabilityUsersManage))
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.request(t, http.MethodGet,
		"/audit/events?user_id="+targetUserID, cookie, csrf, ""))
	if response.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d (%s)", response.Code, response.Body.String())
	}
	if !containsAction(harness.store.recordedActions(), domain.AuditActionAuthorizationDeny+":denied") {
		t.Fatalf("the denial must be recorded, got %v", harness.store.recordedActions())
	}
}

func TestListAuditEvents_FilteredReadRequiresASession(t *testing.T) {
	harness := newHarness(t, adminStore(domain.CapabilityAuditRead))

	request := httptest.NewRequest(http.MethodGet, "/audit/events?user_id="+targetUserID, nil)
	request.Header.Set("Origin", testOrigin)
	if response := harness.do(request); response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", response.Code)
	}
}
