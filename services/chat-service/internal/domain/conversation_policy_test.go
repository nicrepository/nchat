package domain_test

import (
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

type decisions struct {
	assign                                             [3]bool // intended owner, admin, member
	remove, add, transfer, leave, metadata, selfDemote bool
}

func TestConversationPolicy(t *testing.T) {
	owner, admin, member := domain.ConversationOwner, domain.ConversationAdmin, domain.ConversationMember
	allowed := decisions{[3]bool{true, true, false}, true, true, true, false, true, false}
	denied := decisions{}
	memberWithOwner := decisions{[3]bool{}, false, true, false, true, true, false}
	lastParticipant := decisions{[3]bool{}, false, true, false, true, true, false}
	tests := []struct {
		name          string
		actor, target domain.ConversationRole
		change        func(*domain.ConversationPolicyContext)
		want          decisions
	}{
		{"owner/member", owner, member, nil, allowed},
		{"owner/admin", owner, admin, nil, decisions{[3]bool{true, false, true}, true, true, true, false, true, false}},
		{"owner/owner", owner, owner, nil, decisions{[3]bool{false, true, true}, false, true, true, true, true, true}},
		{"admin/member", admin, member, nil, decisions{[3]bool{}, true, true, false, false, true, false}},
		{"admin/admin", admin, admin, nil, decisions{[3]bool{}, false, true, false, false, true, false}},
		{"admin/owner", admin, owner, nil, memberWithOwner},
		{"member/member", member, member, nil, decisions{[3]bool{}, false, true, false, false, true, false}},
		{"member/admin", member, admin, nil, decisions{[3]bool{}, false, true, false, false, true, false}},
		{"member/owner", member, owner, nil, memberWithOwner},
		{"workspace admin local member", member, owner, func(c *domain.ConversationPolicyContext) {
			c.Participants[0].Membership.Role = domain.WorkspaceRoleAdmin
		}, memberWithOwner},
		{"workspace member local owner", owner, member, nil, allowed},
		{"manual guest owner", owner, member, func(c *domain.ConversationPolicyContext) {
			c.Participants[0].Membership.Role = domain.WorkspaceRoleGuest
		}, allowed},
		{"manual guest target", owner, member, func(c *domain.ConversationPolicyContext) {
			c.Participants[1].Membership.Role = domain.WorkspaceRoleGuest
		}, allowed},
		{"guest member keeps add", member, owner, func(c *domain.ConversationPolicyContext) {
			c.Participants[0].Membership.Role = domain.WorkspaceRoleGuest
		}, memberWithOwner},
		{"last participant owner", owner, member, func(c *domain.ConversationPolicyContext) { c.Participants = c.Participants[:1] }, lastParticipant},
		{"last participant member", member, member, func(c *domain.ConversationPolicyContext) { c.Participants = c.Participants[:1] }, lastParticipant},
		{"inaccessible owner not counted", owner, owner, func(c *domain.ConversationPolicyContext) { c.Participants[1].HasAccess = false }, lastParticipant},
		{"private channel", owner, member, func(c *domain.ConversationPolicyContext) { c.Kind = domain.ConversationKindPrivateChannel }, allowed},
		{"missing context", owner, member, func(c *domain.ConversationPolicyContext) { *c = domain.ConversationPolicyContext{} }, denied},
		{"archived conversation", owner, member, func(c *domain.ConversationPolicyContext) { c.Active = false }, denied},
		{"disabled workspace", owner, member, func(c *domain.ConversationPolicyContext) { c.Workspace.Status = domain.WorkspaceStatusDisabled }, denied},
		{"missing workspace", owner, member, func(c *domain.ConversationPolicyContext) { c.Workspace.ID = "" }, denied},
		{"missing conversation", owner, member, func(c *domain.ConversationPolicyContext) { c.ID = "" }, denied},
		{"direct conversation", owner, member, func(c *domain.ConversationPolicyContext) { c.Kind = "direct" }, denied},
		{"public channel", owner, member, func(c *domain.ConversationPolicyContext) { c.Kind = "public" }, denied},
		{"unknown actor role", owner, member, func(c *domain.ConversationPolicyContext) { c.Participants[0].Role = "moderator" }, denied},
		{"unknown target role", owner, member, func(c *domain.ConversationPolicyContext) { c.Participants[1].Role = "" }, denied},
		{"unknown workspace role", owner, member, func(c *domain.ConversationPolicyContext) { c.Participants[0].Membership.Role = "unknown" }, denied},
		{"suspended actor", owner, member, func(c *domain.ConversationPolicyContext) {
			c.Participants[0].Membership.Status = domain.MemberStatusSuspended
		}, denied},
		{"left target", owner, member, func(c *domain.ConversationPolicyContext) {
			c.Participants[1].Membership.Status = domain.MemberStatusLeft
		}, denied},
		{"actor inaccessible", owner, member, func(c *domain.ConversationPolicyContext) { c.Participants[0].HasAccess = false }, denied},
		{"guest without explicit access", owner, member, func(c *domain.ConversationPolicyContext) {
			c.Participants[0].Membership.Role = domain.WorkspaceRoleGuest
			c.Participants[0].HasAccess = false
		}, denied},
		{"actor cross workspace", owner, member, func(c *domain.ConversationPolicyContext) { c.Participants[0].Membership.WorkspaceID = "other" }, denied},
		{"target cross workspace", owner, member, func(c *domain.ConversationPolicyContext) { c.Participants[1].Membership.WorkspaceID = "other" }, denied},
		{"actor wrong conversation", owner, member, func(c *domain.ConversationPolicyContext) { c.Participants[0].ConversationID = "other" }, denied},
		{"target wrong conversation", owner, member, func(c *domain.ConversationPolicyContext) { c.Participants[1].ConversationID = "other" }, denied},
		{"actor identity mismatch", owner, member, func(c *domain.ConversationPolicyContext) { c.Participants[0].Membership.UserID = "other" }, denied},
		{"empty identity", owner, member, func(c *domain.ConversationPolicyContext) { c.Participants[0].Membership.UserID = "" }, denied},
		{"duplicate owner cannot bypass invariant", owner, member, func(c *domain.ConversationPolicyContext) { c.Participants = append(c.Participants, c.Participants[0]) }, denied},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := conversationPolicyFixture(tt.actor, tt.target)
			if tt.change != nil {
				tt.change(&c)
			}
			got := conversationPolicyDecisions(c)
			if got != tt.want {
				t.Fatalf("decisions = %+v, want %+v", got, tt.want)
			}
			assertConversationPolicyBoundaries(t, c)
		})
	}
}

