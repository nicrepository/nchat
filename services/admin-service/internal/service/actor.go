package service

import (
	"context"
	"errors"

	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
)

// Recorder is the audit sink the management services write to. Satisfied by
// AuditService.
type Recorder interface {
	Record(ctx context.Context, event domain.AuditEvent)
}

// Actor is who is performing an administrative operation, and under which
// request.
//
// It is built from the authenticated principal by the HTTP layer and never from
// the request body. A handler that let a caller name the actor would turn every
// audit row into a claim rather than a record, and would defeat the
// self-mutation guards below, which are the reason this type exists at all.
type Actor struct {
	UserID string
	// SessionID is the administrative session this request arrived on.
	//
	// Carried so a privileged write can re-prove the session is still live at
	// the moment it commits, rather than trusting that it was live when the
	// middleware ran. See domain.MutationAuthorization.
	SessionID string
	// Capabilities is the effective set the session guard loaded from the
	// database for this request.
	//
	// It is carried here for the one decision a route cannot make on its own:
	// the configuration surface refuses a *value* that weakens the platform
	// unless the actor holds admin.superuser, and whether a value is dangerous
	// is only knowable after it has been parsed and validated. Everything else
	// is still decided by the capability the route declares. The zero value
	// grants nothing, so a service that forgets to populate it denies.
	Capabilities domain.CapabilitySet
	// Email is the administrator's own address, as the session guard loaded it
	// from the database.
	//
	// Carried for exactly one purpose: the SMTP test message of issue #582 is
	// delivered here and nowhere else. Making the destination an attribute of
	// the authenticated principal rather than a request field is what keeps the
	// console from being usable as a mail relay, so it must arrive with the
	// actor and never from a body.
	Email string
	// CorrelationID is the server-minted request id. admin-service generates it
	// rather than accepting one from the caller, so it cannot be forged into
	// matching somebody else's trail.
	CorrelationID string
}

// record writes one audit row for an operation on a named resource.
//
// Metadata is an allowlist by construction: every caller passes a map it built
// field by field from server-derived values. Nothing in this service can reach
// a header, a cookie, a token or a message body from here.
func record(ctx context.Context, recorder Recorder, actor Actor, action, resource string, result domain.AuditResult, metadata map[string]string) {
	if recorder == nil {
		return
	}
	recorder.Record(ctx, domain.AuditEvent{
		ActorUserID:   actor.UserID,
		Action:        action,
		Resource:      resource,
		Result:        result,
		CorrelationID: actor.CorrelationID,
		Metadata:      metadata,
	})
}

// resultFor classifies an operation's outcome for the trail.
//
// A refusal and a failure are different facts: "denied" is the platform saying
// no, "error" is the platform breaking. Collapsing them would make an attack
// look like an outage.
func resultFor(err error) domain.AuditResult {
	switch {
	case err == nil:
		return domain.AuditResultSuccess
	case isDenial(err):
		return domain.AuditResultDenied
	default:
		return domain.AuditResultError
	}
}

func isDenial(err error) bool {
	return errors.Is(err, domain.ErrUnauthorized) ||
		errors.Is(err, domain.ErrForbidden) ||
		errors.Is(err, domain.ErrConflict) ||
		errors.Is(err, domain.ErrInvalidInput) ||
		errors.Is(err, domain.ErrNotFound)
}
