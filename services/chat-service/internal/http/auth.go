package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// ctxKey is the unexported context key type for this package.
type ctxKey int

const (
	ctxKeyUserID    ctxKey = iota
	ctxKeySessionID        // carries the JWT "sid" claim
	// ctxKeyWorkspaceID carries the canonical workspace of the request, resolved
	// server-side by AntiSpamGuard.Middleware. It is only ever set behind
	// BearerAuth + RequireActiveSession, and never from a client-supplied value.
	ctxKeyWorkspaceID
)

var uuidRE = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// bearerScheme is the Authorization header scheme string defined by RFC 6750.
// It is used as a constant to avoid scattered string literals in middleware and tests.
const bearerScheme = "Bearer "

func isValidUUID(s string) bool { return uuidRE.MatchString(s) }

// GetContextUserID returns the authenticated user ID injected by BearerAuth,
// or "" if the request was not authenticated.
func GetContextUserID(r *http.Request) string {
	uid, _ := r.Context().Value(ctxKeyUserID).(string)
	return uid
}

// GetContextSessionID returns the session ID injected by BearerAuth, or "" if absent.
func GetContextSessionID(r *http.Request) string {
	sid, _ := r.Context().Value(ctxKeySessionID).(string)
	return sid
}

// contextWorkspaceID returns the canonical workspace resolved for this request,
// or "" when no middleware resolved one. Callers must treat "" as "resolve it
// yourself", never as a licence to pick a workspace.
func contextWorkspaceID(ctx context.Context) string {
	id, _ := ctx.Value(ctxKeyWorkspaceID).(string)
	return id
}

// chatAccessClaims holds the minimal JWT claims required by chat-service.
type chatAccessClaims struct {
	SessionID string `json:"sid"`
	jwt.RegisteredClaims
}

// TokenValidator validates HMAC-signed JWT access tokens issued by auth-service.
type TokenValidator struct {
	secret   []byte
	issuer   string
	audience string
}

// NewTokenValidator returns a TokenValidator. Returns an error if secret is
// shorter than 32 bytes, or if issuer/audience are empty.
func NewTokenValidator(secret, issuer, audience string) (*TokenValidator, error) {
	if len([]byte(secret)) < 32 {
		return nil, fmt.Errorf("jwt hmac secret must be at least 32 bytes")
	}
	if issuer == "" {
		return nil, fmt.Errorf("jwt issuer is required")
	}
	if audience == "" {
		return nil, fmt.Errorf("jwt audience is required")
	}
	return &TokenValidator{secret: []byte(secret), issuer: issuer, audience: audience}, nil
}

// validate parses and validates a raw JWT, returning (userID, sessionID).
func (v *TokenValidator) validate(raw string) (string, string, error) {
	var claims chatAccessClaims
	parser := jwt.NewParser(
		jwt.WithIssuer(v.issuer),
		jwt.WithAudience(v.audience),
		jwt.WithExpirationRequired(),
	)
	token, err := parser.ParseWithClaims(raw, &claims, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected jwt signing method")
		}
		return v.secret, nil
	})
	if err != nil {
		return "", "", err
	}
	if !token.Valid || claims.Subject == "" {
		return "", "", fmt.Errorf("invalid token claims")
	}
	// Enforce all claims required by the auth-service access-token contract.
	if claims.SessionID == "" {
		return "", "", fmt.Errorf("missing sid claim")
	}
	if claims.ID == "" {
		return "", "", fmt.Errorf("missing jti claim")
	}
	if claims.IssuedAt == nil {
		return "", "", fmt.Errorf("missing iat claim")
	}
	if claims.NotBefore == nil {
		return "", "", fmt.Errorf("missing nbf claim")
	}
	return claims.Subject, claims.SessionID, nil
}

// BearerAuth returns middleware that validates a Bearer JWT access token.
// On success the user ID and session ID are injected into the request context.
// Tokens passed via query string are explicitly ignored.
// On failure a generic 401 is returned without leaking token details.
// When validator is nil (JWT not configured) the middleware returns 503.
func BearerAuth(validator *TokenValidator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if validator == nil {
				httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "auth not configured")
				return
			}

			hdr := r.Header.Get("Authorization")
			if !strings.HasPrefix(hdr, bearerScheme) {
				httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
				return
			}
			raw := strings.TrimPrefix(hdr, bearerScheme)
			if raw == "" {
				httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
				return
			}

			userID, sessionID, err := validator.validate(raw)
			if err != nil {
				httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
				return
			}

			ctx := context.WithValue(r.Context(), ctxKeyUserID, userID)
			ctx = context.WithValue(ctx, ctxKeySessionID, sessionID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// SessionValidator validates a session ID against the backing auth store.
type SessionValidator interface {
	ValidateActiveSession(ctx context.Context, userID, sessionID string) error
}

// RequireActiveSession rejects requests whose session ID is absent, non-UUID,
// revoked, expired, or whose user is suspended/deleted in the auth store.
// Must be placed after BearerAuth (which injects userID and sessionID).
func RequireActiveSession(validator SessionValidator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if validator == nil {
				httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "session validation not configured")
				return
			}

			userID := GetContextUserID(r)
			if userID == "" {
				httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
				return
			}
			sessionID := GetContextSessionID(r)
			if sessionID == "" || !isValidUUID(sessionID) {
				httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
				return
			}

			if err := validator.ValidateActiveSession(r.Context(), userID, sessionID); err != nil {
				if errors.Is(err, domain.ErrInvalidToken) || errors.Is(err, domain.ErrNotFound) {
					httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
					return
				}
				httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
