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
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// ── Fakes ───────────────────────────────────────────────────────────────────

type fakeMemberManager struct {
	candidates         []domain.DMCandidate
	candidatesErr      error
	candidateCalls     int
	lastCandidateInput service.SearchChannelMemberCandidatesInput
	result             storage.AddMembersResult
	err                error
	lastInput          service.AddChannelMembersInput
	calls              int
}

func (f *fakeMemberManager) SearchChannelMemberCandidates(
	_ context.Context, input service.SearchChannelMemberCandidatesInput,
) ([]domain.DMCandidate, error) {
	f.candidateCalls++
	f.lastCandidateInput = input
	return f.candidates, f.candidatesErr
}

func (f *fakeMemberManager) AddChannelMembers(
	_ context.Context, input service.AddChannelMembersInput,
) (storage.AddMembersResult, error) {
	f.calls++
	f.lastInput = input
	return f.result, f.err
}

// recordingBroadcaster captures what, if anything, was published. The assertions
// that matter are the negative ones: nothing after a refused write.
type recordingBroadcaster struct {
	calls []broadcastRecord
	// available records the user-scoped fan-out (issue #398). Kept separate from
	// calls because the two have different audiences and different rules: the
	// room broadcast goes to existing subscribers, this goes only to the people
	// the transaction actually inserted.
	available []availableRecord
	// The issue #527 signals: a renamed conversation and a persisted system
	// message. Recorded separately so a test can assert each fires only after
	// its own transaction committed.
	conversationUpdates [][3]string
	conversationEvents  [][4]string
}

type availableRecord struct {
	WorkspaceID string
	TargetType  string
	TargetID    string
	UserIDs     []string
}

func (b *recordingBroadcaster) PublishConversationAvailable(
	_ context.Context, workspaceID, targetType, targetID string, userIDs []string,
) {
	b.available = append(b.available, availableRecord{
		WorkspaceID: workspaceID, TargetType: targetType, TargetID: targetID,
		UserIDs: append([]string(nil), userIDs...),
	})
}

type broadcastRecord struct {
	WorkspaceID string
	TargetType  string
	TargetID    string
	ActorUserID string
	AddedCount  int
	MemberCount int
}

// The two issue #527 signals. Recorded so a test can assert a rename or a
// departure announced exactly once, and never before its transaction committed.
func (b *recordingBroadcaster) PublishConversationUpdated(_ context.Context, workspaceID, targetType, targetID string) {
	b.conversationUpdates = append(b.conversationUpdates, [3]string{workspaceID, targetType, targetID})
}

func (b *recordingBroadcaster) PublishConversationEvent(_ context.Context, workspaceID, targetType, targetID, messageID string) {
	b.conversationEvents = append(b.conversationEvents, [4]string{workspaceID, targetType, targetID, messageID})
}

func (b *recordingBroadcaster) PublishMembersAdded(
	_ context.Context, workspaceID, targetType, targetID, actorUserID string, addedCount, memberCount int,
) {
	b.calls = append(b.calls, broadcastRecord{
		WorkspaceID: workspaceID, TargetType: targetType, TargetID: targetID,
		ActorUserID: actorUserID, AddedCount: addedCount, MemberCount: memberCount,
	})
}

// ── Harness ─────────────────────────────────────────────────────────────────

func addMembersHandler(
	members *fakeMemberManager, broadcast *recordingBroadcaster, limiter *fakeDMRateLimiter,
) *httpapi.ChannelHandler {
	if limiter == nil {
		limiter = &fakeDMRateLimiter{}
	}
	return httpapi.NewChannelHandler(
		&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeChannelProvider{}, limiter,
	).WithMembers(members, broadcast)
}

func addMembersRequest(body string) *http.Request {
	r := requestWithUser(
		http.MethodPost, "/api/chat/channels/"+testChannelID+"/members", strings.NewReader(body),
	)
	r.SetPathValue("channelID", testChannelID)
	r.Header.Set("Content-Type", "application/json")
	return r
}

const validAddMembersBody = `{"user_ids":["44444444-4444-4444-8444-444444444444"]}`

func serveAddMembers(handler *httpapi.ChannelHandler, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	handler.AddMembers(rec, r)
	return rec
}

// ── Happy path ──────────────────────────────────────────────────────────────

