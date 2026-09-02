package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// channelProvider is the ChannelService surface used by ChannelHandler.
type channelProvider interface {
	CreateChannel(ctx context.Context, input service.CreateChannelInput) (domain.Channel, error)
	GetChannelDetails(ctx context.Context, input service.ChannelDetailsInput) (service.ChannelDetails, error)
	// GetCallParticipantProfiles resolves presentation identities for a set
	// of call-participant user IDs, scoped to this channel (issue #612).
	GetCallParticipantProfiles(ctx context.Context, input service.ChannelCallParticipantProfilesInput) ([]domain.CallParticipantProfile, error)
	// UpdateChannel edits one channel's mutable fields (issue #527). The Rename
	// route forwards only DisplayName; every other field is left "unchanged".
	// The result carries the system message the same transaction wrote.
	UpdateChannel(ctx context.Context, input service.UpdateChannelInput) (storage.UpdateChannelResult, error)
	// LeaveChannel removes the caller's own membership (issue #527). Self-leave
	// only: there is no target user, here or anywhere below it.
	LeaveChannel(ctx context.Context, workspaceID, channelID, callerID string) (storage.LeaveConversationResult, error)
}

// channelUpdateBroadcaster publishes the post-commit "this channel's metadata
// changed" signal (issue #527). Its own interface rather than a method on
// membersBroadcaster: that one is shared with DMHandler, and a group has no
// channel to update. app.go adapts the hub to both.
type channelUpdateBroadcaster interface {
	// PublishConversationUpdated invalidates a renamed conversation for its
	// subscribers; PublishConversationEvent announces one persisted system
	// message. Both are route-only — the client reads the change back through
	// the authorized endpoints (issue #527).
	PublishConversationUpdated(ctx context.Context, workspaceID, targetType, targetID string)
	PublishConversationEvent(ctx context.Context, workspaceID, targetType, targetID, messageID string)
}

// presenceLookup answers "who in this workspace is online right now" for the
// details panel. It is declared here, as the narrowest thing this handler needs,
// so the HTTP layer never imports the WebSocket package: app.go adapts the hub's
// tracker to it.
//
// It is deliberately a batch question rather than a per-user one. The panel
// needs the online set *before* the database applies its preview limit — asking
// per member would either mean a query per member or, worse, filtering a page
// that had already been cut, which is exactly the defect this shape prevents.
//
// Presence is per-instance state held by the WebSocket hub in this process. It
// is therefore accurate for a single-instance deployment (every overlay in
// infra/k8s runs chat-service with replicas: 1) and under-reports — never
// over-reports — if that ever changes. A nil lookup yields no online members
// rather than an invented status: "presence unknown" is never read as "online".
type presenceLookup interface {
	OnlineUserIDs(workspaceID string) []string
}

// channelRateLimiter is the same shape as the DM limiter, declared separately so
// this handler depends on the operation it uses rather than on the DM handler.
type channelRateLimiter interface {
	AllowActionWithLimit(ctx context.Context, userID, action string, maxActions, windowSeconds int) (bool, error)
}

const (
	// channelCreateRateLimit is deliberately tight: every accepted call creates a
	// workspace-wide, permanent object that only an owner/admin can archive again.
	channelCreateRateLimit        = 10
	channelRateLimitWindowSeconds = 60
)

// channelMemberManager is the MemberService surface used to add participants
// (issue #398), declared here as the narrowest thing this handler needs.
type channelMemberManager interface {
	AddChannelMembers(ctx context.Context, input service.AddChannelMembersInput) (storage.AddMembersResult, error)
	// SearchChannelMemberCandidates lists people not already in the channel.
	SearchChannelMemberCandidates(ctx context.Context, input service.SearchChannelMemberCandidatesInput) ([]domain.DMCandidate, error)
}

// membersBroadcaster publishes the post-commit "this target changed" signal.
// Declared as an interface so the HTTP layer never imports the ws package;
// app.go adapts the hub to it, exactly as it does for pins.
type membersBroadcaster interface {
	PublishMembersAdded(ctx context.Context, workspaceID, targetType, targetID, actorUserID string, addedCount, memberCount int)
	// PublishConversationUpdated invalidates a renamed conversation for its
	// subscribers, and PublishConversationEvent announces the system message the
	// same transaction wrote (issue #527).
	PublishConversationUpdated(ctx context.Context, workspaceID, targetType, targetID string)
	PublishConversationEvent(ctx context.Context, workspaceID, targetType, targetID, messageID string)
	// PublishConversationAvailable signals the newly added users directly, since
	// they are not yet subscribed to the target and so cannot receive the
	// room-scoped event above.
	PublishConversationAvailable(ctx context.Context, workspaceID, targetType, targetID string, userIDs []string)
}

