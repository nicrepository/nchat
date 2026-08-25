package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// Group rename and self-leave (issue #527).
//
// Both are group-DM mutations, both are authorized by *participation* — a group
// has no owner, admin or moderator, because chat.dm_members.role is closed by
// CHECK to the single value 'member' — and both persist a system message in the
// same transaction as the change they describe.
//
// The lock protocol extends the one PGXDMStore.AddGroupParticipants documents,
// and the order is the same everywhere so no cycle is reachable between a
// rename, a leave and an add running at once:
//
//  1. the conversation row;
//  2. the actor's own chat.dm_members row;
//  3. the actor's own chat.workspace_members row;
//  4. the write.
//
// Step 3 is what the security review found missing. Both operations *read*
// chat.workspace_members as a precondition — a dm_members row outlives the
// workspace membership that justified it — but neither held it, so a revocation
// committing mid-transaction was observed too late and the write went through on
// authority that no longer existed. It is held FOR SHARE for the same reason
// lockActorChannelManagementSQL holds the channel path's: suspending, removing
// or demoting a membership is an UPDATE of that row, which takes FOR NO KEY
// UPDATE and conflicts with FOR SHARE, so the two serialise in both directions
// while two participants acting at once still do not block each other.
//
// Step 2's mode depends on the operation, and that is the second thing the
// review found: a transaction that is going to UPDATE a row must not take a
// shared lock on it first. Two self-leaves doing that both hold FOR SHARE and
// both then need the exclusive lock the other is holding — a textbook
// lock-upgrade deadlock, which PostgreSQL resolves by aborting one of them with
// 40P01 rather than by refusing it for a domain reason. Leave therefore takes
// the actor's membership FOR UPDATE from the start; rename, which does not touch
// that row, keeps FOR SHARE. Step 1 follows the same rule for the same reason:
// the rename UPDATEs the conversation row, so it takes it FOR UPDATE, while the
// leave does not and takes it FOR SHARE.

// RenameGroupInput is the whole input of a group rename. The actor is an
// identity and never a decision; the title has already been normalised by the
// service and is re-checked by the database's own length constraint.
type RenameGroupInput struct {
	WorkspaceID    string
	ConversationID string
	CallerID       string
	Title          string
}

// RenameGroupResult carries what the caller needs after a successful commit:
// the conversation as persisted, and the system message the same transaction
// wrote, so the realtime fan-out can announce both without a second read.
type RenameGroupResult struct {
	Conversation domain.DMConversation
	Event        domain.Message
}

// LeaveConversationResult reports the system message a departure produced.
type LeaveConversationResult struct {
	Event domain.Message
}

// lockGroupConversationSQL pins one active group conversation of one workspace.
// `%s` is the locking clause, and only ever one of the two constants below.
//
// The `type = 'group'` predicate is structural, not a check: a 1:1
// conversation's id matches no row here, which is why a direct conversation can
// be neither renamed nor left even by a caller who reached storage directly.
const lockGroupConversationSQL = `
	SELECT dc.id::text, COALESCE(dc.title, '')
	FROM chat.dm_conversations dc
	JOIN chat.workspaces w ON w.id = dc.workspace_id AND w.status = 'active'
	WHERE dc.id = $1::uuid
	  AND dc.workspace_id = $2::uuid
	  AND dc.status = 'active'
	  AND dc.type = 'group'
	`

// shareConversation is for an operation that does not write the conversation
// row: archiving it is an UPDATE that conflicts with FOR SHARE, so an archival
// in flight is still serialised against the operation, while two of them do not
// block each other.
const shareConversation = `FOR SHARE OF dc`

// updateConversation is for the rename, which UPDATEs this very row. Taking the
// exclusive lock up front is what keeps two concurrent renames of one group from
// both holding FOR SHARE and then each waiting for the other to release it; the
// second one waits here and renames afterwards instead of dying on 40P01.
const updateConversation = `FOR UPDATE OF dc`

// requireGroupParticipantSQL re-derives the actor's own participation and holds
// the row, so a removal in flight is serialised against this write instead of
// being observed too late. `%s` is the locking clause, one of the two constants
// below.
//
// A group's authority is participation: there is no role column to consult —
// chat.dm_members.role is CHECK-closed to 'member' — and a workspace admin who
// is not in the group has no standing in it. The workspace half of the
// precondition is the next statement's, one row per statement, so the order in
// which the two are locked is stated by the code rather than left to a join.
const requireGroupParticipantSQL = `
	SELECT true
	FROM chat.dm_members dm
	WHERE dm.conversation_id = $1::uuid
	  AND dm.user_id = $2::uuid
	  AND dm.status = 'active'
	`

