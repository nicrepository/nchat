package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
)

const adminTokenHeader = "X-NChat-Admin-Token" //nolint:gosec

// AdminBootstrapGuard is a temporary dev-only guard — NOT final RBAC.
// Returns 503 if the bootstrap token is not configured in the environment.
// Returns 401 if the X-NChat-Admin-Token header is missing or wrong.
// The provided token is never logged.
func AdminBootstrapGuard(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if token == "" {
				httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "admin endpoint disabled: ADMIN_BOOTSTRAP_TOKEN not set")
				return
			}
			provided := r.Header.Get(adminTokenHeader)
			if !tokensEqual(provided, token) {
				httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func tokensEqual(provided string, expected string) bool {
	providedHash := sha256.Sum256([]byte(provided))
	expectedHash := sha256.Sum256([]byte(expected))
	return subtle.ConstantTimeCompare(providedHash[:], expectedHash[:]) == 1
}
