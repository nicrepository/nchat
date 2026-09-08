package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
)

const (
	amWorkspaceID = "11111111-1111-4111-8111-111111111111"
	amChannelID   = "22222222-2222-4222-8222-222222222222"
	amManagerID   = "33333333-3333-4333-8333-333333333333"
	amTargetA     = "44444444-4444-4444-8444-444444444444"
	amTargetB     = "55555555-5555-4555-8555-555555555555"
)

// addMembersFixture wires the three stores the way app.go does, with one active
// workspace, one active non-general channel and a caller whose role the test
// picks. Everything else defaults to "eligible" so each test states only the one
// condition it is about.
func addMembersFixture(t *testing.T, callerRole domain.WorkspaceRole) (*service.MemberService, *fakeMemberStore, *fakeChannelStore) {
	t.Helper()
	ws := &fakeWorkspaceStore{
		workspace: domain.Workspace{ID: amWorkspaceID, Status: domain.WorkspaceStatusActive},
	}
	channels := &fakeChannelStore{
		channel: domain.Channel{
			ID: amChannelID, WorkspaceID: amWorkspaceID, Slug: "infra",
			Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive,
		},
	}
	members := &fakeMemberStore{
		workspaceMembers: map[string]domain.WorkspaceMember{
			wmKey(amWorkspaceID, amManagerID): {
				WorkspaceID: amWorkspaceID, UserID: amManagerID,
				Role: callerRole, Status: domain.MemberStatusActive,
			},
			wmKey(amWorkspaceID, amTargetA): {
				WorkspaceID: amWorkspaceID, UserID: amTargetA,
				Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusActive,
			},
			wmKey(amWorkspaceID, amTargetB): {
				WorkspaceID: amWorkspaceID, UserID: amTargetB,
				Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusActive,
			},
		},
		channelMembers:     map[string]domain.ChannelMember{},
		ineligibleAccounts: map[string]struct{}{},
	}
	return service.NewMemberService(members, channels, ws), members, channels
}

func addInput(userIDs ...string) service.AddChannelMembersInput {
	return service.AddChannelMembersInput{
		WorkspaceID: amWorkspaceID, CallerID: amManagerID,
		ChannelID: amChannelID, UserIDs: userIDs,
	}
}

func TestAddChannelMembersAllowsAdministrativeRolesWithoutPrivateMembership(t *testing.T) {
	// #705 preserves the administrative add scope even when these actors are not
	// members of the private channel.
	for _, role := range []domain.WorkspaceRole{
		domain.WorkspaceRoleOwner, domain.WorkspaceRoleAdmin, domain.WorkspaceRoleModerator,
	} {
		t.Run(string(role), func(t *testing.T) {
			svc, members, channels := addMembersFixture(t, role)

			result, err := svc.AddChannelMembers(context.Background(), addInput(amTargetA, amTargetB))
			if err != nil {
				t.Fatalf("AddChannelMembers: %v", err)
			}
			if result.Added != 2 {
				t.Fatalf("Added = %d, want 2", result.Added)
			}
			if _, ok := members.channelMembers[cmKey(amChannelID, amTargetA)]; !ok {
				t.Fatal("target A was not persisted")
			}
			if channels.getVisibleByIDCalls != 0 {
				t.Fatal("administrative add must not grant or require normal read access")
			}
		})
	}
}

func TestAddChannelMembersAllowsMemberOnVisibleChannels(t *testing.T) {
	for _, channelType := range []domain.ChannelType{domain.ChannelTypePublic, domain.ChannelTypePrivate} {
		t.Run(string(channelType), func(t *testing.T) {
			svc, members, channels := addMembersFixture(t, domain.WorkspaceRoleMember)
			channels.channel.Type = channelType
			if channelType == domain.ChannelTypePrivate {
				// Private-channel creation seeds this exact membership atomically.
				channels.channel.CreatedBy = amManagerID
				members.channelMembers[cmKey(amChannelID, amManagerID)] = domain.ChannelMember{
					ChannelID: amChannelID, UserID: amManagerID, Role: domain.ChannelRoleMember,
				}
			}

			if _, err := svc.AddChannelMembers(context.Background(), addInput(amTargetA)); err != nil {
				t.Fatalf("AddChannelMembers: %v", err)
			}
			if channels.getVisibleByIDCalls != 1 {
				t.Fatalf("visible channel lookups = %d, want 1", channels.getVisibleByIDCalls)
			}
		})
	}
}

