package ws

import (
	"context"
	"errors"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// SubscriptionAuthorizer decides whether a user may subscribe to a target.
// Implementations must be safe for concurrent use from multiple goroutines.
type SubscriptionAuthorizer interface {
	// CanAccess returns true when userID is permitted to receive events for
	// targetType/targetID in workspaceID.
	//
	// A false result with a nil error means access is denied (non-enumerating).
	// The caller must not distinguish "does not exist" from "access denied".
	CanAccess(ctx context.Context, userID, workspaceID string, targetType TargetType, targetID string) (bool, error)
}

// NopAuthorizer is a SubscriptionAuthorizer that denies all access.
// Use when no database is available; ServeWS returns 503 before any client
// can register in that case, so this authorizer is never actually invoked.
type NopAuthorizer struct{}

func (NopAuthorizer) CanAccess(_ context.Context, _, _ string, _ TargetType, _ string) (bool, error) {
	return false, nil
}

// channelReadChecker is the subset of PermissionService used by serviceAuthorizer.
// Using an interface here keeps the ws package decoupled from the concrete service type.
type channelReadChecker interface {
	CanRead(ctx context.Context, workspaceID, channelID, userID string) (bool, error)
}

// serviceAuthorizer is the production SubscriptionAuthorizer.
//
// Channel access is resolved via channelReadChecker (backed by PermissionService),
// which enforces active workspace membership and, for private channels, active
// channel membership. SQL visibility is the authoritative source of truth.
//
// DM access is resolved via DMStore.GetVisibleConversationByID, which enforces
// active workspace membership and active DM membership in a single SQL query.
// The method returns ErrNotFound for inaccessible conversations (non-enumerating).
type serviceAuthorizer struct {
	channels channelReadChecker
	dms      storage.DMStore
}

// NewServiceAuthorizer returns a SubscriptionAuthorizer backed by the provided
// channel checker and DM store.
//
// github.com/coder/websocket is imported and used by ServeWS (see handler.go).
// coder/websocket (formerly nhooyr.io/websocket) was chosen because it is
// actively maintained, pure Go (no CGO), context-native, and supports WASM.
func NewServiceAuthorizer(channels channelReadChecker, dms storage.DMStore) SubscriptionAuthorizer {
	return &serviceAuthorizer{channels: channels, dms: dms}
}

// CanAccess implements SubscriptionAuthorizer.
//
// Security properties:
//   - workspaceID from the server auth context, never client-provided.
//   - Non-enumerating: both "not found" and "access denied" map to (false, nil).
//   - Unknown target types fail secure (denied).
//   - Stale channel_members / dm_members are rejected because workspace membership
//     is re-verified on every call via the underlying SQL queries.
func (a *serviceAuthorizer) CanAccess(ctx context.Context, userID, workspaceID string, targetType TargetType, targetID string) (bool, error) {
	switch targetType {
	case TargetTypeChannel:
		return a.channels.CanRead(ctx, workspaceID, targetID, userID)

	case TargetTypeDM:
		_, err := a.dms.GetVisibleConversationByID(ctx, workspaceID, targetID, userID)
		if errors.Is(err, domain.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return true, nil

	default:
		// Unknown target type: fail secure.
		return false, nil
	}
}