type ChannelHandler struct {
	workspaces workspaceResolver
	channels   channelProvider
	limiter    channelRateLimiter
	presence   presenceLookup
	members    channelMemberManager
	broadcast  membersBroadcaster
	// channelUpdates is optional: without it a rename still persists and still
	// answers 200, it simply does not tell other sessions to refetch.
	channelUpdates channelUpdateBroadcaster
}

func NewChannelHandler(workspaces workspaceResolver, channels channelProvider, limiter channelRateLimiter) *ChannelHandler {
	return &ChannelHandler{workspaces: workspaces, channels: channels, limiter: limiter}
}

// WithPresence returns a handler that can resolve which members are online.
// Wired after the WebSocket hub exists, exactly like MessageHandler's pin
// broadcaster. Without it the details payload carries no online members.
func (h *ChannelHandler) WithPresence(presence presenceLookup) *ChannelHandler {
	if h == nil {
		return nil
	}
	next := *h
	next.presence = presence
	return &next
}

// WithMembers returns a handler that can add channel participants (issue #398).
// Wired after the hub exists so the broadcaster is available; without it the
// route is left unregistered rather than served without its realtime signal.
func (h *ChannelHandler) WithMembers(members channelMemberManager, broadcast membersBroadcaster) *ChannelHandler {
	if h == nil {
		return nil
	}
	next := *h
	next.members = members
	next.broadcast = broadcast
	return &next
}

// WithChannelUpdates returns a handler that emits the post-commit
// channel.updated signal (issue #527). Wired after the hub exists, like
// WithMembers. Unlike the members routes, the rename route is registered
// regardless: the write is authoritative on its own and a missing broadcaster
// costs a stale name until the next sidebar refetch, never the rename.
func (h *ChannelHandler) WithChannelUpdates(broadcast channelUpdateBroadcaster) *ChannelHandler {
	if h == nil {
		return nil
	}
	next := *h
	next.channelUpdates = broadcast
	return &next
}

// HasMembers reports whether the add-members route can be served. The router
// asks before registering it, so a partially wired service answers 404 for a
// route it cannot honour instead of 503 on every call.
func (h *ChannelHandler) HasMembers() bool {
	return h != nil && h.members != nil
}

// createChannelRequest is the whole accepted body. The workspace, the creator,
// is_general, status and position are server-derived and deliberately absent —
// the strict decoder answers 400 to a client that sends them.
type createChannelRequest struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
	Type        string `json:"type"`
	CategoryID  string `json:"category_id,omitempty"`
}

type createChannelResponse struct {
	ID          string `json:"id"`
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
	Type        string `json:"type"`
}

// Create handles POST /api/chat/channels.
//
// It carries transport concerns only — authentication, rate limiting, body shape
// and the server-side workspace lookup. The slug rules, the reserved "geral"
// name, the channel type, the display-name bound and the authorization itself
// live below: any caller with a valid token, a live session, an active
// membership and an active workspace may create a channel, whatever their role
// (BUG #393), and the storage layer settles that atomically with the write.
func (h *ChannelHandler) Create(w http.ResponseWriter, r *http.Request) {
	if h.workspaces == nil || h.channels == nil || h.limiter == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "channels not available")
		return
	}
	callerID := GetContextUserID(r)
	if callerID == "" {
		writeUnauthorized(w)
		return
	}
	allowed, err := h.limiter.AllowActionWithLimit(r.Context(), callerID, "channel_create", channelCreateRateLimit, channelRateLimitWindowSeconds)
	if err != nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "channels not available")
		return
	}
	if !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(channelRateLimitWindowSeconds))
		httputil.WriteError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
		return
	}
	if !requireJSONContentType(w, r) {
		return
	}
	var request createChannelRequest
	if !decodeStrictJSON(w, r, &request) {
		return
	}
	workspace, err := h.workspaces.GetDefaultWorkspace(r.Context())
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "workspace not found")
		} else {
			httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
		}
		return
	}

	channel, err := h.channels.CreateChannel(r.Context(), service.CreateChannelInput{
		WorkspaceID: workspace.ID,
		CallerID:    callerID,
		CategoryID:  request.CategoryID,
		Slug:        request.Slug,
		DisplayName: request.DisplayName,
		Type:        domain.ChannelType(request.Type),
	})
	if err != nil {
		writeCreateChannelError(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, createChannelResponse{
		ID:          channel.ID,
		Slug:        channel.Slug,
		DisplayName: channel.DisplayName,
		Type:        string(channel.Type),
	})
}

// ── Channel details (issue #435) ─────────────────────────────────────────────

