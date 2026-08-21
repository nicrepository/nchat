package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
)

const bearerScheme = "Bearer "

type bearerContextKey struct{}

// BearerPrincipal is the proven NChat identity behind an access token. It is
// used for exactly one thing: the administrative session handshake. It never
// authorizes a privileged read or write on its own.
type BearerPrincipal struct {
	UserID    string
	SessionID string
}

type accessClaims struct {
	SessionID string `json:"sid"`
	jwt.RegisteredClaims
}

type accessTokenValidator interface {
	ValidateAccessToken(raw string) (BearerPrincipal, error)
}

// TokenValidator verifies access tokens issued by auth-service.
//
// It is a verifier, not an issuer: it holds the shared HMAC secret to check a
// signature and nothing here can mint a token. Issuer and audience are pinned
// so a token minted for another audience by the same secret is refused.
type TokenValidator struct {
	secret   []byte
	issuer   string
	audience string
}

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

func (v *TokenValidator) ValidateAccessToken(raw string) (BearerPrincipal, error) {
	var claims accessClaims
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
		return BearerPrincipal{}, fmt.Errorf("invalid access token")
	}
	if claims.ID == "" || claims.IssuedAt == nil || claims.NotBefore == nil || claims.ExpiresAt == nil {
		return BearerPrincipal{}, fmt.Errorf("access token missing required claims")
	}
	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		return BearerPrincipal{}, fmt.Errorf("invalid access token subject")
	}
	sessionID, err := uuid.Parse(claims.SessionID)
	if err != nil {
		return BearerPrincipal{}, fmt.Errorf("invalid access token session")
	}
	return BearerPrincipal{UserID: userID.String(), SessionID: sessionID.String()}, nil
}

// BearerAuth guards the session handshake. It proves who is asking; whether
// that person may administer anything is decided afterwards, against the
// database.
func BearerAuth(validator accessTokenValidator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if validator == nil {
				writeUnavailable(w)
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
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), bearerContextKey{}, principal)))
		})
	}
}

// bearerFromContext returns the identity BearerAuth proved, or false when the
// guard did not run.
func bearerFromContext(r *http.Request) (BearerPrincipal, bool) {
	principal, ok := r.Context().Value(bearerContextKey{}).(BearerPrincipal)
	return principal, ok
}
