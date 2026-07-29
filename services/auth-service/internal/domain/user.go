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
}

// WorkspaceUserCursor is the keyset position of the workspace user listing.
//
// It names the last row of the previous page and nothing else. Deliberately it
// does *not* carry the sort key: the sort key is the display name or, when that
// is empty, the e-mail address — so putting it in the cursor put an
// administrator's e-mail into a query string, and from there into gateway
// access logs (CWE-532). The position is instead re-derived server-side from
// UserID, which costs one primary-key lookup per page.
//
// What remains is two identifiers the caller already holds: their own workspace
// and a user inside it. It carries its workspace so a cursor minted for one
// tenant is detectably not usable in another — defence in depth, not the
// boundary, since the query always filters by the workspace resolved from the
// session.
type WorkspaceUserCursor struct {
	Version     int    `json:"v"`
	WorkspaceID string `json:"workspaceId"`
	UserID      string `json:"userId"`
}

// WorkspaceUserCursorVersion is the only cursor layout this build accepts.
// A cursor from a future version is rejected rather than guessed at.
const WorkspaceUserCursorVersion = 1

// WorkspaceUserCursorMaxEncodedBytes caps the encoded cursor before it is
// decoded at all. The value is client-controlled, so a length check has to come
// before any allocation or parsing; a legitimate cursor is well under 200 bytes.
const WorkspaceUserCursorMaxEncodedBytes = 512

// WorkspaceUserAnchor is the ordering position of the row a cursor points at.
// Found is false when that row is no longer a member of the workspace, which
// makes the cursor unusable rather than silently empty.
type WorkspaceUserAnchor struct {
	SortKey string
	UserID  string
	Found   bool
}

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