func conversationPolicyFixture(actor, target domain.ConversationRole) domain.ConversationPolicyContext {
	c := domain.ConversationPolicyContext{
		Workspace: domain.Workspace{ID: "ws", Status: domain.WorkspaceStatusActive},
		ID:        "conversation", Kind: domain.ConversationKindGroup, Active: true,
	}
	for i, role := range []domain.ConversationRole{actor, target} {
		c.Participants = append(c.Participants, domain.ConversationPolicyParticipant{
			ConversationID: c.ID, Role: role, HasAccess: true,
			Membership: domain.WorkspaceMember{WorkspaceID: "ws", UserID: []string{"actor", "target"}[i], Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusActive},
		})
	}

	return c
}

func conversationPolicyDecisions(c domain.ConversationPolicyContext) decisions {
	got := decisions{
		remove:     domain.CanRemoveConversationMember(c, "actor", "target"),
		add:        domain.CanAddConversationMember(c, "actor"),
		transfer:   domain.CanTransferConversationOwnership(c, "actor", "target"),
		leave:      domain.CanLeaveConversation(c, "actor"),
		metadata:   domain.CanEditConversationMetadata(c, "actor", true),
		selfDemote: domain.CanAssignConversationRole(c, "actor", "actor", domain.ConversationMember),
	}
	for i, role := range []domain.ConversationRole{domain.ConversationOwner, domain.ConversationAdmin, domain.ConversationMember} {
		got.assign[i] = domain.CanAssignConversationRole(c, "actor", "target", role)
	}

	return got
}

func assertConversationPolicyBoundaries(t *testing.T, c domain.ConversationPolicyContext) {
	t.Helper()
	if domain.CanAssignConversationRole(c, "actor", "target", "unknown") {
		t.Fatal("unknown intended role accepted")
	}
	if domain.CanRemoveConversationMember(c, "actor", "actor") || domain.CanTransferConversationOwnership(c, "actor", "actor") {
		t.Fatal("self removal or transfer accepted")
	}
	if domain.CanEditConversationMetadata(c, "actor", false) {
		t.Fatal("operation permission bypassed")
	}
	if domain.CanAddConversationMember(c, "") || domain.CanAddConversationMember(c, "stranger") {
		t.Fatal("missing actor accepted")
	}
}

func TestCanManageConversationRolesIncludesAuthorizedNoop(t *testing.T) {
	for _, role := range []domain.ConversationRole{domain.ConversationOwner, domain.ConversationAdmin, domain.ConversationMember} {
		c := conversationPolicyFixture(role, domain.ConversationMember)
		if got := domain.CanManageConversationRoles(c, "actor", "target"); got != (role == domain.ConversationOwner) {
			t.Fatalf("role=%s manage=%v", role, got)
		}
		if domain.CanAssignConversationRole(c, "actor", "target", domain.ConversationMember) {
			t.Fatal("no-op became a change")
		}
		c.Participants[1].HasAccess = false
		if domain.CanManageConversationRoles(c, "actor", "target") {
			t.Fatal("inaccessible target authorized")
		}
	}
}
