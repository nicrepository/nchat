package storage

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// executeOwnershipTransfer runs inside the existing serializable, conversation-
// locked idempotency transaction. Replay has already been resolved, so only a
// fresh request must still have owner authority.
func executeOwnershipTransfer(ctx context.Context, tx pgx.Tx, input OwnershipMutation) (OwnershipMutationResult, error) {
	policy, enabled, err := readConversationRolePolicy(ctx, tx, input.Scope)
	if err != nil {
		return OwnershipMutationResult{}, err
	}
	if err = authorizeOwnershipTransfer(input, policy, enabled); err != nil {
		return OwnershipMutationResult{}, err
	}
	target := conversationRoleParticipant(policy, input.TargetUserID)
	if target.Role != domain.ConversationOwner {
		if err = assignOwnership(ctx, tx, input.Scope, input.TargetUserID, domain.ConversationOwner, "transfer"); err != nil {
			return OwnershipMutationResult{}, err
		}
	}
	if err = assignOwnership(ctx, tx, input.Scope, input.Scope.ActorID, input.Role, "transfer"); err != nil {
		return OwnershipMutationResult{}, err
	}
	return OwnershipMutationResult{TargetUserID: input.TargetUserID, Role: domain.ConversationOwner}, nil
}

func authorizeOwnershipTransfer(input OwnershipMutation, policy domain.ConversationPolicyContext, enabled bool) error {
	// Preserve private-resource non-enumerability before checking authority.
	if !domain.CanAddConversationMember(policy, input.Scope.ActorID) || !domain.CanAddConversationMember(policy, input.TargetUserID) {
		return domain.ErrNotFound
	}
	if !enabled || !domain.CanTransferConversationOwnership(policy, input.Scope.ActorID, input.TargetUserID) {
		return domain.ErrForbidden
	}
	return nil
}
