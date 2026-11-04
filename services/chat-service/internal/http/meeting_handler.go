package httpapi

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

type meetingProvider interface {
	ListMeetingParticipants(context.Context, storage.MeetingScope, storage.MeetingPageQuery) (storage.MeetingParticipantPage, error)
	ListMeetingCandidates(context.Context, storage.MeetingCandidateScope, storage.MeetingPageQuery) (storage.MeetingCandidatePage, error)
	AddMeetingParticipant(context.Context, storage.MeetingScope, string, domain.MeetingRole) (domain.MeetingParticipant, error)
	ChangeMeetingParticipantRole(context.Context, storage.MeetingScope, string, domain.MeetingRole) (domain.MeetingParticipant, error)
	RemoveMeetingParticipant(context.Context, storage.MeetingScope, string) error
	InviteAllMeetingParticipants(context.Context, storage.MeetingScope) (int64, error)
	CreateMeeting(context.Context, service.CreateMeetingInput) (service.CreateMeetingResult, error)
	UpdateMeeting(context.Context, service.UpdateMeetingInput) (domain.Meeting, error)
	CancelMeeting(context.Context, storage.MeetingScope, int64) (domain.Meeting, error)
}

type MeetingHandler struct {
	workspaces workspaceResolver
	meetings   meetingProvider
	limiter    actionRateLimiter
}

func NewMeetingHandler(workspaces workspaceResolver, meetings meetingProvider, limiter actionRateLimiter) *MeetingHandler {
	return &MeetingHandler{workspaces: workspaces, meetings: meetings, limiter: limiter}
}

func (h *MeetingHandler) Create(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.admit(w, r, "meeting_create", channelCreateRateLimit)
	if !ok {
		return
	}
	key, ok := parseIdempotencyKey(w, r)
	if !ok {
		return
	}
	if key == "" || r.Header.Get("Idempotency-Key") != key {
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "Idempotency-Key is required")
		return
	}
	var request createMeetingRequest
	if !decodeStrictJSON(w, r, &request) {
		return
	}
	result, err := h.meetings.CreateMeeting(r.Context(), request.input(scope, key))
	if err != nil {
		writeMeetingError(w, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
		w.Header().Set("Idempotency-Replayed", "true")
	}
	w.Header().Set("Location", "/api/chat/meetings/"+result.Meeting.ID)
	writeMeeting(w, status, result.Meeting)
}

func (h *MeetingHandler) Update(w http.ResponseWriter, r *http.Request) {
	scope, version, ok := h.admitMutation(w, r, "meeting_update")
	if !ok {
		return
	}
	var request *patchMeetingRequest
	if !decodeStrictJSON(w, r, &request) {
		return
	}
	if request == nil {
		writeMeetingError(w, domain.ErrInvalidInput)
		return
	}
	m, err := h.meetings.UpdateMeeting(r.Context(), service.UpdateMeetingInput{
		Scope: scope, ExpectedVersion: version, Patch: request.patch(),
	})
	if err != nil {
		writeMeetingError(w, err)
		return
	}
	writeMeeting(w, http.StatusOK, m)
}

func (h *MeetingHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	scope, version, ok := h.admitMutation(w, r, "meeting_cancel")
	if !ok {
		return
	}
	// A strict empty object keeps cancel from acquiring mutation fields later by accident.
	var request *struct{}
	if !decodeStrictJSON(w, r, &request) {
		return
	}
	if request == nil {
		writeMeetingError(w, domain.ErrInvalidInput)
		return
	}
	m, err := h.meetings.CancelMeeting(r.Context(), scope, version)
	if err != nil {
		writeMeetingError(w, err)
		return
	}
	writeMeeting(w, http.StatusOK, m)
}

func (h *MeetingHandler) admitMutation(w http.ResponseWriter, r *http.Request, action string) (storage.MeetingScope, int64, bool) {
	scope, ok := h.admit(w, r, action, channelRenameRateLimit)
	if !ok {
		return storage.MeetingScope{}, 0, false
	}
	if !validateTargetID(w, r.PathValue("meetingID"), "meeting_id") {
		return storage.MeetingScope{}, 0, false
	}
	scope.MeetingID = r.PathValue("meetingID")
	version, ok := parseMeetingVersion(w, r)
	return scope, version, ok
}

func (h *MeetingHandler) admit(w http.ResponseWriter, r *http.Request, action string, limit int) (storage.MeetingScope, bool) {
	w.Header().Set("Cache-Control", "no-store")
	if h.workspaces == nil || h.meetings == nil || h.limiter == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "meetings not available")
		return storage.MeetingScope{}, false
	}
	actor := GetContextUserID(r)
	if actor == "" {
		writeUnauthorized(w)
		return storage.MeetingScope{}, false
	}
	allowed, err := h.limiter.AllowActionWithLimit(r.Context(), actor, action, limit, channelRateLimitWindowSeconds)
	if err != nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "meetings not available")
		return storage.MeetingScope{}, false
	}
	if !allowed {
		w.Header().Set("Retry-After", "60")
		httputil.WriteError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
		return storage.MeetingScope{}, false
	}
	if !meetingContentTypeAllowed(w, r) {
		return storage.MeetingScope{}, false
	}
	workspace, err := h.workspaces.GetDefaultWorkspace(r.Context())
	if err != nil {
		writeMeetingError(w, err)
		return storage.MeetingScope{}, false
	}
	return storage.MeetingScope{WorkspaceID: workspace.ID, ActorID: actor}, true
}

var meetingETagPattern = regexp.MustCompile(`^"[1-9][0-9]{0,18}"$`)

func parseMeetingVersion(w http.ResponseWriter, r *http.Request) (int64, bool) {
	values := r.Header.Values("If-Match")
	if len(values) == 0 {
		httputil.WriteError(w, http.StatusPreconditionRequired, "precondition_required", "If-Match is required")
		return 0, false
	}
	if len(values) != 1 || !meetingETagPattern.MatchString(values[0]) {
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid If-Match")
		return 0, false
	}
	version, err := strconv.ParseInt(values[0][1:len(values[0])-1], 10, 64)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid If-Match")
		return 0, false
	}
	return version, true
}

func writeMeeting(w http.ResponseWriter, status int, m domain.Meeting) {
	w.Header().Set("ETag", `"`+strconv.FormatInt(m.Version, 10)+`"`)
	w.Header().Set("Cache-Control", "no-store")
	httputil.WriteJSON(w, status, mapMeetingJSON(m))
}

func writeMeetingError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "not found")
	case errors.Is(err, domain.ErrForbidden):
		httputil.WriteError(w, http.StatusForbidden, httputil.ErrCodeForbidden, "forbidden")
	case errors.Is(err, domain.ErrMeetingStaleVersion):
		httputil.WriteError(w, http.StatusPreconditionFailed, "stale_version", "meeting version is stale")
	case errors.Is(err, domain.ErrIdempotencyKeyReused):
		httputil.WriteError(w, http.StatusConflict, "idempotency_key_reused", "idempotency key already used for a different request")
	case errors.Is(err, domain.ErrConflict):
		httputil.WriteError(w, http.StatusConflict, httputil.ErrCodeConflict, "meeting conflict")
	case errors.Is(err, domain.ErrInvalidInput):
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid meeting")
	default:
		httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
	}
}

func meetingContentTypeAllowed(w http.ResponseWriter, r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodDelete:
		return true
	default:
		return requireJSONContentType(w, r)
	}
}
