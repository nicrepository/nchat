package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/nicrepository/nchat/libs/go/platform/channelmembership"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// Channel self-leave (issue #527).
//
// Deliberately its own path rather than a reuse of MemberService's
// administrative removal. Those two operations look alike and are not: removing
// somebody else takes a workspace management role and names a target user;
// leaving takes no role at all and names nobody, because the row it removes is
// the actor's own. Routing self-leave through the administrative endpoint would
// have meant an endpoint that accepts a user ID from a caller who has no
// authority over anyone — exactly the shape a privilege escalation wears.
//
// The general channel is refused in SQL. Membership there is owned by the
// workspace sync, so leaving it is not a thing a person may do, and the refusal
// must not depend on the UI having hidden the action.

// LeaveChannelSelf removes the actor's own channel membership and records the
// departure as a system message, in one transaction.
//
// Lock order is the canonical one channelmembership.LockChannelSQL documents and
// every membership mutation obeys — the channel row first, then the membership
// being changed — so this serialises against an add or a rename in flight
// without a cycle being reachable.
//
// A public channel is readable without a chat.channel_members row, so "leaving"
// one that was never joined removes nothing. That is reported as ErrNotFound
// rather than a silent success: the client asked to change something that is not
// there, and pretending otherwise would put a "you left" event in the history of
// a conversation nobody left.
func (s *PGXChannelStore) LeaveChannelSelf(ctx context.Context, workspaceID, channelID, callerID string) (LeaveConversationResult, error) {
	if callerID == "" {
		return LeaveConversationResult{}, domain.ErrForbidden
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return LeaveConversationResult{}, fmt.Errorf("begin leave channel: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	if err := lockLeavableChannel(ctx, tx, workspaceID, channelID); err != nil {
		return LeaveConversationResult{}, err
	}

	// Written before the membership disappears and inside the same transaction:
	// both land together or neither does, so there is never an event claiming a
	// departure that did not happen, nor a departure with nothing in the history.
	event, err := InsertConversationEvent(ctx, tx, ConversationEventInput{
		WorkspaceID: workspaceID,
		ChannelID:   channelID,
		ActorID:     callerID,
		Event:       domain.ConversationEventMemberLeft,
	})
	if err != nil {
		return LeaveConversationResult{}, err
	}

	tag, err := tx.Exec(ctx, `
		DELETE FROM chat.channel_members cm
		USING chat.channels c
		WHERE cm.channel_id = $1::uuid
		  AND cm.user_id = $2::uuid
		  AND c.id = cm.channel_id
		  AND c.workspace_id = $3::uuid`,
		channelID, callerID, workspaceID,
	)
	if err != nil {
		return LeaveConversationResult{}, fmt.Errorf("leave channel: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return LeaveConversationResult{}, domain.ErrNotFound
	}

	if err := tx.Commit(ctx); err != nil {
		return LeaveConversationResult{}, fmt.Errorf("commit leave channel: %w", err)
	}
	committed = true
	return LeaveConversationResult{Event: event}, nil
}

// lockLeavableChannel pins the channel and refuses the ones that may not be
// left, before anything is written.
//
// Two conditions, and both are structural rather than about the actor: the
// channel must be an active channel of this workspace, and it must not be the
// general one. The identity test is chat.channels.is_general, never the display
// name — a channel called "Geral" that is not the general channel is ordinary,
// and the general channel renamed to anything else is still structural.
func lockLeavableChannel(ctx context.Context, tx pgx.Tx, workspaceID, channelID string) error {
	var lockedID string
	if err := tx.QueryRow(ctx, channelmembership.LockChannelSQL, channelID).Scan(&lockedID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		return fmt.Errorf("lock channel for leave: %w", err)
	}

	var isGeneral bool
	err := tx.QueryRow(ctx, `
		SELECT c.is_general
		FROM chat.channels c
		JOIN chat.workspaces w ON w.id = c.workspace_id AND w.status = 'active'
		WHERE c.id = $1::uuid AND c.workspace_id = $2::uuid AND c.status = 'active'`,
		channelID, workspaceID,
	).Scan(&isGeneral)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		return fmt.Errorf("read channel for leave: %w", err)
	}
	if isGeneral {
		return domain.ErrGeneralChannelImmutable
	}
	return nil
}
