package domain

import "time"

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
