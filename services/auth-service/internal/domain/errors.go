package domain

import "errors"

var (
	ErrDuplicateEmail         = errors.New("email already registered")
	ErrPasswordPolicy         = errors.New("password does not meet policy requirements")
	ErrInvalidInput           = errors.New("invalid input")
	ErrInvalidToken           = errors.New("invalid or expired token")
	ErrEmailOutboxUnavailable = errors.New("email outbox encryption unavailable")
)

var ErrInvalidRefreshToken = errors.New("invalid refresh token")

var ErrInvalidCredentials = errors.New("invalid credentials")

var ErrInviteAlreadyPending = errors.New("active invite already exists for this email")

var ErrNotFound = errors.New("not found")
