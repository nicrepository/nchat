package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
)

type adminContextKey struct{}

// AdminAuthenticator resolves a cookie value into a live administrative
// session plus the principal's current capabilities.
type AdminAuthenticator interface {
	Authenticate(ctx context.Context, value string) (domain.AuthenticatedAdmin, error)
}

// RequireAdminSession is the entry guard of every privileged route.
//
// What it does *not* accept is as important as what it does: no Authorization
// header, no query parameter, no request body and no header of any kind can
// stand in for the cookie. The credential is the HttpOnly cookie and nothing
// else, so a script that has been injected into the page cannot read it, and a
// caller cannot present a capability list of its own choosing.
//
// It re-authorizes on every request. There is no cached principal and no role
// claim inside the cookie: the cookie identifies a row, and the row is joined
// against the live session and the live role bindings each time. A revoked
// login, a suspended principal or a removed role therefore takes effect on the
// very next request.
func RequireAdminSession(authenticator AdminAuthenticator, cookie string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if authenticator == nil {
				writeUnavailable(w)
				return
			}
			presented, err := r.Cookie(cookie)
			if err != nil || presented.Value == "" {
				httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
				return
			}
			admin, err := authenticator.Authenticate(r.Context(), presented.Value)
			if err != nil {
				writeAuthError(w, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), adminContextKey{}, admin)))
		})
	}
}

// AdminFromContext returns the authenticated administrator, or false when the
// guard did not run. Handlers treat false as a refusal, never as "anyone".
func AdminFromContext(r *http.Request) (domain.AuthenticatedAdmin, bool) {
	admin, ok := r.Context().Value(adminContextKey{}).(domain.AuthenticatedAdmin)
	return admin, ok
}

// writeAuthError maps the domain errors onto the two answers a client is
// allowed to tell apart, and nothing more granular: 401 means "prove who you
// are again", 403 means "you are known and still not allowed". Neither body
// says which of the several possible reasons applied.
func writeAuthError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrUnauthorized):
		httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
	case errors.Is(err, domain.ErrForbidden):
		httputil.WriteError(w, http.StatusForbidden, httputil.ErrCodeForbidden, "forbidden")
	case errors.Is(err, domain.ErrUnavailable):
		writeUnavailable(w)
	default:
		httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
	}
}

func writeUnavailable(w http.ResponseWriter) {
	httputil.WriteError(w, http.StatusServiceUnavailable, errCodeUnavailable, "admin api unavailable")
}
