package service_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Issue #1025: initial members of a private channel. Eligibility, atomicity and
// idempotency under concurrency are the store's and are proved on PostgreSQL;
// these cover what the service owns — normalization, the public/private
// contract and the request fingerprint.

const (
	ccCreator  = "c0000000-0000-4000-8000-000000000001"
	ccInviteeA = "c0000000-0000-4000-8000-00000000000a"
	ccInviteeB = "c0000000-0000-4000-8000-00000000000b"
)

func creationService(t *testing.T) (*service.ChannelService, *fakeChannelStore) {
	t.Helper()
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", ccCreator)] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: ccCreator, Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusActive,
	}
	channels := &fakeChannelStore{createdChannel: domain.Channel{ID: "ch-new", WorkspaceID: "ws-1", Type: domain.ChannelTypePrivate}}
	return service.NewChannelService(activeWorkspaceStore("ws-1"), channels, ms), channels
}

func privateInput(invitees ...string) service.CreateChannelInput {
	return service.CreateChannelInput{
		WorkspaceID: "ws-1", CallerID: ccCreator, Slug: "infra", DisplayName: "Infra",
		Type: domain.ChannelTypePrivate, InitialMemberIDs: invitees,
	}
}

func TestChannelService_CreateChannel_NormalizesInitialMembers(t *testing.T) {
	svc, channels := creationService(t)

	got, err := svc.CreateChannel(context.Background(), privateInput(
		" "+strings.ToUpper(ccInviteeB)+" ", ccInviteeA, ccInviteeB, ccCreator,
	))
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	// Trimmed, canonical, de-duplicated, sorted — and the creator, implicit,
	// dropped rather than inserted twice.
	want := []string{ccInviteeA, ccInviteeB}
	if !slices.Equal(channels.lastCreateInput.InitialMemberIDs, want) {
		t.Fatalf("store got %v, want %v", channels.lastCreateInput.InitialMemberIDs, want)
	}
	if !slices.Equal(got.InitialMemberIDs, want) || got.Replayed {
		t.Fatalf("result = %+v", got)
	}
	if channels.lastCreateInput.EnsureCreatorMemberRole != domain.ChannelRoleMember {
		t.Fatalf("creator membership not requested: %+v", channels.lastCreateInput)
	}
}

func TestChannelService_CreateChannel_CreatorOnlyListIsEmpty(t *testing.T) {
	svc, channels := creationService(t)
	if _, err := svc.CreateChannel(context.Background(), privateInput(ccCreator)); err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	if len(channels.lastCreateInput.InitialMemberIDs) != 0 {
		t.Fatalf("creator became an invitee: %v", channels.lastCreateInput.InitialMemberIDs)
	}
}

func distinctUserIDs(n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = uuid.NewString()
	}
	return ids
}

// assertRefusedBeforeStorage: the request is invalid input and the store never
// saw it.
func assertRefusedBeforeStorage(t *testing.T, input service.CreateChannelInput) {
	t.Helper()
	svc, channels := creationService(t)
	if _, err := svc.CreateChannel(context.Background(), input); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("error = %v, want ErrInvalidInput", err)
	}
	if channels.lastCreateInput.Slug != "" {
		t.Fatalf("a refused request reached storage: %+v", channels.lastCreateInput)
	}
}

func TestChannelService_CreateChannel_RejectsMalformedInitialMemberIDs(t *testing.T) {
	for name, rawID := range map[string]string{
		"invalid uuid": "not-a-uuid",
		"zero uuid":    uuid.Nil.String(),
		"blank id":     "  ",
	} {
		t.Run(name, func(t *testing.T) { assertRefusedBeforeStorage(t, privateInput(ccInviteeA, rawID)) })
	}
}

// The cap is on the raw list: one duplicate past the limit is still refused,
// even though it would de-duplicate to a list that fits.
func TestChannelService_CreateChannel_RejectsARawListOverTheLimit(t *testing.T) {
	atLimit := distinctUserIDs(domain.MaxAddMembersPerRequest)
	assertRefusedBeforeStorage(t, privateInput(append(atLimit, atLimit[0])...))
}

func TestChannelService_CreateChannel_RejectsInviteesOnAPublicChannel(t *testing.T) {
	input := privateInput(ccInviteeA)
	input.Type = domain.ChannelTypePublic
	assertRefusedBeforeStorage(t, input)
}