func TestAddChannelMembersHidesPrivateChannelFromMemberWithoutAccess(t *testing.T) {
	svc, members, channels := addMembersFixture(t, domain.WorkspaceRoleMember)
	channels.getVisibleErr = domain.ErrNotFound

	_, err := svc.AddChannelMembers(context.Background(), addInput(amTargetA))

	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if len(members.addChannelMembersCalls) != 0 {
		t.Fatal("an invisible private channel must not reach the write store")
	}
}

// A private channel is the case that matters: its whole point is that its
// audience does not grow on its own, so the manager gate is what a public
// channel's openness would otherwise disguise.
func TestAddChannelMembersWorksForPublicAndPrivateChannels(t *testing.T) {
	for _, channelType := range []domain.ChannelType{domain.ChannelTypePublic, domain.ChannelTypePrivate} {
		t.Run(string(channelType), func(t *testing.T) {
			svc, _, channels := addMembersFixture(t, domain.WorkspaceRoleAdmin)
			channels.channel.Type = channelType

			result, err := svc.AddChannelMembers(context.Background(), addInput(amTargetA))
			if err != nil {
				t.Fatalf("AddChannelMembers: %v", err)
			}
			if result.Added != 1 {
				t.Fatalf("Added = %d, want 1", result.Added)
			}
		})
	}
}

// Guest and unknown roles remain outside the add capability.
func TestAddChannelMembersRejectsRolesWithoutAddCapability(t *testing.T) {
	for _, role := range []domain.WorkspaceRole{domain.WorkspaceRoleGuest, domain.WorkspaceRole("wizard")} {
		t.Run(string(role), func(t *testing.T) {
			svc, members, _ := addMembersFixture(t, role)

			_, err := svc.AddChannelMembers(context.Background(), addInput(amTargetA))
			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("err = %v, want ErrForbidden", err)
			}
			if len(members.channelMembers) != 0 {
				t.Fatal("a refused caller must not persist a membership")
			}
		})
	}
}

// On a public channel, the endpoint must consult CanAddChannelMembers and
// nothing stricter above it. Private-member visibility is tested separately.
//
// This proves the wiring by widening the predicate's own inputs: every role the
// predicate accepts must be accepted by the service, and every role it rejects
// must be rejected, with no third opinion in between.
func TestAddChannelMembersFollowsAddCapabilityOnPublicChannel(t *testing.T) {
	for _, role := range []domain.WorkspaceRole{
		domain.WorkspaceRoleOwner, domain.WorkspaceRoleAdmin, domain.WorkspaceRoleModerator,
		domain.WorkspaceRoleMember, domain.WorkspaceRoleGuest, domain.WorkspaceRole("wizard"),
	} {
		t.Run(string(role), func(t *testing.T) {
			svc, _, channels := addMembersFixture(t, role)
			channels.channel.Type = domain.ChannelTypePublic
			allowed := domain.CanAddChannelMembers(&domain.WorkspaceMember{
				Role: role, Status: domain.MemberStatusActive,
			})

			_, err := svc.AddChannelMembers(context.Background(), addInput(amTargetA))

			if allowed && err != nil {
				t.Fatalf("predicate allows %s but the service refused: %v", role, err)
			}
			if !allowed && !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("predicate denies %s but the service returned %v", role, err)
			}
		})
	}
}

func TestAddChannelMembersRejectsSuspendedManager(t *testing.T) {
	svc, members, _ := addMembersFixture(t, domain.WorkspaceRoleAdmin)
	m := members.workspaceMembers[wmKey(amWorkspaceID, amManagerID)]
	m.Status = domain.MemberStatusSuspended
	members.workspaceMembers[wmKey(amWorkspaceID, amManagerID)] = m

	_, err := svc.AddChannelMembers(context.Background(), addInput(amTargetA))
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
}

// Authorization is checked before the channel is read, so a caller with no
// management rights cannot use the error to learn whether a channel ID exists.
func TestAddChannelMembersChecksAuthorizationBeforeReadingTheChannel(t *testing.T) {
	svc, _, channels := addMembersFixture(t, domain.WorkspaceRoleGuest)
	channels.getInWorkspaceErr = errors.New("must not be called")

	_, err := svc.AddChannelMembers(context.Background(), addInput(amTargetA))
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
}