// channelDetailsMemberJSON is one member of the panel's online preview.
//
// It carries exactly what the panel renders and nothing else: a stable ID (so
// the client can mark "you" by identity rather than by name), the resolved
// display name, an optional avatar URL and the channel role. E-mail, workspace
// role, join date and every other profile attribute are deliberately absent —
// a details panel is not a directory export.
//
// Presence is a constant here rather than a per-member field: every entry in
// online_members is online by construction, and the field is still sent so a
// client can assert that rather than infer it from the list's name.
type channelDetailsMemberJSON struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
	AvatarURL   string `json:"avatar_url,omitempty"`
	Role        string `json:"role"`
	Presence    string `json:"presence"`
}

// channelDetailsResponse is the panel payload.
//
// The three member fields answer three different questions and none is derived
// from another:
//   - member_count is every active member of the channel, online or not;
//   - online_member_count is how many of those are online right now, which can
//     exceed len(online_members) when more are online than the preview shows;
//   - online_members is the capped preview itself, ordered by normalised display
//     name with the user ID as a deterministic tie-break.
//
// The list is named online_members, not members, because it is not a general
// roster and must not be read as one: it is filtered by presence *before* the
// limit is applied, so an offline member never occupies one of its slots and an
// online member never loses a slot to one.
//
// There is no description field: chat.channels has no description column, so
// there is nothing truthful to send. When the domain grows one, it is added
// here and the panel's existing empty state stops being the only outcome.
type channelDetailsResponse struct {
	ID                string                     `json:"id"`
	Slug              string                     `json:"slug"`
	DisplayName       string                     `json:"display_name"`
	Type              string                     `json:"type"`
	CreatedAt         string                     `json:"created_at"`
	MemberCount       int                        `json:"member_count"`
	OnlineMemberCount int                        `json:"online_member_count"`
	OnlineMembers     []channelDetailsMemberJSON `json:"online_members"`
	// CanManageMembers lets the panel disable an action the server would refuse
	// (issue #398). It is a hint for the UI and never a control: the add-members
	// route re-derives the same decision from the session on every call. It is
	// always sent, so a client that predates it reads absent-as-false and hides
	// the action — the safe direction — rather than enabling it by default.
	CanManageMembers bool `json:"can_manage_members"`
}

// Presence values serialised by the details endpoints. presenceOnline is the
// only status an entry in online_members can carry; presenceOffline exists for
// the group panel, whose participant list is not presence-filtered and so has
// to say which of its rows are connected.
const (
	presenceOnline  = "online"
	presenceOffline = "offline"
)

// Details handles GET /api/chat/channels/{channelID}/details.
//
// The channel ID comes from the path and is never trusted: the service resolves
// the caller's visibility against the server-side workspace before any member
// row is read, and an invisible channel is answered with the same 404 as a
// missing one.
//
// The presence snapshot is collected once, in a single batch call, and handed
// to the service as a filter. It is read from in-process state and never
// reaches a response body unless the service's authorization gate passes first.
func (h *ChannelHandler) Details(w http.ResponseWriter, r *http.Request) {
	if h.workspaces == nil || h.channels == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "channels not available")
		return
	}
	channelID := r.PathValue("channelID")
	if !validateTargetID(w, channelID, "channel_id") {
		return
	}
	callerID := GetContextUserID(r)
	if callerID == "" {
		writeUnauthorized(w)
		return
	}
	workspace, err := h.workspaces.GetDefaultWorkspace(r.Context())
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "workspace not found")
		} else {
			httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
		}
		return
	}

	details, err := h.channels.GetChannelDetails(r.Context(), service.ChannelDetailsInput{
		WorkspaceID:   workspace.ID,
		CallerID:      callerID,
		ChannelID:     channelID,
		OnlineUserIDs: h.onlineUserIDs(workspace.ID),
		MemberLimit:   domain.MaxChannelDetailsMembers,
	})
	if err != nil {
		writeChannelDetailsError(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, channelDetailsBody(details))
}

// onlineUserIDs returns the workspace's online users in one call, or nothing
// when presence is not wired — an unknown presence source yields an empty
// preview rather than members the server cannot vouch for.
func (h *ChannelHandler) onlineUserIDs(workspaceID string) []string {
	if h.presence == nil {
		return nil
	}
	return h.presence.OnlineUserIDs(workspaceID)
}

func channelDetailsBody(details service.ChannelDetails) channelDetailsResponse {
	members := make([]channelDetailsMemberJSON, 0, len(details.OnlineMembers))
	for _, member := range details.OnlineMembers {
		members = append(members, channelDetailsMemberJSON{
			UserID:      member.UserID,
			DisplayName: member.DisplayName,
			AvatarURL:   member.AvatarURL,
			Role:        string(member.Role),
			Presence:    presenceOnline,
		})
	}
	return channelDetailsResponse{
		ID:                details.Channel.ID,
		Slug:              details.Channel.Slug,
		DisplayName:       details.Channel.DisplayName,
		Type:              string(details.Channel.Type),
		CreatedAt:         details.Channel.CreatedAt.UTC().Format(time.RFC3339),
		MemberCount:       details.MemberCount,
		OnlineMemberCount: details.OnlineCount,
		OnlineMembers:     members,
		CanManageMembers:  details.CanManageMembers,
	}
}

