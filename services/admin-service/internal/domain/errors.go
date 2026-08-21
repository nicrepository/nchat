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
)
