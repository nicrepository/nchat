package httpapi

import (
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

const meetingCandidateSearchRateLimit = 60

type meetingParticipantJSON struct {
	UserID    string             `json:"user_id"`
	Role      domain.MeetingRole `json:"role"`
	RSVP      domain.MeetingRSVP `json:"rsvp"`
	InvitedAt time.Time          `json:"invited_at"`
	UpdatedAt time.Time          `json:"updated_at"`
}

func mapMeetingParticipant(p domain.MeetingParticipant) meetingParticipantJSON {
	return meetingParticipantJSON{p.UserID, p.Role, p.RSVP, p.InvitedAt, p.UpdatedAt}
}

func writeMeetingRosterJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Cache-Control", "no-store")
	httputil.WriteJSON(w, status, data)
}

func (h *MeetingHandler) admitRoster(w http.ResponseWriter, r *http.Request, action string) (storage.MeetingScope, bool) {
	scope, ok := h.admit(w, r, action, channelRenameRateLimit)
	if !ok {
		return storage.MeetingScope{}, false
	}
	if !validateTargetID(w, r.PathValue("meetingID"), "meeting_id") {
		return storage.MeetingScope{}, false
	}
	scope.MeetingID = r.PathValue("meetingID")
	return scope, true
}

func parseMeetingPage(r *http.Request) (storage.MeetingPageQuery, error) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return storage.MeetingPageQuery{}, domain.ErrInvalidInput
	}
	q := storage.MeetingPageQuery{After: values.Get("cursor"), Search: values.Get("q")}
	for key, value := range values {
		if (key != "limit" && key != "cursor" && key != "q") || len(value) != 1 {
			return q, domain.ErrInvalidInput
		}
	}
	if values.Has("limit") {
		q.Limit, err = strconv.Atoi(values.Get("limit"))
		if err != nil || q.Limit < 1 {
			return q, domain.ErrInvalidInput
		}
	}
	return q.Normalize()
}

func (h *MeetingHandler) Participants(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.admitRoster(w, r, "meeting_roster")
	if !ok {
		return
	}
	q, err := parseMeetingPage(r)
	if err != nil || q.Search != "" {
		writeMeetingError(w, domain.ErrInvalidInput)
		return
	}
	page, err := h.meetings.ListMeetingParticipants(r.Context(), scope, q)
	if err != nil {
		writeMeetingError(w, err)
		return
	}
	items := make([]meetingParticipantJSON, 0, len(page.Items))
	for _, p := range page.Items {
		items = append(items, mapMeetingParticipant(p))
	}
	writeMeetingRosterJSON(w, http.StatusOK, struct {
		Items      []meetingParticipantJSON `json:"items"`
		NextCursor string                   `json:"next_cursor,omitempty"`
	}{items, page.NextCursor})
}

func (h *MeetingHandler) Candidates(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.admit(w, r, "meeting_candidates", meetingCandidateSearchRateLimit)
	if !ok {
		return
	}
	if !validateTargetID(w, r.PathValue("conversationID"), "conversation_id") {
		return
	}
	q, err := parseMeetingPage(r)
	if err != nil {
		writeMeetingError(w, err)
		return
	}
	page, err := h.meetings.ListMeetingCandidates(r.Context(), storage.MeetingCandidateScope{
		WorkspaceID: scope.WorkspaceID, ActorID: scope.ActorID,
		Conversation: domain.MeetingConversation{Kind: domain.MeetingConversationKind(r.PathValue("kind")), ID: r.PathValue("conversationID")},
	}, q)
	if err != nil {
		writeMeetingError(w, err)
		return
	}
	writeMeetingRosterJSON(w, http.StatusOK, page)
}

func (h *MeetingHandler) AddParticipant(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.admitRoster(w, r, "meeting_participant_add")
	if !ok {
		return
	}
	var request *meetingInviteeJSON
	if !decodeStrictJSON(w, r, &request) {
		return
	}
	if request == nil {
		writeMeetingError(w, domain.ErrInvalidInput)
		return
	}
	if !validateTargetID(w, request.UserID, "user_id") {
		return
	}
	p, err := h.meetings.AddMeetingParticipant(r.Context(), scope, request.UserID, request.Role)
	if err != nil {
		writeMeetingError(w, err)
		return
	}
	writeMeetingRosterJSON(w, http.StatusCreated, mapMeetingParticipant(p))
}

func (h *MeetingHandler) ChangeParticipantRole(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.admitRoster(w, r, "meeting_participant_role")
	if !ok {
		return
	}
	if !validateTargetID(w, r.PathValue("userID"), "user_id") {
		return
	}
	var request *struct {
		Role domain.MeetingRole `json:"role"`
	}
	if !decodeStrictJSON(w, r, &request) {
		return
	}
	if request == nil {
		writeMeetingError(w, domain.ErrInvalidInput)
		return
	}
	p, err := h.meetings.ChangeMeetingParticipantRole(r.Context(), scope, r.PathValue("userID"), request.Role)
	if err != nil {
		writeMeetingError(w, err)
		return
	}
	writeMeetingRosterJSON(w, http.StatusOK, mapMeetingParticipant(p))
}

func (h *MeetingHandler) RemoveParticipant(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.admitRoster(w, r, "meeting_participant_remove")
	if !ok {
		return
	}
	if !validateTargetID(w, r.PathValue("userID"), "user_id") {
		return
	}
	if err := h.meetings.RemoveMeetingParticipant(r.Context(), scope, r.PathValue("userID")); err != nil {
		writeMeetingError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (h *MeetingHandler) InviteAllParticipants(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.admitRoster(w, r, "meeting_participants_all")
	if !ok {
		return
	}
	var request *struct{}
	if !decodeStrictJSON(w, r, &request) {
		return
	}
	if request == nil {
		writeMeetingError(w, domain.ErrInvalidInput)
		return
	}
	added, err := h.meetings.InviteAllMeetingParticipants(r.Context(), scope)
	if err != nil {
		writeMeetingError(w, err)
		return
	}
	writeMeetingRosterJSON(w, http.StatusOK, struct {
		Added int64 `json:"added"`
	}{added})
}
