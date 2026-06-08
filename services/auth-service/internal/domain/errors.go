package domain

import "errors"

var (
	ErrDuplicateEmail           = errors.New("email already registered")
	ErrPasswordPolicy           = errors.New("password does not meet policy requirements")
	ErrInvalidInput             = errors.New("invalid input")
	ErrInvalidToken             = errors.New("invalid or expired token")
	ErrEmailOutboxUnavailable   = errors.New("email outbox encryption unavailable")
	ErrOIDCDisabled             = errors.New("oidc disabled")
	ErrOIDCMisconfigured        = errors.New("oidc misconfigured")
	ErrOIDCInvalidCallback      = errors.New("invalid oidc callback")
	ErrOIDCAccountConflict      = errors.New("oidc account conflict")
	ErrOIDCEmailUnverified      = errors.New("oidc email unverified")
	ErrOIDCDomainForbidden      = errors.New("oidc email domain forbidden")
	ErrOIDCProvisioningDisabled = errors.New("oidc provisioning disabled")
)

var ErrInvalidRefreshToken = errors.New("invalid refresh token")

var ErrInvalidCredentials = errors.New("invalid credentials")

var ErrInviteAlreadyPending = errors.New("active invite already exists for this email")

var ErrNotFound = errors.New("not found")

var ErrStatusTransitionNotAllowed = errors.New("status transition not allowed")
var ErrForbidden                  = errors.New("forbidden")
