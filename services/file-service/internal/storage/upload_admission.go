package storage

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/nicrepository/nchat/services/file-service/internal/domain"
)

// Cluster-wide admission control for uploads (RF-32 follow-up).
//
// The per-minute rate limiter in front of the routes counts request *starts*
// and lives in one process, so N replicas grant N times the budget and a client
// holding several slow uploads open is never counted at all. What has to be
// bounded is how many transfers are *in flight* across the whole cluster, which
// is a different question and needs shared state.
//
// PostgreSQL session advisory locks answer it with the database the service
// already depends on:
//
//   - a fixed set of global slots and a fixed set of per-user slots, each one a
//     lock key; holding a slot is holding its lock;
//   - the locks are taken on a connection reserved for the upload, so they last
//     exactly as long as the transfer and no longer;
//   - no transaction is held open — a long-running transaction would pin the
//     xmin horizon and block vacuum for the duration of a 250 MiB upload;
//   - a crashed process drops its connections, and PostgreSQL releases their
//     session locks with them, so a slot cannot leak. There is no lease to
//     renew, no table to sweep and no reaper job.
//
// This mirrors the advisory-lock usage already established elsewhere in the
// codebase (chat-service's reaction toggle, auth-service's invite claim).

// LockConn is one connection able to hold session advisory locks. It is the
// seam that keeps this package unit-testable without a live database.
//
// A connection has exactly two valid destinies and they are mutually exclusive:
// Release, for a session proven to hold no locks, and Discard, for anything
// else. Calling either twice, or one after the other, is a bug this
// implementation prevents rather than tolerates.
type LockConn interface {
	// TryLock takes the lock without waiting, reporting whether it got it.
	TryLock(ctx context.Context, key int64) (bool, error)
	// Unlock releases a lock this connection holds.
	//
	// The boolean is as load-bearing as the error: pg_advisory_unlock returns
	// false when the session did not hold the lock it was asked to release.
	// That is not a benign no-op — it means the caller's idea of what this
	// session holds is wrong, so the session can no longer be trusted to be
	// lock-free.
	Unlock(ctx context.Context, key int64) (bool, error)
	// Release returns the connection to its pool. Only ever valid once every
	// lock this connection took has been confirmed released.
	Release()
	// Discard removes the connection from its pool and ends the physical
	// session, which is what makes PostgreSQL drop any advisory lock still held
	// on it. It must remove the connection from the pool *before* attempting to
	// close, so that a failure to close cannot put a suspect session back into
	// circulation. An error is diagnostic; the connection is unusable either
	// way.
	Discard(ctx context.Context) error
}

// LockConnPool hands out connections that can hold session advisory locks.
type LockConnPool interface {
	Acquire(ctx context.Context) (LockConn, error)
}

// UploadAdmissionLimits bounds how many uploads may be in flight.
type UploadAdmissionLimits struct {
	// Global is the number of concurrent uploads the whole cluster accepts.
	Global int
	// PerUser is how many of those one authenticated user may hold. It is
	// always less than or equal to Global.
	PerUser int
}

// slotReleaseTimeout bounds the cleanup that gives a slot back.
//
// Cleanup runs on a context detached from the request — by the time the deferred
// release fires, the request's own context is usually already cancelled — so it
// needs a deadline of its own. Short on purpose: a cleanup that cannot finish
// quickly is a connection worth discarding, not waiting on.
//
// It is a var rather than a const only so a test can shorten it; nothing in
// production writes to it.
var slotReleaseTimeout = 5 * time.Second

// PGXUploadAdmission grants upload slots backed by PostgreSQL advisory locks.
type PGXUploadAdmission struct {
	pool   LockConnPool
	limits UploadAdmissionLimits
	logger *slog.Logger
}