// shareParticipation is for an operation that leaves the membership row alone.
// Removing the actor from the group is an UPDATE of it and conflicts with FOR
// SHARE, so a removal in flight is still serialised against the operation.
const shareParticipation = `FOR SHARE OF dm`

// updateParticipation is for the self-leave, which marks this very row left.
// Anything weaker would have to be upgraded at the UPDATE, and two self-leaves
// upgrading at once deadlock. Under READ COMMITTED the loser re-evaluates the
// predicate after the winner commits, finds the row no longer active, and gets
// the same ErrForbidden a late second leave has always produced.
const updateParticipation = `FOR UPDATE OF dm`

// requireActorWorkspaceMembershipSQL holds the actor's workspace membership for
// the rest of the transaction.
//
// A chat.dm_members row is not evidence of anything on its own: it outlives the
// workspace membership that justified it, so a participant whose workspace
// access was suspended or removed is not a participant this operation may act
// for. Holding the row is what makes a revocation commit either strictly before
// this statement — in which case it returns nothing and the operation is refused
// — or strictly after the transaction that holds it.
const requireActorWorkspaceMembershipSQL = `
	SELECT true
	FROM chat.workspace_members wm
	JOIN chat.workspaces w ON w.id = wm.workspace_id AND w.status = 'active'
	WHERE wm.workspace_id = $1::uuid
	  AND wm.user_id = $2::uuid
	  AND wm.status = 'active'
	FOR SHARE OF wm`

