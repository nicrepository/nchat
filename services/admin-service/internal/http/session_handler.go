package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/admin-service/internal/config"
	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
	"github.com/nicrepository/nchat/services/admin-service/internal/service"
)

const errCodeUnavailable = "service_unavailable"

// AdminSessionManager is the session policy the handlers drive.
type AdminSessionManager interface {
	Establish(ctx context.Context, input service.EstablishInput) (service.EstablishedSession, error)
	Revoke(ctx context.Context, value string, reason string) error
	CSRFToken(sessionID string) string
	IdleTTL() time.Duration
}

// CreateAdminSession exchanges a proven NChat identity for an administrative
// session.
//
// This is the only place a chat access token is accepted by the Admin API, and
// it is accepted for one purpose: proving who is asking. It authorizes
// nothing. Whether that person may administer the platform is decided
// afterwards, from auth.admin_principals, and the answer is recorded in a
// server-side row — not in the token, which is never stored, never echoed and
// never becomes the console's credential.
func CreateAdminSession(sessions AdminSessionManager, audit AuthorizationRecorder, cfg config.Config, trustedProxyCIDRs []*net.IPNet) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sessions == nil {
			writeUnavailable(w)
			return
		}
		bearer, ok := bearerFromContext(r)
		if !ok {
			httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
			return
		}
		established, err := sessions.Establish(r.Context(), service.EstablishInput{
			UserID:        bearer.UserID,
			AuthSessionID: bearer.SessionID,
			IPAddress:     httputil.ClientIP(r, trustedProxyCIDRs),
			UserAgent:     r.UserAgent(),
		})
		if err != nil {
			recordSessionEvent(r, audit, bearer.UserID, domain.AuditActionSessionCreate, auditResultFor(err))
			writeDomainError(w, err)
			return
		}
		recordSessionEvent(r, audit, bearer.UserID, domain.AuditActionSessionCreate, domain.AuditResultSuccess)

		http.SetCookie(w, newSessionCookie(established.Value, sessions.IdleTTL()))
		httputil.WriteJSON(w, http.StatusCreated, newBootstrapPayload(cfg, domain.AuthenticatedAdmin{
			Session:   established.Session,
			Principal: established.Principal,
		}, established.CSRFToken))
	})
}

// DestroyAdminSession ends the administrative session and clears the cookie.
//
// It runs behind the session guard and behind the CSRF guard: logging an
// administrator out is a state change, and forcing it cross-site is a real
// (if mild) attack. The cookie is cleared even when the revocation fails, so a
// browser is never left holding a credential the operator asked to discard.
func DestroyAdminSession(sessions AdminSessionManager, audit AuthorizationRecorder, cfg config.Config, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sessions == nil {
			writeUnavailable(w)
			return
		}
		admin, ok := AdminFromContext(r)
		if !ok {
			httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
			return
		}
		presented, err := r.Cookie(sessionCookieName)
		result := domain.AuditResultSuccess
		if err == nil {
			if revokeErr := sessions.Revoke(r.Context(), presented.Value, "admin_logout"); revokeErr != nil {
				result = domain.AuditResultError
				// A logout the database did not record is the one failure here
				// worth waking someone for: the browser is told the session is
				// gone, and the row still says it is live until its own
				// deadlines expire it. The audit row already carries the
				// outcome; this line is what an operator finds when the audit
				// table is the thing that is unavailable.
				//
				// Only the identifiers stay: no cookie value, no Authorization
				// header, no error text from the driver — a driver error can
				// carry the DSN, and the DSN carries the database password.
				logger.Error("admin session revocation failed",
					"actor_user_id", admin.Principal.UserID,
					"admin_session_id", admin.Session.ID,
					"correlation_id", httputil.RequestIDFromContext(r.Context()),
				)
			}
		}
		recordSessionEvent(r, audit, admin.Principal.UserID, domain.AuditActionSessionDestroy, result)
		http.SetCookie(w, clearSessionCookie())
		w.WriteHeader(http.StatusNoContent)
	})
}

func recordSessionEvent(r *http.Request, audit AuthorizationRecorder, actorUserID string, action string, result domain.AuditResult) {
	if audit == nil {
		return
	}
	audit.Record(r.Context(), domain.AuditEvent{
		ActorUserID:   actorUserID,
		Action:        action,
		Resource:      "admin.session",
		Result:        result,
		CorrelationID: httputil.RequestIDFromContext(r.Context()),
		Metadata:      map[string]string{"method": r.Method},
	})
}

// auditResultFor separates "the platform refused this person" from "the
// platform broke". Both are worth a row; conflating them would hide an outage
// inside what looks like a stream of denials.
func auditResultFor(err error) domain.AuditResult {
	switch {
	case err == nil:
		return domain.AuditResultSuccess
	case errors.Is(err, domain.ErrForbidden), errors.Is(err, domain.ErrUnauthorized):
		return domain.AuditResultDenied
	default:
		return domain.AuditResultError
	}
}
