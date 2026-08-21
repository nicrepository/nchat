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
