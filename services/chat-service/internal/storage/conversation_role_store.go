package storage

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

func (s *PGXOwnershipStore) mutateRoleOnce(ctx context.Context, input OwnershipMutation) (OwnershipMutationResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return OwnershipMutationResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, "SET TRANSACTION ISOLATION LEVEL SERIALIZABLE"); err != nil {
		return OwnershipMutationResult{}, err
	}
	if _, err = tx.Exec(ctx, `SELECT chat.lock_ownership_conversation($1,$2::uuid)`, input.Scope.Kind, input.Scope.ConversationID); err != nil {
		return OwnershipMutationResult{}, err
	}
	policy, enabled, err := readConversationRolePolicy(ctx, tx, input.Scope)
	if err != nil {
		return OwnershipMutationResult{}, err
	}
	if err = authorizeConversationRole(input, policy, enabled); err != nil {
		return OwnershipMutationResult{}, err
	}
	target := conversationRoleParticipant(policy, input.TargetUserID)
	if target.Role != input.Role {
		if err = assignOwnership(ctx, tx, input.Scope, input.TargetUserID, input.Role, "manual"); err != nil {
			return OwnershipMutationResult{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return OwnershipMutationResult{}, err
	}
	return OwnershipMutationResult{TargetUserID: input.TargetUserID, Role: input.Role}, nil
}

// Resolve the resource independently of the expected, server-selected workspace.
// The view contains the complete eligible roster, including account status and
// deletion checks; membership facts are loaded from the same transaction snapshot.
func readConversationRolePolicy(ctx context.Context, tx pgx.Tx, scope OwnershipScope) (domain.ConversationPolicyContext, bool, error) {
	policy := domain.ConversationPolicyContext{ID: scope.ConversationID}
	var enabled bool
	err := tx.QueryRow(ctx, `SELECT resource.workspace_id::text,w.status,r.enabled FROM (
 SELECT workspace_id FROM chat.dm_conversations WHERE $1='dm' AND id=$2::uuid AND type='group' AND status='active'
 UNION ALL
 SELECT workspace_id FROM chat.channels WHERE $1='channel' AND id=$2::uuid AND type='private' AND status='active'
 ) resource JOIN chat.workspaces w ON w.id=resource.workspace_id
 CROSS JOIN chat.ownership_rollout r WHERE r.singleton`, scope.Kind, scope.ConversationID).Scan(&policy.Workspace.ID, &policy.Workspace.Status, &enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return policy, false, domain.ErrNotFound
	}
	if err != nil {
		return policy, false, err
	}
	if !strings.EqualFold(policy.Workspace.ID, scope.WorkspaceID) || policy.Workspace.Status != domain.WorkspaceStatusActive {
		return policy, false, domain.ErrNotFound
	}
	policy.Active = true
	policy.Kind = domain.ConversationKindGroup
	if scope.Kind == "channel" {
		policy.Kind = domain.ConversationKindPrivateChannel
	}
	rows, err := tx.Query(ctx, `SELECT p.user_id::text,p.role,wm.role,wm.status
 FROM chat.active_ownership_participants p
 JOIN chat.workspace_members wm ON wm.workspace_id=p.workspace_id AND wm.user_id=p.user_id
 WHERE p.kind=$1 AND p.conversation_id=$2::uuid AND p.workspace_id=$3::uuid`, scope.Kind, scope.ConversationID, policy.Workspace.ID)
	if err != nil {
		return policy, false, err
	}
	defer rows.Close()
	for rows.Next() {
		member := domain.ConversationPolicyParticipant{ConversationID: policy.ID, HasAccess: true}
		member.Membership.WorkspaceID = policy.Workspace.ID
		if err = rows.Scan(&member.Membership.UserID, &member.Role, &member.Membership.Role, &member.Membership.Status); err != nil {
			return policy, false, err
		}
		policy.Participants = append(policy.Participants, member)
	}
	return policy, enabled, rows.Err()
}

func conversationRoleParticipant(policy domain.ConversationPolicyContext, userID string) domain.ConversationPolicyParticipant {
	for _, member := range policy.Participants {
		if member.Membership.UserID == userID {
			return member
		}
	}
	return domain.ConversationPolicyParticipant{}
}

func authorizeConversationRole(input OwnershipMutation, policy domain.ConversationPolicyContext, enabled bool) error {
	// Access checks precede permission checks to avoid enumerating private resources.
	if !domain.CanAddConversationMember(policy, input.Scope.ActorID) || !domain.CanAddConversationMember(policy, input.TargetUserID) {
		return domain.ErrNotFound
	}
	if !enabled || !domain.CanManageConversationRoles(policy, input.Scope.ActorID, input.TargetUserID) {
		return domain.ErrForbidden
	}
	target := conversationRoleParticipant(policy, input.TargetUserID)
	if target.Role == input.Role {
		return nil
	}
	if !domain.CanAssignConversationRole(policy, input.Scope.ActorID, input.TargetUserID, input.Role) {
		return domain.ErrOwnershipConflict
	}
	return nil
}
