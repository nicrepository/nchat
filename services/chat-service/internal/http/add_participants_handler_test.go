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
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

func addParticipantsHandler(
	provider *fakeDMProvider, broadcast *recordingBroadcaster, limiter *fakeDMRateLimiter,
) *httpapi.DMHandler {
	if limiter == nil {
		limiter = &fakeDMRateLimiter{}
	}
	return httpapi.NewDMHandler(
		&fakeWorkspaceResolver{workspace: activeWorkspace()}, provider, limiter,
	).WithMembersBroadcast(broadcast)
}

func addParticipantsRequest(body string) *http.Request {
	r := requestWithUser(
		http.MethodPost, "/api/chat/dm/"+dmConversationID+"/members", strings.NewReader(body),
	)
	r.SetPathValue("conversationID", dmConversationID)
	r.Header.Set("Content-Type", "application/json")
	return r
}

func serveAddParticipants(handler *httpapi.DMHandler, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	handler.AddParticipants(rec, r)
	return rec
}

func TestAddParticipants_PersistsAndReportsTheServerCounts(t *testing.T) {
	provider := &fakeDMProvider{
		addMembersResult: storage.AddMembersResult{Added: 1, AlreadyMembers: 1, TotalCount: 5},
	}
	broadcast := &recordingBroadcaster{}

	rec := serveAddParticipants(
		addParticipantsHandler(provider, broadcast, nil), addParticipantsRequest(validAddMembersBody),
	)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	data := detailsData(t, rec)
	if got, _ := data["member_count"].(float64); got != 5 {
		t.Fatalf("member_count = %v, want 5", data["member_count"])
	}
}

// Same invariant as the channel route: nothing about who is acting or which
// workspace is touched comes from the request.
func TestAddParticipants_DerivesActorAndWorkspaceServerSide(t *testing.T) {
	provider := &fakeDMProvider{}

	serveAddParticipants(
		addParticipantsHandler(provider, &recordingBroadcaster{}, nil),
		addParticipantsRequest(validAddMembersBody),
	)

	if provider.lastAddMembers.CallerID != msgTestUserID {
		t.Fatalf("CallerID = %q, want %q", provider.lastAddMembers.CallerID, msgTestUserID)
	}
	if provider.lastAddMembers.WorkspaceID != testWorkspaceID {
		t.Fatalf("WorkspaceID = %q, want %q", provider.lastAddMembers.WorkspaceID, testWorkspaceID)
	}
	if provider.lastAddMembers.ConversationID != dmConversationID {
		t.Fatalf("ConversationID = %q, want the path value", provider.lastAddMembers.ConversationID)
	}
}

func TestAddParticipants_RejectsClientSuppliedAuthorityFields(t *testing.T) {
	provider := &fakeDMProvider{}
	body := `{"user_ids":["44444444-4444-4444-8444-444444444444"],"workspace_id":"x"}`

	rec := serveAddParticipants(
		addParticipantsHandler(provider, &recordingBroadcaster{}, nil), addParticipantsRequest(body),
	)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if provider.addMembersCalls != 0 {
		t.Fatal("a body carrying an authority field must not reach the service")
	}
}

func TestAddParticipants_RequiresAuthenticatedUser(t *testing.T) {
	provider := &fakeDMProvider{}
	r := httptest.NewRequest(
		http.MethodPost, "/api/chat/dm/"+dmConversationID+"/members",
		strings.NewReader(validAddMembersBody),
	)
	r.SetPathValue("conversationID", dmConversationID)
	r.Header.Set("Content-Type", "application/json")

	rec := serveAddParticipants(addParticipantsHandler(provider, &recordingBroadcaster{}, nil), r)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if provider.addMembersCalls != 0 {
		t.Fatal("an unauthenticated request must not reach the service")
	}
}

func TestAddParticipants_RejectsNonUUIDConversationID(t *testing.T) {
	provider := &fakeDMProvider{}
	r := requestWithUser(http.MethodPost, "/api/chat/dm/nope/members", strings.NewReader(validAddMembersBody))
	r.SetPathValue("conversationID", "nope")
	r.Header.Set("Content-Type", "application/json")

	rec := serveAddParticipants(addParticipantsHandler(provider, &recordingBroadcaster{}, nil), r)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if provider.addMembersCalls != 0 {
		t.Fatal("a malformed conversation ID must not reach the service")
	}
}

