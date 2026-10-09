package storage

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/nicrepository/nchat/libs/go/platform/conversationownership"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// succeedOwnership runs under the conversation lock, before removing membership.
// Eligibility and UUID ordering stay in PostgreSQL, shared with the guards.
func succeedOwnership(ctx context.Context, tx pgx.Tx, scope OwnershipScope, departing string) error {
	err := conversationownership.Succeed(ctx, ownershipSuccessionSession(tx), conversationownership.Scope{
		WorkspaceID: scope.WorkspaceID, Kind: scope.Kind, ConversationID: scope.ConversationID,
	}, departing, scope.ActorID)
	if conversationownership.SQLState(err) == "P0953" {
		return domain.ErrOwnershipConflict
	}
	return err
}

func prepareOwnershipDeparture(ctx context.Context, tx pgx.Tx, input OwnershipMutation) error {
	switch input.Operation {
	case "leave":
		return succeedOwnership(ctx, tx, input.Scope, input.Scope.ActorID)
	case "remove":
		return succeedOwnership(ctx, tx, input.Scope, input.TargetUserID)
	default:
		return nil
	}
}
