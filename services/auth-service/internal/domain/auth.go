package domain

import "time"

const OIDCFrontendCallbackPath = "/oidc-callback"

// Session identifies an active auth.user_sessions row that can receive tokens.
type Session struct {
	ID     string
	UserID string
}

// TokenPair is the successful refresh response payload.
type TokenPair struct {
	AccessToken  string
	RefreshToken string
	TokenType    string
	ExpiresIn    int
}

// LoginInput carries the raw request data for a login attempt.
type LoginInput struct {
	Email             string
	Password          string
	DeviceFingerprint string
	DeviceName        string
	Platform          string
	IPAddress         string
	UserAgent         string
}

// LoginUser is the safe user representation returned after a successful login.
type LoginUser struct {
	ID                 string
	Email              string
	DisplayName        string
	MustChangePassword bool
}

// LoginResult is returned to the HTTP layer after a successful login.
type LoginResult struct {
	AccessToken  string
	RefreshToken string
	TokenType    string
	ExpiresIn    int
	User         LoginUser
}

// CreateSessionInput carries the data needed to create a new login session.
// Password is the raw plaintext used by the store for credential verification;
// it is never persisted — only the hash fetched from the DB is used for comparison.
type CreateSessionInput struct {
	Password              string
	Email                 string
	RefreshTokenHash      string
	RefreshExpiresAt      time.Time
	DeviceFingerprintHash string
	DeviceName            string
	Platform              string
	IPAddress             string
	UserAgent             string
}

// CreatedLoginSession is returned by the store after a session is created.
type CreatedLoginSession struct {
	Session Session
	User    LoginUser
}

// ForgotPasswordInput carries the email submitted to the enumeration-safe forgot-password endpoint.
type ForgotPasswordInput struct {
	Email string
}

// ResetPasswordInput carries the opaque reset token and replacement password.
type ResetPasswordInput struct {
	Token       string
	NewPassword string
}

// AdminInviteInput carries admin-created email invite data.
type AdminInviteInput struct {
	Email       string
	DisplayName string
	FullName    string
}

// InviteResult is the safe invite metadata returned after admin invite creation.
type InviteResult struct {
	ID        string
	Email     string
	CreatedAt time.Time
}

// AcceptInviteInput carries the public invite acceptance payload.
type AcceptInviteInput struct {
	Token       string
	DisplayName string
	FullName    string
	Password    string
}

// AcceptInviteResult is the safe user metadata returned after invite acceptance.
type AcceptInviteResult struct {
	UserID      string
	Email       string
	DisplayName string
	FullName    string
	CreatedAt   time.Time
}

// OIDCLoginRequest stores generated one-time login request material.
type OIDCLoginRequest struct {
	ID                    string
	Provider              string
	StateHash             string
	NonceHash             string
	PKCEVerifierEncrypted string
	RedirectAfter         string
	ExpiresAt             time.Time
}

// OIDCConsumedAuthRequest is returned after a state value is atomically consumed.
type OIDCConsumedAuthRequest struct {
	ID                    string
	Provider              string
	NonceHash             string
	PKCEVerifierEncrypted string
	RedirectAfter         string
}

// OIDCClaims carries the validated identity claims used by nchat.
// GivenName, FamilyName and Picture come from the standard `profile` scope and
// are all optional: a provider that omits them must keep working.
type OIDCClaims struct {
	Subject           string
	Email             string
	EmailVerified     bool
	PreferredUsername string
	Name              string
	GivenName         string
	FamilyName        string
	Picture           string
	Nonce             string
}

// OIDCSessionInput carries provider identity and internal session material for atomic persistence.
// FullName and AvatarURL are empty when the provider supplied nothing usable;
// an empty value never overwrites an existing one (see resolveOIDCUser).
type OIDCSessionInput struct {
	Provider              string
	Subject               string
	Email                 string
	DisplayName           string
	FullName              string
	AvatarURL             string
	RefreshTokenHash      string
	RefreshExpiresAt      time.Time
	DeviceFingerprintHash string
	DeviceName            string
	Platform              string
	IPAddress             string
	UserAgent             string
	AutoProvision         bool
}

// OIDCExchangeInput carries a one-time frontend exchange row.
type OIDCExchangeInput struct {
	ID                    string
	Provider              string
	CodeHash              string
	AccessValueEncrypted  string
	RefreshValueEncrypted string
	BearerScheme          string
	ExpiresIn             int
	User                  LoginUser
	ExpiresAt             time.Time
}

// OIDCConsumedExchange is returned after a frontend exchange code is atomically consumed.
type OIDCConsumedExchange struct {
	ID                    string
	Provider              string
	AccessValueEncrypted  string
	RefreshValueEncrypted string
	BearerScheme          string
	ExpiresIn             int
	User                  LoginUser
}

// OIDCCreatedSession is returned after OIDC user resolution, session creation, and exchange insertion.
type OIDCCreatedSession struct {
	Session Session
	User    LoginUser
}

// OIDCExchangeResult is returned to the frontend after consuming a one-time exchange code.
type OIDCExchangeResult struct {
	AccessToken  string
	RefreshToken string
	TokenType    string
	ExpiresIn    int
	User         LoginUser
}

// LoginAttempt is a single failed login attempt record for a user.
type LoginAttempt struct {
	ID            int64
	Email         string
	IPAddress     string // raw; masking happens in HTTP layer
	UserAgent     string
	FailureReason string
	CreatedAt     time.Time
}

// LoginAttemptsCursor is used for keyset pagination over login attempts.
type LoginAttemptsCursor struct {
	CreatedAt time.Time
	ID        int64
}

// SessionInfo is the safe, displayable representation of a user session row.
// IPAddress and UserAgent are raw — masking/sanitizing is done in the HTTP layer.
type SessionInfo struct {
	ID                string
	DeviceID          *string
	CreatedAt         time.Time
	LastSeenAt        time.Time
	IdleExpiresAt     time.Time
	AbsoluteExpiresAt *time.Time
	RevokedAt         *time.Time
	IPAddress         string
	UserAgent         string
}

// DeviceInfo is the safe, displayable representation of a user device row.
// LastIP is raw — masking is done in the HTTP layer.
// Current is true when the current access token's session belongs to this device.
type DeviceInfo struct {
	ID           string
	DisplayName  *string
	Platform     *string
	LastIP       string
	FirstSeenAt  time.Time
	LastSeenAt   time.Time
	RevokedAt    *time.Time
	SessionCount int
	Current      bool
}

// DeviceSessionPolicy carries policy fields surfaced by the device/session endpoints.
type DeviceSessionPolicy struct {
	MaxDevicesPerUser int
}