func TestAddChannelMembersRejectsIneligibleTarget(t *testing.T) {
	tests := map[string]func(*fakeMemberStore){
		"not in workspace": func(m *fakeMemberStore) {
			delete(m.workspaceMembers, wmKey(amWorkspaceID, amTargetA))
		},
		"suspended in workspace": func(m *fakeMemberStore) {
			wm := m.workspaceMembers[wmKey(amWorkspaceID, amTargetA)]
			wm.Status = domain.MemberStatusSuspended
			m.workspaceMembers[wmKey(amWorkspaceID, amTargetA)] = wm
		},
		"account disabled or deleted": func(m *fakeMemberStore) {
			m.ineligibleAccounts[amTargetA] = struct{}{}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			svc, members, _ := addMembersFixture(t, domain.WorkspaceRoleAdmin)
			mutate(members)

			_, err := svc.AddChannelMembers(context.Background(), addInput(amTargetA, amTargetB))
			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("err = %v, want ErrForbidden", err)
			}
			// Atomicity: the eligible target in the same batch is not written
			// either. Partial success is not a mode this endpoint has.
			if len(members.channelMembers) != 0 {
				t.Fatalf("partial write: %d membership(s) persisted", len(members.channelMembers))
			}
		})
	}
}

// Re-adding somebody is idempotent, which is what makes a retry after a timeout
// and a double click safe. It is reported, not raised as an error.
func TestAddChannelMembersReportsExistingParticipantsWithoutError(t *testing.T) {
	svc, _, _ := addMembersFixture(t, domain.WorkspaceRoleAdmin)
	if _, err := svc.AddChannelMembers(context.Background(), addInput(amTargetA)); err != nil {
		t.Fatalf("first add: %v", err)
	}

	result, err := svc.AddChannelMembers(context.Background(), addInput(amTargetA, amTargetB))
	if err != nil {
		t.Fatalf("second add: %v", err)
	}
	if result.Added != 1 || result.AlreadyMembers != 1 {
		t.Fatalf("Added/AlreadyMembers = %d/%d, want 1/1", result.Added, result.AlreadyMembers)
	}
	if result.TotalCount != 2 {
		t.Fatalf("TotalCount = %d, want 2", result.TotalCount)
	}
}

// The identical request replayed must converge on the same state, not add twice.
func TestAddChannelMembersRetryIsIdempotent(t *testing.T) {
	svc, members, _ := addMembersFixture(t, domain.WorkspaceRoleAdmin)
	first, err := svc.AddChannelMembers(context.Background(), addInput(amTargetA, amTargetB))
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := svc.AddChannelMembers(context.Background(), addInput(amTargetA, amTargetB))
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if second.Added != 0 || second.AlreadyMembers != 2 {
		t.Fatalf("retry Added/Already = %d/%d, want 0/2", second.Added, second.AlreadyMembers)
	}
	if first.TotalCount != second.TotalCount {
		t.Fatalf("total drifted on retry: %d then %d", first.TotalCount, second.TotalCount)
	}
	if len(members.channelMembers) != 2 {
		t.Fatalf("membership rows = %d, want 2", len(members.channelMembers))
	}
}

func TestAddChannelMembersNormalizesTheRequestedIDs(t *testing.T) {
	svc, members, _ := addMembersFixture(t, domain.WorkspaceRoleAdmin)

	// The same user twice, one of them upper-cased and padded. It must reach the
	// store once, canonicalised — a duplicate would otherwise make the store's
	// row count disagree with the requested count and refuse the whole batch.
	_, err := svc.AddChannelMembers(context.Background(),
		addInput(amTargetA, "  "+strings.ToUpper(amTargetA)+"  "))
	if err != nil {
		t.Fatalf("AddChannelMembers: %v", err)
	}
	if len(members.addChannelMembersCalls) != 1 {
		t.Fatalf("store calls = %d, want 1", len(members.addChannelMembersCalls))
	}
	got := members.addChannelMembersCalls[0].UserIDs
	if len(got) != 1 || got[0] != amTargetA {
		t.Fatalf("store received %v, want [%s]", got, amTargetA)
	}
}

