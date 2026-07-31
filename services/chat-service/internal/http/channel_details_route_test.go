package httpapi_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Route-level tests for GET /api/chat/channels/{channelID}/details.
//
// These go through the real router and the real ChannelService, so the
// authorization that matters — an active workspace membership plus channel
// visibility — is exercised end to end rather than stubbed at the service
// boundary the way channel_details_handler_test.go does.

const otherChannelID = "88888888-8888-8888-8888-888888888888"

func getDetails(authorization, channelID string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/api/chat/channels/"+channelID+"/details", nil)
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	return request
}

// rosterOf builds a channel roster of size n whose display names sort in the
// order they are generated, so "the first N alphabetically" is unambiguous.
func rosterOf(n int) []domain.ChannelMemberProfile {
	members := make([]domain.ChannelMemberProfile, 0, n)
	for i := 0; i < n; i++ {
		members = append(members, domain.ChannelMemberProfile{
			UserID:      fmt.Sprintf("user-%03d", i),
			DisplayName: fmt.Sprintf("Membro %03d", i),
			Role:        domain.ChannelRoleMember,
		})
	}
	return members
}

func seedVisibleChannel(env channelRouteEnv, channelID string, channelType domain.ChannelType, page storage.ChannelMemberPage) {
	env.channels.visible[channelID] = domain.Channel{
		ID: channelID, WorkspaceID: testWorkspaceID, Slug: "canal-" + channelID[:4],
		DisplayName: "Canal " + channelID[:4], Type: channelType,
		Status: domain.ChannelStatusActive, CreatedAt: time.Date(2024, 1, 12, 9, 30, 0, 0, time.UTC),
	}
	env.members.memberPages[channelID] = page
}

func decodeDetails(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode details envelope: %v; body: %s", err, recorder.Body.String())
	}
	return body.Data
}

func TestChannelRoute_Details_ServesAPrivateChannelTheCallerBelongsTo(t *testing.T) {
	env := newChannelRouteEnv(t, allowAllSessionValidator{}, routeActiveWorkspace(),
		routeMember(domain.WorkspaceRoleMember, domain.MemberStatusActive), true)
	seedVisibleChannel(env, testChannelID, domain.ChannelTypePrivate, storage.ChannelMemberPage{
		Online:     []domain.ChannelMemberProfile{{UserID: testUserID, DisplayName: "Álvaro", Role: domain.ChannelRoleMember}},
		TotalCount: 7,
	})
	// A second channel the caller can also see. Its members must never appear in
	// the first channel's response, even though its member is online too.
	seedVisibleChannel(env, otherChannelID, domain.ChannelTypePublic, storage.ChannelMemberPage{
		Online:     []domain.ChannelMemberProfile{{UserID: "intruder", DisplayName: "Outro Canal", Role: domain.ChannelRoleMember}},
		TotalCount: 1,
	})
	env.presence.online = []string{testUserID, "intruder"}

	recorder := httptest.NewRecorder()
	env.router.ServeHTTP(recorder, getDetails(bearer(makeTestToken(t, testUserID, testHMACSecret, testIssuer, testAudience, time.Hour)), testChannelID))

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	data := decodeDetails(t, recorder)
	if data["id"] != testChannelID || data["type"] != "private" {
		t.Fatalf("unexpected channel: %v", data)
	}
	if data["member_count"] != float64(7) {
		t.Fatalf("expected the store's total, got %v", data["member_count"])
	}
	members := data["online_members"].([]any)
	if len(members) != 1 || members[0].(map[string]any)["user_id"] != testUserID {
		t.Fatalf("details must carry only the requested channel's members: %v", members)
	}
	if env.members.memberLimit != domain.MaxChannelDetailsMembers {
		t.Fatalf("expected the server-side cap to reach the store, got %d", env.members.memberLimit)
	}
}

func TestChannelRoute_Details_RefusesChannelsTheCallerCannotSee(t *testing.T) {
	valid := bearer(makeTestToken(t, testUserID, testHMACSecret, testIssuer, testAudience, time.Hour))

	for _, test := range []struct {
		name          string
		member        domain.WorkspaceMember
		memberPresent bool
		// seed decides whether the channel is visible to this caller at all.
		seed bool
	}{
		{name: "channel not visible to the caller", member: routeMember(domain.WorkspaceRoleMember, domain.MemberStatusActive), memberPresent: true},
		{name: "caller is not a workspace member", member: domain.WorkspaceMember{}, seed: true},
		{name: "caller's membership is suspended", member: routeMember(domain.WorkspaceRoleMember, domain.MemberStatusSuspended), memberPresent: true, seed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			env := newChannelRouteEnv(t, allowAllSessionValidator{}, routeActiveWorkspace(), test.member, test.memberPresent)
			if test.seed {
				seedVisibleChannel(env, testChannelID, domain.ChannelTypePrivate, storage.ChannelMemberPage{
					Online:     []domain.ChannelMemberProfile{{UserID: "secret", DisplayName: "Segredo"}},
					TotalCount: 1,
				})
				env.presence.online = []string{"secret"}
			}

			recorder := httptest.NewRecorder()
			env.router.ServeHTTP(recorder, getDetails(valid, testChannelID))

			if recorder.Code != http.StatusNotFound {
				t.Fatalf("expected 404, got %d: %s", recorder.Code, recorder.Body.String())
			}
			if code := errorCode(t, recorder); code != "not_found" {
				t.Fatalf("unexpected error code %q", code)
			}
		})
	}
}

