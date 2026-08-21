// Package antispampolicy holds the canonical per-user message rate limit
// (RF-19).
//
// It exists for the same reason uploadpolicy does: the value is decided in one
// place and now edited from two. chat-service owns the enforcement (a column on
// chat.workspaces, a database CHECK, the shared Valkey limiter) and the Admin
// Console is a second authorized writer of the same column, from the platform
// scope instead of the workspace one. Restating "1, 60, 600" in both modules is
// exactly the drift the requirement forbids, so the three numbers live here once
// and both modules import them.
package antispampolicy

// Bounds for the per-user, per-minute message budget of one workspace.
//
// The window itself is fixed at one minute and is not configurable; these
// numbers describe how many messages fit in it.
const (
	// Default is what a workspace gets with no explicit policy, and what
	// enforcement falls back to when the policy cannot be read. It is the value
	// the limit was hardcoded to before RF-19, so making the limit configurable
	// changed no existing behaviour on its own.
	Default = 60
	// Min is deliberately 1 and not 0: an anti-spam control must not double as
	// a way to mute a workspace.
	Min = 1
	// Max caps the policy at 10 messages/second per user, well above any human
	// cadence, so raising the limit cannot be used to disable the protection
	// outright. There is no value that means "unlimited".
	Max = 600
)

// Valid reports whether value is an acceptable policy.
//
// It is the single definition of that rule: the workspace admin endpoint in
// chat-service, the platform admin endpoint in admin-service and the CHECK
// constraint in migration 000018 all express the same bounds. Nothing anywhere
// corrects a value that fails it — an invalid limit is refused, never clamped.
func Valid(value int) bool {
	return value >= Min && value <= Max
}

// Effective normalises a persisted policy into a value safe to enforce with.
//
// A zero or out-of-range value can only come from a row written before the
// migration that added the column, or from a struct that was never populated.
// In both cases the answer is the default — never "no limit".
func Effective(value int) int {
	if !Valid(value) {
		return Default
	}
	return value
}
