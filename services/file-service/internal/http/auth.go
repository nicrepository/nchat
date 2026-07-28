package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/nicrepository/nchat/libs/go/platform/httputil"
)

const bearerScheme = "Bearer "

type principalContextKey struct{}

type fileAccessClaims struct {
	SessionID string `json:"sid"`
	jwt.RegisteredClaims
}

// Principal is the authenticated access-token state safe for downstream use.
type Principal struct {
	UserID          string
	SessionID       string
	AccessExpiresAt time.Time
}

type accessTokenValidator interface {
	ValidateAccessToken(rawToken string) (Principal, error)
}

// TokenValidator validates access tokens issued by auth-service. Token validity
// alone is never sufficient: every attachment route also revalidates the
// session and the caller's current membership in the same SQL query that
// resolves the resource.
type TokenValidator struct {
	secret   []byte
	issuer   string
	audience string
}

var _ accessTokenValidator = (*TokenValidator)(nil)

func NewTokenValidator(secret, issuer, audience string) (*TokenValidator, error) {
	if len([]byte(secret)) < 32 {
		return nil, fmt.Errorf("jwt hmac secret must be at least 32 bytes")
	}
	if strings.TrimSpace(issuer) == "" {
		return nil, fmt.Errorf("jwt issuer is required")
	}
	if strings.TrimSpace(audience) == "" {
		return nil, fmt.Errorf("jwt audience is required")
	}
	return &TokenValidator{secret: []byte(secret), issuer: issuer, audience: audience}, nil
}

func (v *TokenValidator) ValidateAccessToken(raw string) (Principal, error) {
	var claims fileAccessClaims
	parser := jwt.NewParser(
		jwt.WithIssuer(v.issuer),
		jwt.WithAudience(v.audience),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
	)
	token, err := parser.ParseWithClaims(raw, &claims, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected jwt signing method")
		}
		return v.secret, nil
	})
	if err != nil || !token.Valid {
		return Principal{}, fmt.Errorf("invalid access token")
	}
	if claims.SessionID == "" || claims.ID == "" || claims.IssuedAt == nil ||
		claims.NotBefore == nil || claims.ExpiresAt == nil {
		return Principal{}, fmt.Errorf("access token missing required claims")
	}
	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		return Principal{}, fmt.Errorf("invalid access token subject")
	}
	sessionID, err := uuid.Parse(claims.SessionID)
	if err != nil {
		return Principal{}, fmt.Errorf("invalid access token session")
	}
	return Principal{
		UserID:          userID.String(),
		SessionID:       sessionID.String(),
		AccessExpiresAt: claims.ExpiresAt.UTC(),
	}, nil
}

// BearerAuth validates the Authorization header and injects the principal. The
// header itself is never logged or copied anywhere beyond this function.
func BearerAuth(validator accessTokenValidator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if validator == nil {
				httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "auth not configured")
				return
			}
			raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), bearerScheme)
			if !ok || raw == "" {
				httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
				return
			}
			principal, err := validator.ValidateAccessToken(raw)
			if err != nil {
				httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
				return
			}
			ctx := context.WithValue(r.Context(), principalContextKey{}, principal)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// AuthenticatedPrincipal returns the principal injected by BearerAuth.
func AuthenticatedPrincipal(r *http.Request) (Principal, bool) {
	principal, ok := r.Context().Value(principalContextKey{}).(Principal)
	return principal, ok
}
