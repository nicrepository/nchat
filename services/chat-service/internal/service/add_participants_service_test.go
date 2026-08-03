package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

const apConversationID = "66666666-6666-4666-8666-666666666666"

// groupParticipantsFixture builds a DMService over a conversation the caller
// participates in. GetVisibleConversationByID is the authorization for groups,
// so the fake returning a conversation *is* the fake saying "this caller
// participates" — which is exactly the coupling the real SQL predicate has.
func groupParticipantsFixture(t *testing.T, conversationType domain.DMConversationType) (*service.DMService, *fakeDMStore) {
	t.Helper()
	dms := &fakeDMStore{
		visibleConversation: domain.DMConversation{
			ID: apConversationID, WorkspaceID: amWorkspaceID,
			Type: conversationType, Status: domain.DMConversationStatusActive,
		},
		addParticipantsResult: storage.AddMembersResult{Added: 1, TotalCount: 4},
	}
	members := &fakeMemberStore{
		workspaceMembers:   map[string]domain.WorkspaceMember{},
		channelMembers:     map[string]domain.ChannelMember{},
		ineligibleAccounts: map[string]struct{}{},
	}
	return service.NewDMService(dms, members), dms
}

func participantsInput(userIDs ...string) service.AddGroupParticipantsInput {
	return service.AddGroupParticipantsInput{
		WorkspaceID: amWorkspaceID, CallerID: amManagerID,
		ConversationID: apConversationID, UserIDs: userIDs,
	}
}

// Any active participant may add — there is no manager in a group, because
// chat.dm_members.role is closed by CHECK to 'member'. The caller here is a
// plain workspace member, which is the point.
func TestAddGroupParticipantsAllowsAnyActiveParticipant(t *testing.T) {
	svc, dms := groupParticipantsFixture(t, domain.DMConversationTypeGroup)

	result, err := svc.AddGroupParticipants(context.Background(), participantsInput(amTargetA))
	if err != nil {
		t.Fatalf("AddGroupParticipants: %v", err)
	}
	if result.Added != 1 || result.TotalCount != 4 {
		t.Fatalf("result = %+v, want Added 1 / Total 4", result)
	}
	if dms.addParticipantsCalls != 1 {
		t.Fatalf("store calls = %d, want 1", dms.addParticipantsCalls)
	}
}

// A 1:1 DM is refused even though the caller participates in it: adding a third
// person would silently convert the conversation into a group, and the direct
// pair key would then describe a conversation that is no longer a pair.
func TestAddGroupParticipantsRejectsDirectConversation(t *testing.T) {
	svc, dms := groupParticipantsFixture(t, domain.DMConversationTypeDirect)

	_, err := svc.AddGroupParticipants(context.Background(), participantsInput(amTargetA))
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if dms.addParticipantsCalls != 0 {
		t.Fatal("a 1:1 conversation must never reach the store")
	}
}

// Non-participation, an archived conversation, another workspace's ID and an
// unknown ID are one predicate in SQL and must stay one answer here, so the
// route cannot be used to probe which conversation UUIDs exist.
func TestAddGroupParticipantsRejectsInvisibleConversation(t *testing.T) {
	svc, dms := groupParticipantsFixture(t, domain.DMConversationTypeGroup)
	dms.getVisibleErr = domain.ErrNotFound

	_, err := svc.AddGroupParticipants(context.Background(), participantsInput(amTargetA))
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if dms.addParticipantsCalls != 0 {
		t.Fatal("an invisible conversation must not reach the store")
	}
}

// Access is settled before the payload is validated, so an unauthorized caller
// cannot learn from a 400-vs-404 whether the conversation exists.
func TestAddGroupParticipantsChecksAccessBeforeValidatingThePayload(t *testing.T) {
	svc, dms := groupParticipantsFixture(t, domain.DMConversationTypeGroup)
	dms.getVisibleErr = domain.ErrNotFound

	_, err := svc.AddGroupParticipants(context.Background(), participantsInput("not-a-uuid"))
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound (not a validation error)", err)
	}
}

func TestAddGroupParticipantsRejectsMalformedRequests(t *testing.T) {
	tests := map[string]struct {
		userIDs []string
		want    error
	}{
		"empty list":     {userIDs: []string{}, want: domain.ErrNoMembersRequested},
		"not a uuid":     {userIDs: []string{"nope"}, want: domain.ErrInvalidInput},
		"over batch cap": {userIDs: make([]string, domain.MaxAddMembersPerRequest+1), want: domain.ErrTooManyMembersRequested},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			svc, dms := groupParticipantsFixture(t, domain.DMConversationTypeGroup)

			_, err := svc.AddGroupParticipants(context.Background(), participantsInput(test.userIDs...))
			if !errors.Is(err, test.want) {
				t.Fatalf("err = %v, want %v", err, test.want)
			}
			if dms.addParticipantsCalls != 0 {
				t.Fatal("a malformed request must not reach the store")
			}
		})
	}
}

