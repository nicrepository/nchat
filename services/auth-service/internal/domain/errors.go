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
var ErrForbidden = errors.New("forbidden")

// ErrInviteRateLimited is returned when an actor exceeded the invite budget for
// the workspace they are inviting into. It is deliberately distinct from
// ErrForbidden: the caller is authorized, just over quota.
var ErrInviteRateLimited = errors.New("invite rate limit exceeded")

// ErrAlreadyMember is returned when the invited address already belongs to an
// active member of the target workspace. Inviting them again would create a
// second onboarding path to a workspace they can already reach.
var ErrAlreadyMember = errors.New("already a member of this workspace")

// ErrBootstrapUnavailable is returned when the bootstrap invite endpoint is
// asked to act outside the initialization window: either it was never
// configured, or the target workspace already has an active administrator and
// must be managed through the authenticated, workspace-scoped API instead.
var ErrBootstrapUnavailable = errors.New("bootstrap invite unavailable")
