// Package uploadpolicy holds the canonical attachment size limit (RF-32).
//
// It exists because the limit is decided in one service and enforced in
// another: chat-service owns the administrative value (a column on
// chat.workspaces, an admin endpoint, a database CHECK) and file-service is the
// authority that actually refuses an oversized upload. Restating the three
// numbers in both modules would let them drift, which is the exact failure the
// requirement calls out — so they live here, once, and both modules import them.
//
// Everything is int64 bytes. Bytes are never represented as a float, and the
// conversion to a human-readable unit happens only at the presentation edge.
package uploadpolicy

// BytesPerMiB is the unit the policy is expressed in.
//
// Every value the policy may take is an exact multiple of it. That is a real
// constraint and not a formatting nicety: the administrative UI edits whole
// MiB, so a byte count that is not a whole MiB could not be shown there without
// being changed, and rounding an administrator's stored limit into a different
// one is worse than refusing the value outright.
const BytesPerMiB int64 = 1 << 20

// Size bounds for one attachment, in bytes.
//
// The unit is binary (MiB), matching every other size in this repository —
// file-service's previous default (50 << 20), auth-service's avatar cap
// (5 << 20) and the web formatter, which divides by 1024 and labels the result
// "KB"/"MB". The product requirement of "250 MB" is therefore 250 MiB, which is
// also the more permissive reading: any file a user calls 250 MB
// (250,000,000 bytes) fits.
const (
	// DefaultMaxUploadBytes is what a workspace gets with no explicit policy,
	// and what enforcement falls back to when a persisted value is missing or
	// out of range. It is never "no limit".
	DefaultMaxUploadBytes int64 = 250 << 20 // 250 MiB = 262144000 bytes

	// MinMaxUploadBytes is a floor, not a policy: a cap below it would make
	// attachments unusable through a typo. Zero and negative values are not
	// expressible as a limit at all.
	MinMaxUploadBytes int64 = 1 << 20 // 1 MiB

	// MaxMaxUploadBytes is the ceiling an administrator may set. Above it a
	// single request could tie up a service instance for an unbounded time and
	// consume unbounded storage, so "unlimited" is deliberately inexpressible.
	MaxMaxUploadBytes int64 = 512 << 20 // 512 MiB
)

// MultipartOverheadBytes is the slack a multipart body carries on top of the
// file itself: boundaries and part headers. It is reserved so a request holding
// a file exactly at the limit is not refused for bytes the user did not send,
// and it is never part of the size a file is allowed to be.
const MultipartOverheadBytes int64 = 8 << 10

// GatewayHardCapBytes is the static ceiling the gateway applies to the whole
// HTTP body. It is deliberately not the workspace policy.
//
// Two different controls exist and must not be confused:
//
//   - this one is static, applies to the entire HTTP body, protects the
//     infrastructure, and is the largest administrative value plus multipart
//     overhead. It does not vary per workspace and answers 413 at the edge.
//   - the workspace policy is dynamic, administrator-configurable, applies to
//     the real bytes of the file, and is enforced authoritatively by
//     file-service.
//
// Trying to keep a per-workspace value in sync with a static proxy
// configuration would guarantee drift, so the gateway is pinned at the ceiling
// instead: by construction it is at least as large as any policy an
// administrator can store, so it can never refuse an upload the service would
// have accepted.
const GatewayHardCapBytes = MaxMaxUploadBytes + MultipartOverheadBytes

// Valid reports whether value is an acceptable limit: inside the bounds and an
// exact whole number of MiB.
//
// It is the single definition of that rule. The admin handler calls it before
// touching the database, file-service's configuration calls it for the
// deployment ceiling, and the CHECK constraint in migration 000020 repeats both
// halves as a backstop. Nothing anywhere corrects a value that fails it — an
// invalid limit is refused, never clamped, truncated or rounded to the nearest
// acceptable one.
//
// Zero, negatives and anything past the ceiling fail the range test; the
// modulo makes values like 1.5 MiB, or one byte either side of a bound,
// inexpressible. Because MinMaxUploadBytes is itself a whole MiB, no valid
// value can overflow anything downstream.
func Valid(value int64) bool {
	return value >= MinMaxUploadBytes && value <= MaxMaxUploadBytes && value%BytesPerMiB == 0
}

// Effective normalises a persisted limit into a value safe to enforce with.
//
// A zero or out-of-range value can only come from a row written before the
// migration that added the column, or from a struct that was never populated.
// In both cases the answer is the default — never "no limit" and never a
// silently widened bound.
func Effective(value int64) int64 {
	if !Valid(value) {
		return DefaultMaxUploadBytes
	}
	return value
}

// EffectiveUnder returns the limit to enforce for one upload: the workspace's
// administrative policy, further narrowed by a deployment ceiling.
//
// The two are separate controls with separate owners. The administrator decides
// the product policy; the operator decides what a given cluster can physically
// absorb. Neither silently rewrites the other's stored value — the narrowing
// happens here, per request, and is documented rather than clamped into the
// database.
//
// A non-positive ceiling means "no deployment ceiling configured".
func EffectiveUnder(value, ceiling int64) int64 {
	effective := Effective(value)
	if ceiling > 0 && ceiling < effective {
		return ceiling
	}
	return effective
}
