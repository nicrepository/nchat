package httpapi

import (
	"context"
	"net/http"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
)

// AuthorizationRecorder receives the denials worth keeping. Satisfied by
// service.AuditService.
type AuthorizationRecorder interface {
	Record(ctx context.Context, event domain.AuditEvent)
}

// RequireCapability declares, at the route, the one permission the endpoint
// needs. Every privileged route must be wrapped in one: an endpoint that names
// no capability is an endpoint nobody reviewed the authorization of.
//
// It fails closed at each step. No principal in context (guard misordered or
// missing) is a refusal, not a pass; an unknown capability is a refusal even
// for a superuser, so a typo here cannot accidentally match a grant.
//
// Reads are guarded exactly like writes. "It only lists things" is how an
// audit trail and a user directory become public.
func RequireCapability(capability domain.Capability, recorder AuthorizationRecorder) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			admin, ok := AdminFromContext(r)
			if !ok {
				httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
				return
			}
			if !admin.Principal.Capabilities.Has(capability) {
				recordDenial(r, recorder, admin.Principal.UserID, string(capability))
				httputil.WriteError(w, http.StatusForbidden, httputil.ErrCodeForbidden, "forbidden")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func recordDenial(r *http.Request, recorder AuthorizationRecorder, actorUserID string, capability string) {
	if recorder == nil {
		return
	}
	recorder.Record(r.Context(), domain.AuditEvent{
		ActorUserID:   actorUserID,
		Action:        domain.AuditActionAuthorizationDeny,
		Resource:      r.URL.Path,
		Result:        domain.AuditResultDenied,
		CorrelationID: httputil.RequestIDFromContext(r.Context()),
		Metadata: map[string]string{
			"capability": capability,
			"method":     r.Method,
		},
	})
}
