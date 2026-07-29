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

// WorkspaceUser is one row of the workspace user administration list.
//
// It is deliberately narrower than User: it carries exactly the fields the
// admin table renders and nothing else. No password hash, token, session,
// external subject or soft-delete marker is representable here, so a future
// change to the query cannot leak one through this type.
type WorkspaceUser struct {
	ID          string
	Email       string
	DisplayName string
	FullName    string
	Status      string
	AuthSource  string
	CreatedAt   time.Time
	// SortKey is the value the listing is ordered by. It is carried out of the
	// query rather than recomputed in Go so the cursor resumes from exactly the
	// position the database ordered by, even for names whose lowercasing
	// differs between PostgreSQL's collation and Go's.
	SortKey string
}

// WorkspaceUserCursor is the keyset position of the workspace user listing.
//
// It carries its workspace so a cursor minted for one tenant is detectably not
// usable in another. That check is defence in depth, not the boundary: the
// query always filters by the workspace resolved from the session, so even an
// accepted foreign cursor could only skip rows, never reveal another tenant's.
type WorkspaceUserCursor struct {
	Version     int    `json:"v"`
	WorkspaceID string `json:"workspaceId"`
	SortKey     string `json:"sortKey"`
	UserID      string `json:"userId"`
}

// WorkspaceUserCursorVersion is the only cursor layout this build accepts.
// A cursor from a future version is rejected rather than guessed at.
const WorkspaceUserCursorVersion = 1

// WorkspaceUserPageLimits bound how many rows one page may carry.
const (
	WorkspaceUserPageDefaultLimit = 50
	WorkspaceUserPageMinLimit     = 1
	WorkspaceUserPageMaxLimit     = 100
)

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
