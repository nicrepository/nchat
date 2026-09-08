package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/notification-service/internal/domain"
)

// bearerScheme is the Authorization header scheme from RFC 6750.
const bearerScheme = "Bearer "

type principalContextKey struct{}

// accessClaims is the minimal access-token contract auth-service issues. The
// same shape media-service and chat-service already validate; the signing
// method, issuer, audience and required claims are checked identically, so one
// service cannot end up accepting a token another would refuse.
type accessClaims struct {
	SessionID string `json:"sid"`
	jwt.RegisteredClaims
}

// tokenIdentity is what a valid token asserts. It is a claim and not a
// conclusion: the session still has to exist in the database before any of it
// authorises anything.
type tokenIdentity struct {
	UserID    string
	SessionID string
}

type accessTokenValidator interface {
	ValidateAccessToken(rawToken string) (tokenIdentity, error)
}

// PrincipalResolver turns a token's identity into the server-derived actor and
// workspace. Implemented by storage.PGXPrincipalResolver.
type PrincipalResolver interface {
	Resolve(ctx context.Context, userID, sessionID string) (domain.Principal, error)
}

// TokenValidator validates HMAC-signed access tokens issued by auth-service.
type TokenValidator struct {
	secret   []byte
	issuer   string
	audience string
}

var _ accessTokenValidator = (*TokenValidator)(nil)

// NewTokenValidator returns a validator, or an error when the configuration
// cannot support one.
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

// ValidateAccessToken parses and checks one raw token.
func (v *TokenValidator) ValidateAccessToken(raw string) (tokenIdentity, error) {
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
		return tokenIdentity{}, fmt.Errorf("invalid access token")
	}
	return identityFromClaims(claims)
}

// identityFromClaims enforces the claims the contract requires and canonicalises
// the two identifiers.
//
// Parsing sub and sid as UUIDs is not decoration: both are interpolated into a
// query as uuid, and a value that is not one would surface as a database error
// rather than as the 401 it is.
func identityFromClaims(claims accessClaims) (tokenIdentity, error) {
	if claims.SessionID == "" || claims.ID == "" ||
		claims.IssuedAt == nil || claims.NotBefore == nil || claims.ExpiresAt == nil {
		return tokenIdentity{}, fmt.Errorf("access token missing required claims")
	}
	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		return tokenIdentity{}, fmt.Errorf("invalid access token subject")
	}
	sessionID, err := uuid.Parse(claims.SessionID)
	if err != nil {
		return tokenIdentity{}, fmt.Errorf("invalid access token session")
	}
	return tokenIdentity{UserID: userID.String(), SessionID: sessionID.String()}, nil
}

// Authenticate validates the bearer token and resolves the caller's principal
// against the database, injecting it into the request context.
//
// Both halves are required and neither substitutes for the other. The signature
// proves the token was issued; only the database knows whether that session was
// since revoked, has expired, or belongs to a user who has been suspended — and
// only it can say which workspace the caller is actually a member of.
//
// Neither absence nor failure is ever an allow. A missing validator or resolver
// — a partial wiring, which the production path does not produce — refuses every
// request with 503; a resolver that is present but cannot answer surfaces as
// 500 through writePushError. What never happens is a request proceeding
// unauthenticated.
func Authenticate(validator accessTokenValidator, resolver PrincipalResolver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authenticate(w, r, validator, resolver, next)
		})
	}
}

// authenticate is Authenticate's body, named rather than nested three closures
// deep so the decision it makes is readable on its own.
func authenticate(
	w http.ResponseWriter, r *http.Request,
	validator accessTokenValidator, resolver PrincipalResolver, next http.Handler,
) {
	if validator == nil || resolver == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable,
			"service_unavailable", "authentication not configured")
		return
	}
	identity, ok := validatedIdentity(w, r, validator)
	if !ok {
		return
	}
	principal, err := resolver.Resolve(r.Context(), identity.UserID, identity.SessionID)
	if err != nil {
		writePushError(w, err)
		return
	}
	next.ServeHTTP(w, r.WithContext(
		context.WithValue(r.Context(), principalContextKey{}, principal)))
}

// validatedIdentity reads and validates the Authorization header, writing the
// generic 401 itself when it cannot.
//
// A token supplied any other way — a query parameter, a cookie — is not read at
// all: those are logged by proxies and attached by browsers to requests the user
// did not make.
func validatedIdentity(
	w http.ResponseWriter, r *http.Request, validator accessTokenValidator,
) (tokenIdentity, bool) {
	raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), bearerScheme)
	if !ok || raw == "" {
		writeUnauthorized(w)
		return tokenIdentity{}, false
	}
	identity, err := validator.ValidateAccessToken(raw)
	if err != nil {
		// Nothing about why. A caller learns that the token was refused, never
		// whether the signature, the audience or the expiry was the reason.
		writeUnauthorized(w)
		return tokenIdentity{}, false
	}
	return identity, true
}

func writeUnauthorized(w http.ResponseWriter) {
	httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
}

// authenticatedPrincipal returns the principal Authenticate injected.
func authenticatedPrincipal(r *http.Request) (domain.Principal, bool) {
	principal, ok := r.Context().Value(principalContextKey{}).(domain.Principal)
	return principal, ok
}

// requirePrincipal is the handler-side guard.
//
// It can only fail if a route were ever mounted without Authenticate, and that
// is exactly why it is here: the failure is a 401 rather than a handler running
// with an empty user and workspace, which would read every row of neither.
func requirePrincipal(w http.ResponseWriter, r *http.Request) (domain.Principal, bool) {
	principal, ok := authenticatedPrincipal(r)
	if !ok || principal.UserID == "" || principal.WorkspaceID == "" {
		writeUnauthorized(w)
		return domain.Principal{}, false
	}
	return principal, true
}

// Error codes this feature adds to the shared set in httputil.
const (
	errCodeEndpointConflict = "push_endpoint_conflict"
	errCodeUnsupportedMedia = "unsupported_media_type"
)

// writePushError maps a domain error onto the response.
//
// Every message is fixed text chosen here, and none of them repeats anything the
// caller sent: no endpoint, no key, no identifier. ErrNotFound covers both "no
// such subscription" and "not yours" because telling those apart is how one user
// enumerates another's.
func writePushError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidRegistration):
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest,
			"invalid push subscription")
	case errors.Is(err, domain.ErrUnauthenticated):
		writeUnauthorized(w)
	case errors.Is(err, domain.ErrForbidden):
		httputil.WriteError(w, http.StatusForbidden, httputil.ErrCodeForbidden, "forbidden")
	case errors.Is(err, domain.ErrNotFound):
		httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "not found")
	case errors.Is(err, domain.ErrEndpointConflict):
		httputil.WriteError(w, http.StatusConflict, errCodeEndpointConflict,
			"this push endpoint is already registered")
	default:
		httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal,
			"internal error")
	}
}