// ── Call-participant profiles (issue #612) ───────────────────────────────────

type callParticipantProfilesRequest struct {
	UserIDs []string `json:"user_ids"`
}

type callParticipantProfileJSON struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
	AvatarURL   string `json:"avatar_url"`
}

type callParticipantProfilesResponse struct {
	Profiles []callParticipantProfileJSON `json:"profiles"`
}

func callParticipantProfilesBody(profiles []domain.CallParticipantProfile) callParticipantProfilesResponse {
	out := make([]callParticipantProfileJSON, 0, len(profiles))
	for _, profile := range profiles {
		out = append(out, callParticipantProfileJSON{
			UserID:      profile.UserID,
			DisplayName: profile.DisplayName,
			AvatarURL:   profile.AvatarURL,
		})
	}
	return callParticipantProfilesResponse{Profiles: out}
}

// callParticipantsRateLimit caps identity-resolution requests: a call joins
// and leaves change the room roster far less often than this allows, so the
// budget exists only to bound abuse, not to constrain real usage.
const callParticipantsRateLimit = 30

// callParticipantsAction is the shared limiter namespace for both
// call-participants routes (channel and group), mirroring addMembersAction.
const callParticipantsAction = "call_participants"

// CallParticipants handles POST /api/chat/channels/{channelID}/call-participants
// (issue #612).
//
// Transport concerns only: batch validation, the cap and de-duplication all
// live in ChannelService.GetCallParticipantProfiles. A user ID that is not
// an active member of this channel is silently absent from the response —
// never a 403/404 for that one ID — so the client's per-participant fallback
// (initials) is the only thing that ever surfaces an unresolved identity.
func (h *ChannelHandler) CallParticipants(w http.ResponseWriter, r *http.Request) {
	if h.workspaces == nil || h.channels == nil || h.limiter == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "channels not available")
		return
	}
	channelID := r.PathValue("channelID")
	if !validateTargetID(w, channelID, "channel_id") {
		return
	}
	callerID := GetContextUserID(r)
	if callerID == "" {
		writeUnauthorized(w)
		return
	}
	allowed, err := h.limiter.AllowActionWithLimit(r.Context(), callerID, callParticipantsAction, callParticipantsRateLimit, channelRateLimitWindowSeconds)
	if err != nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "channels not available")
		return
	}
	if !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(channelRateLimitWindowSeconds))
		httputil.WriteError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
		return
	}
	if !requireJSONContentType(w, r) {
		return
	}
	var request callParticipantProfilesRequest
	if !decodeStrictJSON(w, r, &request) {
		return
	}
	workspace, err := h.workspaces.GetDefaultWorkspace(r.Context())
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "workspace not found")
		} else {
			httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
		}
		return
	}
	profiles, err := h.channels.GetCallParticipantProfiles(r.Context(), service.ChannelCallParticipantProfilesInput{
		WorkspaceID: workspace.ID,
		CallerID:    callerID,
		ChannelID:   channelID,
		UserIDs:     request.UserIDs,
	})
	if err != nil {
		writeCallParticipantProfilesError(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, callParticipantProfilesBody(profiles))
}

// writeCallParticipantProfilesError mirrors writeChannelDetailsError's
// not-found folding (an unauthorized caller and a nonexistent/foreign
// channel are indistinguishable) plus writeAddMembersError's 400 for a
// malformed batch. Shared by both call-participants routes (channel and
// group DM).
func writeCallParticipantProfilesError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidInput):
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid request")
	case errors.Is(err, domain.ErrNotFound), errors.Is(err, domain.ErrForbidden):
		httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "not found")
	default:
		httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
	}
}

// ── Add members (issue #398) ─────────────────────────────────────────────────

// addMembersRequest is the whole accepted body.
//
// One field, and it is the only thing a client is in a position to know: which
// people the user picked. The workspace, the actor, the membership role, the
// membership status and every eligibility verdict are server-derived and
// deliberately absent — the strict decoder answers 400 to a client that sends
// them, so there is no field through which a caller could nominate themselves an
// admin, aim the write at another workspace, or assert that someone is eligible.
type addMembersRequest struct {
	UserIDs []string `json:"user_ids"`
}

