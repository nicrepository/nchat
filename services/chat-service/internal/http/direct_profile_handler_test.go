package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	httpapi "github.com/nicrepository/nchat/services/chat-service/internal/http"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
)

// Direct-profile handler tests (issue #443).
//
// The endpoint names a conversation and nothing else. What it must never become
// is a way to read a chosen person's profile, so these tests pin down both the
// shape of what is returned and — just as much — what cannot be asked for.

func directConversation() domain.DMConversation {
	return domain.DMConversation{
		ID:          testConversationID,
		WorkspaceID: testWorkspaceID,
		Type:        domain.DMConversationTypeDirect,
		Status:      domain.DMConversationStatusActive,
		CreatedAt:   time.Date(2024, 3, 4, 15, 0, 0, 0, time.UTC),
	}
}

func directProfileResult() service.DirectProfile {
	return service.DirectProfile{
		Conversation: directConversation(),
		Profile: domain.DMDirectProfile{
			UserID:      dmOtherUserID,
			DisplayName: "Juliane Lino",
			AvatarURL:   "/media/juliane.png",
			Email:       "juliane@nic.test",
		},
	}
}

func directProfileRequest(conversationID string) *http.Request {
	r := requestWithUser(http.MethodGet, "/api/chat/dm/"+conversationID+"/profile", nil)
	r.SetPathValue("conversationID", conversationID)
	return r
}

func serveDirectProfile(t *testing.T, handler *httpapi.DMHandler, conversationID string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.DirectProfile(rec, directProfileRequest(conversationID))
	return rec
}

func directProfileHandler(provider *fakeDMProvider) *httpapi.DMHandler {
	return httpapi.NewDMHandler(
		&fakeWorkspaceResolver{workspace: activeWorkspace()}, provider, &fakeDMRateLimiter{},
	)
}

// profileOf reads the profile out of an already-decoded envelope. The recorder's
// body is a one-shot buffer, so a test that needs both halves decodes once.
func profileOf(t *testing.T, data map[string]any) map[string]any {
	t.Helper()
	if data["kind"] != "direct" {
		t.Fatalf("expected the direct variant tag, got %v", data["kind"])
	}
	profile, ok := data["profile"].(map[string]any)
	if !ok {
		t.Fatalf("expected a profile object, got %v", data)
	}
	return profile
}

func TestDMHandler_DirectProfile_ReturnsTheCounterpartProfile(t *testing.T) {
	provider := &fakeDMProvider{directProfile: directProfileResult()}
	presence := &fakePresence{online: []string{dmOtherUserID}}
	handler := directProfileHandler(provider).WithPresence(presence)

	rec := serveDirectProfile(t, handler, testConversationID)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	data := detailsData(t, rec)
	if data["conversation_id"] != testConversationID {
		t.Fatalf("unexpected conversation id: %v", data)
	}
	profile := profileOf(t, data)
	for key, want := range map[string]any{
		"user_id":      dmOtherUserID,
		"display_name": "Juliane Lino",
		"avatar_url":   "/media/juliane.png",
		"email":        "juliane@nic.test",
		"presence":     "online",
	} {
		if profile[key] != want {
			t.Fatalf("profile[%q] = %v, want %v", key, profile[key], want)
		}
	}
	// The workspace the presence source was consulted for is the one derived
	// server-side, never anything the request carried.
	if len(presence.asked) != 1 || presence.asked[0] != testWorkspaceID {
		t.Fatalf("presence must be read for the server-side workspace, got %v", presence.asked)
	}
	// A conversation is never a roster here: shipping participants would invite
	// the client to pick a side the server has already picked.
	if _, hasParticipants := data["participants"]; hasParticipants {
		t.Fatalf("a direct profile must not carry a participant list: %v", data)
	}
}

func TestDMHandler_DirectProfile_ExposesNothingBeyondTheProfileCard(t *testing.T) {
	handler := directProfileHandler(&fakeDMProvider{directProfile: directProfileResult()}).
		WithPresence(&fakePresence{})

	profile := profileOf(t, detailsData(t, serveDirectProfile(t, handler, testConversationID)))
	// A profile summary is not a directory record. None of these has any reason
	// to reach a chat panel, and each would be a real disclosure.
	for _, forbidden := range []string{
		"phone", "auth_source", "external_subject", "subject", "password_hash",
		"status", "roles", "role", "last_login_at", "email_verified_at",
		"created_at", "ip", "session", "claims", "workspace_id", "deleted_at",
	} {
		if _, present := profile[forbidden]; present {
			t.Fatalf("profile must not expose %q: %v", forbidden, profile)
		}
	}
}

func TestDMHandler_DirectProfile_OmitsFieldsTheDomainDoesNotRecord(t *testing.T) {
	provider := &fakeDMProvider{directProfile: service.DirectProfile{
		Conversation: directConversation(),
		// Only an identity: no avatar, no e-mail.
		Profile: domain.DMDirectProfile{UserID: dmOtherUserID, DisplayName: "Juliane Lino"},
	}}
	// Presence unwired: the field must be absent rather than asserted "offline".
	handler := directProfileHandler(provider)

	profile := profileOf(t, detailsData(t, serveDirectProfile(t, handler, testConversationID)))
	// Absent, not empty: a client can tell "this deployment records nothing" from
	// "recorded as blank", and renders "Não informado" either way without ever
	// being told a falsehood. job_title/department/timezone have no column
	// anywhere in the domain, so they are never sent at all.
	for _, absent := range []string{"avatar_url", "email", "presence", "job_title", "department", "timezone"} {
		if _, present := profile[absent]; present {
			t.Fatalf("%q must be omitted when unknown: %v", absent, profile)
		}
	}
	if profile["display_name"] != "Juliane Lino" {
		t.Fatalf("the name is the one field that must survive: %v", profile)
	}
}

