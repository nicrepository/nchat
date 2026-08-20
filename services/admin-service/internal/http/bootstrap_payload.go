package httpapi

import (
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/buildinfo"
	"github.com/nicrepository/nchat/services/admin-service/internal/config"
	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
)

// bootstrapPayload is the entire contract between the Admin API and the
// console shell.
//
// It is an allowlist, not a filter: the struct names every field that may
// leave this service, so a value can only be exposed by someone adding it here
// deliberately. Nothing is spread in from configuration, from the environment
// or from a database row, which is what keeps an access token, a refresh
// token, the bootstrap token, a DSN, a client secret or a Kubernetes
// credential from arriving by accident when one of those is added elsewhere.
type bootstrapPayload struct {
	Identity     identityPayload `json:"identity"`
	Capabilities []string        `json:"capabilities"`
	Environment  string          `json:"environment"`
	Build        buildPayload    `json:"build"`
	Session      sessionPayload  `json:"session"`
	// CSRFToken is not a credential: it authorizes nothing on its own and is
	// useless without the HttpOnly session cookie. It is returned in the body
	// precisely because a cross-site attacker cannot read a response body.
	CSRFToken string `json:"csrf_token"`
}

type identityPayload struct {
	UserID      string `json:"user_id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	AvatarURL   string `json:"avatar_url"`
}

type buildPayload struct {
	Service string `json:"service"`
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

type sessionPayload struct {
	IdleExpiresAt     time.Time `json:"idle_expires_at"`
	AbsoluteExpiresAt time.Time `json:"absolute_expires_at"`
}

func newBootstrapPayload(cfg config.Config, admin domain.AuthenticatedAdmin, csrfToken string) bootstrapPayload {
	info := buildinfo.Current()
	capabilities := admin.Principal.Capabilities.Effective()
	names := make([]string, 0, len(capabilities))
	for _, capability := range capabilities {
		names = append(names, string(capability))
	}
	return bootstrapPayload{
		Identity: identityPayload{
			UserID:      admin.Principal.UserID,
			Email:       admin.Principal.Email,
			DisplayName: admin.Principal.DisplayName,
			AvatarURL:   admin.Principal.AvatarURL,
		},
		Capabilities: names,
		// The environment is read from the deployment's own configuration.
		// The request cannot influence it: no header, host, query parameter or
		// stored preference participates in this value.
		Environment: string(cfg.Environment()),
		Build: buildPayload{
			Service: cfg.ServiceName,
			Version: info.Version,
			Commit:  info.Commit,
		},
		Session: sessionPayload{
			IdleExpiresAt:     admin.Session.IdleExpiresAt.UTC(),
			AbsoluteExpiresAt: admin.Session.AbsoluteExpiresAt.UTC(),
		},
		CSRFToken: csrfToken,
	}
}