func TestAddGroupParticipantsPropagatesIneligibleParticipant(t *testing.T) {
	svc, dms := groupParticipantsFixture(t, domain.DMConversationTypeGroup)
	dms.addParticipantsErr = domain.ErrForbidden

	_, err := svc.AddGroupParticipants(context.Background(), participantsInput(amTargetA, amTargetB))
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
}

// The conversation ID and the participant cap handed to the store come from the
// row the database returned and from the service's own constant — never from the
// request, which is what keeps a caller from aiming the write elsewhere or
// widening the ceiling.
func TestAddGroupParticipantsPassesServerDerivedScopeToTheStore(t *testing.T) {
	svc, dms := groupParticipantsFixture(t, domain.DMConversationTypeGroup)

	if _, err := svc.AddGroupParticipants(context.Background(), participantsInput(amTargetA)); err != nil {
		t.Fatalf("AddGroupParticipants: %v", err)
	}
	call := dms.lastAddParticipants
	if call.ConversationID != apConversationID || call.WorkspaceID != amWorkspaceID {
		t.Fatalf("store scoped to %s/%s", call.WorkspaceID, call.ConversationID)
	}
}

func TestAddGroupParticipantsDeduplicatesBeforeTheStore(t *testing.T) {
	svc, dms := groupParticipantsFixture(t, domain.DMConversationTypeGroup)

	if _, err := svc.AddGroupParticipants(context.Background(),
		participantsInput(amTargetA, amTargetA, amTargetB)); err != nil {
		t.Fatalf("AddGroupParticipants: %v", err)
	}
	if got := len(dms.lastAddParticipants.UserIDs); got != 2 {
		t.Fatalf("store received %d IDs, want 2 (%v)", got, dms.lastAddParticipants.UserIDs)
	}
}

func TestAddGroupParticipantsPropagatesStorageFailure(t *testing.T) {
	svc, dms := groupParticipantsFixture(t, domain.DMConversationTypeGroup)
	dms.addParticipantsErr = errors.New("connection reset")

	_, err := svc.AddGroupParticipants(context.Background(), participantsInput(amTargetA))
	if err == nil {
		t.Fatal("expected the storage failure to surface")
	}
	if errors.Is(err, domain.ErrForbidden) || errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("storage failure masqueraded as a denial: %v", err)
	}
}

func TestAddGroupParticipantsPassesTheActorToTheStore(t *testing.T) {
	svc, dms := groupParticipantsFixture(t, domain.DMConversationTypeGroup)

	if _, err := svc.AddGroupParticipants(context.Background(), participantsInput(amTargetA)); err != nil {
		t.Fatalf("AddGroupParticipants: %v", err)
	}
	if got := dms.lastAddParticipants.CallerID; got != amManagerID {
		t.Fatalf("store received actor %q, want %q", got, amManagerID)
	}
}

// A participation revoked between the visibility read and the write surfaces
// from the store as ErrForbidden, and must not be reshaped into a 404 or a
// capacity conflict on the way out.
func TestAddGroupParticipantsHonoursAStoreLevelAuthorizationRefusal(t *testing.T) {
	svc, dms := groupParticipantsFixture(t, domain.DMConversationTypeGroup)
	dms.addParticipantsErr = domain.ErrForbidden

	_, err := svc.AddGroupParticipants(context.Background(), participantsInput(amTargetA))

	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
}

// ── GetGroupDetails (issues #441, #398) ─────────────────────────────────────

func TestGetGroupDetailsReturnsTheParticipantPage(t *testing.T) {
	svc, dms := groupParticipantsFixture(t, domain.DMConversationTypeGroup)
	dms.participantPage = storage.DMParticipantPage{
		Participants: []domain.DMParticipantProfile{{UserID: amTargetA, DisplayName: "Ana"}},
		TotalCount:   12,
	}

	details, err := svc.GetGroupDetails(context.Background(), service.GroupDetailsInput{
		WorkspaceID: amWorkspaceID, CallerID: amManagerID, ConversationID: apConversationID,
	})
	if err != nil {
		t.Fatalf("GetGroupDetails: %v", err)
	}
	// The total is the store's, never len(Participants): that slice is a capped
	// preview and the group does not shrink because the page did.
	if details.ParticipantCount != 12 {
		t.Fatalf("ParticipantCount = %d, want 12", details.ParticipantCount)
	}
	if len(details.Participants) != 1 {
		t.Fatalf("Participants = %d, want 1", len(details.Participants))
	}
	// Participation is the policy, and reaching this payload proves it holds.
	if !details.CanManageMembers {
		t.Fatal("CanManageMembers = false for a participant who can already read the group")
	}
}

// A 1:1 is refused with the same ErrNotFound as a conversation the caller
// cannot see, so the endpoint cannot tell them apart.
func TestGetGroupDetailsRefusesDirectConversation(t *testing.T) {
	svc, dms := groupParticipantsFixture(t, domain.DMConversationTypeDirect)

	_, err := svc.GetGroupDetails(context.Background(), service.GroupDetailsInput{
		WorkspaceID: amWorkspaceID, CallerID: amManagerID, ConversationID: apConversationID,
	})

	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if dms.participantPageCalls != 0 {
		t.Fatal("a 1:1 must never reach the participant query")
	}
}

