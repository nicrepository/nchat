package httpapi_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	httpapi "github.com/nicrepository/nchat/services/chat-service/internal/http"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
)

// The administrable roster route (issue #469).
//
// It exists because the details panel's online_members is a presence preview
// and the removal control needs membership. These tests hold that distinction:
// what the route answers, who it answers, and that nothing about it is taken
// from the request beyond the channel it names.

func rosterRequest(channelID string) *http.Request {
	r := requestWithUser(http.MethodGet, "/api/chat/channels/"+channelID+"/members", nil)
	r.SetPathValue("channelID", channelID)
	return r
}

func serveRoster(t *testing.T, handler *httpapi.ChannelHandler, channelID string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.Members(rec, rosterRequest(channelID))
	return rec
}

func TestChannelHandler_Members_ReturnsMembershipAndTheServerTotal(t *testing.T) {
	provider := &fakeChannelProvider{roster: service.ChannelRoster{
		Members: []domain.ChannelMemberProfile{
			{UserID: "u-1", DisplayName: "Álvaro Neto", AvatarURL: "/media/a.png", Role: domain.ChannelRoleModerator},
			{UserID: "u-2", DisplayName: "Juliane Lino", Role: domain.ChannelRoleMember},
		},
		// Larger than the page on purpose: the count is the membership, never
		// the length of what fits.
		MemberCount: 12,
	}}

	rec := serveRoster(t, channelTestHandler(provider), testChannelID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	data := detailsData(t, rec)
	if data["total"] != float64(12) {
		t.Fatalf("total = %v, want 12", data["total"])
	}
	members, ok := data["members"].([]any)
	if !ok || len(members) != 2 {
		t.Fatalf("members = %v", data["members"])
	}
	first, _ := members[0].(map[string]any)
	if first["user_id"] != "u-1" || first["display_name"] != "Álvaro Neto" || first["role"] != "moderator" {
		t.Fatalf("first member = %v", first)
	}
	// A roster row says who belongs, not who is connected: a presence field here
	// would be a second, staler answer to a question the realtime store already
	// owns.
	if _, present := first["presence"]; present {
		t.Fatalf("roster rows must not carry presence: %v", first)
	}
	second, _ := members[1].(map[string]any)
	if _, present := second["avatar_url"]; present {
		t.Fatalf("an absent avatar must be omitted, not sent empty: %v", second)
	}
}

// The workspace and the caller are the session's. The only thing the request
// contributes is the channel it names.
func TestChannelHandler_Members_DerivesCallerAndWorkspaceServerSide(t *testing.T) {
	provider := &fakeChannelProvider{}

	if rec := serveRoster(t, channelTestHandler(provider), testChannelID); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	want := service.ChannelRosterInput{
		WorkspaceID: testWorkspaceID,
		CallerID:    msgTestUserID,
		ChannelID:   testChannelID,
		MemberLimit: domain.MaxChannelDetailsMembers,
	}
	if provider.lastRosterInput != want {
		t.Fatalf("input = %+v, want %+v", provider.lastRosterInput, want)
	}
}

func TestChannelHandler_Members_RequiresAnAuthenticatedCaller(t *testing.T) {
	provider := &fakeChannelProvider{}
	request := httptest.NewRequest(http.MethodGet, "/api/chat/channels/"+testChannelID+"/members", nil)
	request.SetPathValue("channelID", testChannelID)

	rec := httptest.NewRecorder()
	channelTestHandler(provider).Members(rec, request)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if provider.rosterCalls != 0 {
		t.Fatal("an unauthenticated request reached the service")
	}
}

func TestChannelHandler_Members_RejectsAMalformedChannelID(t *testing.T) {
	provider := &fakeChannelProvider{}

	rec := serveRoster(t, channelTestHandler(provider), "not-a-uuid")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if provider.rosterCalls != 0 {
		t.Fatal("a malformed id reached the service")
	}
}

// The two denials stay distinguishable in the way the rest of the member
// surface already distinguishes them: "you may not administer channels" is a
// 403, while a channel you cannot see is indistinguishable from one that does
// not exist.
func TestChannelHandler_Members_MapsServiceDenials(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want int
	}{
		{name: "forbidden", err: domain.ErrForbidden, want: http.StatusForbidden},
		{name: "not found", err: domain.ErrNotFound, want: http.StatusNotFound},
		{name: "unknown", err: errors.New("boom: relation chat.channel_members"), want: http.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			rec := serveRoster(t, channelTestHandler(&fakeChannelProvider{rosterErr: test.err}), testChannelID)
			if rec.Code != test.want {
				t.Fatalf("status = %d, want %d", rec.Code, test.want)
			}
			if body := rec.Body.String(); test.want == http.StatusInternalServerError &&
				strings.Contains(body, "chat.channel_members") {
				t.Fatalf("an internal failure leaked the store's message: %s", body)
			}
		})
	}
}

// Without wiring the route answers "unavailable" rather than an empty roster,
// which would read on screen as a channel with nobody in it.
func TestChannelHandler_Members_WithoutWiringIsUnavailable(t *testing.T) {
	rec := httptest.NewRecorder()
	(&httpapi.ChannelHandler{}).Members(rec, rosterRequest(testChannelID))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}
