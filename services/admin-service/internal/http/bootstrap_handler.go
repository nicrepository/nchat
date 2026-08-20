package httpapi

import (
	"net/http"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/admin-service/internal/config"
)

// CSRFTokenIssuer derives the double-submit token bound to a session.
type CSRFTokenIssuer interface {
	CSRFToken(sessionID string) string
}

// Bootstrap is what the console shell loads before it renders anything.
//
// It carries the minimum the shell needs and nothing else: who is signed in,
// what they may do, which environment this is, and the build. It deliberately
// carries no configuration, no topology, no counts and no list of other
// people — a bootstrap that answered "how many users exist" would be a
// pre-authorization information leak on the one endpoint every session hits.
//
// The capability list here is presentation data. It tells the shell which
// navigation entries to render; it does not tell any endpoint what to allow.
func Bootstrap(cfg config.Config, csrf CSRFTokenIssuer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		admin, ok := AdminFromContext(r)
		if !ok {
			httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
			return
		}
		token := ""
		if csrf != nil {
			token = csrf.CSRFToken(admin.Session.ID)
		}
		httputil.WriteJSON(w, http.StatusOK, newBootstrapPayload(cfg, admin, token))
	})
}