// A 1:1 conversation and a group the caller is not in are the same ErrNotFound
// in the service, and must stay the same 404 here.
func TestAddParticipants_MapsDomainErrors(t *testing.T) {
	tests := map[string]struct {
		err  error
		want int
	}{
		"1:1 or invisible conversation": {domain.ErrNotFound, http.StatusNotFound},
		"ineligible participant":        {domain.ErrForbidden, http.StatusForbidden},
		"invalid payload":               {domain.ErrNoMembersRequested, http.StatusBadRequest},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			provider := &fakeDMProvider{addMembersErr: test.err}

			rec := serveAddParticipants(
				addParticipantsHandler(provider, &recordingBroadcaster{}, nil),
				addParticipantsRequest(validAddMembersBody),
			)

			if rec.Code != test.want {
				t.Fatalf("status = %d, want %d", rec.Code, test.want)
			}
		})
	}
}

func TestAddParticipants_BroadcastsWithTheDMTargetTypeAfterCommit(t *testing.T) {
	provider := &fakeDMProvider{
		// Added and AddedUserIDs both come from the transaction's RETURNING and
		// cannot disagree.
		addMembersResult: storage.AddMembersResult{
			Added: 2, TotalCount: 6,
			AddedUserIDs: []string{dmOtherUserID, "99999999-9999-4999-8999-999999999994"},
		},
	}
	broadcast := &recordingBroadcaster{}

	serveAddParticipants(
		addParticipantsHandler(provider, broadcast, nil), addParticipantsRequest(validAddMembersBody),
	)

	if len(broadcast.calls) != 1 {
		t.Fatalf("broadcasts = %d, want 1", len(broadcast.calls))
	}
	// "dm", not "channel": a group lives in chat.dm_conversations, and a
	// subscriber routes the refetch off this field.
	if broadcast.calls[0].TargetType != "dm" {
		t.Fatalf("TargetType = %q, want \"dm\"", broadcast.calls[0].TargetType)
	}
	if broadcast.calls[0].TargetID != dmConversationID {
		t.Fatalf("TargetID = %q, want the conversation", broadcast.calls[0].TargetID)
	}
}

func TestAddParticipants_DoesNotBroadcastOnFailure(t *testing.T) {
	broadcast := &recordingBroadcaster{}

	serveAddParticipants(
		addParticipantsHandler(&fakeDMProvider{addMembersErr: domain.ErrNotFound}, broadcast, nil),
		addParticipantsRequest(validAddMembersBody),
	)

	if len(broadcast.calls) != 0 {
		t.Fatalf("broadcast %d event(s) for a failed write", len(broadcast.calls))
	}
}

// Both add-members routes must spend the same budget, so a caller cannot get a
// second allowance for the same capability by switching conversation type.
func TestAddParticipants_SharesTheAddMembersBudgetWithChannels(t *testing.T) {
	limiter := &fakeDMRateLimiter{}
	channels := addMembersHandler(&fakeMemberManager{}, &recordingBroadcaster{}, limiter)
	groups := addParticipantsHandler(&fakeDMProvider{}, &recordingBroadcaster{}, limiter)

	// Spend the whole budget on the channel route...
	for i := 0; i < 10; i++ {
		serveAddMembers(channels, addMembersRequest(validAddMembersBody))
	}
	// ...then the group route must already be exhausted.
	rec := serveAddParticipants(groups, addParticipantsRequest(validAddMembersBody))

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 — the two routes must share one budget", rec.Code)
	}
}

// ── Group details route (issues #441, #398) ─────────────────────────────────

func groupDetailsRequest() *http.Request {
	r := requestWithUser(http.MethodGet, "/api/chat/dm/"+dmConversationID+"/details", nil)
	r.SetPathValue("conversationID", dmConversationID)
	return r
}

func serveGroupDetails(handler *httpapi.DMHandler, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	handler.Details(rec, r)
	return rec
}

func groupDetailsHandler(provider *fakeDMProvider) *httpapi.DMHandler {
	return httpapi.NewDMHandler(
		&fakeWorkspaceResolver{workspace: activeWorkspace()}, provider, &fakeDMRateLimiter{},
	)
}