func TestAddMembers_PersistsAndReportsTheServerCounts(t *testing.T) {
	members := &fakeMemberManager{
		result: storage.AddMembersResult{Added: 2, AlreadyMembers: 1, TotalCount: 14},
	}
	broadcast := &recordingBroadcaster{}

	rec := serveAddMembers(addMembersHandler(members, broadcast, nil), addMembersRequest(validAddMembersBody))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	data := detailsData(t, rec)
	for field, want := range map[string]float64{
		"added": 2, "already_members": 1, "member_count": 14,
	} {
		if got, _ := data[field].(float64); got != want {
			t.Fatalf("%s = %v, want %v", field, data[field], want)
		}
	}
}

// The actor and the workspace come from the session and the server-side
// resolver, never from the request. This is the whole point of the contract.
func TestAddMembers_DerivesActorAndWorkspaceServerSide(t *testing.T) {
	members := &fakeMemberManager{}

	serveAddMembers(addMembersHandler(members, &recordingBroadcaster{}, nil), addMembersRequest(validAddMembersBody))

	if members.lastInput.CallerID != msgTestUserID {
		t.Fatalf("CallerID = %q, want the authenticated user %q", members.lastInput.CallerID, msgTestUserID)
	}
	if members.lastInput.WorkspaceID != testWorkspaceID {
		t.Fatalf("WorkspaceID = %q, want the resolved workspace %q", members.lastInput.WorkspaceID, testWorkspaceID)
	}
	if members.lastInput.ChannelID != testChannelID {
		t.Fatalf("ChannelID = %q, want the path value", members.lastInput.ChannelID)
	}
}

// Every field a client might use to claim a privilege must be rejected outright
// by the strict decoder, not silently ignored.
func TestAddMembers_RejectsClientSuppliedAuthorityFields(t *testing.T) {
	bodies := map[string]string{
		"workspace_id": `{"user_ids":["44444444-4444-4444-8444-444444444444"],"workspace_id":"x"}`,
		"actor_id":     `{"user_ids":["44444444-4444-4444-8444-444444444444"],"actor_id":"x"}`,
		"role":         `{"user_ids":["44444444-4444-4444-8444-444444444444"],"role":"moderator"}`,
		"created_by":   `{"user_ids":["44444444-4444-4444-8444-444444444444"],"created_by":"x"}`,
		"status":       `{"user_ids":["44444444-4444-4444-8444-444444444444"],"status":"active"}`,
		"eligible":     `{"user_ids":["44444444-4444-4444-8444-444444444444"],"eligible":true}`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			members := &fakeMemberManager{}

			rec := serveAddMembers(addMembersHandler(members, &recordingBroadcaster{}, nil), addMembersRequest(body))

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 for an unknown field", rec.Code)
			}
			if members.calls != 0 {
				t.Fatal("a body carrying an authority field must never reach the service")
			}
		})
	}
}

// ── Transport validation ────────────────────────────────────────────────────

func TestAddMembers_RequiresAuthenticatedUser(t *testing.T) {
	members := &fakeMemberManager{}
	r := httptest.NewRequest(
		http.MethodPost, "/api/chat/channels/"+testChannelID+"/members",
		strings.NewReader(validAddMembersBody),
	)
	r.SetPathValue("channelID", testChannelID)
	r.Header.Set("Content-Type", "application/json")

	rec := serveAddMembers(addMembersHandler(members, &recordingBroadcaster{}, nil), r)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if members.calls != 0 {
		t.Fatal("an unauthenticated request must not reach the service")
	}
}

func TestAddMembers_RejectsNonUUIDChannelID(t *testing.T) {
	members := &fakeMemberManager{}
	r := requestWithUser(http.MethodPost, "/api/chat/channels/nope/members", strings.NewReader(validAddMembersBody))
	r.SetPathValue("channelID", "nope")
	r.Header.Set("Content-Type", "application/json")

	rec := serveAddMembers(addMembersHandler(members, &recordingBroadcaster{}, nil), r)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if members.calls != 0 {
		t.Fatal("a malformed channel ID must not reach the service")
	}
}

func TestAddMembers_RejectsWrongContentType(t *testing.T) {
	members := &fakeMemberManager{}
	r := addMembersRequest(validAddMembersBody)
	r.Header.Set("Content-Type", "text/plain")

	rec := serveAddMembers(addMembersHandler(members, &recordingBroadcaster{}, nil), r)

	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", rec.Code)
	}
	if members.calls != 0 {
		t.Fatal("a non-JSON request must not reach the service")
	}
}

