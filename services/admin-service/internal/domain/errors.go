package domain

import "errors"

var (
	// ErrUnauthorized means the caller proved no usable identity: no
	// administrative session, or one that is revoked or expired.
	ErrUnauthorized = errors.New("unauthorized")
	// ErrForbidden means the caller is authenticated but is not a platform
	// administrator, or lacks the capability the endpoint requires.
	ErrForbidden = errors.New("forbidden")
	// ErrUnavailable means a dependency the endpoint needs is not wired.
	ErrUnavailable = errors.New("unavailable")
	// ErrInvalidInput means the request itself is malformed.
	ErrInvalidInput = errors.New("invalid input")
	// ErrNotFound means the named object does not exist, or is soft-deleted.
	//
	// The Admin API answers it plainly rather than hiding it behind 403: every
	// endpoint that can return it is already behind a platform capability, so
	// the caller is entitled to know the platform-wide set it is asking about,
	// and pretending otherwise would only make a broken console harder to
	// diagnose.
	ErrNotFound = errors.New("not found")
	// ErrTooManyRequests means the caller is authorized and the request is
	// well formed, but it is being made too often to be served.
	//
	// It exists because the active diagnostics of issue #582 open outbound
	// connections and, for the SMTP test message, spend a relay's reputation.
	// Refusing those with 400 or 409 would tell an operator their request was
	// wrong when it was only early, and would hide the rate limit from anyone
	// auditing what bounds this surface.
	ErrTooManyRequests = errors.New("too many requests")
	// ErrConflict means the object is not in the state the command requires —
	// a status transition onto the status it already has, an object another
	// request changed first, or an invariant the change would break.
	ErrConflict = errors.New("conflict")
)
