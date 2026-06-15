package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"github.com/nicrepository/nchat/libs/go/platform/httputil"
)

// ctxKey is the unexported context key type for this package.
type ctxKey int

const ctxKeyUserID ctxKey = iota

// GetContextUserID returns the authenticated user ID injected by BearerAuth,
// or "" if the request was not authenticated.
func GetContextUserID(r *http.Request) string {
	uid, _ := r.Context().Value(ctxKeyUserID).(string)
	return uid
}

// chatAccessClaims holds the minimal JWT claims required by chat-service.
// The `sid` claim is present in auth-service tokens; we parse but do not use it here.
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

func (v *TokenValidator) validate(raw string) (string, error) {
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
		return "", err
	}
	if !token.Valid || claims.Subject == "" {
		return "", fmt.Errorf("invalid token claims")
	}
	return claims.Subject, nil
}

// BearerAuth returns middleware that validates a Bearer JWT access token.
// On success the user ID is injected into the request context.
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
			if !strings.HasPrefix(hdr, "Bearer ") {
				httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
				return
			}
			raw := strings.TrimPrefix(hdr, "Bearer ")
			if raw == "" {
				httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
				return
			}

			userID, err := validator.validate(raw)
			if err != nil {
				httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
				return
			}

			ctx := context.WithValue(r.Context(), ctxKeyUserID, userID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