// addMembersResponse reports what the write actually did.
//
// Added and AlreadyMembers are separate so a retry stays legible: the second
// identical request reports the same people under already_members and adds
// nothing, rather than looking like a fresh success or like a failure.
// MemberCount is the server's post-commit total, so the panel updates its
// counter from the authority instead of incrementing a local guess.
type addMembersResponse struct {
	Added          int `json:"added"`
	AlreadyMembers int `json:"already_members"`
	MemberCount    int `json:"member_count"`
}

// addMembersRateLimit is one budget for the whole add-participants capability.
//
// Deliberately a single action name shared by the channel and the group route,
// following the channel-categories precedent: a caller must not get a separate
// allowance per conversation type for what is the same write. Ten per minute is
// far above a human using a picker and far below anything that could use the
// endpoint to enumerate or to amplify membership rows.
const addMembersRateLimit = 10

// addMembersAction is the shared limiter namespace for both add-members routes.
const addMembersAction = "add_members"

// AddMembers handles POST /api/chat/channels/{channelID}/members.
//
// Transport concerns only. Who may add, who may be added, the batch cap, the
// de-duplication and the atomicity all live below in MemberService and the
// store; this function never inspects the list beyond decoding it, and never
// makes an authorization decision of its own.
//
// The order matters: the caller is authenticated, then throttled, then the body
// is read. Rate limiting before the body means an unauthenticated flood cannot
// make the server parse anything, and decodeStrictJSON caps the read at
// maxBodyBytes so an oversized payload is refused by the reader rather than
// buffered.
//
// The realtime event is published only after the service returns successfully,
// so a rolled-back transaction broadcasts nothing.
func (h *ChannelHandler) AddMembers(w http.ResponseWriter, r *http.Request) {
	if h.workspaces == nil || h.members == nil || h.limiter == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "channels not available")
		return
	}
	channelID := r.PathValue("channelID")
	if !validateTargetID(w, channelID, "channel_id") {
		return
	}
	callerID := GetContextUserID(r)
	if callerID == "" {
		writeUnauthorized(w)
		return
	}
	allowed, err := h.limiter.AllowActionWithLimit(r.Context(), callerID, addMembersAction, addMembersRateLimit, channelRateLimitWindowSeconds)
	if err != nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "channels not available")
		return
	}
	if !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(channelRateLimitWindowSeconds))
		httputil.WriteError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
		return
	}
	if !requireJSONContentType(w, r) {
		return
	}
	var request addMembersRequest
	if !decodeStrictJSON(w, r, &request) {
		return
	}
	workspace, err := h.workspaces.GetDefaultWorkspace(r.Context())
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "workspace not found")
		} else {
			httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
		}
		return
	}

	result, err := h.members.AddChannelMembers(r.Context(), service.AddChannelMembersInput{
		WorkspaceID: workspace.ID,
		CallerID:    callerID,
		ChannelID:   channelID,
		UserIDs:     request.UserIDs,
	})
	if err != nil {
		writeAddMembersError(w, err)
		return
	}
	// Gated on the ID list rather than on Added: both come from the same
	// RETURNING and cannot disagree, but the list is what the fan-out below
	// addresses.
	if h.broadcast != nil && len(result.AddedUserIDs) > 0 {
		h.broadcast.PublishMembersAdded(
			r.Context(), workspace.ID, "channel", channelID, callerID, result.Added, result.TotalCount,
		)
		// The people who were actually inserted are told directly: they do not
		// subscribe to this channel yet, so the broadcast above cannot reach
		// them. AddedUserIDs comes from the transaction's RETURNING, so a
		// request that named an existing member does not signal them.
		h.broadcast.PublishConversationAvailable(
			r.Context(), workspace.ID, "channel", channelID, result.AddedUserIDs,
		)
	}
	httputil.WriteJSON(w, http.StatusOK, addMembersResponse{
		Added:          result.Added,
		AlreadyMembers: result.AlreadyMembers,
		MemberCount:    result.TotalCount,
	})
}

