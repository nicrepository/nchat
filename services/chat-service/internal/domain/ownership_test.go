package domain

import (
	"testing"
	"time"
)

func TestPrivateParticipantActions(t *testing.T) {
	roles := []ConversationRole{ConversationOwner, ConversationAdmin, ConversationMember, "moderator", ""}
	for _, actorRole := range roles {
		for _, targetRole := range roles {
			actor := OwnershipParticipant{UserID: "a", Role: actorRole, HasAccess: true}
			target := OwnershipParticipant{UserID: "b", Role: targetRole, HasAccess: true}
			got := PrivateParticipantActions(actor, target)
			valid := actorRole.Valid() && targetRole.Valid()
			wantRemove := valid && targetRole != ConversationOwner && (actorRole == ConversationOwner || actorRole == ConversationAdmin && targetRole == ConversationMember)
			if got.Remove != wantRemove || got.AssignRole != (valid && actorRole == ConversationOwner) || got.Transfer != (valid && actorRole == ConversationOwner) {
				t.Fatalf("actor %q target %q: %+v", actorRole, targetRole, got)
			}
			target.HasAccess = false
			if got := PrivateParticipantActions(actor, target); got != (ParticipantActions{}) {
				t.Fatalf("inaccessible target: %+v", got)
			}
		}
	}
	actor := OwnershipParticipant{UserID: "a", Role: ConversationOwner, HasAccess: true}
	if got := PrivateParticipantActions(actor, actor); got.Remove || got.Transfer {
		t.Fatalf("self actions: %+v", got)
	}
}

func TestOwnershipLeaveSuccession(t *testing.T) {
	old := time.Unix(1, 0)
	newer := time.Unix(2, 0)
	owner := OwnershipParticipant{UserID: "owner", Role: ConversationOwner, HasAccess: true}
	member := OwnershipParticipant{UserID: "member", Role: ConversationMember, JoinedAt: old, HasAccess: true}
	admin := OwnershipParticipant{UserID: "admin", Role: ConversationAdmin, JoinedAt: newer, HasAccess: true}
	cases := []struct {
		name    string
		members []OwnershipParticipant
		want    OwnershipLeavePreview
	}{
		{"admin priority", []OwnershipParticipant{owner, member, admin}, OwnershipLeavePreview{LastOwner: true, SuccessorUserID: "admin"}},
		{"member fallback", []OwnershipParticipant{owner, member}, OwnershipLeavePreview{LastOwner: true, SuccessorUserID: "member"}},
		{"last participant", []OwnershipParticipant{owner}, OwnershipLeavePreview{LastOwner: true}},
		{"another owner", []OwnershipParticipant{owner, {UserID: "other", Role: ConversationOwner, HasAccess: true}, admin}, OwnershipLeavePreview{}},
		{"guest blocked", []OwnershipParticipant{owner, {UserID: "guest", Role: ConversationAdmin, HasAccess: true, Guest: true}}, OwnershipLeavePreview{LastOwner: true, Blocked: true}},
		{"invalid ignored", []OwnershipParticipant{owner, {UserID: "invalid", Role: ConversationAdmin}, member}, OwnershipLeavePreview{LastOwner: true, SuccessorUserID: "member"}},
		{"guest owner counts", []OwnershipParticipant{owner, {UserID: "guest", Role: ConversationOwner, HasAccess: true, Guest: true}}, OwnershipLeavePreview{}},
		{"stable tie", []OwnershipParticipant{owner, {UserID: "b", Role: ConversationMember, HasAccess: true, JoinedAt: old}, {UserID: "a", Role: ConversationMember, HasAccess: true, JoinedAt: old}}, OwnershipLeavePreview{LastOwner: true, SuccessorUserID: "a"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := PreviewOwnershipLeave(tc.members, "owner"); got != tc.want {
				t.Fatalf("got %+v want %+v", got, tc.want)
			}
		})
	}
}

func TestOwnershipCapabilitiesFailClosed(t *testing.T) {
	if got := PrivateConversationCapabilities(OwnershipParticipant{}, true); got != (OwnershipCapabilities{}) {
		t.Fatal(got)
	}
	for _, role := range []ConversationRole{ConversationOwner, ConversationAdmin, ConversationMember} {
		actor := OwnershipParticipant{Role: role, HasAccess: true}
		got := PrivateConversationCapabilities(actor, true)
		if !got.AddMembers || !got.Leave || got.ManageRoles != (role == ConversationOwner) || got.EditMetadata != (role != ConversationMember) {
			t.Fatal(got)
		}
	}
}