// NewPGXUploadAdmission wires admission control over pool.
//
// Limits are expected to be validated by configuration; a non-positive value
// here would mean "no slots", which refuses every upload rather than allowing
// them all — the safe direction if validation is ever bypassed.
//
// The logger records cleanup failures, which is the only place they can surface:
// the release handed to callers is a func() by design, because by the time it
// runs the HTTP response has usually been written and there is nothing left to
// tell the client. Operators still need to know, so the failure is logged.
func NewPGXUploadAdmission(
	pool LockConnPool, limits UploadAdmissionLimits, logger *slog.Logger,
) *PGXUploadAdmission {
	if logger == nil {
		logger = slog.Default()
	}
	return &PGXUploadAdmission{pool: pool, limits: limits, logger: logger}
}

// Acquire reserves one global slot and one slot for userID.
//
// It never waits for capacity: a full cluster is answered immediately so the
// caller can refuse before reading a single byte of the request body. That
// ordering is the whole point — waiting would mean holding an unread body open,
// which is the resource this control exists to protect.
//
// reservationBytes is the caller's budget for this upload, used only for the
// resource accounting recorded by the caller. Nothing is allocated for it here:
// slots are counted, bytes are not buffered.
//
// The returned release is safe to call more than once and must be deferred
// immediately, so every exit — success, read failure, storage failure, panic or
// a cancelled request — gives the slot back.
func (a *PGXUploadAdmission) Acquire(
	ctx context.Context, userID string, reservationBytes int64,
) (func(), error) {
	if a == nil || a.pool == nil {
		return nil, domain.ErrAdmissionUnavailable
	}
	if userID == "" {
		return nil, domain.ErrUnauthorized
	}
	_ = reservationBytes

	conn, err := a.pool.Acquire(ctx)
	if err != nil {
		// No connection means no way to decide, and "cannot decide" must never
		// read as "unlimited".
		return nil, fmt.Errorf("%w: %w", domain.ErrAdmissionUnavailable, err)
	}

	globalKey, err := a.takeSlot(ctx, conn, globalSlotKey, a.limits.Global)
	if err != nil {
		// No lock was taken, so there is nothing to unlock. Whether the session
		// may go back to the pool depends on *why* this failed: a full cluster
		// leaves a perfectly healthy connection, while a failed TryLock means
		// the session's state is unknown.
		if errors.Is(err, domain.ErrNoCapacity) {
			conn.Release()
			return nil, domain.ErrClusterAtCapacity
		}
		a.discard(conn, "global slot acquisition failed")
		return nil, err
	}

	userKey, err := a.takeSlot(ctx, conn, func(slot int) int64 {
		return userSlotKey(userID, slot)
	}, a.limits.PerUser)
	if err != nil {
		// The global slot was ours; hand it back before answering, or a client
		// that repeatedly trips its own limit would drain the cluster's. If that
		// rollback cannot be confirmed, the connection is discarded rather than
		// returned — a session that may still hold the global lock would take a
		// slot out of circulation for as long as the pool keeps it.
		rollbackErr := a.releaseConn(conn, "rollback of partial acquisition", globalKey)
		if errors.Is(err, domain.ErrNoCapacity) {
			err = domain.ErrUserAtCapacity
		}
		// Joined, not replaced: the caller still maps the acquisition failure to
		// its status, and the rollback failure stays observable behind it.
		return nil, errors.Join(err, rollbackErr)
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			// Unlocked in the reverse order they were taken. The error is
			// swallowed here and nowhere else: releaseConn has already decided
			// the connection's fate and logged what went wrong, and the caller
			// has no response left to change.
			_ = a.releaseConn(conn, "upload slot release", userKey, globalKey)
		})
	}, nil
}