func TestAddChannelMembersRejectsMalformedRequests(t *testing.T) {
	tests := map[string]struct {
		userIDs []string
		want    error
	}{
		"empty list":        {userIDs: []string{}, want: domain.ErrNoMembersRequested},
		"nil list":          {userIDs: nil, want: domain.ErrNoMembersRequested},
		"blank entry":       {userIDs: []string{"   "}, want: domain.ErrInvalidInput},
		"not a uuid":        {userIDs: []string{"not-a-uuid"}, want: domain.ErrInvalidInput},
		"nil uuid rejected": {userIDs: []string{"00000000-0000-0000-0000-000000000000"}, want: domain.ErrInvalidInput},
		"over batch cap":    {userIDs: make([]string, domain.MaxAddMembersPerRequest+1), want: domain.ErrTooManyMembersRequested},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			svc, members, _ := addMembersFixture(t, domain.WorkspaceRoleAdmin)

			_, err := svc.AddChannelMembers(context.Background(), addInput(test.userIDs...))
			if !errors.Is(err, test.want) {
				t.Fatalf("err = %v, want %v", err, test.want)
			}
			if len(members.addChannelMembersCalls) != 0 {
				t.Fatal("a malformed request must not reach the store")
			}
		})
	}
}

// The cap is checked on the raw list before any UUID is parsed, so an oversized
// payload costs one comparison rather than a parse per entry.
func TestAddChannelMembersRejectsOversizedBatchBeforeParsing(t *testing.T) {
	svc, _, _ := addMembersFixture(t, domain.WorkspaceRoleAdmin)
	garbage := make([]string, domain.MaxAddMembersPerRequest+1)
	for i := range garbage {
		garbage[i] = "definitely-not-a-uuid"
	}

	_, err := svc.AddChannelMembers(context.Background(), addInput(garbage...))
	if !errors.Is(err, domain.ErrTooManyMembersRequested) {
		t.Fatalf("err = %v, want ErrTooManyMembersRequested", err)
	}
}

func TestAddChannelMembersAcceptsExactlyTheBatchCap(t *testing.T) {
	svc, members, _ := addMembersFixture(t, domain.WorkspaceRoleAdmin)
	userIDs := make([]string, 0, domain.MaxAddMembersPerRequest)
	for i := 0; i < domain.MaxAddMembersPerRequest; i++ {
		id := uuidWithSuffix(i)
		userIDs = append(userIDs, id)
		members.workspaceMembers[wmKey(amWorkspaceID, id)] = domain.WorkspaceMember{
			WorkspaceID: amWorkspaceID, UserID: id,
			Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusActive,
		}
	}

	result, err := svc.AddChannelMembers(context.Background(), addInput(userIDs...))
	if err != nil {
		t.Fatalf("AddChannelMembers at the cap: %v", err)
	}
	if result.Added != domain.MaxAddMembersPerRequest {
		t.Fatalf("Added = %d, want %d", result.Added, domain.MaxAddMembersPerRequest)
	}
}

// An archived channel is invisible to GetChannelByIDInWorkspace, so it is the
// same ErrNotFound as a channel from another workspace or one that never was.
func TestAddChannelMembersRejectsUnreachableChannel(t *testing.T) {
	tests := map[string]func(*fakeChannelStore){
		"archived":        func(c *fakeChannelStore) { c.channel.Status = domain.ChannelStatusArchived },
		"other workspace": func(c *fakeChannelStore) { c.channel.WorkspaceID = "99999999-9999-4999-8999-999999999999" },
		"does not exist":  func(c *fakeChannelStore) { c.channel.ID = "88888888-8888-4888-8888-888888888888" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			svc, members, channels := addMembersFixture(t, domain.WorkspaceRoleAdmin)
			mutate(channels)

			_, err := svc.AddChannelMembers(context.Background(), addInput(amTargetA))
			if !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("err = %v, want ErrNotFound", err)
			}
			if len(members.addChannelMembersCalls) != 0 {
				t.Fatal("an unreachable channel must not reach the store")
			}
		})
	}
}