func TestGroupDetails_ReturnsThePanelPayload(t *testing.T) {
	provider := &fakeDMProvider{groupDetails: service.GroupDetails{
		Conversation: domain.DMConversation{
			ID: dmConversationID, Type: domain.DMConversationTypeGroup, Title: "Time de Infra",
			CreatedAt: testNow(),
		},
		Participants: []domain.DMParticipantProfile{
			{UserID: dmOtherUserID, DisplayName: "Álvaro"},
		},
		ParticipantCount: 12,
		CanManageMembers: true,
	}}

	rec := serveGroupDetails(groupDetailsHandler(provider), groupDetailsRequest())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	data := detailsData(t, rec)
	if got, _ := data["participant_count"].(float64); got != 12 {
		t.Fatalf("participant_count = %v, want 12", data["participant_count"])
	}
	if got, _ := data["can_manage_members"].(bool); !got {
		t.Fatalf("can_manage_members = %v, want true", data["can_manage_members"])
	}
	if got, _ := data["name"].(string); got != "Time de Infra" {
		t.Fatalf("name = %q", got)
	}
	// A group is not a channel: no visibility, slug, category or description.
	for _, absent := range []string{"type_visibility", "slug", "category_id", "description"} {
		if _, present := data[absent]; present {
			t.Fatalf("payload carries %q, which the group domain does not have", absent)
		}
	}
}

// The caller and the workspace are server-derived; the path only names the
// conversation.
func TestGroupDetails_DerivesActorAndWorkspaceServerSide(t *testing.T) {
	provider := &fakeDMProvider{}

	serveGroupDetails(groupDetailsHandler(provider), groupDetailsRequest())

	if provider.lastGroupDetails.CallerID != msgTestUserID {
		t.Fatalf("CallerID = %q, want %q", provider.lastGroupDetails.CallerID, msgTestUserID)
	}
	if provider.lastGroupDetails.WorkspaceID != testWorkspaceID {
		t.Fatalf("WorkspaceID = %q, want %q", provider.lastGroupDetails.WorkspaceID, testWorkspaceID)
	}
	if provider.lastGroupDetails.ConversationID != dmConversationID {
		t.Fatalf("ConversationID = %q, want the path value", provider.lastGroupDetails.ConversationID)
	}
}

// A 1:1, a conversation the caller does not participate in, one in another
// workspace and one that never existed are one predicate in the service and must
// stay one answer here.
func TestGroupDetails_CollapsesEveryDenialInto404(t *testing.T) {
	for name, err := range map[string]error{
		"not found or 1:1": domain.ErrNotFound,
		"forbidden":        domain.ErrForbidden,
	} {
		t.Run(name, func(t *testing.T) {
			provider := &fakeDMProvider{groupDetailsErr: err}

			rec := serveGroupDetails(groupDetailsHandler(provider), groupDetailsRequest())

			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", rec.Code)
			}
			if strings.Contains(rec.Body.String(), dmConversationID) {
				t.Fatalf("error body echoed the conversation ID: %s", rec.Body.String())
			}
		})
	}
}

func TestGroupDetails_RequiresAuthenticatedUser(t *testing.T) {
	provider := &fakeDMProvider{}
	r := httptest.NewRequest(http.MethodGet, "/api/chat/dm/"+dmConversationID+"/details", nil)
	r.SetPathValue("conversationID", dmConversationID)

	rec := serveGroupDetails(groupDetailsHandler(provider), r)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if provider.groupDetailsCalls != 0 {
		t.Fatal("an unauthenticated request must not reach the service")
	}
}

func TestGroupDetails_RejectsNonUUIDConversationID(t *testing.T) {
	provider := &fakeDMProvider{}
	r := requestWithUser(http.MethodGet, "/api/chat/dm/nope/details", nil)
	r.SetPathValue("conversationID", "nope")

	rec := serveGroupDetails(groupDetailsHandler(provider), r)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if provider.groupDetailsCalls != 0 {
		t.Fatal("a malformed ID must not reach the service")
	}
}

// Without a presence source the field is omitted rather than asserted as
// "offline" for everyone — "not tracked" and "offline" are different claims.
func TestGroupDetails_OmitsPresenceWhenNotTracked(t *testing.T) {
	provider := &fakeDMProvider{groupDetails: service.GroupDetails{
		Conversation: domain.DMConversation{ID: dmConversationID, Type: domain.DMConversationTypeGroup},
		Participants: []domain.DMParticipantProfile{{UserID: dmOtherUserID, DisplayName: "Álvaro"}},
	}}

	rec := serveGroupDetails(groupDetailsHandler(provider), groupDetailsRequest())

	data := detailsData(t, rec)
	participants, _ := data["participants"].([]any)
	if len(participants) != 1 {
		t.Fatalf("participants = %v", data["participants"])
	}
	first, _ := participants[0].(map[string]any)
	if _, present := first["presence"]; present {
		t.Fatalf("presence was serialized without a presence source: %v", first)
	}
}

