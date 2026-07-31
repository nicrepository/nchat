package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	httpapi "github.com/nicrepository/nchat/services/chat-service/internal/http"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
)

// fakePresence answers a scripted online set and records every workspace it was
// asked about, so "the handler asks once, for the workspace it resolved
// server-side" is a property under test rather than an assumption.
type fakePresence struct {
	online []string
	asked  []string
}

func (f *fakePresence) OnlineUserIDs(workspaceID string) []string {
	f.asked = append(f.asked, workspaceID)
	return f.online
}

func detailsChannel() domain.Channel {
	return domain.Channel{
		ID:          testChannelID,
		WorkspaceID: testWorkspaceID,
		Slug:        "infra",
		DisplayName: "Infraestrutura",
		Type:        domain.ChannelTypePrivate,
		Status:      domain.ChannelStatusActive,
		CreatedAt:   time.Date(2024, 1, 12, 9, 30, 0, 0, time.UTC),
	}
}

func detailsRequest(channelID string) *http.Request {
	r := requestWithUser(http.MethodGet, "/api/chat/channels/"+channelID+"/details", nil)
	r.SetPathValue("channelID", channelID)
	return r
}

func serveDetails(t *testing.T, handler *httpapi.ChannelHandler, channelID string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.Details(rec, detailsRequest(channelID))
	return rec
}

func detailsData(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	body := decodeBody(t, rec)
	data, ok := body["data"].(map[string]any)
	if !ok {
		t.Fatalf("expected a data envelope, got %v", body)
	}
	return data
}

func TestChannelHandler_Details_ReturnsChannelAndOnlineMembers(t *testing.T) {
	provider := &fakeChannelProvider{details: service.ChannelDetails{
		Channel: detailsChannel(),
		OnlineMembers: []domain.ChannelMemberProfile{
			{UserID: "u-1", DisplayName: "Álvaro Neto", AvatarURL: "/media/a.png", Role: domain.ChannelRoleModerator},
			{UserID: "u-2", DisplayName: "Juliane Lino", Role: domain.ChannelRoleMember},
		},
		// Deliberately larger than len(OnlineMembers): both counts describe the
		// whole set, never the size of the capped preview.
		OnlineCount: 9,
		MemberCount: 12,
	}}
	presence := &fakePresence{online: []string{"u-1", "u-2"}}
	handler := channelTestHandler(provider).WithPresence(presence)

	rec := serveDetails(t, handler, testChannelID)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	data := detailsData(t, rec)
	if data["id"] != testChannelID || data["slug"] != "infra" || data["display_name"] != "Infraestrutura" {
		t.Fatalf("unexpected channel identity: %v", data)
	}
	if data["type"] != "private" {
		t.Fatalf("expected the domain type, got %v", data["type"])
	}
	if data["created_at"] != "2024-01-12T09:30:00Z" {
		t.Fatalf("expected an RFC3339 UTC creation date, got %v", data["created_at"])
	}
	if data["member_count"] != float64(12) {
		t.Fatalf("expected the store's total, got %v", data["member_count"])
	}
	if data["online_member_count"] != float64(9) {
		t.Fatalf("expected the online total, got %v", data["online_member_count"])
	}
	// The general roster field must not exist: this list is presence-filtered
	// and must never be mistaken for one.
	if _, present := data["members"]; present {
		t.Fatalf("the payload must expose online_members only: %v", data)
	}

	members, ok := data["online_members"].([]any)
	if !ok || len(members) != 2 {
		t.Fatalf("expected two online members, got %v", data["online_members"])
	}
	first := members[0].(map[string]any)
	if first["user_id"] != "u-1" || first["display_name"] != "Álvaro Neto" {
		t.Fatalf("unexpected first member: %v", first)
	}
	if first["role"] != "moderator" {
		t.Fatalf("expected the channel role on the first member: %v", first)
	}
	// Every entry states its presence explicitly, so a client can assert it
	// rather than infer it from the field name.
	for _, member := range members {
		if member.(map[string]any)["presence"] != "online" {
			t.Fatalf("every entry of online_members must be online: %v", member)
		}
	}
	second := members[1].(map[string]any)
	if _, present := second["avatar_url"]; present {
		t.Fatal("a member without an avatar must omit the field")
	}

	// Presence is asked once, in one batch, for the workspace resolved server-side.
	if len(presence.asked) != 1 || presence.asked[0] != testWorkspaceID {
		t.Fatalf("presence must be resolved once per request, scoped by workspace, got %v", presence.asked)
	}
	if provider.lastDetailsInput.WorkspaceID != testWorkspaceID ||
		provider.lastDetailsInput.CallerID != msgTestUserID ||
		provider.lastDetailsInput.ChannelID != testChannelID {
		t.Fatalf("unexpected service input: %+v", provider.lastDetailsInput)
	}
	// The presence snapshot must reach the service as a filter — that is what
	// makes the database apply it before the limit.
	if len(provider.lastDetailsInput.OnlineUserIDs) != 2 {
		t.Fatalf("expected the online snapshot to reach the service, got %v",
			provider.lastDetailsInput.OnlineUserIDs)
	}
	if provider.lastDetailsInput.MemberLimit != domain.MaxChannelDetailsMembers {
		t.Fatalf("expected the server-side member cap, got %d", provider.lastDetailsInput.MemberLimit)
	}
}

