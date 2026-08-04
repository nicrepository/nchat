package storage

import "time"

// Test seam for the cleanup deadline.
//
// slotReleaseTimeout is five seconds in production, which is the right value for
// a real connection and far too long for a test to wait on. Overriding it here —
// rather than making it a field nobody sets in production — keeps the exported
// surface unchanged while letting a test prove that a cleanup which overruns its
// deadline really does discard the connection instead of blocking or, worse,
// returning it.
func SetSlotReleaseTimeoutForTest(d time.Duration) (restore func()) {
	previous := slotReleaseTimeout
	slotReleaseTimeout = d
	return func() { slotReleaseTimeout = previous }
}

// NewFinishedLockConnForTest builds a pgx-backed lock connection that has
// already given up its connection, which is the state Release and Discard leave
// behind.
//
// It exists because that state is where the dangerous behaviour lives: pgxpool
// panics if a connection is released or hijacked twice, so the nil guards on
// this type are what stand between an idempotent release and a crashed process.
// Testing them needs no database — there is deliberately no connection left.
func NewFinishedLockConnForTest() LockConn {
	return &pgxLockConn{conn: nil}
}

// GlobalSlotKeyForTest exposes the global slot key derivation so a test can
// pre-fill the lock namespace without restating the hashing rule.
func GlobalSlotKeyForTest(slot int) int64 { return globalSlotKey(slot) }