// Membership in #geral is owned by the workspace sync. Adding to it here would
// either be a no-op or a second writer for rows that path maintains.
func TestAddChannelMembersRejectsGeneralChannel(t *testing.T) {
	svc, members, channels := addMembersFixture(t, domain.WorkspaceRoleOwner)
	channels.channel.IsGeneral = true
	channels.channel.Type = domain.ChannelTypePublic

	_, err := svc.AddChannelMembers(context.Background(), addInput(amTargetA))
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
	if len(members.addChannelMembersCalls) != 0 {
		t.Fatal("geral must not reach the store")
	}
}

// The store is handed the workspace the service resolved, never anything a
// caller supplied, so a cross-workspace write has no path to SQL.
func TestAddChannelMembersScopesTheStoreCallToTheResolvedWorkspace(t *testing.T) {
	svc, members, _ := addMembersFixture(t, domain.WorkspaceRoleAdmin)

	if _, err := svc.AddChannelMembers(context.Background(), addInput(amTargetA)); err != nil {
		t.Fatalf("AddChannelMembers: %v", err)
	}
	call := members.addChannelMembersCalls[0]
	if call.WorkspaceID != amWorkspaceID || call.ChannelID != amChannelID {
		t.Fatalf("store scoped to %s/%s, want %s/%s",
			call.WorkspaceID, call.ChannelID, amWorkspaceID, amChannelID)
	}
}

func TestAddChannelMembersPropagatesStorageFailure(t *testing.T) {
	svc, members, _ := addMembersFixture(t, domain.WorkspaceRoleAdmin)
	members.addCMsErr = errors.New("connection reset")

	_, err := svc.AddChannelMembers(context.Background(), addInput(amTargetA))
	if err == nil {
		t.Fatal("expected the storage failure to surface")
	}
	// Not a denial: an infrastructure fault must not be reported as "forbidden",
	// which the handler would turn into a 403 the caller could never resolve.
	if errors.Is(err, domain.ErrForbidden) || errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("storage failure masqueraded as a denial: %v", err)
	}
}

// uuidWithSuffix builds distinct valid UUIDs for bulk fixtures.
func uuidWithSuffix(i int) string {
	const hex = "0123456789abcdef"
	return "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaa" +
		string(hex[(i/16)%16]) + string(hex[i%16])
}

// ── storage.AddMembersResult plumbing ───────────────────────────────────────

// The service must hand the store's counts back untouched: the panel sets its
// member counter from TotalCount, and a service that recomputed it locally would
// be a second, drifting source of truth.
func TestAddChannelMembersReturnsStoreCountsUnchanged(t *testing.T) {
	svc, _, _ := addMembersFixture(t, domain.WorkspaceRoleAdmin)

	result, err := svc.AddChannelMembers(context.Background(), addInput(amTargetA, amTargetB))
	if err != nil {
		t.Fatalf("AddChannelMembers: %v", err)
	}
	if result.Added != 2 || result.AlreadyMembers != 0 || result.TotalCount != 2 {
		t.Fatalf("result = %+v, want 2/0/2", result)
	}
}

// The actor must reach the store, which re-derives authority transactionally.
// A store that never learns who is acting cannot enforce anything.
func TestAddChannelMembersPassesTheActorToTheStore(t *testing.T) {
	svc, members, _ := addMembersFixture(t, domain.WorkspaceRoleAdmin)

	if _, err := svc.AddChannelMembers(context.Background(), addInput(amTargetA)); err != nil {
		t.Fatalf("AddChannelMembers: %v", err)
	}
	if got := members.addChannelMembersCalls[0].CallerID; got != amManagerID {
		t.Fatalf("store received actor %q, want %q", got, amManagerID)
	}
}

// The store's verdict is final. When it refuses a caller whose authority was
// revoked after the service's check, the service must surface that rather than
// let its own earlier "yes" stand.
func TestAddChannelMembersHonoursAStoreLevelAuthorizationRefusal(t *testing.T) {
	svc, members, _ := addMembersFixture(t, domain.WorkspaceRoleAdmin)
	members.addCMsErr = domain.ErrForbidden

	_, err := svc.AddChannelMembers(context.Background(), addInput(amTargetA))

	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
}

// ── Contextual candidate search (issue #398) ────────────────────────────────

func candidateInput(query string) service.SearchChannelMemberCandidatesInput {
	return service.SearchChannelMemberCandidatesInput{
		WorkspaceID: amWorkspaceID, CallerID: amManagerID,
		ChannelID: amChannelID, Query: query,
	}
}

