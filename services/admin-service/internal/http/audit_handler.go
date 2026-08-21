package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
)

// AuditReader lists the administrative audit trail.
type AuditReader interface {
	List(ctx context.Context, limit int) ([]domain.AuditEntry, error)
}

type auditEntryPayload struct {
	ID            string    `json:"id"`
	OccurredAt    time.Time `json:"occurred_at"`
	ActorUserID   string    `json:"actor_user_id"`
	ActorEmail    string    `json:"actor_email"`
	Action        string    `json:"action"`
	Resource      string    `json:"resource"`
	Result        string    `json:"result"`
	CorrelationID string    `json:"correlation_id"`
}

// ListAuditEvents serves the administrative audit trail.
//
// It is a read, and it is guarded by admin.audit.read exactly like a write
// would be: the trail names who did what to whom, so exposing it to any
// authenticated administrator would be a privilege escalation dressed as a
// listing.
//
// There is no resource identifier in the request. The endpoint returns the
// platform-wide trail or refuses; there is no per-object path segment to
// tamper with, which is what keeps an IDOR out of this surface by
// construction rather than by validation.
func ListAuditEvents(audit AuditReader) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if audit == nil {
			writeUnavailable(w)
			return
		}
		limit, ok := parseLimit(r.URL.Query().Get("limit"))
		if !ok {
			httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "limit must be a positive integer")
			return
		}
		entries, err := audit.List(r.Context(), limit)
		if err != nil {
			writeAuthError(w, err)
			return
		}
		payload := make([]auditEntryPayload, 0, len(entries))
		for _, entry := range entries {
			payload = append(payload, auditEntryPayload{
				ID:            strconv.FormatInt(entry.ID, 10),
				OccurredAt:    entry.OccurredAt,
				ActorUserID:   entry.ActorUserID,
				ActorEmail:    entry.ActorEmail,
				Action:        entry.Action,
				Resource:      entry.Resource,
				Result:        string(entry.Result),
				CorrelationID: entry.CorrelationID,
			})
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"events": payload})
	})
}

// parseLimit accepts an absent parameter as "use the default" and refuses
// anything that is not a positive integer. The upper bound is applied by the
// service, so a caller cannot widen the page by sending a larger number.
func parseLimit(raw string) (int, bool) {
	if raw == "" {
		return 0, true
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit <= 0 {
		return 0, false
	}
	return limit, true
}