// Presence is annotated onto the page the query already selected, in one batch
// read — never one lookup per participant, and never used to drop anyone.
func TestGroupDetails_AnnotatesPresenceWithoutFilteringParticipants(t *testing.T) {
	provider := &fakeDMProvider{groupDetails: service.GroupDetails{
		Conversation: domain.DMConversation{ID: dmConversationID, Type: domain.DMConversationTypeGroup},
		Participants: []domain.DMParticipantProfile{
			{UserID: dmOtherUserID, DisplayName: "Online"},
			{UserID: msgTestUserID, DisplayName: "Offline"},
		},
		ParticipantCount: 2,
	}}
	presence := &fakePresence{online: []string{dmOtherUserID}}
	handler := groupDetailsHandler(provider).WithPresence(presence)

	rec := serveGroupDetails(handler, groupDetailsRequest())

	data := detailsData(t, rec)
	participants, _ := data["participants"].([]any)
	// Both are still listed: an offline participant is never removed from a
	// group, unlike the channel panel's online-only preview.
	if len(participants) != 2 {
		t.Fatalf("participants = %v, want both", data["participants"])
	}
	first, _ := participants[0].(map[string]any)
	second, _ := participants[1].(map[string]any)
	if first["presence"] != "online" {
		t.Fatalf("first presence = %v, want online", first["presence"])
	}
	if second["presence"] != "offline" {
		t.Fatalf("second presence = %v, want offline", second["presence"])
	}
	// One batch read for the whole page.
	if len(presence.asked) != 1 {
		t.Fatalf("presence consulted %d times, want 1 batch", len(presence.asked))
	}
	if presence.asked[0] != testWorkspaceID {
		t.Fatalf("presence asked for workspace %q", presence.asked[0])
	}
}

// ── Contextual candidate search (issue #398) ────────────────────────────────

func participantCandidatesRequest(query string) *http.Request {
	target := "/api/chat/dm/" + dmConversationID + "/member-candidates?query=" + query
	r := requestWithUser(http.MethodGet, target, nil)
	r.SetPathValue("conversationID", dmConversationID)
	return r
}

func serveParticipantCandidates(handler *httpapi.DMHandler, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	handler.ParticipantCandidates(rec, r)
	return rec
}

func TestParticipantCandidates_ReturnsOnlyIdentityAndName(t *testing.T) {
	provider := &fakeDMProvider{participantCandidates: []domain.DMCandidate{
		{UserID: dmOtherUserID, DisplayName: "Bruno"},
	}}

	rec := serveParticipantCandidates(
		addParticipantsHandler(provider, &recordingBroadcaster{}, nil),
		participantCandidatesRequest("br"),
	)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	data := detailsData(t, rec)
	list, _ := data["candidates"].([]any)
	if len(list) != 1 {
		t.Fatalf("candidates = %v", data["candidates"])
	}
	first, _ := list[0].(map[string]any)
	if len(first) != 2 {
		t.Fatalf("candidate carries %d fields, want exactly two: %v", len(first), first)
	}
}

func TestParticipantCandidates_DerivesActorAndWorkspaceServerSide(t *testing.T) {
	provider := &fakeDMProvider{}

	serveParticipantCandidates(
		addParticipantsHandler(provider, &recordingBroadcaster{}, nil),
		participantCandidatesRequest("br"),
	)

	if provider.lastParticipantCandidates.CallerID != msgTestUserID {
		t.Fatalf("CallerID = %q, want the session user", provider.lastParticipantCandidates.CallerID)
	}
	if provider.lastParticipantCandidates.WorkspaceID != testWorkspaceID {
		t.Fatalf("WorkspaceID = %q, want the resolved workspace", provider.lastParticipantCandidates.WorkspaceID)
	}
	if provider.lastParticipantCandidates.ConversationID != dmConversationID {
		t.Fatalf("ConversationID = %q, want the path value", provider.lastParticipantCandidates.ConversationID)
	}
}

func TestParticipantCandidates_RequiresAuthenticatedUser(t *testing.T) {
	provider := &fakeDMProvider{}
	r := httptest.NewRequest(http.MethodGet,
		"/api/chat/dm/"+dmConversationID+"/member-candidates?query=br", nil)
	r.SetPathValue("conversationID", dmConversationID)

	rec := serveParticipantCandidates(addParticipantsHandler(provider, &recordingBroadcaster{}, nil), r)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if provider.participantCandidateCalls != 0 {
		t.Fatal("an unauthenticated request must not reach the service")
	}
}