// MemberCandidates handles GET /api/chat/channels/{channelID}/member-candidates.
//
// The contextual replacement for a workspace-wide people search. Who is
// offerable depends on who is already in *this* channel, and only the database
// knows that: the details panel's member section is presence-filtered and
// capped, so it never was a membership list.
//
// Response fields are the two the picker renders. No e-mail, no role, no
// membership state — a candidate list is not a directory.
func (h *ChannelHandler) MemberCandidates(w http.ResponseWriter, r *http.Request) {
	if h.workspaces == nil || h.members == nil || h.limiter == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "channels not available")
		return
	}
	channelID := r.PathValue("channelID")
	if !validateTargetID(w, channelID, "channel_id") {
		return
	}
	callerID := GetContextUserID(r)
	if callerID == "" {
		writeUnauthorized(w)
		return
	}
	// Shares the enumeration budget with the workspace-wide DM candidate search,
	// so adding a contextual route does not hand a caller a second allowance for
	// probing who exists.
	allowed, err := h.limiter.AllowActionWithLimit(
		r.Context(), callerID, dmCandidateSearchAction, dmCandidateSearchRateLimit, channelRateLimitWindowSeconds,
	)
	if err != nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "channels not available")
		return
	}
	if !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(channelRateLimitWindowSeconds))
		httputil.WriteError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
		return
	}
	limit, ok := parseCandidateLimit(w, r)
	if !ok {
		return
	}
	workspace, err := h.workspaces.GetDefaultWorkspace(r.Context())
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "workspace not found")
		} else {
			httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
		}
		return
	}

	candidates, err := h.members.SearchChannelMemberCandidates(r.Context(), service.SearchChannelMemberCandidatesInput{
		WorkspaceID: workspace.ID,
		CallerID:    callerID,
		ChannelID:   channelID,
		Query:       r.URL.Query().Get("query"),
		Limit:       limit,
	})
	if err != nil {
		writeCandidateSearchError(w, err)
		return
	}
	writeCandidates(w, candidates)
}

// writeCandidateSearchError maps both contextual candidate searches.
//
// A caller without management rights on a channel, and one who does not
// participate in a group, both land on the same statuses the corresponding
// write already returns, so the search cannot be used to discover something the
// mutation would refuse to act on.
func writeCandidateSearchError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidInput):
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid search")
	case errors.Is(err, domain.ErrNotFound):
		httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "not found")
	case errors.Is(err, domain.ErrForbidden):
		httputil.WriteError(w, http.StatusForbidden, httputil.ErrCodeForbidden, "forbidden")
	default:
		httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
	}
}

// writeAddMembersError maps the add-members failures for both routes.
//
// An unauthorized caller and an ineligible target both surface as ErrForbidden
// and both answer 403. They are deliberately *not* separated: the layer that
// raised the error would be enough to tell them apart internally, but exposing
// that distinction is exactly what would turn the endpoint into an account
// oracle. The body says only "forbidden" — never which user was refused, nor
// whether they are suspended, deleted, in another workspace or nonexistent, so
// the four are indistinguishable from outside.
//
// 404 covers a channel or conversation the caller cannot act on, collapsing
// missing, archived, cross-workspace, wrong-type and not-a-participant into one
// answer so the route cannot be used to probe which UUIDs exist.
//
// There is deliberately no capacity status. Channels and groups have no fixed
// participant limit: the only bound is how many IDs one request may carry, and
// exceeding that is ErrInvalidInput (400), because it is a property of the
// request rather than of the conversation.
//
// No branch carries SQL text, a constraint name, a user ID or a rejected value.
func writeAddMembersError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidInput):
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid request")
	case errors.Is(err, domain.ErrNotFound):
		httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "not found")
	case errors.Is(err, domain.ErrForbidden):
		httputil.WriteError(w, http.StatusForbidden, httputil.ErrCodeForbidden, "forbidden")
	default:
		httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
	}
}

// writeChannelDetailsError maps the service errors. Unlike channel creation, a
// denial here must not distinguish "private channel you are not in" from
// "no such channel": both are ErrNotFound by construction in the service, and
// ErrForbidden (no active workspace membership) is folded into the same 404 so
// the route cannot confirm that a channel UUID exists in this workspace.
func writeChannelDetailsError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrNotFound), errors.Is(err, domain.ErrForbidden):
		httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "channel not found")
	default:
		httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
	}
}

// writeCreateChannelError keeps a denial legible. Unlike DM creation — where a
// 404 hides whether a given user exists — there is no identity to protect here,
// so a caller whose membership or workspace is no longer active gets a plain 403
// and the UI can say why instead of showing a generic failure. Which of the two
// it was stays unsaid; that distinction is the workspace's business.
func writeCreateChannelError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidInput):
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid channel")
	case errors.Is(err, domain.ErrForbidden):
		httputil.WriteError(w, http.StatusForbidden, httputil.ErrCodeForbidden, "forbidden")
	case errors.Is(err, domain.ErrDuplicateSlug), errors.Is(err, domain.ErrConflict):
		httputil.WriteError(w, http.StatusConflict, "conflict", "channel already exists")
	case errors.Is(err, domain.ErrNotFound):
		httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "workspace not found")
	default:
		httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
	}
}

// ── Rename (issue #527) ──────────────────────────────────────────────────────

// renameChannelRequest is the whole accepted body.
//
// One field, and the omissions are the contract: no workspace_id, no type, no
// slug, no role and no actor. The workspace is resolved from the session, the
// caller comes from the authenticated context, and the strict decoder answers
// 400 to a client that sends anything else — so a payload cannot claim a
// privilege, move a channel between workspaces, or turn a rename into a
// visibility change.
type renameChannelRequest struct {
	DisplayName string `json:"display_name"`
}

