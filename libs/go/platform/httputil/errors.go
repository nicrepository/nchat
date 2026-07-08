// Package httputil provides shared HTTP response and middleware helpers.
package httputil

const (
	ErrCodeInternal     = "internal_error"
	ErrCodeNotFound     = "not_found"
	ErrCodeBadRequest   = "bad_request"
	ErrCodeUnauthorized = "unauthorized"
	ErrCodeForbidden    = "forbidden"
	ErrCodeRateLimited  = "rate_limited"
	ErrCodeConflict     = "conflict"
)
