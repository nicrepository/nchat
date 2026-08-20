package httpapi

import (
	"net/http"
	"strings"
)

// CORS answers cross-origin preflights for the administrative browser origin.
//
// The allowlist is explicit and finite. `*` is never emitted, and cannot be:
// the configuration loader drops it, and this middleware only ever echoes an
// origin it matched literally. That matters more than usual here because the
// Admin API is credentialed — a wildcard alongside
// Access-Control-Allow-Credentials is a specification error browsers reject,
// and the combination is exactly what would let any page on the internet drive
// the console with the operator's own session.
//
// An empty allowlist emits nothing at all, which is the correct answer for the
// deployed topology: the console and the API share a host, so no request is
// cross-origin and no header is needed to permit one.
func CORS(allowedOrigins []string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := strings.TrimSpace(r.Header.Get("Origin"))
			allowed := origin != "" && matchOrigin(origin, allowedOrigins)
			if allowed {
				header := w.Header()
				header.Set("Access-Control-Allow-Origin", origin)
				header.Set("Access-Control-Allow-Credentials", "true")
				header.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, "+csrfHeaderName)
				header.Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
				header.Set("Access-Control-Max-Age", "600")
				header.Add("Vary", "Origin")
			}
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				status := http.StatusNoContent
				if !allowed {
					status = http.StatusForbidden
				}
				w.WriteHeader(status)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func matchOrigin(origin string, allowedOrigins []string) bool {
	for _, allowed := range allowedOrigins {
		if strings.EqualFold(origin, allowed) {
			return true
		}
	}
	return false
}