func TestAddMembers_RejectsMalformedJSON(t *testing.T) {
	members := &fakeMemberManager{}

	rec := serveAddMembers(addMembersHandler(members, &recordingBroadcaster{}, nil), addMembersRequest(`{"user_ids":`))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if members.calls != 0 {
		t.Fatal("malformed JSON must not reach the service")
	}
}

// The body is capped by MaxBytesReader before it is buffered, so an oversized
// payload is refused by the reader rather than held in memory.
func TestAddMembers_RejectsOversizedBody(t *testing.T) {
	members := &fakeMemberManager{}
	// Well past the 64 KiB shared cap.
	huge := `{"user_ids":["` + strings.Repeat("a", 100_000) + `"]}`

	rec := serveAddMembers(addMembersHandler(members, &recordingBroadcaster{}, nil), addMembersRequest(huge))

	if rec.Code != http.StatusBadRequest && rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 400 or 413", rec.Code)
	}
	if members.calls != 0 {
		t.Fatal("an oversized body must not reach the service")
	}
}

// ── Domain error mapping ────────────────────────────────────────────────────

func TestAddMembers_MapsDomainErrors(t *testing.T) {
	tests := map[string]struct {
		err  error
		want int
	}{
		"invalid input":       {domain.ErrInvalidInput, http.StatusBadRequest},
		"empty list":          {domain.ErrNoMembersRequested, http.StatusBadRequest},
		"over batch cap":      {domain.ErrTooManyMembersRequested, http.StatusBadRequest},
		"unauthorized caller": {domain.ErrForbidden, http.StatusForbidden},
		"unreachable channel": {domain.ErrNotFound, http.StatusNotFound},
		"unexpected storage":  {errors.New("boom"), http.StatusInternalServerError},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			members := &fakeMemberManager{err: test.err}

			rec := serveAddMembers(
				addMembersHandler(members, &recordingBroadcaster{}, nil), addMembersRequest(validAddMembersBody),
			)

			if rec.Code != test.want {
				t.Fatalf("status = %d, want %d", rec.Code, test.want)
			}
		})
	}
}

// A denial must not name a user, a channel, a constraint or the rejected value —
// otherwise the endpoint becomes a way to probe account and channel existence.
func TestAddMembers_ErrorBodiesRevealNothing(t *testing.T) {
	for _, err := range []error{domain.ErrForbidden, domain.ErrNotFound, errors.New("pq: duplicate key value violates unique constraint \"channel_members_pkey\"")} {
		members := &fakeMemberManager{err: err}

		rec := serveAddMembers(
			addMembersHandler(members, &recordingBroadcaster{}, nil), addMembersRequest(validAddMembersBody),
		)

		body := rec.Body.String()
		for _, leak := range []string{
			"44444444-4444-4444-8444-444444444444", testChannelID, testWorkspaceID,
			"channel_members", "constraint", "pq:", "suspended", "deleted",
		} {
			if strings.Contains(body, leak) {
				t.Fatalf("error body leaked %q: %s", leak, body)
			}
		}
	}
}

// ── Rate limiting ───────────────────────────────────────────────────────────

func TestAddMembers_EnforcesTheRateLimit(t *testing.T) {
	limiter := &fakeDMRateLimiter{}
	handler := addMembersHandler(&fakeMemberManager{}, &recordingBroadcaster{}, limiter)

	var last *httptest.ResponseRecorder
	for i := 0; i < 12; i++ {
		last = serveAddMembers(handler, addMembersRequest(validAddMembersBody))
	}

	if last.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 once the budget is spent", last.Code)
	}
	if last.Header().Get("Retry-After") == "" {
		t.Fatal("a 429 must carry Retry-After")
	}
}

// The limiter failing is an infrastructure fault, not permission to proceed.
func TestAddMembers_FailsClosedWhenTheLimiterErrors(t *testing.T) {
	members := &fakeMemberManager{}
	limiter := &fakeDMRateLimiter{err: errors.New("valkey down")}

	rec := serveAddMembers(
		addMembersHandler(members, &recordingBroadcaster{}, limiter), addMembersRequest(validAddMembersBody),
	)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if members.calls != 0 {
		t.Fatal("the write must not run when the limiter cannot be consulted")
	}
}

