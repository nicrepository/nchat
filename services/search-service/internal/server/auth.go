package server

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/search-service/internal/domain"
)

type accessClaims struct {
	SessionID string `json:"sid"`
	jwt.RegisteredClaims
}
type AccessTokenValidator interface {
	ValidateAccessToken(string) (Principal, error)
}
type TokenValidator struct {
	secret           []byte
	issuer, audience string
}

func NewTokenValidator(secret, issuer, audience string) (*TokenValidator, error) {
	if len([]byte(secret)) < 32 {
		return nil, errors.New("jwt hmac secret must be at least 32 bytes")
	}
	if strings.TrimSpace(issuer) == "" || strings.TrimSpace(audience) == "" {
		return nil, errors.New("jwt issuer and audience are required")
	}
	return &TokenValidator{[]byte(secret), issuer, audience}, nil
}
func (v *TokenValidator) ValidateAccessToken(raw string) (Principal, error) {
	var c accessClaims
	p := jwt.NewParser(jwt.WithIssuer(v.issuer), jwt.WithAudience(v.audience), jwt.WithExpirationRequired(), jwt.WithIssuedAt())
	tok, err := p.ParseWithClaims(raw, &c, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, errors.New("unexpected signing method")
		}
		return v.secret, nil
	})
	if err != nil || !tok.Valid || c.ID == "" || c.IssuedAt == nil || c.NotBefore == nil || c.ExpiresAt == nil {
		return Principal{}, errors.New("invalid token")
	}
	u, e := uuid.Parse(c.Subject)
	if e != nil {
		return Principal{}, errors.New("invalid subject")
	}
	s, e := uuid.Parse(c.SessionID)
	if e != nil {
		return Principal{}, errors.New("invalid session")
	}
	return Principal{UserID: u.String(), SessionID: s.String()}, nil
}

func BearerAuth(v AccessTokenValidator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if v == nil {
				httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "auth not configured")
				return
			}
			raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || raw == "" {
				httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
				return
			}
			p, err := v.ValidateAccessToken(raw)
			if err != nil {
				httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
		})
	}
}

type SessionValidator interface {
	ValidateActiveSession(context.Context, string, string) error
}

func RequireActiveSession(v SessionValidator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := authenticatedPrincipal(r)
			if !ok || p.UserID == "" || p.SessionID == "" {
				httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
				return
			}
			if v == nil {
				httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "session validation not configured")
				return
			}
			if err := v.ValidateActiveSession(r.Context(), p.UserID, p.SessionID); err != nil {
				if errors.Is(err, domain.ErrUnauthorized) {
					httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
				} else {
					httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
				}
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