func TestChannelHandler_Details_AsksForNoOnlineMembersWhenPresenceIsNotTracked(t *testing.T) {
	provider := &fakeChannelProvider{details: service.ChannelDetails{
		Channel: detailsChannel(), MemberCount: 12,
	}}
	// No WithPresence: an unknown presence source must not be read as "online".
	rec := serveDetails(t, channelTestHandler(provider), testChannelID)

	if len(provider.lastDetailsInput.OnlineUserIDs) != 0 {
		t.Fatalf("an unwired presence source must claim nobody is online, got %v",
			provider.lastDetailsInput.OnlineUserIDs)
	}
	data := detailsData(t, rec)
	if members := data["online_members"].([]any); len(members) != 0 {
		t.Fatalf("expected an empty online preview, got %v", members)
	}
	// The channel's size is still reported: nobody online is not "no members".
	if data["member_count"] != float64(12) {
		t.Fatalf("expected the member total to survive, got %v", data["member_count"])
	}
}

func TestChannelHandler_Details_HidesInvisibleChannelsBehindNotFound(t *testing.T) {
	for name, err := range map[string]error{
		"not found": domain.ErrNotFound,
		// A caller with no active workspace membership must not be able to tell
		// "this channel exists but is not yours" from "no such channel".
		"forbidden": domain.ErrForbidden,
	} {
		t.Run(name, func(t *testing.T) {
			rec := serveDetails(t, channelTestHandler(&fakeChannelProvider{detailsErr: err}), testChannelID)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("expected 404, got %d", rec.Code)
			}
			if body := rec.Body.String(); !json.Valid([]byte(body)) {
				t.Fatalf("expected a JSON error envelope, got %q", body)
			}
		})
	}
}

func TestChannelHandler_Details_RejectsAMalformedChannelID(t *testing.T) {
	provider := &fakeChannelProvider{}
	rec := serveDetails(t, channelTestHandler(provider), "not-a-uuid")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	if provider.detailsCalls != 0 {
		t.Fatal("a malformed channel id must never reach the service")
	}
}

func TestChannelHandler_Details_RequiresAnAuthenticatedCaller(t *testing.T) {
	provider := &fakeChannelProvider{}
	handler := channelTestHandler(provider)
	request := httptest.NewRequest(http.MethodGet, "/api/chat/channels/"+testChannelID+"/details", nil)
	request.SetPathValue("channelID", testChannelID)

	rec := httptest.NewRecorder()
	handler.Details(rec, request)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	if provider.detailsCalls != 0 {
		t.Fatal("an unauthenticated request must never reach the service")
	}
}

func TestChannelHandler_Details_IsUnavailableWithoutWiring(t *testing.T) {
	handler := httpapi.NewChannelHandler(nil, nil, nil)
	rec := serveDetails(t, handler, testChannelID)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}
