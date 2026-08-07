package storage

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/google/uuid"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
)

// The attachment fence: mutual exclusion between rendering an attachment and
// invalidating it (RF-31, SR-001).
//
// # The race it closes
//
// The preview claim requires a clean attachment, but a claim is a moment and a
// render is an interval. Between the two, a scan verdict can land:
//
//	worker: claim (clean)  ...  open, decrypt  ...  parser  ...  publish
//	scanner:                        reject ✓
//
// Re-reading the status just before the parser does not fix this — it only
// narrows the window to the microseconds between the read and the call. The
// publish is already fenced, so nothing *reaches a user*, but by then the
// rejected bytes have been through PDFium or an image decoder, and keeping
// hostile content out of those parsers is the entire point of scan-before-
// render.
//
// # Why an advisory lock
//
// Mutual exclusion has to hold across replicas, so it cannot be a mutex. It has
// to hold for the length of a render — tens of seconds — so it must not be a
// transaction, which would pin the xmin horizon and stall vacuum for as long as
// the parser runs.
//
// A PostgreSQL *session* advisory lock is neither: it lives on a connection,
// costs nothing to hold, is visible to every replica, and is released by the
// server the moment the connection goes away — so a worker that dies mid-render
// cannot wedge an attachment. This is the same primitive upload admission
// already uses, reached through the same LockConn seam.
type attachmentFenceNamespace uint32

// fenceNamespace separates these keys from every other advisory lock in the
// database. Upload admission derives its keys from its own namespace, and two
// unrelated features colliding on a 64-bit key would serialise strangers.
const fenceNamespace attachmentFenceNamespace = 0x6e636631 // "ncf1"

// fenceReleaseTimeout bounds giving the fence back. It is short: the statement
// is one function call on a connection this process already holds, and a
// release that cannot finish quickly means the session is unhealthy, which is
// exactly the case that must end in a discard rather than a wait.
const fenceReleaseTimeout = 5 * time.Second

// TransactionFence runs one short statement under the same lock the renderer
// holds, on a single connection.
//
// It is the invalidation half of the fence and it is deliberately not the same
// method as Acquire: an invalidation is one indexed UPDATE, so it wants a
// transaction-scoped lock that the commit releases, while a render is tens of
// seconds long and wants a session lock with no transaction at all. They meet
// in PostgreSQL's lock space, which does not care which of the two took the
// lock — the exclusion is the same either way.
type TransactionFence interface {
	WithinTransaction(
		ctx context.Context,
		key int64,
		run func(TransactionalQuerier) (service.AttachmentLifecycleState, error),
	) (service.AttachmentLifecycleState, error)
}

// AttachmentFencing is both halves of the fence: the session lock a render
// holds and the transaction lock an invalidation takes. One implementation
// provides both, because they only exclude each other if they are the same
// lock in the same key space.
type AttachmentFencing interface {
	service.AttachmentFence
	TransactionFence
}

// AttachmentFence hands out exclusive access to one attachment's processing.
//
// It is deliberately not a general-purpose distributed lock: the only key it
// can express is an attachment id, so no caller can invent a lock name and no
// client value ever reaches the SQL.
//
// The handle type is the service package's, so one implementation satisfies
// both the preview job's fence and this package's own — the two must be the
// same lock or the exclusion means nothing.
type AttachmentFence = service.AttachmentFence

// PGXAttachmentFence implements both halves of the fence on PostgreSQL advisory
// locks: session-scoped for a render, transaction-scoped for an invalidation.
type PGXAttachmentFence struct {
	pool LockConnPool
	// txPool runs the invalidation side. It is the ordinary statement pool
	// rather than the lock pool because that side needs exactly one connection
	// for exactly one transaction — asking for a second while holding a lock is
	// how a pool deadlocks under concurrent invalidations.
	txPool TransactionPool
	logger *slog.Logger
}

// TransactionPool is the sliver of pgxpool the invalidation side needs.
type TransactionPool interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// TransactionPoolFrom exposes a pool as a source of transactions.
//
// It reports false for anything that is not the pgx implementation, for the
// same reason LockConnPoolFrom does: without a real transaction there is no
// lock to take, and a fence that cannot lock is not a fence.
func TransactionPoolFrom(pool Pool) (TransactionPool, bool) {
	txPool, ok := pool.(TransactionPool)
	return txPool, ok
}

// NewPGXAttachmentFence builds the fence from a pool that can lend connections.
//
// It needs LockConnPool rather than Pool for the same reason admission control
// does: a session lock lives on one connection for the length of the work, and
// the per-statement interface cannot lend one out.
func NewPGXAttachmentFence(
	pool LockConnPool, txPool TransactionPool, logger *slog.Logger,
) *PGXAttachmentFence {
	if logger == nil {
		logger = slog.Default()
	}
	return &PGXAttachmentFence{pool: pool, txPool: txPool, logger: logger}
}