// Access is settled before any participant row is read, so an unauthorised
// caller never reaches a roster.
func TestGetGroupDetailsChecksAccessBeforeReadingParticipants(t *testing.T) {
	svc, dms := groupParticipantsFixture(t, domain.DMConversationTypeGroup)
	dms.getVisibleErr = domain.ErrNotFound

	_, err := svc.GetGroupDetails(context.Background(), service.GroupDetailsInput{
		WorkspaceID: amWorkspaceID, CallerID: amManagerID, ConversationID: apConversationID,
	})

	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if dms.participantPageCalls != 0 {
		t.Fatal("participants were read for a caller who was refused access")
	}
}

// ── Contextual candidate search (issue #398) ────────────────────────────────

func groupCandidateInput(query string) service.SearchGroupParticipantCandidatesInput {
	return service.SearchGroupParticipantCandidatesInput{
		WorkspaceID: amWorkspaceID, CallerID: amManagerID,
		ConversationID: apConversationID, Query: query,
	}
}

func TestSearchGroupParticipantCandidatesPassesServerDerivedScope(t *testing.T) {
	svc, dms := groupParticipantsFixture(t, domain.DMConversationTypeGroup)
	dms.groupCandidates = []domain.DMCandidate{{UserID: amTargetA, DisplayName: "Ana"}}

	got, err := svc.SearchGroupParticipantCandidates(context.Background(), groupCandidateInput("an"))
	if err != nil {
		t.Fatalf("SearchGroupParticipantCandidates: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("candidates = %d, want 1", len(got))
	}
	call := dms.candidateCalls[0]
	// The conversation ID comes from the row the database returned, and the
	// workspace from the session — never from the request as authority.
	if call.ConversationID != apConversationID || call.WorkspaceID != amWorkspaceID {
		t.Fatalf("scope = %s/%s", call.WorkspaceID, call.ConversationID)
	}
	if call.CallerID != amManagerID {
		t.Fatalf("CallerID = %q, want %q", call.CallerID, amManagerID)
	}
	if call.Limit != 20 {
		t.Fatalf("Limit = %d, want the server default 20", call.Limit)
	}
}

// Participation authorises the search, exactly as it authorises the write. A
// conversation the caller cannot see and one that does not exist are one answer.
func TestSearchGroupParticipantCandidatesRequiresParticipation(t *testing.T) {
	svc, dms := groupParticipantsFixture(t, domain.DMConversationTypeGroup)
	dms.getVisibleErr = domain.ErrNotFound

	_, err := svc.SearchGroupParticipantCandidates(context.Background(), groupCandidateInput("an"))

	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if len(dms.candidateCalls) != 0 {
		t.Fatal("a caller without access must not reach the store")
	}
}

// A 1:1 has no add-participants flow, so it has no candidate list either.
func TestSearchGroupParticipantCandidatesRejectsDirectConversation(t *testing.T) {
	svc, dms := groupParticipantsFixture(t, domain.DMConversationTypeDirect)

	_, err := svc.SearchGroupParticipantCandidates(context.Background(), groupCandidateInput("an"))

	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if len(dms.candidateCalls) != 0 {
		t.Fatal("a 1:1 must not reach the store")
	}
}

// Access is settled before the payload is judged, so an unauthorised caller
// cannot tell a 400 from a 404.
func TestSearchGroupParticipantCandidatesChecksAccessBeforeTheQuery(t *testing.T) {
	svc, dms := groupParticipantsFixture(t, domain.DMConversationTypeGroup)
	dms.getVisibleErr = domain.ErrNotFound

	_, err := svc.SearchGroupParticipantCandidates(context.Background(), groupCandidateInput("x"))

	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound (not a validation error)", err)
	}
}

func TestSearchGroupParticipantCandidatesBoundsTheQuery(t *testing.T) {
	svc, dms := groupParticipantsFixture(t, domain.DMConversationTypeGroup)

	if _, err := svc.SearchGroupParticipantCandidates(
		context.Background(), groupCandidateInput("a"),
	); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
	if len(dms.candidateCalls) != 0 {
		t.Fatal("a too-short query must not reach the store")
	}
}

// ── Fan-out recipients ──────────────────────────────────────────────────────

// The store computes who became a participant under its lock; the service must
// hand that through untouched, since it is the fan-out audience.
func TestAddGroupParticipantsPropagatesTheAddedUserIDs(t *testing.T) {
	svc, dms := groupParticipantsFixture(t, domain.DMConversationTypeGroup)
	dms.addParticipantsResult = storage.AddMembersResult{
		Added: 1, TotalCount: 4, AddedUserIDs: []string{amTargetB},
	}

	result, err := svc.AddGroupParticipants(context.Background(), participantsInput(amTargetA, amTargetB))
	if err != nil {
		t.Fatalf("AddGroupParticipants: %v", err)
	}
	// amTargetA was already a participant; only amTargetB is signalled.
	if len(result.AddedUserIDs) != 1 || result.AddedUserIDs[0] != amTargetB {
		t.Fatalf("AddedUserIDs = %v, want [%s]", result.AddedUserIDs, amTargetB)
	}
}