// The exclusion of current members belongs to the store's SQL. The service's job
// is to authorise, bound the query and hand over the server-derived scope.
func TestSearchChannelMemberCandidatesPassesServerDerivedScope(t *testing.T) {
	svc, members, _ := addMembersFixture(t, domain.WorkspaceRoleAdmin)
	members.dmCandidates = []domain.DMCandidate{{UserID: amTargetA, DisplayName: "Ana"}}

	got, err := svc.SearchChannelMemberCandidates(context.Background(), candidateInput("an"))
	if err != nil {
		t.Fatalf("SearchChannelMemberCandidates: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("candidates = %d, want 1", len(got))
	}
	call := members.candidateCalls[0]
	if call.WorkspaceID != amWorkspaceID || call.TargetID != amChannelID {
		t.Fatalf("scope = %s/%s, want %s/%s", call.WorkspaceID, call.TargetID, amWorkspaceID, amChannelID)
	}
	// The actor reaches SQL from the session-derived membership, never a payload.
	if call.CallerID != amManagerID {
		t.Fatalf("CallerID = %q, want %q", call.CallerID, amManagerID)
	}
	if call.Limit != 20 {
		t.Fatalf("Limit = %d, want the server default 20", call.Limit)
	}
}

// A member of the channel must not come back, and the proof is that the store's
// predicate — not the panel — decides it.
func TestSearchChannelMemberCandidatesExcludesCurrentMembers(t *testing.T) {
	svc, members, _ := addMembersFixture(t, domain.WorkspaceRoleAdmin)
	members.dmCandidates = []domain.DMCandidate{
		{UserID: amTargetA, DisplayName: "Ana"},
		{UserID: amTargetB, DisplayName: "Bruno"},
	}
	// amTargetA is already in the channel — including offline, which the panel's
	// preview would never have shown.
	members.channelMembers[cmKey(amChannelID, amTargetA)] = domain.ChannelMember{
		ChannelID: amChannelID, UserID: amTargetA, Role: domain.ChannelRoleMember,
	}

	got, err := svc.SearchChannelMemberCandidates(context.Background(), candidateInput("an"))
	if err != nil {
		t.Fatalf("SearchChannelMemberCandidates: %v", err)
	}
	if len(got) != 1 || got[0].UserID != amTargetB {
		t.Fatalf("candidates = %+v, want only %s", got, amTargetB)
	}
}

// Same gate as the write, checked before the channel is read so a refused caller
// cannot learn whether the channel exists.
func TestSearchChannelMemberCandidatesRequiresAddCapability(t *testing.T) {
	for _, role := range []domain.WorkspaceRole{domain.WorkspaceRoleGuest, domain.WorkspaceRole("wizard")} {
		t.Run(string(role), func(t *testing.T) {
			svc, members, channels := addMembersFixture(t, role)
			channels.getInWorkspaceErr = errors.New("must not be called")

			_, err := svc.SearchChannelMemberCandidates(context.Background(), candidateInput("an"))

			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("err = %v, want ErrForbidden", err)
			}
			if len(members.candidateCalls) != 0 {
				t.Fatal("an unauthorised caller must not reach the store")
			}
		})
	}
}

func TestSearchChannelMemberCandidatesAllowsVisibleMember(t *testing.T) {
	svc, members, channels := addMembersFixture(t, domain.WorkspaceRoleMember)
	members.channelMembers[cmKey(amChannelID, amManagerID)] = domain.ChannelMember{
		ChannelID: amChannelID, UserID: amManagerID, Role: domain.ChannelRoleMember,
	}
	members.dmCandidates = []domain.DMCandidate{{UserID: amTargetA, DisplayName: "Ana"}}

	got, err := svc.SearchChannelMemberCandidates(context.Background(), candidateInput("an"))

	if err != nil {
		t.Fatalf("SearchChannelMemberCandidates: %v", err)
	}
	if len(got) != 1 || channels.getVisibleByIDCalls != 1 {
		t.Fatalf("candidates = %+v, visible lookups = %d", got, channels.getVisibleByIDCalls)
	}
}

