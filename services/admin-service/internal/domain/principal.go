package domain

import "time"

// AdminPrincipal is a platform administrator as the database knows them.
//
// Identity fields are carried for display only. Nothing in this struct is
// consulted to decide access except Capabilities, which is re-read from the
// database on every request rather than cached in a token.
type AdminPrincipal struct {
	UserID       string
	Email        string
	DisplayName  string
	AvatarURL    string
	Capabilities CapabilitySet
}

// AdminSession is the administrative session backing one browser.
//
// It is distinct from the NChat login session that established it: it has its
// own, shorter idle window and its own absolute lifetime, and revoking either
// one ends administrative access.
type AdminSession struct {
	ID                string
	UserID            string
	AuthSessionID     string
	IdleExpiresAt     time.Time
	AbsoluteExpiresAt time.Time
}

// AuthenticatedAdmin is what a guarded handler receives: a live
// administrative session and the principal's current capabilities.
type AuthenticatedAdmin struct {
	Session   AdminSession
	Principal AdminPrincipal
}

// MutationAuthorization is the identity a privileged write re-proves inside its
// own transaction, and the capability it must still hold to be allowed.
//
// It carries identity, never authority. The capability set the middleware
// loaded is a snapshot from before the request body was even read; between that
// moment and the commit, a role can be revoked, a principal suspended or the
// session ended, and all three would leave the snapshot saying yes. So the
// write re-derives the answer from the database, under a lock the revocation
// paths contend for, in the transaction that performs the write.
//
// The middleware is not replaced by this. It stays the first barrier — it
// refuses early, it keeps unauthorized work out of the handlers, and it is what
// produces the identity below. This is the second one, at the only point where
// "still allowed" and "already written" cannot be separated.
type MutationAuthorization struct {
	// SessionID is the administrative session row, not the chat one.
	SessionID string
	UserID    string
	// Capability is the one the mutation actually demands, which for a
	// configuration change may be stricter than the route's — a value that
	// weakens the platform requires admin.superuser.
	Capability Capability
}

// Valid reports whether this authorization names enough to be checkable.
//
// A zero value must never authorize anything: a caller that forgot to populate
// it is refused rather than silently checked against nothing.
func (a MutationAuthorization) Valid() bool {
	return a.SessionID != "" && a.UserID != "" && IsKnownCapability(a.Capability)
}

// AuditResult classifies the outcome recorded for an audit event.
type AuditResult string

const (
	AuditResultSuccess AuditResult = "success"
	AuditResultDenied  AuditResult = "denied"
	AuditResultError   AuditResult = "error"
)

// Audit action names. Kept as constants so the same string is written by the
// producer and asserted by the tests, and so a rename cannot silently split
// one action into two in the trail.
const (
	AuditActionSessionCreate     = "admin.session.create"
	AuditActionSessionDestroy    = "admin.session.destroy"
	AuditActionAuthorizationDeny = "admin.authorization.deny"
)

// AuditEvent is one row of the administrative audit trail.
//
// Metadata is an allowlist by construction: only the fields a producer sets
// explicitly are stored, and no producer in this service sets a request
// header, a token, a cookie or any chat content.
type AuditEvent struct {
	ActorUserID   string
	Action        string
	Resource      string
	Result        AuditResult
	CorrelationID string
	Metadata      map[string]string
}

// AuditEntry is one row as the audit reader returns it.
type AuditEntry struct {
	ID            int64
	OccurredAt    time.Time
	ActorUserID   string
	ActorEmail    string
	Action        string
	Resource      string
	Result        AuditResult
	CorrelationID string
}

// AdminSessionInput is everything the store needs to record a new
// administrative session. SessionHash is a keyed hash of the opaque cookie
// value; the value itself never leaves the HTTP layer.
type AdminSessionInput struct {
	UserID            string
	AuthSessionID     string
	SessionHash       string
	IPAddress         string
	UserAgent         string
	IdleExpiresAt     time.Time
	AbsoluteExpiresAt time.Time
}