// RenameGroupConversation renames a group and records the event, atomically.
//
// A 1:1 conversation is unreachable here by construction: the lock statement
// requires type = 'group', so a direct conversation's ID matches nothing and
// comes back as ErrNotFound. That is the structural half of "a DM can never be
// renamed" — the service refuses it too, but this is the half that holds for a
// caller who bypassed the service.
func (s *PGXDMStore) RenameGroupConversation(ctx context.Context, input RenameGroupInput) (RenameGroupResult, error) {
	if input.CallerID == "" {
		return RenameGroupResult{}, domain.ErrForbidden
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return RenameGroupResult{}, fmt.Errorf("begin rename group: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	previousTitle, err := lockGroupForActor(ctx, tx, input.ConversationID, input.WorkspaceID, input.CallerID, renameLocks)
	if err != nil {
		return RenameGroupResult{}, err
	}

	var conversation domain.DMConversation
	err = tx.QueryRow(ctx, `
		UPDATE chat.dm_conversations
		SET title = $3, updated_at = now()
		WHERE id = $1::uuid AND workspace_id = $2::uuid AND status = 'active' AND type = 'group'
		RETURNING id::text, workspace_id::text, type, COALESCE(title, ''), status,
		          COALESCE(created_by::text, ''), created_at, updated_at`,
		input.ConversationID, input.WorkspaceID, input.Title,
	).Scan(
		&conversation.ID, &conversation.WorkspaceID, (*string)(&conversation.Type),
		&conversation.Title, (*string)(&conversation.Status), &conversation.CreatedBy,
		&conversation.CreatedAt, &conversation.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RenameGroupResult{}, domain.ErrNotFound
		}
		return RenameGroupResult{}, fmt.Errorf("rename group conversation: %w", err)
	}

	event, err := InsertConversationEvent(ctx, tx, ConversationEventInput{
		WorkspaceID:      input.WorkspaceID,
		DMConversationID: conversation.ID,
		ActorID:          input.CallerID,
		Event:            domain.ConversationEventRenamed,
		Payload:          domain.ConversationEventPayload{OldName: previousTitle, NewName: conversation.Title},
	})
	if err != nil {
		return RenameGroupResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return RenameGroupResult{}, fmt.Errorf("commit rename group: %w", err)
	}
	committed = true
	return RenameGroupResult{Conversation: conversation, Event: event}, nil
}

// LeaveGroupConversation removes the actor's own participation and records it.
//
// Self-leave only: the actor is the row that is updated, so there is no target
// user parameter for a caller to aim at somebody else. That is deliberate and is
// the difference from the administrative removal paths — nothing here can be
// turned into "remove that person".
//
// The membership is marked left rather than deleted, matching the schema's
// status/left_at pair, so the history of who was in the conversation survives.
func (s *PGXDMStore) LeaveGroupConversation(ctx context.Context, workspaceID, conversationID, callerID string) (LeaveConversationResult, error) {
	if callerID == "" {
		return LeaveConversationResult{}, domain.ErrForbidden
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return LeaveConversationResult{}, fmt.Errorf("begin leave group: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	if _, err := lockGroupForActor(ctx, tx, conversationID, workspaceID, callerID, leaveLocks); err != nil {
		return LeaveConversationResult{}, err
	}

	// The event is written *before* the membership is dropped, inside the same
	// transaction. Order matters only for legibility — both are visible together
	// or not at all — but writing it first keeps the row's authorship reading as
	// "someone who was in the conversation said this".
	event, err := InsertConversationEvent(ctx, tx, ConversationEventInput{
		WorkspaceID:      workspaceID,
		DMConversationID: conversationID,
		ActorID:          callerID,
		Event:            domain.ConversationEventMemberLeft,
	})
	if err != nil {
		return LeaveConversationResult{}, err
	}

	tag, err := tx.Exec(ctx, `
		UPDATE chat.dm_members
		SET status = 'left', left_at = now()
		WHERE conversation_id = $1::uuid AND user_id = $2::uuid AND status = 'active'`,
		conversationID, callerID,
	)
	if err != nil {
		return LeaveConversationResult{}, fmt.Errorf("leave group conversation: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Unreachable in practice: the participation check above holds this very
		// row FOR UPDATE, so nothing can have changed it since. Kept because the
		// alternative to an explicit answer here is a commit that claims a
		// departure the database did not record.
		return LeaveConversationResult{}, domain.ErrForbidden
	}

	if err := tx.Commit(ctx); err != nil {
		return LeaveConversationResult{}, fmt.Errorf("commit leave group: %w", err)
	}
	committed = true
	return LeaveConversationResult{Event: event}, nil
}

// groupLockModes says how one operation holds the two rows whose mode depends on
// what it is about to write. Both fields are one of the locking-clause constants
// above; nothing else is ever concatenated into these statements.
type groupLockModes struct {
	conversation  string
	participation string
}

// renameLocks: the rename writes the conversation row and leaves the membership
// alone.
var renameLocks = groupLockModes{conversation: updateConversation, participation: shareParticipation}

// leaveLocks: the mirror image — the membership row is the one being written.
var leaveLocks = groupLockModes{conversation: shareConversation, participation: updateParticipation}

// lockGroupForActor takes the three locks every group mutation shares, in the
// canonical order, and returns the conversation's current title.
//
// Order: the conversation row, then the actor's participation, then the actor's
// workspace membership. It is the same order the channel paths use on their own
// two rows (channel, then workspace membership) and the reverse of nothing —
// chat-service's workspace-membership writers touch chat.workspaces and
// chat.channels after the membership and never a chat.dm_* row, so no cycle is
// reachable.
//
// A conversation that is not an active group of this workspace is ErrNotFound;
// an actor who does not participate, or whose workspace membership is gone, is
// ErrForbidden. The two stay distinct because a participant is already entitled
// to know the group exists, and the two ErrForbidden cases are deliberately
// indistinguishable from each other.
func lockGroupForActor(ctx context.Context, tx pgx.Tx, conversationID, workspaceID, callerID string, modes groupLockModes) (string, error) {
	var lockedID, title string
	err := tx.QueryRow(ctx, lockGroupConversationSQL+modes.conversation, conversationID, workspaceID).Scan(&lockedID, &title)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", domain.ErrNotFound
		}
		return "", fmt.Errorf("lock group conversation: %w", err)
	}

	var participates bool
	err = tx.QueryRow(ctx, requireGroupParticipantSQL+modes.participation, conversationID, callerID).Scan(&participates)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", domain.ErrForbidden
		}
		return "", fmt.Errorf("lock actor dm membership: %w", err)
	}

	var authorized bool
	err = tx.QueryRow(ctx, requireActorWorkspaceMembershipSQL, workspaceID, callerID).Scan(&authorized)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", domain.ErrForbidden
		}
		return "", fmt.Errorf("lock actor workspace membership: %w", err)
	}
	return title, nil
}
