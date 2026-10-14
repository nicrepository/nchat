package domain

type ConversationKind string

const (
	ConversationKindGroup          ConversationKind = "group"
	ConversationKindPrivateChannel ConversationKind = "private_channel"
)

// ConversationPolicyParticipant is server-resolved membership, not a request
// DTO. HasAccess includes conversation membership and account eligibility.
// Role reuses ConversationRole; no workspace or legacy role is inferred.
type ConversationPolicyParticipant struct {
	ConversationID string
	Membership     WorkspaceMember
	Role           ConversationRole
	HasAccess      bool
}

// ConversationPolicyContext must contain the complete roster from one coherent
// server-side snapshot, never a paginated details preview. Writers must repeat
// authorization under transaction locks. These pure decisions neither persist
// roles nor activate or replace legacy ownership/succession endpoints.
type ConversationPolicyContext struct {
	Workspace    Workspace
	ID           string
	Kind         ConversationKind
	Active       bool
	Participants []ConversationPolicyParticipant
}

func (c ConversationPolicyContext) valid() bool {
	return c.ID != "" && c.Workspace.ID != "" && c.Active &&
		c.Workspace.Status == WorkspaceStatusActive &&
		(c.Kind == ConversationKindGroup || c.Kind == ConversationKindPrivateChannel)
}

func (c ConversationPolicyContext) accessible(p ConversationPolicyParticipant) bool {
	return p.HasAccess && p.Role.Valid() && p.ConversationID == c.ID &&
		p.Membership.UserID != "" && p.Membership.WorkspaceID == c.Workspace.ID &&
		eligibleConversationWorkspaceMember(p.Membership)
}

func eligibleConversationWorkspaceMember(wm WorkspaceMember) bool {
	if wm.Status != MemberStatusActive {
		return false
	}
	switch wm.Role {
	case WorkspaceRoleOwner, WorkspaceRoleAdmin, WorkspaceRoleModerator, WorkspaceRoleMember, WorkspaceRoleGuest:
		return true
	default:
		return false
	}
}

func (c ConversationPolicyContext) participant(userID string) (ConversationPolicyParticipant, bool) {
	if !c.valid() || userID == "" {
		return ConversationPolicyParticipant{}, false
	}
	var found ConversationPolicyParticipant
	seen := make(map[string]bool, len(c.Participants))
	for _, p := range c.Participants {
		if seen[p.Membership.UserID] {
			return ConversationPolicyParticipant{}, false
		}
		seen[p.Membership.UserID] = true
		if p.HasAccess && !c.accessible(p) {
			return ConversationPolicyParticipant{}, false
		}
		if p.Membership.UserID == userID {
			found = p
		}
	}
	return found, c.accessible(found)
}

func (c ConversationPolicyContext) remaining(userID string) (members, owners int) {
	for _, p := range c.Participants {
		if p.Membership.UserID == userID || !c.accessible(p) {
			continue
		}
		members++
		if p.Role == ConversationOwner {
			owners++
		}
	}
	return members, owners
}

// CanManageConversationRoles authorizes both changes and explicit no-ops.
func CanManageConversationRoles(c ConversationPolicyContext, actorID, targetID string) bool {
	actor, actorOK := c.participant(actorID)
	_, targetOK := c.participant(targetID)
	return actorOK && targetOK && actor.Role == ConversationOwner
}

// CanAssignConversationRole checks the intended role, including self-demotion.
// A no-op is not a role change. Guests can be promoted manually; this does not
// make them eligible for the separate legacy automatic succession policy.
func CanAssignConversationRole(c ConversationPolicyContext, actorID, targetID string, role ConversationRole) bool {
	target, _ := c.participant(targetID)
	if !CanManageConversationRoles(c, actorID, targetID) || !role.Valid() || target.Role == role {
		return false
	}
	if target.Role == ConversationOwner {
		_, owners := c.remaining(targetID)
		return owners > 0
	}
	return true
}

func CanRemoveConversationMember(c ConversationPolicyContext, actorID, targetID string) bool {
	actor, actorOK := c.participant(actorID)
	target, targetOK := c.participant(targetID)
	if !actorOK || !targetOK || actorID == targetID || target.Role == ConversationOwner {
		return false
	}
	return actor.Role == ConversationOwner || (actor.Role == ConversationAdmin && target.Role == ConversationMember)
}

// CanAddConversationMember preserves #705: adding follows access for all local
// roles, including explicitly admitted guests. Target eligibility and batch
// validation remain the responsibility of the existing server-side add path.
func CanAddConversationMember(c ConversationPolicyContext, actorID string) bool {
	_, ok := c.participant(actorID)
	return ok
}

// CanTransferConversationOwnership only checks eligibility; it performs no
// transfer and cannot substitute for atomic writer validation.
func CanTransferConversationOwnership(c ConversationPolicyContext, actorID, targetID string) bool {
	actor, actorOK := c.participant(actorID)
	_, targetOK := c.participant(targetID)
	return actorOK && targetOK && actor.Role == ConversationOwner && actorID != targetID
}

// CanLeaveConversation does not select or promote a successor.
func CanLeaveConversation(c ConversationPolicyContext, actorID string) bool {
	if _, ok := c.participant(actorID); !ok {
		return false
	}
	members, owners := c.remaining(actorID)
	return members == 0 || owners > 0
}

// CanEditConversationMetadata requires the server's permission for the specific
// metadata operation. Local ownership alone does not widen that permission;
// operationAllowed must never come from a client-provided capability.
func CanEditConversationMetadata(c ConversationPolicyContext, actorID string, operationAllowed bool) bool {
	_, ok := c.participant(actorID)
	return ok && operationAllowed
}