func TestChannelService_CreateChannel_AcceptsExactlyTheLimit(t *testing.T) {
	svc, channels := creationService(t)
	if _, err := svc.CreateChannel(context.Background(), privateInput(distinctUserIDs(domain.MaxAddMembersPerRequest)...)); err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	if got := len(channels.lastCreateInput.InitialMemberIDs); got != domain.MaxAddMembersPerRequest {
		t.Fatalf("invitees = %d, want %d", got, domain.MaxAddMembersPerRequest)
	}
}

// A public channel with an empty list is the unchanged public path.
func TestChannelService_CreateChannel_PublicEmptyListIsUnchanged(t *testing.T) {
	svc, channels := creationService(t)
	input := privateInput()
	input.Type = domain.ChannelTypePublic
	input.InitialMemberIDs = []string{}
	if _, err := svc.CreateChannel(context.Background(), input); err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	got := channels.lastCreateInput
	if !got.EnsurePublicWorkspaceMembers || got.EnsureCreatorMemberRole != "" || len(got.InitialMemberIDs) != 0 {
		t.Fatalf("public creation changed: %+v", got)
	}
}

func TestChannelService_CreateChannel_FingerprintsTheNormalizedRequest(t *testing.T) {
	hashOf := func(input service.CreateChannelInput) storage.CreateChannelInput {
		t.Helper()
		svc, channels := creationService(t)
		input.IdempotencyKey = "intent-1"
		if _, err := svc.CreateChannel(context.Background(), input); err != nil {
			t.Fatalf("CreateChannel: %v", err)
		}
		return channels.lastCreateInput
	}

	base := hashOf(privateInput(ccInviteeA, ccInviteeB))
	if base.IdempotencyKey != "intent-1" || len(base.RequestHash) != 64 {
		t.Fatalf("key/hash not forwarded: %+v", base)
	}
	same := hashOf(privateInput(ccInviteeB, ccInviteeA, ccCreator, ccInviteeA))
	if same.RequestHash != base.RequestHash {
		t.Fatal("the same intent spelled differently hashed differently")
	}
	renamed := privateInput(ccInviteeA, ccInviteeB)
	renamed.DisplayName = "Infra 2"
	for name, input := range map[string]service.CreateChannelInput{
		"fewer invitees": privateInput(ccInviteeA),
		"other name":     renamed,
	} {
		if hashOf(input).RequestHash == base.RequestHash {
			t.Fatalf("%s hashed like the original", name)
		}
	}

	svc, channels := creationService(t)
	if _, err := svc.CreateChannel(context.Background(), privateInput()); err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	if channels.lastCreateInput.RequestHash != "" {
		t.Fatal("an unkeyed request carries a fingerprint nobody compares")
	}
}

func TestChannelService_CreateChannel_ReplayAnnouncesNobody(t *testing.T) {
	svc, channels := creationService(t)
	channels.createReplayed = true
	got, err := svc.CreateChannel(context.Background(), privateInput(ccInviteeA))
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	if !got.Replayed || len(got.InitialMemberIDs) != 0 || got.Channel.ID != "ch-new" {
		t.Fatalf("replay result = %+v", got)
	}
}

func TestChannelService_CreateChannel_StoreErrorIsReturned(t *testing.T) {
	svc, channels := creationService(t)
	channels.createChanErr = domain.ErrIdempotencyKeyReused
	if _, err := svc.CreateChannel(context.Background(), privateInput(ccInviteeA)); !errors.Is(err, domain.ErrIdempotencyKeyReused) {
		t.Fatalf("error = %v", err)
	}
}

// The session caller is a UUID by construction; anything else is a wiring bug
// and fails closed rather than skipping the creator de-duplication.
func TestChannelService_CreateChannel_NonUUIDCallerWithInviteesIsForbidden(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "member-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "member-1", Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusActive,
	}
	svc := service.NewChannelService(activeWorkspaceStore("ws-1"), &fakeChannelStore{}, ms)
	input := privateInput(ccInviteeA)
	input.CallerID = "member-1"
	if _, err := svc.CreateChannel(context.Background(), input); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("error = %v, want ErrForbidden", err)
	}
}
