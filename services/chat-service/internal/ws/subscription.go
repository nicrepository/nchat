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

// channelVisibilityChecker is the exact channel visibility check used by
// MessageService before reading or creating channel messages.
type channelVisibilityChecker interface {
	GetVisibleChannelByID(ctx context.Context, workspaceID, channelID, userID string) (domain.Channel, error)
}

// SubjectAuthorizer answers the same question as CanAccess for a whole list of
// people at once.
//
// It exists because presence produces exactly that question: a target's roster
// is a set of users the server itself asserted, and every one of them has to be
// re-checked before anybody sees it. Asking one at a time turns one snapshot
// into as many queries as there are people in the conversation.
//
// It is an optional capability, not a replacement: a caller that does not have
// one falls back to CanAccess per subject, which is why the interface is
// discovered rather than required. Implementations must return a subset of the
// users they were given and must not widen the rule CanAccess applies.
type SubjectAuthorizer interface {
	AuthorizeSubjects(
		ctx context.Context, workspaceID string, targetType TargetType, targetID string, userIDs []string,
	) ([]string, error)
}

// channelSubjectFilter and dmSubjectFilter are the batch reads serviceAuthorizer
// uses when its stores provide them. Declared here rather than added to the
// store interfaces so nothing that already satisfies those interfaces has to
// change.
type channelSubjectFilter interface {
	FilterUsersVisibleToChannel(ctx context.Context, workspaceID, channelID string, userIDs []string) ([]string, error)
}

type dmSubjectFilter interface {
	FilterUsersInConversation(ctx context.Context, workspaceID, conversationID string, userIDs []string) ([]string, error)
}

// ErrSubjectBatchUnsupported reports that this authorizer cannot answer for a
// list, so the caller should ask one at a time.
var ErrSubjectBatchUnsupported = errors.New("ws: batch subject authorization unavailable")

// AuthorizeSubjects implements SubjectAuthorizer against the same authority
// CanAccess uses: the canonical channel visibility function for a channel, and
// active conversation membership for a DM.
//
// Unknown target types are refused rather than passed through, exactly as in
// CanAccess — an unrecognised kind is not a reason to authorise anybody.
func (a *serviceAuthorizer) AuthorizeSubjects(
	ctx context.Context, workspaceID string, targetType TargetType, targetID string, userIDs []string,
) ([]string, error) {
	if len(userIDs) == 0 {
		return nil, nil
	}
	switch targetType {
	case TargetTypeChannel:
		channels, ok := a.channels.(channelSubjectFilter)
		if !ok {
			return nil, ErrSubjectBatchUnsupported
		}
		return channels.FilterUsersVisibleToChannel(ctx, workspaceID, targetID, userIDs)

	case TargetTypeDM:
		dms, ok := any(a.dms).(dmSubjectFilter)
		if !ok {
			return nil, ErrSubjectBatchUnsupported
		}
		return dms.FilterUsersInConversation(ctx, workspaceID, targetID, userIDs)

	default:
		// Fail secure, like CanAccess.
		return nil, nil
	}
}

// serviceAuthorizer is the production SubscriptionAuthorizer.
//
// Channel access is resolved via GetVisibleChannelByID, the same storage check
// used by MessageService. It enforces active workspace membership and, for
// private channels, channel membership in one non-enumerating SQL query.
//
// DM access is resolved via DMStore.GetVisibleConversationByID, which enforces
// active workspace membership and active DM membership in a single SQL query.
// The method returns ErrNotFound for inaccessible conversations (non-enumerating).
type serviceAuthorizer struct {
	channels channelVisibilityChecker
	dms      storage.DMStore
}

// NewServiceAuthorizer returns a SubscriptionAuthorizer backed by the provided
// channel checker and DM store.
//
// github.com/coder/websocket is imported and used by ServeWS (see handler.go).
// coder/websocket (formerly nhooyr.io/websocket) was chosen because it is
// actively maintained, pure Go (no CGO), context-native, and supports WASM.
func NewServiceAuthorizer(channels channelVisibilityChecker, dms storage.DMStore) SubscriptionAuthorizer {
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
		_, err := a.channels.GetVisibleChannelByID(ctx, workspaceID, targetID, userID)
		if errors.Is(err, domain.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return true, nil

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