func TestParticipantCandidates_RejectsNonUUIDConversationID(t *testing.T) {
	provider := &fakeDMProvider{}
	r := requestWithUser(http.MethodGet, "/api/chat/dm/nope/member-candidates?query=br", nil)
	r.SetPathValue("conversationID", "nope")

	rec := serveParticipantCandidates(addParticipantsHandler(provider, &recordingBroadcaster{}, nil), r)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if provider.participantCandidateCalls != 0 {
		t.Fatal("a malformed ID must not reach the service")
	}
}

// A 1:1 and a group the caller cannot see are the same 404 in the service, and
// must stay one answer here.
func TestParticipantCandidates_MapsDomainErrors(t *testing.T) {
	tests := map[string]struct {
		err  error
		want int
	}{
		"1:1 or invisible group": {domain.ErrNotFound, http.StatusNotFound},
		"invalid query":          {domain.ErrInvalidInput, http.StatusBadRequest},
		"forbidden":              {domain.ErrForbidden, http.StatusForbidden},
		"storage failure":        {errors.New("boom"), http.StatusInternalServerError},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			provider := &fakeDMProvider{participantCandidatesErr: test.err}

			rec := serveParticipantCandidates(
				addParticipantsHandler(provider, &recordingBroadcaster{}, nil),
				participantCandidatesRequest("br"),
			)

			if rec.Code != test.want {
				t.Fatalf("status = %d, want %d", rec.Code, test.want)
			}
		})
	}
}

func TestParticipantCandidates_RejectsInvalidLimit(t *testing.T) {
	provider := &fakeDMProvider{}
	r := requestWithUser(http.MethodGet,
		"/api/chat/dm/"+dmConversationID+"/member-candidates?query=br&limit=0", nil)
	r.SetPathValue("conversationID", dmConversationID)

	rec := serveParticipantCandidates(addParticipantsHandler(provider, &recordingBroadcaster{}, nil), r)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// Both add-participants routes and the workspace-wide search share one search
// budget, so alternating routes does not buy extra enumeration.
func TestParticipantCandidates_SharesTheSearchBudget(t *testing.T) {
	limiter := &fakeDMRateLimiter{}
	handler := addParticipantsHandler(&fakeDMProvider{}, &recordingBroadcaster{}, limiter)

	var last *httptest.ResponseRecorder
	for i := 0; i < 32; i++ {
		last = serveParticipantCandidates(handler, participantCandidatesRequest("br"))
	}

	if last.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 once the search budget is spent", last.Code)
	}
}

// The user-scoped signal reaches only the participants the transaction added.
func TestAddParticipants_SignalsOnlyTheActuallyAddedUsers(t *testing.T) {
	provider := &fakeDMProvider{addMembersResult: storage.AddMembersResult{
		Added: 1, AlreadyMembers: 1, TotalCount: 6,
		AddedUserIDs: []string{dmOtherUserID},
	}}
	broadcast := &recordingBroadcaster{}

	serveAddParticipants(
		addParticipantsHandler(provider, broadcast, nil), addParticipantsRequest(validAddMembersBody),
	)

	if len(broadcast.available) != 1 {
		t.Fatalf("user-scoped publishes = %d, want 1", len(broadcast.available))
	}
	got := broadcast.available[0]
	if len(got.UserIDs) != 1 || got.UserIDs[0] != dmOtherUserID {
		t.Fatalf("recipients = %v, want only the inserted participant", got.UserIDs)
	}
	if got.TargetType != "dm" {
		t.Fatalf("TargetType = %q, want \"dm\"", got.TargetType)
	}
}

func TestAddParticipants_DoesNotSignalOnPureRetryOrFailure(t *testing.T) {
	t.Run("pure retry", func(t *testing.T) {
		provider := &fakeDMProvider{addMembersResult: storage.AddMembersResult{
			Added: 0, AlreadyMembers: 1, TotalCount: 5,
		}}
		broadcast := &recordingBroadcaster{}

		serveAddParticipants(
			addParticipantsHandler(provider, broadcast, nil),
			addParticipantsRequest(validAddMembersBody),
		)

		if len(broadcast.available) != 0 {
			t.Fatalf("a no-op retry signalled %d user(s)", len(broadcast.available))
		}
	})

	t.Run("failure", func(t *testing.T) {
		broadcast := &recordingBroadcaster{}

		serveAddParticipants(
			addParticipantsHandler(&fakeDMProvider{addMembersErr: domain.ErrForbidden}, broadcast, nil),
			addParticipantsRequest(validAddMembersBody),
		)

		if len(broadcast.available) != 0 {
			t.Fatalf("a failed write signalled %d user(s)", len(broadcast.available))
		}
	})
}
