package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/libs/go/platform/observability"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// RoutePresenceMe is the caller's own manual presence (issue #798). The path
// names no user because it cannot address anyone else: the actor is the
// session, and the workspace is resolved server-side like every chat route.
const RoutePresenceMe = "/api/chat/presence/me"

// presenceRateLimit bounds manual presence writes per user per minute. A human
// changes their status a handful of times a day; this leaves room for a menu
// clicked around in and nothing for a script.
const presenceRateLimit = 30

// presenceSettings is the PresenceService surface the handler uses.
type presenceSettings interface {
	Manual(ctx context.Context, workspaceID, userID string) (domain.PresenceOverride, error)
	SetManual(ctx context.Context, workspaceID, userID, state string, expiresAt time.Time) (domain.PresenceOverride, error)
	ClearManual(ctx context.Context, workspaceID, userID string) error
}

// PresenceHandler serves the caller's own manual presence.
type PresenceHandler struct {
	workspaces workspaceResolver
	settings   presenceSettings
	metrics    *presenceUpdateMetrics
	// writable is the CHAT_MANUAL_PRESENCE_ENABLED rollout gate (issue #798).
	// Closed, reads still answer — the stored state is honoured by every reader
	// in this build — and writes are refused, because a slot from before #798
	// could still be serving sessions that would never see the change.
	writable bool
}

func NewPresenceHandler(
	workspaces workspaceResolver, settings presenceSettings, metrics *observability.Metrics,
) *PresenceHandler {
	return &PresenceHandler{workspaces: workspaces, settings: settings, metrics: newPresenceUpdateMetrics(metrics)}
}

// WithWritesEnabled opens or closes the manual presence writer. Closed unless
// a deployment opens it.
func (h *PresenceHandler) WithWritesEnabled(enabled bool) *PresenceHandler {
	h.writable = enabled
	return h
}

// presenceSettingsJSON is the whole contract: the manual state and when it
// ends, or null for both when presence is automatic, and whether this
// deployment accepts a change to it.
type presenceSettingsJSON struct {
	State     *string    `json:"state"`
	ExpiresAt *time.Time `json:"expires_at"`
	Writable  bool       `json:"writable"`
}

// setPresenceRequest is what a client may ask for. There is no user id, no
// workspace and no timestamp other than the requested end: everything else is
// the server's.
type setPresenceRequest struct {
	State     string    `json:"state"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (h *PresenceHandler) presenceSettingsResponse(override domain.PresenceOverride) presenceSettingsJSON {
	if override.State == "" {
		return presenceSettingsJSON{Writable: h.writable}
	}
	state := string(override.State)
	expires := override.ExpiresAt.UTC()
	return presenceSettingsJSON{State: &state, ExpiresAt: &expires, Writable: h.writable}
}

// writer is actor plus the rollout gate, for the two routes that change state.
func (h *PresenceHandler) writer(w http.ResponseWriter, r *http.Request) (workspaceID, userID string, ok bool) {
	workspaceID, userID, ok = h.actor(w, r)
	if ok && !h.writable {
		httputil.WriteError(w, http.StatusServiceUnavailable, "manual_presence_unavailable",
			"manual presence is not enabled")
		return "", "", false
	}
	return workspaceID, userID, ok
}

// actor resolves the caller and their workspace, or writes the error.
func (h *PresenceHandler) actor(w http.ResponseWriter, r *http.Request) (workspaceID, userID string, ok bool) {
	if h == nil || h.settings == nil || h.workspaces == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "presence not available")
		return "", "", false
	}
	userID = GetContextUserID(r)
	if userID == "" {
		httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
		return "", "", false
	}
	workspaceID, ok = resolveRequestWorkspace(r.Context(), w, h.workspaces)
	return workspaceID, userID, ok
}

// Get handles GET /api/chat/presence/me.
func (h *PresenceHandler) Get(w http.ResponseWriter, r *http.Request) {
	workspaceID, userID, ok := h.actor(w, r)
	if !ok {
		return
	}
	override, err := h.settings.Manual(r.Context(), workspaceID, userID)
	if err != nil {
		mapServiceError(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, h.presenceSettingsResponse(override))
}

// Put handles PUT /api/chat/presence/me: the complete desired manual state.
func (h *PresenceHandler) Put(w http.ResponseWriter, r *http.Request) {
	workspaceID, userID, ok := h.writer(w, r)
	if !ok {
		return
	}
	var req setPresenceRequest
	if !decodeStrictJSON(w, r, &req) {
		return
	}
	override, err := h.settings.SetManual(r.Context(), workspaceID, userID, req.State, req.ExpiresAt)
	if err != nil {
		writePresenceWriteError(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, h.presenceSettingsResponse(override))
}

// Delete handles DELETE /api/chat/presence/me: back to automatic presence.
func (h *PresenceHandler) Delete(w http.ResponseWriter, r *http.Request) {
	workspaceID, userID, ok := h.writer(w, r)
	if !ok {
		return
	}
	if err := h.settings.ClearManual(r.Context(), workspaceID, userID); err != nil {
		writePresenceWriteError(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, presenceSettingsJSON{Writable: h.writable})
}

// writePresenceWriteError answers a refused write. A write that could not be
// announced to presence first was not made at all (issue #798): retryable,
// and nothing changed.
func writePresenceWriteError(w http.ResponseWriter, err error) {
	if errors.Is(err, domain.ErrPresenceFactsUnavailable) {
		httputil.WriteError(w, http.StatusServiceUnavailable, "presence_unavailable",
			"presence is temporarily unavailable; nothing was changed")
		return
	}
	mapServiceError(w, err)
}

// presenceUpdateMetrics counts manual presence writes and their latency by
// result. The only label is the result: no user, no state, no workspace.
type presenceUpdateMetrics struct {
	total    *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

func newPresenceUpdateMetrics(metrics *observability.Metrics) *presenceUpdateMetrics {
	total := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "chat_presence_manual_update_total",
		Help: "Manual presence writes by result.",
	}, []string{"result"})
	duration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "chat_presence_manual_update_duration_seconds",
		Help:    "Manual presence write duration by result.",
		Buckets: prometheus.DefBuckets,
	}, []string{"result"})
	if !metrics.Register(total, duration) {
		return &presenceUpdateMetrics{}
	}
	return &presenceUpdateMetrics{total: total, duration: duration}
}

// Middleware observes one write.
func (m *presenceUpdateMetrics) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &forwardStatusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		if m == nil || m.total == nil {
			return
		}
		result := presenceUpdateResult(recorder.status)
		m.total.WithLabelValues(result).Inc()
		m.duration.WithLabelValues(result).Observe(time.Since(started).Seconds())
	})
}

func presenceUpdateResult(status int) string {
	switch {
	case status < http.StatusMultipleChoices:
		return "success"
	case status == http.StatusBadRequest:
		return "invalid"
	case status == http.StatusForbidden || status == http.StatusUnauthorized:
		return "denied"
	case status == http.StatusTooManyRequests:
		return "rate_limited"
	default:
		return "error"
	}
}
