package domain

import "errors"

// Principal is the server-derived identity of one authenticated request: who is
// asking, and which workspace they are authorised in.
//
// Both fields are resolved from the database against the caller's session. No
// constructor takes them from a request body, a query string or a path, which is
// what makes "the client cannot name a user or a workspace" a property of the
// type rather than a rule handlers are asked to remember.
type Principal struct {
	UserID      string
	WorkspaceID string
}

var (
	// ErrUnauthenticated: no live session backs this request.
	ErrUnauthenticated = errors.New("push subscription: unauthenticated")

	// ErrForbidden: the session is live, but the caller is not an active member
	// of a workspace, so there is no scope to register anything in.
	ErrForbidden = errors.New("push subscription: forbidden")

	// ErrNotFound: no subscription with this identifier belongs to the caller.
	//
	// Deliberately the same error whether the row does not exist or belongs to
	// somebody else. Any other answer would let one user probe another's
	// subscription identifiers.
	ErrNotFound = errors.New("push subscription: not found")

	// ErrEndpointConflict: the endpoint already belongs to another subscription.
	//
	// A Web Push endpoint is a capability URL, so re-pointing one at a different
	// owner would hand that owner somebody else's browser. The registration is
	// refused rather than resolved; the client's own recovery is to unsubscribe
	// in the browser and register the fresh endpoint that produces.
	ErrEndpointConflict = errors.New("push subscription: endpoint already registered")
)

// There is deliberately no ceiling on how many subscriptions one user may hold.
//
// A cap on persistent cardinality is not a requirement of this feature, and the
// count-then-insert that expressed it could not actually hold: two registrations
// racing at the limit both passed the count. Making it hold would have meant
// serialising every registration of one user behind a lock, which is real
// contention bought for a bound nobody asked for. Request-frequency abuse is a
// different problem with a different control, and the gateway already owns it.