func TestSearchChannelMemberCandidatesAllowsAdministrativeRolesWithoutPrivateMembership(t *testing.T) {
	for _, role := range []domain.WorkspaceRole{
		domain.WorkspaceRoleOwner, domain.WorkspaceRoleAdmin, domain.WorkspaceRoleModerator,
	} {
		t.Run(string(role), func(t *testing.T) {
			svc, members, channels := addMembersFixture(t, role)
			members.dmCandidates = []domain.DMCandidate{{UserID: amTargetA, DisplayName: "Ana"}}

			got, err := svc.SearchChannelMemberCandidates(context.Background(), candidateInput("an"))
			if err != nil {
				t.Fatalf("SearchChannelMemberCandidates: %v", err)
			}
			if len(got) != 1 || channels.getVisibleByIDCalls != 0 {
				t.Fatalf("candidates = %+v, visible lookups = %d", got, channels.getVisibleByIDCalls)
			}
		})
	}
}

func TestSearchChannelMemberCandidatesHidesPrivateChannelFromMemberWithoutAccess(t *testing.T) {
	svc, members, channels := addMembersFixture(t, domain.WorkspaceRoleMember)
	channels.getVisibleErr = domain.ErrNotFound

	_, err := svc.SearchChannelMemberCandidates(context.Background(), candidateInput("an"))

	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if len(members.candidateCalls) != 0 {
		t.Fatal("an invisible private channel must not reach candidate storage")
	}
}

func TestSearchChannelMemberCandidatesRejectsGeneralChannel(t *testing.T) {
	svc, members, channels := addMembersFixture(t, domain.WorkspaceRoleOwner)
	channels.channel.IsGeneral = true
	channels.channel.Type = domain.ChannelTypePublic

	_, err := svc.SearchChannelMemberCandidates(context.Background(), candidateInput("an"))

	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
	if len(members.candidateCalls) != 0 {
		t.Fatal("general channel must not reach candidate storage")
	}
}

func TestSearchChannelMemberCandidatesRejectsInvalidQueries(t *testing.T) {
	tests := map[string]struct {
		query string
		limit int
	}{
		"too short":      {query: "a"},
		"empty":          {query: "   "},
		"negative limit": {query: "an", limit: -1},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			svc, members, _ := addMembersFixture(t, domain.WorkspaceRoleAdmin)
			input := candidateInput(test.query)
			input.Limit = test.limit

			_, err := svc.SearchChannelMemberCandidates(context.Background(), input)

			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("err = %v, want ErrInvalidInput", err)
			}
			if len(members.candidateCalls) != 0 {
				t.Fatal("an invalid search must not reach the store")
			}
		})
	}
}

// The client never picks the ceiling.
func TestSearchChannelMemberCandidatesClampsTheLimit(t *testing.T) {
	svc, members, _ := addMembersFixture(t, domain.WorkspaceRoleAdmin)
	input := candidateInput("an")
	input.Limit = 5_000

	if _, err := svc.SearchChannelMemberCandidates(context.Background(), input); err != nil {
		t.Fatalf("SearchChannelMemberCandidates: %v", err)
	}
	if got := members.candidateCalls[0].Limit; got != 50 {
		t.Fatalf("Limit = %d, want the server maximum 50", got)
	}
}

// Archived, cross-workspace and missing channels are one answer.
func TestSearchChannelMemberCandidatesRejectsUnreachableChannel(t *testing.T) {
	svc, members, channels := addMembersFixture(t, domain.WorkspaceRoleAdmin)
	channels.channel.Status = domain.ChannelStatusArchived

	_, err := svc.SearchChannelMemberCandidates(context.Background(), candidateInput("an"))

	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if len(members.candidateCalls) != 0 {
		t.Fatal("an unreachable channel must not reach the store")
	}
}

// ── Fan-out recipients (issue #398) ─────────────────────────────────────────