// ── Broadcast ordering ──────────────────────────────────────────────────────

func TestAddMembers_BroadcastsOnlyAfterASuccessfulCommit(t *testing.T) {
	members := &fakeMemberManager{
		// Added and AddedUserIDs both come from the transaction's RETURNING and
		// cannot disagree; a fixture that made them disagree would be testing a
		// state the store cannot produce.
		result: storage.AddMembersResult{
			Added: 3, TotalCount: 9,
			AddedUserIDs: []string{
				"99999999-9999-4999-8999-999999999991",
				"99999999-9999-4999-8999-999999999992",
				"99999999-9999-4999-8999-999999999993",
			},
		},
	}
	broadcast := &recordingBroadcaster{}

	serveAddMembers(addMembersHandler(members, broadcast, nil), addMembersRequest(validAddMembersBody))

	if len(broadcast.calls) != 1 {
		t.Fatalf("broadcasts = %d, want 1", len(broadcast.calls))
	}
	got := broadcast.calls[0]
	want := broadcastRecord{
		WorkspaceID: testWorkspaceID, TargetType: "channel", TargetID: testChannelID,
		ActorUserID: msgTestUserID, AddedCount: 3, MemberCount: 9,
	}
	if got != want {
		t.Fatalf("broadcast = %+v, want %+v", got, want)
	}
}

// The negative case is the one that matters: a rolled-back write must not tell
// every subscriber their view is stale, or they refetch their way back to the
// state they already had — or worse, believe a member exists.
func TestAddMembers_DoesNotBroadcastOnFailure(t *testing.T) {
	for name, err := range map[string]error{
		"forbidden": domain.ErrForbidden,
		"not found": domain.ErrNotFound,
		"internal":  errors.New("rollback"),
	} {
		t.Run(name, func(t *testing.T) {
			broadcast := &recordingBroadcaster{}

			serveAddMembers(
				addMembersHandler(&fakeMemberManager{err: err}, broadcast, nil),
				addMembersRequest(validAddMembersBody),
			)

			if len(broadcast.calls) != 0 {
				t.Fatalf("broadcast %d event(s) for a failed write", len(broadcast.calls))
			}
		})
	}
}

// A retry that adds nobody is a success, but there is nothing to tell anyone
// about — broadcasting would make every subscriber refetch for no change.
func TestAddMembers_DoesNotBroadcastWhenNobodyWasAdded(t *testing.T) {
	members := &fakeMemberManager{
		result: storage.AddMembersResult{Added: 0, AlreadyMembers: 2, TotalCount: 9},
	}
	broadcast := &recordingBroadcaster{}

	rec := serveAddMembers(addMembersHandler(members, broadcast, nil), addMembersRequest(validAddMembersBody))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — a no-op retry is not an error", rec.Code)
	}
	if len(broadcast.calls) != 0 {
		t.Fatalf("broadcast %d event(s) for a no-op", len(broadcast.calls))
	}
}

// ── Wiring ──────────────────────────────────────────────────────────────────