// releaseConn gives every named lock back and then decides what happens to the
// connection: the pool, or the bin.
//
// The rule is deliberately unforgiving. A session goes back to the pool only
// when every unlock is confirmed — no error and pg_advisory_unlock returning
// true. Anything else means this process cannot prove the session holds no
// locks, and a session that might still hold one is a slot removed from the
// cluster for as long as the pool keeps the connection alive. Ending the session
// is what makes PostgreSQL drop whatever is left.
//
// Every key is attempted even after one fails: the connection may still accept
// commands, in which case the remaining unlocks are real cleanup, and the log
// then describes the whole picture rather than the first symptom.
//
// It returns nil only when the connection went back to the pool clean.
func (a *PGXUploadAdmission) releaseConn(conn LockConn, operation string, keys ...int64) error {
	// Detached from the request context, which is normally already cancelled by
	// the time a deferred release runs, and bounded so cleanup cannot hang.
	ctx, cancel := context.WithTimeout(context.Background(), slotReleaseTimeout)
	defer cancel()

	var failures []error
	for _, key := range keys {
		released, err := conn.Unlock(ctx, key)
		switch {
		case err != nil:
			failures = append(failures, fmt.Errorf("unlock upload slot: %w", err))
		case !released:
			// The session did not hold the lock it was asked to release, so what
			// this process believes about the session is wrong.
			failures = append(failures, errors.New("unlock upload slot: lock was not held"))
		}
	}

	if len(failures) == 0 {
		conn.Release()
		return nil
	}
	cleanupErr := errors.Join(failures...)
	a.logger.LogAttrs(context.Background(), slog.LevelError,
		"upload slot cleanup failed; discarding connection",
		slog.String("operation", operation),
		slog.Int("failed_unlocks", len(failures)),
	)
	a.discard(conn, operation)
	return cleanupErr
}

// discard ends the session and records the outcome.
//
// The discard context is created here rather than reused from the caller so a
// cleanup deadline already spent on unlocks still leaves room for a graceful
// close. It barely matters for correctness — Discard removes the connection from
// the pool before it tries to close, so even a failure leaves nothing reusable —
// but a clean FIN is worth the few milliseconds.
//
// Nothing here logs a lock key, a user, a DSN or any driver detail beyond the
// error the driver itself produced.
func (a *PGXUploadAdmission) discard(conn LockConn, operation string) {
	ctx, cancel := context.WithTimeout(context.Background(), slotReleaseTimeout)
	defer cancel()
	if err := conn.Discard(ctx); err != nil {
		a.logger.LogAttrs(context.Background(), slog.LevelError,
			"discarding upload admission connection failed",
			slog.String("operation", operation),
			slog.String("error", err.Error()),
		)
	}
}

// takeSlot walks the slot keys and claims the first free one. It returns
// domain.ErrNoCapacity when every slot is held, which the caller translates
// into the status the exhausted resource deserves.
func (a *PGXUploadAdmission) takeSlot(
	ctx context.Context, conn LockConn, keyOf func(slot int) int64, slots int,
) (int64, error) {
	for slot := range slots {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		key := keyOf(slot)
		taken, err := conn.TryLock(ctx, key)
		if err != nil {
			return 0, fmt.Errorf("%w: %w", domain.ErrAdmissionUnavailable, err)
		}
		if taken {
			return key, nil
		}
	}
	return 0, domain.ErrNoCapacity
}

// Slot keys are full 64-bit hashes of a namespaced string rather than a packed
// (user, slot) pair, because an advisory key is one int64 and packing both into
// it would make two different users share a slot space. A hash collision is
// possible in principle; its only effect is that two keys contend, which
// refuses an upload rather than admitting an extra one.
func globalSlotKey(slot int) int64 {
	return hashSlotKey("nchat:file:upload:global:" + strconv.Itoa(slot))
}

func userSlotKey(userID string, slot int) int64 {
	return hashSlotKey("nchat:file:upload:user:" + userID + ":" + strconv.Itoa(slot))
}

func hashSlotKey(value string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(value))
	return int64(h.Sum64()) //nolint:gosec // Intentional uint64→int64 reinterpretation for PostgreSQL advisory keys.
}