// The signal must be aimed at who the transaction inserted, not at who was
// requested — the two differ whenever the payload names an existing member.
func TestAddChannelMembersReturnsOnlyTheActuallyAddedIDs(t *testing.T) {
	svc, _, _ := addMembersFixture(t, domain.WorkspaceRoleAdmin)

	first, err := svc.AddChannelMembers(context.Background(), addInput(amTargetA))
	if err != nil {
		t.Fatalf("first add: %v", err)
	}
	if len(first.AddedUserIDs) != 1 || first.AddedUserIDs[0] != amTargetA {
		t.Fatalf("AddedUserIDs = %v, want [%s]", first.AddedUserIDs, amTargetA)
	}

	// Second call repeats A and adds B: only B is newly a member.
	second, err := svc.AddChannelMembers(context.Background(), addInput(amTargetA, amTargetB))
	if err != nil {
		t.Fatalf("second add: %v", err)
	}
	if len(second.AddedUserIDs) != 1 || second.AddedUserIDs[0] != amTargetB {
		t.Fatalf("AddedUserIDs = %v, want only %s", second.AddedUserIDs, amTargetB)
	}
	if second.Added != len(second.AddedUserIDs) {
		t.Fatalf("Added=%d disagrees with AddedUserIDs=%v", second.Added, second.AddedUserIDs)
	}
}

// A pure retry inserts nobody, so there is nobody to signal.
func TestAddChannelMembersReturnsNoAddedIDsOnPureRetry(t *testing.T) {
	svc, _, _ := addMembersFixture(t, domain.WorkspaceRoleAdmin)
	if _, err := svc.AddChannelMembers(context.Background(), addInput(amTargetA)); err != nil {
		t.Fatalf("first add: %v", err)
	}

	retry, err := svc.AddChannelMembers(context.Background(), addInput(amTargetA))
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if retry.Added != 0 || len(retry.AddedUserIDs) != 0 {
		t.Fatalf("retry produced recipients: Added=%d IDs=%v", retry.Added, retry.AddedUserIDs)
	}
}

// ── Batch bound vs. conversation capacity (product decision) ────────────────

// 25 is the size of one request, not the size a conversation may reach. These
// pin both halves of that so neither drifts into the other.
func TestAddChannelMembersAcceptsSuccessiveBatchesWithoutCapacityLimit(t *testing.T) {
	svc, members, _ := addMembersFixture(t, domain.WorkspaceRoleAdmin)

	// Three full batches in a row, each the maximum a request may carry.
	total := 0
	for batch := 0; batch < 3; batch++ {
		userIDs := make([]string, 0, domain.MaxAddMembersPerRequest)
		for i := 0; i < domain.MaxAddMembersPerRequest; i++ {
			id := uuidWithSuffix(batch*domain.MaxAddMembersPerRequest + i)
			userIDs = append(userIDs, id)
			members.workspaceMembers[wmKey(amWorkspaceID, id)] = domain.WorkspaceMember{
				WorkspaceID: amWorkspaceID, UserID: id,
				Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusActive,
			}
		}

		result, err := svc.AddChannelMembers(context.Background(), addInput(userIDs...))
		if err != nil {
			t.Fatalf("batch %d refused: %v", batch, err)
		}
		if result.Added != domain.MaxAddMembersPerRequest {
			t.Fatalf("batch %d added %d, want %d", batch, result.Added, domain.MaxAddMembersPerRequest)
		}
		total += result.Added
	}

	// The accumulated membership is far past any single batch, and nothing
	// refused it.
	if total != 3*domain.MaxAddMembersPerRequest {
		t.Fatalf("accumulated %d members, want %d", total, 3*domain.MaxAddMembersPerRequest)
	}
}

// One more than the batch cap is still refused — the request bound is intact.
func TestAddChannelMembersStillRejectsOneOverTheBatchCap(t *testing.T) {
	svc, members, _ := addMembersFixture(t, domain.WorkspaceRoleAdmin)
	userIDs := make([]string, 0, domain.MaxAddMembersPerRequest+1)
	for i := 0; i <= domain.MaxAddMembersPerRequest; i++ {
		id := uuidWithSuffix(i)
		userIDs = append(userIDs, id)
		members.workspaceMembers[wmKey(amWorkspaceID, id)] = domain.WorkspaceMember{
			WorkspaceID: amWorkspaceID, UserID: id,
			Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusActive,
		}
	}

	_, err := svc.AddChannelMembers(context.Background(), addInput(userIDs...))

	if !errors.Is(err, domain.ErrTooManyMembersRequested) {
		t.Fatalf("err = %v, want ErrTooManyMembersRequested", err)
	}
	// It is a property of the request, so it maps to invalid input — not to a
	// conflict, which would imply the conversation was full.
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatal("the batch cap must remain an input error, not a capacity conflict")
	}
	if len(members.addChannelMembersCalls) != 0 {
		t.Fatal("an oversized batch must not reach the store")
	}
}
