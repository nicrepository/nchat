package domain

import "errors"

var (
	ErrDuplicateEmail         = errors.New("email already registered")
	ErrPasswordPolicy         = errors.New("password does not meet policy requirements")
	ErrInvalidInput           = errors.New("invalid input")
	ErrInvalidToken           = errors.New("invalid or expired token")
	ErrEmailOutboxUnavailable = errors.New("email outbox encryption unavailable")
	ErrOIDCDisabled           = errors.New("oidc disabled")
	ErrOIDCMisconfigured      = errors.New("oidc misconfigured")
	ErrOIDCInvalidCallback    = errors.New("invalid oidc callback")
	ErrOIDCAccountConflict    = errors.New("oidc account conflict")
	ErrOIDCEmailUnverified    = errors.New("oidc email unverified")
	// ErrOIDCInsufficientAssurance means the identity provider authenticated
	// the person, but not with the authentication context the administrative
	// policy requires. It is a refusal, never a prompt to retry with less.
	ErrOIDCInsufficientAssurance = errors.New("oidc insufficient authentication assurance")
	ErrOIDCDomainForbidden       = errors.New("oidc email domain forbidden")
	ErrOIDCProvisioningDisabled  = errors.New("oidc provisioning disabled")
)

var ErrInvalidRefreshToken = errors.New("invalid refresh token")

var ErrInvalidCredentials = errors.New("invalid credentials")

// ErrPasswordExpired means the password was correct and is too old to be used.
//
// Deliberately distinct from ErrInvalidCredentials. It is only ever reachable
// after the password has already been verified, so it reveals nothing to
// somebody who does not hold the password — and the person who does hold it
// needs to be told, or they will keep retrying a correct password until the
// lockout closes the account on them.
var ErrPasswordExpired = errors.New("password expired")

var ErrInviteAlreadyPending = errors.New("active invite already exists for this email")

var ErrNotFound = errors.New("not found")

var ErrStatusTransitionNotAllowed = errors.New("status transition not allowed")
var ErrForbidden = errors.New("forbidden")