// renameChannelResponse reports the persisted result.
//
// The ID is echoed because the whole point of a rename is that it does not
// change: a client that sees the same id knows it is looking at the same
// channel and not at a new one.
type renameChannelResponse struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

const (
	// channelRenameRateLimit bounds renames per user per minute. Tight for the
	// same reason channel creation is: every accepted call changes a
	// workspace-wide object the whole sidebar renders from.
	channelRenameRateLimit = 20
	channelRenameAction    = "channel_update"
)

// Rename handles PATCH /api/chat/channels/{channelID} (issue #527).
//
// Transport concerns only. Who may rename (domain.CanManageWorkspace, via
// ChannelService.requireManagePermission), what a valid name is
// (domain.NormalizeChannelDisplayName) and the #geral immutability all live in
// ChannelService.UpdateChannel; this function makes no authorization decision
// of its own and never inspects the name beyond decoding it.
//
// Only DisplayName is forwarded. Slug, Type, CategoryID and Position are left
// nil/empty, which UpdateChannel reads as "unchanged", so this route cannot
// change a channel's visibility, its category or its address — a rename is a
// rename.
//
// The realtime signal is published only after the service returns successfully,
// so a refused or rolled-back write broadcasts nothing.
func (h *ChannelHandler) Rename(w http.ResponseWriter, r *http.Request) {
	callerID, workspaceID, channelID, ok := h.beginRename(w, r)
	if !ok {
		return
	}
	var request renameChannelRequest
	if !decodeStrictJSON(w, r, &request) {
		return
	}
	// The same domain rule the create path uses, applied here for one reason
	// UpdateChannel cannot cover: an empty display_name is "leave it unchanged"
	// to a general-purpose update, and a silent no-op is the worst answer a
	// rename can give — the dialog would close on a name that was never
	// persisted. Normalising here makes an empty or blank name a refusal, and it
	// is emphatically not a second validator: it is the same function, and
	// UpdateChannel applies it again to the value forwarded below.
	displayName, err := domain.NormalizeChannelDisplayName(request.DisplayName)
	if err != nil {
		writeRenameChannelError(w, err)
		return
	}
	result, err := h.channels.UpdateChannel(r.Context(), service.UpdateChannelInput{
		WorkspaceID: workspaceID,
		CallerID:    callerID,
		ChannelID:   channelID,
		DisplayName: displayName,
	})
	if err != nil {
		writeRenameChannelError(w, err)
		return
	}
	// Both signals fire only here, after the service returned — which means only
	// after the transaction that wrote the name *and* the system message
	// committed. A refused or rolled-back rename announces nothing.
	h.publishRename(r.Context(), workspaceID, result)
	httputil.WriteJSON(w, http.StatusOK, renameChannelResponse{
		ID:          result.Channel.ID,
		DisplayName: result.Channel.DisplayName,
	})
}

// publishRename announces a committed rename to the channel's subscribers.
//
// Two signals for two different jobs: the sidebar copy of the name is stale for
// everyone, and the timeline gained an entry. The event is published only when
// the transaction actually wrote one — an update that changed no name has none,
// and inventing an announcement for it would put a line in every reader's
// timeline for nothing.
func (h *ChannelHandler) publishRename(ctx context.Context, workspaceID string, result storage.UpdateChannelResult) {
	if h.channelUpdates == nil {
		return
	}
	h.channelUpdates.PublishConversationUpdated(ctx, workspaceID, "channel", result.Channel.ID)
	if result.Event.ID != "" {
		h.channelUpdates.PublishConversationEvent(ctx, workspaceID, "channel", result.Channel.ID, result.Event.ID)
	}
}

// beginRename performs everything that must hold before a rename is attempted,
// in the order that matters: a well-formed target, then the actor and the
// budget, then the workspace. The workspace is resolved last and never from the
// request, so nothing a client sends can aim the write elsewhere.
func (h *ChannelHandler) beginRename(w http.ResponseWriter, r *http.Request) (string, string, string, bool) {
	channelID := r.PathValue("channelID")
	if !validateTargetID(w, channelID, "channel_id") {
		return "", "", "", false
	}
	callerID, ok := h.admitChannelWriter(w, r, channelRenameAction, channelRenameRateLimit)
	if !ok {
		return "", "", "", false
	}
	workspaceID, ok := h.resolveDefaultWorkspaceID(w, r)
	if !ok {
		return "", "", "", false
	}
	return callerID, workspaceID, channelID, true
}

