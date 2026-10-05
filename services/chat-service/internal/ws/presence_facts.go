package ws

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// Presence facts kept in the database — a manual state, a call participation
// — change only inside a presence facts change (issue #798): ChangePresenceFacts
// wraps a whole database change in one; OpenPresenceFacts opens one inside a
// writer's own transaction, for a writer that only knows whom it changes once
// it holds its locks (the call store).
//
// A projection is committed in the shared store, the facts it is composed of
// live partly in PostgreSQL, and no transaction spans the two. The change
// makes the database write linearizable with the commit anyway:
//
//  1. Begin: each affected person's revision moves and a change is marked in
//     flight. Failing here, nothing is changed and the caller gets
//     domain.ErrPresenceFactsUnavailable.
//  2. The database change commits.
//  3. End: the revision moves again and the mark is cleared.
//
// A composer reads the revision before the database. If it read before Begin,
// its commit conflicts. If it read while the change was in flight, the mark is
// still there at its commit and it conflicts. If it read after End, the
// database already held the new fact. A writer that dies, or an End that
// fails, leaves the mark until its lease: compositions about those people are
// refused until then, and the first commit or change after it recovers the
// mark — removes it and moves the revision — so a composition read inside the
// change, which carries Begin's revision, conflicts too and is read again from
// the database as it ended up. Never a commit of a fact read before the change
// became visible.

const (
	// presenceFactsChangeLease bounds how long a change in flight holds
	// compositions back when nobody ends it. The store dates it on its own
	// clock, so it is this long whatever this process's clock says.
	presenceFactsChangeLease = 30 * time.Second
	// presenceFactsMutationTimeout bounds the database change itself, well
	// inside the lease: a change that could still commit after its mark lapsed
	// would be read as settled while it was not.
	presenceFactsMutationTimeout = 10 * time.Second
)

// The mark must outlive the change it covers: the store starts the lease when
// Begin runs, and the change can commit as late as the Begin reply's own
// bound plus the mutation's. Both are durations, so the invariant holds
// across clocks; a constant edited past it fails the build here (a negative
// constant does not convert to uint).
const _ = uint(presenceFactsChangeLease - directoryWriteTimeout - presenceFactsMutationTimeout)

// ChangePresenceFacts runs mutate — a database change to presence facts of
// userIDs — inside one presence facts change.
func (h *Hub) ChangePresenceFacts(
	ctx context.Context, workspaceID string, userIDs []string, mutate func(context.Context) error,
) error {
	mutateCtx, end, err := h.beginPresenceFacts(ctx, workspaceID, userIDs)
	if err != nil {
		return err
	}
	defer end()
	return mutate(mutateCtx)
}

// OpenPresenceFacts begins a presence facts change for userIDs inside a
// writer's transaction, which must still be uncommitted. The writer finishes
// under the returned context — it bounds the commit — and then calls the
// returned function, committed or not: it ends the change and recomposes each
// person here.
func (h *Hub) OpenPresenceFacts(
	ctx context.Context, workspaceID string, userIDs []string,
) (context.Context, func(), error) {
	mutateCtx, end, err := h.beginPresenceFacts(ctx, workspaceID, userIDs)
	if err != nil {
		return nil, nil, err
	}
	return mutateCtx, func() {
		end()
		for _, userID := range userIDs {
			h.RefreshPresence(workspaceID, userID)
		}
	}, nil
}

func (h *Hub) beginPresenceFacts(
	ctx context.Context, workspaceID string, userIDs []string,
) (context.Context, func(), error) {
	change := FactsChange{Token: uuid.NewString(), Lease: presenceFactsChangeLease}
	beginCtx, cancel := context.WithTimeout(ctx, directoryWriteTimeout)
	err := h.userPresence().BeginFactsChange(beginCtx, workspaceID, userIDs, change)
	cancel()
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", domain.ErrPresenceFactsUnavailable, err)
	}
	mutateCtx, cancelMutation := context.WithTimeout(ctx, presenceFactsMutationTimeout)
	return mutateCtx, func() {
		cancelMutation()
		endCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), directoryWriteTimeout)
		defer cancel()
		if err := h.userPresence().EndFactsChange(endCtx, workspaceID, userIDs, change); err != nil {
			// Safe, not merely logged: the mark holds compositions back until
			// its lease, and the owed and swept publications recompose after it.
			h.logger.WarnContext(ctx, "ws: presence facts change left in flight until its lease", "error", err)
		}
	}, nil
}