func TestDMHandler_DirectProfile_ReportsOfflineOnlyWhenPresenceIsTracked(t *testing.T) {
	handler := directProfileHandler(&fakeDMProvider{directProfile: directProfileResult()}).
		WithPresence(&fakePresence{online: []string{"someone-else"}})

	profile := profileOf(t, detailsData(t, serveDirectProfile(t, handler, testConversationID)))
	if profile["presence"] != "offline" {
		t.Fatalf("presence = %v, want offline", profile["presence"])
	}
	// Presence never gates the profile: the rest of the card is still there.
	if profile["display_name"] != "Juliane Lino" || profile["email"] != "juliane@nic.test" {
		t.Fatalf("presence must not affect the rest of the profile: %v", profile)
	}
}

func TestDMHandler_DirectProfile_FoldsEveryDenialIntoTheSame404(t *testing.T) {
	for name, err := range map[string]error{
		// A conversation that does not exist, one in another workspace, an
		// archived one, one the caller never joined and one they left all arrive
		// as ErrNotFound; a group arrives the same way. The response must not let
		// a caller tell them apart, or the endpoint becomes a probe for which
		// conversation UUIDs exist.
		"unknown, foreign, archived, group or not a participant": domain.ErrNotFound,
		"forbidden": domain.ErrForbidden,
	} {
		t.Run(name, func(t *testing.T) {
			handler := directProfileHandler(&fakeDMProvider{directProfileErr: err})

			rec := serveDirectProfile(t, handler, testConversationID)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
			}
			if body := rec.Body.String(); !strings.Contains(body, "conversation not found") {
				t.Fatalf("unexpected body: %s", body)
			}
		})
	}
}

func TestDMHandler_DirectProfile_ReportsACorruptConversationAsAServerError(t *testing.T) {
	handler := directProfileHandler(&fakeDMProvider{
		directProfileErr: domain.ErrInconsistentDirectConversation,
	})

	rec := serveDirectProfile(t, handler, testConversationID)
	// Not a 404: a 'direct' row without exactly one counterpart is broken data,
	// and hiding it among the denials would make it invisible to operators.
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
	// The body still says nothing specific — the caller learns no more than they
	// would from any other internal failure.
	if body := rec.Body.String(); !strings.Contains(body, "internal error") || strings.Contains(body, "counterpart") {
		t.Fatalf("unexpected body: %s", body)
	}
}

func TestDMHandler_DirectProfile_RejectsAnInvalidConversationID(t *testing.T) {
	provider := &fakeDMProvider{directProfile: directProfileResult()}
	handler := directProfileHandler(provider)

	rec := serveDirectProfile(t, handler, "not-a-uuid")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if provider.profileCalls != 0 {
		t.Fatalf("a malformed ID must not reach the service, got %d calls", provider.profileCalls)
	}
}

func TestDMHandler_DirectProfile_RequiresAnAuthenticatedCaller(t *testing.T) {
	provider := &fakeDMProvider{directProfile: directProfileResult()}
	handler := directProfileHandler(provider)

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/chat/dm/"+testConversationID+"/profile", nil)
	r.SetPathValue("conversationID", testConversationID)
	handler.DirectProfile(rec, r)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
	if provider.profileCalls != 0 {
		t.Fatalf("an anonymous request must not reach the service, got %d calls", provider.profileCalls)
	}
}

func TestDMHandler_DirectProfile_DerivesCallerAndWorkspaceServerSide(t *testing.T) {
	provider := &fakeDMProvider{directProfile: directProfileResult()}
	handler := directProfileHandler(provider)

	rec := httptest.NewRecorder()
	// A hostile query string offering an identity of its own. The handler builds
	// its input from the session and the workspace resolver, so none of this can
	// reach the service.
	r := requestWithUser(http.MethodGet,
		"/api/chat/dm/"+testConversationID+"/profile?user_id="+dmOtherUserID+"&workspace_id=other-ws", nil)
	r.SetPathValue("conversationID", testConversationID)
	handler.DirectProfile(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got := provider.lastProfileInput
	if got.CallerID != msgTestUserID || got.WorkspaceID != testWorkspaceID {
		t.Fatalf("caller and workspace must be server-derived, got %+v", got)
	}
	if got.ConversationID != testConversationID {
		t.Fatalf("conversation must come from the path, got %+v", got)
	}
}

func TestDMHandler_DirectProfile_IsUnavailableWithoutItsDependencies(t *testing.T) {
	handler := httpapi.NewDMHandler(nil, nil, nil)

	rec := serveDirectProfile(t, handler, testConversationID)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestDMHandler_DirectProfile_RevokedMembershipIsAGeneric404WithoutPresenceLookup(t *testing.T) {
	// The membership was revoked between the visibility check and the profile
	// query; the service reports the second query's verdict.
	provider := &fakeDMProvider{directProfileErr: domain.ErrNotFound}
	presence := &fakePresence{online: []string{dmOtherUserID}}
	handler := directProfileHandler(provider).WithPresence(presence)

	rec := serveDirectProfile(t, handler, testConversationID)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
	// Presence is read only for a profile that is actually being returned, so a
	// refused request cannot be used to probe who is online.
	if len(presence.asked) != 0 {
		t.Fatalf("presence must not be consulted for a refused request, got %v", presence.asked)
	}
	// Not a single attribute of the counterpart may appear in the refusal.
	body := rec.Body.String()
	for _, leaked := range []string{"Juliane", "juliane@nic.test", "/media/juliane.png", "online", dmOtherUserID} {
		if strings.Contains(body, leaked) {
			t.Fatalf("refusal must not contain %q: %s", leaked, body)
		}
	}
}