func TestChannelRoute_Details_RequiresAUsableSession(t *testing.T) {
	env := newChannelRouteEnv(t, denySessionValidator{err: domain.ErrInvalidToken}, routeActiveWorkspace(),
		routeMember(domain.WorkspaceRoleMember, domain.MemberStatusActive), true)
	seedVisibleChannel(env, testChannelID, domain.ChannelTypePublic, storage.ChannelMemberPage{})

	recorder := httptest.NewRecorder()
	env.router.ServeHTTP(recorder, getDetails(bearer(makeTestToken(t, testUserID, testHMACSecret, testIssuer, testAudience, time.Hour)), testChannelID))

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestChannelRoute_Details_RejectsAMalformedChannelID(t *testing.T) {
	env := newChannelRouteEnv(t, allowAllSessionValidator{}, routeActiveWorkspace(),
		routeMember(domain.WorkspaceRoleMember, domain.MemberStatusActive), true)

	recorder := httptest.NewRecorder()
	env.router.ServeHTTP(recorder, getDetails(bearer(makeTestToken(t, testUserID, testHMACSecret, testIssuer, testAudience, time.Hour)), "not-a-uuid"))

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

// TestChannelRoute_Details_ShowsAnOnlineMemberPastTheAlphabeticalCut is the
// end-to-end regression for the reported defect, through the real router,
// middleware and service.
//
// Setup is exactly the reported scenario: a channel with 31 members, the first
// 30 alphabetically offline and the 31st online. Before the fix the preview was
// cut to the first 30 by name and presence was applied afterwards, so the panel
// showed nobody at all.
func TestChannelRoute_Details_ShowsAnOnlineMemberPastTheAlphabeticalCut(t *testing.T) {
	env := newChannelRouteEnv(t, allowAllSessionValidator{}, routeActiveWorkspace(),
		routeMember(domain.WorkspaceRoleMember, domain.MemberStatusActive), true)

	roster := rosterOf(domain.MaxChannelDetailsMembers + 1)
	lastMember := roster[len(roster)-1]
	seedVisibleChannel(env, testChannelID, domain.ChannelTypePublic, storage.ChannelMemberPage{
		Online:     roster,
		TotalCount: len(roster),
	})
	// Only the member who sorts last of all 31 is connected.
	env.presence.online = []string{lastMember.UserID}

	recorder := httptest.NewRecorder()
	env.router.ServeHTTP(recorder, getDetails(bearer(makeTestToken(t, testUserID, testHMACSecret, testIssuer, testAudience, time.Hour)), testChannelID))

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	data := decodeDetails(t, recorder)
	members := data["online_members"].([]any)
	if len(members) != 1 {
		t.Fatalf("expected exactly the one online member, got %d: %v", len(members), members)
	}
	if members[0].(map[string]any)["user_id"] != lastMember.UserID {
		t.Fatalf("the online member past the alphabetical cut must be shown, got %v", members[0])
	}
	if data["online_member_count"] != float64(1) {
		t.Fatalf("expected 1 online, got %v", data["online_member_count"])
	}
	// The channel's size is reported in full and is not the online figure.
	if data["member_count"] != float64(domain.MaxChannelDetailsMembers+1) {
		t.Fatalf("expected the full member total, got %v", data["member_count"])
	}
	// The presence snapshot reached the query rather than being applied after it.
	if len(env.members.lastOnlineUserIDs) != 1 || env.members.lastOnlineUserIDs[0] != lastMember.UserID {
		t.Fatalf("the presence filter must reach the member query, got %v", env.members.lastOnlineUserIDs)
	}
}

func TestChannelRoute_Details_OfflineMembersNeverFillThePreview(t *testing.T) {
	env := newChannelRouteEnv(t, allowAllSessionValidator{}, routeActiveWorkspace(),
		routeMember(domain.WorkspaceRoleMember, domain.MemberStatusActive), true)

	roster := rosterOf(domain.MaxChannelDetailsMembers + 20)
	// Everyone from the cap onwards is online; everyone before it is not.
	online := make([]string, 0, 20)
	for _, member := range roster[domain.MaxChannelDetailsMembers:] {
		online = append(online, member.UserID)
	}
	seedVisibleChannel(env, testChannelID, domain.ChannelTypePublic, storage.ChannelMemberPage{
		Online: roster, TotalCount: len(roster),
	})
	env.presence.online = online

	recorder := httptest.NewRecorder()
	env.router.ServeHTTP(recorder, getDetails(bearer(makeTestToken(t, testUserID, testHMACSecret, testIssuer, testAudience, time.Hour)), testChannelID))

	data := decodeDetails(t, recorder)
	members := data["online_members"].([]any)
	if len(members) != 20 {
		t.Fatalf("expected all 20 online members, got %d", len(members))
	}
	returned := map[string]bool{}
	for _, member := range members {
		returned[member.(map[string]any)["user_id"].(string)] = true
	}
	for _, member := range roster[:domain.MaxChannelDetailsMembers] {
		if returned[member.UserID] {
			t.Fatalf("an offline member reached the preview: %s", member.UserID)
		}
	}
}

func TestChannelRoute_Details_CapsThePreviewWithoutTruncatingTheCounts(t *testing.T) {
	env := newChannelRouteEnv(t, allowAllSessionValidator{}, routeActiveWorkspace(),
		routeMember(domain.WorkspaceRoleMember, domain.MemberStatusActive), true)

	roster := rosterOf(domain.MaxChannelDetailsMembers + 12)
	online := make([]string, 0, len(roster))
	for _, member := range roster {
		online = append(online, member.UserID)
	}
	seedVisibleChannel(env, testChannelID, domain.ChannelTypePublic, storage.ChannelMemberPage{
		Online: roster, TotalCount: len(roster),
	})
	env.presence.online = online

	recorder := httptest.NewRecorder()
	env.router.ServeHTTP(recorder, getDetails(bearer(makeTestToken(t, testUserID, testHMACSecret, testIssuer, testAudience, time.Hour)), testChannelID))

	data := decodeDetails(t, recorder)
	members := data["online_members"].([]any)
	if len(members) != domain.MaxChannelDetailsMembers {
		t.Fatalf("expected exactly the cap, got %d", len(members))
	}
	// Deterministic page: the alphabetically first slice of the online set.
	if members[0].(map[string]any)["user_id"] != roster[0].UserID {
		t.Fatalf("expected a deterministic ordered page, got %v", members[0])
	}
	if data["online_member_count"] != float64(len(roster)) || data["member_count"] != float64(len(roster)) {
		t.Fatalf("counts must not be truncated by the preview: %v", data)
	}
}

func TestChannelRoute_Details_ReportsNobodyOnlineWithoutHidingTheChannelSize(t *testing.T) {
	env := newChannelRouteEnv(t, allowAllSessionValidator{}, routeActiveWorkspace(),
		routeMember(domain.WorkspaceRoleMember, domain.MemberStatusActive), true)

	roster := rosterOf(9)
	seedVisibleChannel(env, testChannelID, domain.ChannelTypePublic, storage.ChannelMemberPage{
		Online: roster, TotalCount: len(roster),
	})
	// Nobody connected.
	env.presence.online = nil

	recorder := httptest.NewRecorder()
	env.router.ServeHTTP(recorder, getDetails(bearer(makeTestToken(t, testUserID, testHMACSecret, testIssuer, testAudience, time.Hour)), testChannelID))

	data := decodeDetails(t, recorder)
	if members := data["online_members"].([]any); len(members) != 0 {
		t.Fatalf("expected an empty preview, got %v", members)
	}
	if data["online_member_count"] != float64(0) {
		t.Fatalf("expected 0 online, got %v", data["online_member_count"])
	}
	if data["member_count"] != float64(9) {
		t.Fatalf("nobody online must not mean no members, got %v", data["member_count"])
	}
}

func TestChannelRoute_Details_IgnoresOnlineUsersFromOtherChannels(t *testing.T) {
	env := newChannelRouteEnv(t, allowAllSessionValidator{}, routeActiveWorkspace(),
		routeMember(domain.WorkspaceRoleMember, domain.MemberStatusActive), true)

	roster := rosterOf(3)
	seedVisibleChannel(env, testChannelID, domain.ChannelTypePublic, storage.ChannelMemberPage{
		Online: roster, TotalCount: len(roster),
	})
	seedVisibleChannel(env, otherChannelID, domain.ChannelTypePublic, storage.ChannelMemberPage{
		Online: []domain.ChannelMemberProfile{
			{UserID: "outsider", DisplayName: "Aaa Outro Canal", Role: domain.ChannelRoleMember},
		},
		TotalCount: 1,
	})
	// The presence snapshot covers the whole workspace, so it naturally includes
	// people who are online in other channels — and someone who is in no channel
	// this caller can see at all.
	env.presence.online = []string{roster[0].UserID, "outsider", "ghost"}

	recorder := httptest.NewRecorder()
	env.router.ServeHTTP(recorder, getDetails(bearer(makeTestToken(t, testUserID, testHMACSecret, testIssuer, testAudience, time.Hour)), testChannelID))

	data := decodeDetails(t, recorder)
	members := data["online_members"].([]any)
	if len(members) != 1 || members[0].(map[string]any)["user_id"] != roster[0].UserID {
		t.Fatalf("only online members of this channel may appear, got %v", members)
	}
}