// WithinTransaction takes the transaction-scoped lock and runs the statement
// under it.
//
// One connection does both, so an invalidation never waits for a second one
// while holding a lock — the shape that deadlocks a pool as soon as a few
// invalidations race. The commit releases the lock; so does the rollback, so
// no failure path can leave an attachment fenced.
func (f *PGXAttachmentFence) WithinTransaction(
	ctx context.Context,
	key int64,
	run func(TransactionalQuerier) (service.AttachmentLifecycleState, error),
) (service.AttachmentLifecycleState, error) {
	if f == nil || f.txPool == nil {
		return service.AttachmentLifecycleState{}, domain.ErrDependenciesUnavailable
	}
	tx, err := f.txPool.Begin(ctx)
	if err != nil {
		return service.AttachmentLifecycleState{}, fmt.Errorf("begin fenced transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Blocking on purpose: a verdict that arrives during a render must queue
	// behind it rather than commit underneath it.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, key); err != nil {
		return service.AttachmentLifecycleState{}, fmt.Errorf("take attachment fence: %w", err)
	}
	state, err := run(tx)
	if err != nil {
		return service.AttachmentLifecycleState{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return service.AttachmentLifecycleState{}, fmt.Errorf("commit fenced transaction: %w", err)
	}
	return state, nil
}

// Acquire takes the fence for one attachment, waiting if another holder has it.
//
// Waiting is the point. A scan verdict that arrives mid-render must queue
// behind the render rather than skip past it: that is what makes the two
// orderings the only two possible outcomes, instead of leaving a third in which
// both proceed.
func (f *PGXAttachmentFence) Acquire(
	ctx context.Context, attachmentID string,
) (service.FenceHandle, error) {
	if f == nil || f.pool == nil {
		return nil, domain.ErrDependenciesUnavailable
	}
	key, err := attachmentFenceKey(attachmentID)
	if err != nil {
		return nil, err
	}
	conn, err := f.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire fence connection: %w", err)
	}
	if err := conn.Lock(ctx, key); err != nil {
		// The lock was never taken, so the session is provably lock-free and
		// the connection can go back to the pool.
		conn.Release()
		return nil, err
	}
	return &pgxFenceHandle{conn: conn, key: key, logger: f.logger}, nil
}

// pgxFenceHandle owns one held lock and the connection it lives on.
type pgxFenceHandle struct {
	conn     LockConn
	key      int64
	logger   *slog.Logger
	released bool
}

// Release gives the fence back.
//
// The connection's destiny follows what can be *proven*: a confirmed unlock
// means the session holds nothing and may be reused; anything else — an error,
// or an unlock that reports the lock was not held — means this process can no
// longer describe the session's state, so the connection is discarded and
// PostgreSQL drops whatever it still holds when the backend goes away. That is
// the same rule upload admission follows, and it is what stops a suspect
// session from silently keeping an attachment fenced forever.
//
// It runs on a context detached from the caller's on purpose: the usual reason
// a render ends is that its deadline expired, and releasing on an expired
// context would fail every time and discard a healthy connection.
func (h *pgxFenceHandle) Release(ctx context.Context) {
	if h == nil || h.released {
		return
	}
	h.released = true

	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fenceReleaseTimeout)
	defer cancel()

	released, err := h.conn.Unlock(releaseCtx, h.key)
	if err != nil || !released {
		h.logger.LogAttrs(releaseCtx, slog.LevelWarn, "attachment fence released by discarding its connection",
			slog.Bool("unlock_confirmed", released),
		)
		if discardErr := h.conn.Discard(releaseCtx); discardErr != nil {
			h.logger.LogAttrs(releaseCtx, slog.LevelError, "attachment fence connection could not be closed",
				slog.String("error_type", "fence_discard_failed"),
			)
		}
		return
	}
	h.conn.Release()
}

// attachmentFenceKey derives the advisory-lock key from an attachment id.
//
// The id is parsed as a UUID first, so nothing that is not one can reach the
// derivation, and the key is a parameter of the query rather than text spliced
// into it. Two attachments can collide in 64 bits and would then serialise
// against each other — slower, never less safe: a collision can only make an
// unrelated verdict wait, it can never let one through.
func attachmentFenceKey(attachmentID string) (int64, error) {
	parsed, err := uuid.Parse(attachmentID)
	if err != nil {
		return 0, fmt.Errorf("%w: invalid attachment id", domain.ErrInvalidInput)
	}
	digest := fnv.New64a()
	// The namespace is hashed in rather than concatenated, so this feature's
	// keys cannot coincide with another's by construction.
	var namespace [4]byte
	binary.BigEndian.PutUint32(namespace[:], uint32(fenceNamespace))
	_, _ = digest.Write(namespace[:])
	_, _ = digest.Write(parsed[:])
	return int64(digest.Sum64()), nil //nolint:gosec // G115: an advisory-lock key is an opaque 64-bit value.
}

// errFenceUnavailable is returned when an operation that must be fenced has no
// fence wired. It is deliberately an error and not a silent bypass: an
// unfenced invalidation is the bug this whole file exists to prevent.
var errFenceUnavailable = errors.New("attachment fence unavailable")

// The fence this package installs on the lifecycle operations is the same one
// the preview job holds, asserted so the two cannot drift apart into two locks
// that exclude nothing.
var _ AttachmentFencing = (*PGXAttachmentFence)(nil)
