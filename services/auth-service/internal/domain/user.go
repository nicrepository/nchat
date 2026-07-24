package domain

import "time"

// DefaultDisplayName is the last-resort visible label for a user provisioned by
// an identity provider that supplied no usable name. display_name is NOT NULL,
// so provisioning needs a value; it is applied only at creation, never on a
// re-login, so it can not overwrite a name the user already has.
const DefaultDisplayName = "Usuário"

type User struct {
	ID               string
	Email            string
	DisplayName      string
	FullName         string
	Status           string
	AuthSource       string
	ExternalProvider string
	ExternalSubject  string
	EmailVerifiedAt  time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// SelfProfile is the minimal identity a signed-in user may read about
// themselves, used by GET /auth/me to hydrate the profile screen. It carries no
// e-mail, status, auth source or other PII beyond what the UI renders. AvatarURL
// is empty when no avatar is set.
type SelfProfile struct {
	ID          string
	DisplayName string
	AvatarURL   string
}

type PolicySettings struct {
	MinPasswordLength            int
	RequireUppercase             bool
	RequireLowercase             bool
	RequireNumber                bool
	RequireSymbol                bool
	FailedLoginLimit             int
	FailedLoginWindowMinutes     int
	FailedLoginLockoutMinutes    int
	SessionIdleTimeoutMinutes    int
	MaxDevicesPerUser            int
	PasswordResetTokenTTLMinutes int
	InviteTokenTTLHours          int
}

type CreateUserInput struct {
	Email              string
	DisplayName        string
	FullName           string
	InitialPassword    string
	MustChangePassword bool
}

// ValidateStatusTransition returns ErrStatusTransitionNotAllowed for any
// transition not in the supported set. Only active↔suspended is allowed.
// Transitions involving locked, invited, or deleted are not part of this flow.
func ValidateStatusTransition(from, to string) error {
	switch {
	case from == "active" && to == "suspended":
		return nil
	case from == "suspended" && to == "active":
		return nil
	default:
		return ErrStatusTransitionNotAllowed
	}
}