// admitChannelWriter checks the wiring, authenticates the actor, spends the
// named per-user budget and requires a JSON content type — returning the caller
// ID only when all four hold.
//
// Rate limiting runs before the body is read, so an oversized or malformed
// payload cannot be used to make the server parse anything for free.
func (h *ChannelHandler) admitChannelWriter(
	w http.ResponseWriter, r *http.Request, action string, limit int,
) (string, bool) {
	if h.workspaces == nil || h.channels == nil || h.limiter == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "channels not available")
		return "", false
	}
	callerID := GetContextUserID(r)
	if callerID == "" {
		writeUnauthorized(w)
		return "", false
	}
	allowed, err := h.limiter.AllowActionWithLimit(
		r.Context(), callerID, action, limit, channelRateLimitWindowSeconds,
	)
	if err != nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "channels not available")
		return "", false
	}
	if !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(channelRateLimitWindowSeconds))
		httputil.WriteError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
		return "", false
	}
	if !requireJSONContentType(w, r) {
		return "", false
	}
	return callerID, true
}

// resolveDefaultWorkspaceID answers with the session's workspace, or writes the
// failure. The workspace never comes from a path segment, a query parameter or
// a body field — there is no request shape that names one.
func (h *ChannelHandler) resolveDefaultWorkspaceID(w http.ResponseWriter, r *http.Request) (string, bool) {
	workspace, err := h.workspaces.GetDefaultWorkspace(r.Context())
	if err == nil {
		return workspace.ID, true
	}
	if errors.Is(err, domain.ErrNotFound) {
		httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "workspace not found")
	} else {
		httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
	}
	return "", false
}

// writeRenameChannelError keeps denials legible without describing state.
//
// A caller who may not manage the workspace gets 403 before the channel is ever
// read, so the status code cannot be used to learn whether a channel ID exists.
// A channel in another workspace, an archived one and one that never existed all
// answer 404. No branch carries a SQL message, a constraint name or the rejected
// value — a refused name can be tens of kilobytes of caller-controlled text.
func writeRenameChannelError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrChannelDisplayNameRequired),
		errors.Is(err, domain.ErrChannelDisplayNameTooLong),
		errors.Is(err, domain.ErrInvalidInput):
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid channel name")
	case errors.Is(err, domain.ErrForbidden):
		httputil.WriteError(w, http.StatusForbidden, httputil.ErrCodeForbidden, "forbidden")
	case errors.Is(err, domain.ErrNotFound):
		httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "channel not found")
	case errors.Is(err, domain.ErrConflict):
		httputil.WriteError(w, http.StatusConflict, httputil.ErrCodeConflict, "conflict")
	default:
		httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
	}
}

// ── Self-leave (issue #527) ──────────────────────────────────────────────────

// leaveChannelRateLimit shares the rename budget's window. Leaving is rarer than
// renaming and just as permanent from the actor's point of view, so it gets the
// same tight per-user allowance rather than the general write budget.
const (
	leaveChannelRateLimit = 20
	leaveChannelAction    = "channel_leave"
)

// Leave handles DELETE /api/chat/channels/{channelID}/membership (issue #527).
//
// The body is not read at all — there is none — so there is no field through
// which a caller could name a user, a workspace or a role. The membership this
// removes is the session's own, decided in SQL.
//
// The realtime signal is published only after the service returns successfully,
// which means only after the transaction that removed the membership and wrote
// the departure event committed.
func (h *ChannelHandler) Leave(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("channelID")
	if !validateTargetID(w, channelID, "channel_id") {
		return
	}
	callerID, ok := h.admitChannelWriter(w, r, leaveChannelAction, leaveChannelRateLimit)
	if !ok {
		return
	}
	workspaceID, ok := h.resolveDefaultWorkspaceID(w, r)
	if !ok {
		return
	}
	result, err := h.channels.LeaveChannel(r.Context(), workspaceID, channelID, callerID)
	if err != nil {
		writeLeaveConversationError(w, err)
		return
	}
	if h.channelUpdates != nil {
		h.channelUpdates.PublishConversationEvent(r.Context(), workspaceID, "channel", channelID, result.Event.ID)
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeLeaveConversationError keeps the structural refusals legible without
// describing state.
//
// The general channel answers 403 and says so, because the caller can plainly
// see the channel and a "not found" would be a lie they cannot act on. Every
// other refusal keeps the non-enumerating shape the rest of the channel surface
// has: an invisible channel, one in another workspace and one that never existed
// are all 404.
func writeLeaveConversationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrGeneralChannelImmutable):
		httputil.WriteError(w, http.StatusForbidden, httputil.ErrCodeForbidden, "the general channel cannot be left")
	case errors.Is(err, domain.ErrForbidden):
		httputil.WriteError(w, http.StatusForbidden, httputil.ErrCodeForbidden, "forbidden")
	case errors.Is(err, domain.ErrNotFound):
		httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "not found")
	case errors.Is(err, domain.ErrInvalidInput):
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid request")
	default:
		httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
	}
}