// Without the member service the route is not registered at all, so this only
// guards the direct call: a partially wired handler must refuse rather than
// panic.
func TestAddMembers_UnavailableWithoutMemberService(t *testing.T) {
	handler := httpapi.NewChannelHandler(
		&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeChannelProvider{}, &fakeDMRateLimiter{},
	)
	if handler.HasMembers() {
		t.Fatal("HasMembers must be false before WithMembers is called")
	}

	rec := serveAddMembers(handler, addMembersRequest(validAddMembersBody))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

// A missing broadcaster must degrade to "no realtime signal", never to a failed
// write: the membership is already committed by the time it would be published.
func TestAddMembers_SucceedsWithoutABroadcaster(t *testing.T) {
	members := &fakeMemberManager{result: storage.AddMembersResult{Added: 1, TotalCount: 2}}
	handler := httpapi.NewChannelHandler(
		&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeChannelProvider{}, &fakeDMRateLimiter{},
	).WithMembers(members, nil)

	rec := serveAddMembers(handler, addMembersRequest(validAddMembersBody))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

// ── Channel details permission field ────────────────────────────────────────

// The panel decides whether to render the action from this field, so it must
// carry the server's answer and must always be present.
func TestChannelDetails_ReportsCanManageMembers(t *testing.T) {
	for _, canManage := range []bool{true, false} {
		t.Run(fmt.Sprintf("can_manage_members=%v", canManage), func(t *testing.T) {
			provider := &fakeChannelProvider{details: service.ChannelDetails{
				Channel:          detailsChannel(),
				CanManageMembers: canManage,
			}}

			rec := serveDetails(t, channelTestHandler(provider), testChannelID)

			data := detailsData(t, rec)
			got, ok := data["can_manage_members"].(bool)
			if !ok {
				t.Fatalf("can_manage_members missing from the payload: %v", data)
			}
			if got != canManage {
				t.Fatalf("can_manage_members = %v, want %v", got, canManage)
			}
		})
	}
}

// ── Contextual candidate search (issue #398) ────────────────────────────────

func candidatesRequest(query string) *http.Request {
	target := "/api/chat/channels/" + testChannelID + "/member-candidates?query=" + query
	r := requestWithUser(http.MethodGet, target, nil)
	r.SetPathValue("channelID", testChannelID)
	return r
}

func serveCandidates(handler *httpapi.ChannelHandler, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	handler.MemberCandidates(rec, r)
	return rec
}

func TestMemberCandidates_ReturnsOnlyIdentityAndName(t *testing.T) {
	members := &fakeMemberManager{candidates: []domain.DMCandidate{
		{UserID: "44444444-4444-4444-8444-444444444444", DisplayName: "Ana"},
	}}

	rec := serveCandidates(addMembersHandler(members, &recordingBroadcaster{}, nil), candidatesRequest("an"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	data := detailsData(t, rec)
	list, _ := data["candidates"].([]any)
	if len(list) != 1 {
		t.Fatalf("candidates = %v", data["candidates"])
	}
	first, _ := list[0].(map[string]any)
	// A candidate list is not a directory: only the two fields the picker draws.
	if len(first) != 2 {
		t.Fatalf("candidate carries %d fields, want exactly user_id and display_name: %v", len(first), first)
	}
	for _, leaked := range []string{"email", "role", "status", "workspace_id"} {
		if _, present := first[leaked]; present {
			t.Fatalf("candidate leaked %q", leaked)
		}
	}
}

func TestMemberCandidates_DerivesActorAndWorkspaceServerSide(t *testing.T) {
	members := &fakeMemberManager{}

	serveCandidates(addMembersHandler(members, &recordingBroadcaster{}, nil), candidatesRequest("an"))

	if members.lastCandidateInput.CallerID != msgTestUserID {
		t.Fatalf("CallerID = %q, want the session user", members.lastCandidateInput.CallerID)
	}
	if members.lastCandidateInput.WorkspaceID != testWorkspaceID {
		t.Fatalf("WorkspaceID = %q, want the resolved workspace", members.lastCandidateInput.WorkspaceID)
	}
	if members.lastCandidateInput.ChannelID != testChannelID {
		t.Fatalf("ChannelID = %q, want the path value", members.lastCandidateInput.ChannelID)
	}
}

// A client-supplied workspace must be ignored, not honoured.
func TestMemberCandidates_IgnoresClientSuppliedWorkspace(t *testing.T) {
	members := &fakeMemberManager{}
	r := requestWithUser(http.MethodGet,
		"/api/chat/channels/"+testChannelID+"/member-candidates?query=an&workspace_id=evil", nil)
	r.SetPathValue("channelID", testChannelID)

	serveCandidates(addMembersHandler(members, &recordingBroadcaster{}, nil), r)

	if members.lastCandidateInput.WorkspaceID != testWorkspaceID {
		t.Fatalf("WorkspaceID = %q — a query parameter changed the scope", members.lastCandidateInput.WorkspaceID)
	}
}

func TestMemberCandidates_RequiresAuthenticatedUser(t *testing.T) {
	members := &fakeMemberManager{}
	r := httptest.NewRequest(http.MethodGet,
		"/api/chat/channels/"+testChannelID+"/member-candidates?query=an", nil)
	r.SetPathValue("channelID", testChannelID)

	rec := serveCandidates(addMembersHandler(members, &recordingBroadcaster{}, nil), r)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if members.candidateCalls != 0 {
		t.Fatal("an unauthenticated request must not reach the service")
	}
}

func TestMemberCandidates_RejectsNonUUIDChannelID(t *testing.T) {
	members := &fakeMemberManager{}
	r := requestWithUser(http.MethodGet, "/api/chat/channels/nope/member-candidates?query=an", nil)
	r.SetPathValue("channelID", "nope")

	rec := serveCandidates(addMembersHandler(members, &recordingBroadcaster{}, nil), r)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if members.candidateCalls != 0 {
		t.Fatal("a malformed ID must not reach the service")
	}
}

func TestMemberCandidates_MapsDomainErrors(t *testing.T) {
	tests := map[string]struct {
		err  error
		want int
	}{
		"invalid query":       {domain.ErrInvalidInput, http.StatusBadRequest},
		"not a manager":       {domain.ErrForbidden, http.StatusForbidden},
		"unreachable channel": {domain.ErrNotFound, http.StatusNotFound},
		"storage failure":     {errors.New("boom"), http.StatusInternalServerError},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			members := &fakeMemberManager{candidatesErr: test.err}

			rec := serveCandidates(addMembersHandler(members, &recordingBroadcaster{}, nil), candidatesRequest("an"))

			if rec.Code != test.want {
				t.Fatalf("status = %d, want %d", rec.Code, test.want)
			}
		})
	}
}

func TestMemberCandidates_EnforcesTheSearchBudget(t *testing.T) {
	limiter := &fakeDMRateLimiter{}
	handler := addMembersHandler(&fakeMemberManager{}, &recordingBroadcaster{}, limiter)

	var last *httptest.ResponseRecorder
	for i := 0; i < 32; i++ {
		last = serveCandidates(handler, candidatesRequest("an"))
	}

	if last.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 once the search budget is spent", last.Code)
	}
}

// ── User-scoped fan-out (issue #398) ────────────────────────────────────────

// The recipients are the IDs the transaction inserted, not the request payload.
func TestAddMembers_SignalsOnlyTheActuallyAddedUsers(t *testing.T) {
	members := &fakeMemberManager{result: storage.AddMembersResult{
		Added: 1, AlreadyMembers: 1, TotalCount: 9,
		AddedUserIDs: []string{"99999999-9999-4999-8999-999999999999"},
	}}
	broadcast := &recordingBroadcaster{}

	// The request names two people; only one was actually inserted.
	body := `{"user_ids":["44444444-4444-4444-8444-444444444444","99999999-9999-4999-8999-999999999999"]}`
	serveAddMembers(addMembersHandler(members, broadcast, nil), addMembersRequest(body))

	if len(broadcast.available) != 1 {
		t.Fatalf("user-scoped publishes = %d, want 1", len(broadcast.available))
	}
	got := broadcast.available[0]
	if len(got.UserIDs) != 1 || got.UserIDs[0] != "99999999-9999-4999-8999-999999999999" {
		t.Fatalf("recipients = %v, want only the inserted user", got.UserIDs)
	}
	if got.TargetType != "channel" || got.TargetID != testChannelID {
		t.Fatalf("signal aimed at %s/%s", got.TargetType, got.TargetID)
	}
}

// A retry inserts nobody, so nobody is told they gained a conversation.
func TestAddMembers_DoesNotSignalOnPureRetry(t *testing.T) {
	members := &fakeMemberManager{result: storage.AddMembersResult{
		Added: 0, AlreadyMembers: 2, TotalCount: 9, AddedUserIDs: nil,
	}}
	broadcast := &recordingBroadcaster{}

	rec := serveAddMembers(addMembersHandler(members, broadcast, nil), addMembersRequest(validAddMembersBody))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if len(broadcast.available) != 0 {
		t.Fatalf("a no-op retry signalled %d user(s)", len(broadcast.available))
	}
}

func TestAddMembers_DoesNotSignalOnFailure(t *testing.T) {
	for name, err := range map[string]error{
		"forbidden": domain.ErrForbidden,
		"not found": domain.ErrNotFound,
		"internal":  errors.New("rollback"),
	} {
		t.Run(name, func(t *testing.T) {
			broadcast := &recordingBroadcaster{}

			serveAddMembers(
				addMembersHandler(&fakeMemberManager{err: err}, broadcast, nil),
				addMembersRequest(validAddMembersBody),
			)

			if len(broadcast.available) != 0 {
				t.Fatalf("a failed write signalled %d user(s)", len(broadcast.available))
			}
		})
	}
}
