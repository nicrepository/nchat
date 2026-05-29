package domain

import "time"

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
