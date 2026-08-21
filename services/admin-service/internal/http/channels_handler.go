package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
	"github.com/nicrepository/nchat/services/admin-service/internal/service"
)

// ChannelAdmin is the channel and conversation surface the routes drive.
type ChannelAdmin interface {
	List(ctx context.Context, filter domain.AdminChannelFilter) (domain.Page[domain.AdminChannelSummary], error)
	Get(ctx context.Context, channelID string) (domain.AdminChannelDetail, error)
	SetStatus(ctx context.Context, actor service.Actor, channelID, status string) (domain.AdminChannelSummary, error)
	MemberCandidates(ctx context.Context, channelID, query string) ([]domain.ChannelMemberCandidate, error)
	AddMembers(ctx context.Context, actor service.Actor, channelID string, userIDs []string) (domain.ChannelMembershipChange, error)
	RemoveMember(ctx context.Context, actor service.Actor, channelID, userID string) (domain.ChannelMembershipChange, error)
	ListConversations(ctx context.Context, filter domain.AdminConversationFilter) (domain.Page[domain.AdminConversationSummary], error)
}

// maxMemberFilter bounds the "at least this many members" filter. It is a
// filter, not a page size, so the ceiling only has to be larger than any real
// channel.
const maxMemberFilter = 1_000_000

type adminChannelPayload struct {
	ID             string     `json:"id"`
	WorkspaceID    string     `json:"workspace_id"`
	WorkspaceName  string     `json:"workspace_name"`
	Slug           string     `json:"slug"`
	DisplayName    string     `json:"display_name"`
	Type           string     `json:"type"`
	Status         string     `json:"status"`
	IsGeneral      bool       `json:"is_general"`
	MemberCount    int        `json:"member_count"`
	ModeratorCount int        `json:"moderator_count"`
	CreatedByName  string     `json:"created_by_name"`
	CreatedByEmail string     `json:"created_by_email"`
	CreatedAt      time.Time  `json:"created_at"`
	LastActivityAt *time.Time `json:"last_activity_at"`
}

type channelMemberPayload struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	Role        string `json:"role"`
}

type adminChannelDetailPayload struct {
	adminChannelPayload
	CategoryName string `json:"category_name"`
	// Moderators and WorkspaceAdmins are separate fields because they are
	// separate authorities: a channel moderator governs one channel, a
	// workspace owner or admin governs a workspace. Merging them into one
	// "admins" array is the collapse docs/security/rbac-matrix.md exists to
	// prevent, and a payload that merged them would teach the console the wrong
	// model.
	Moderators      []channelMemberPayload `json:"moderators"`
	WorkspaceAdmins []channelMemberPayload `json:"workspace_admins"`
	// Members is a bounded preview, not the membership. member_count on the
	// summary is the real total; capping this list is what keeps a detail view
	// from doubling as a directory export.
	Members      []channelMemberPayload `json:"members"`
	MessageCount int64                  `json:"message_count"`
}

// ListChannels serves the platform channel directory.
//
// Requires admin.channels.read. Private channels appear: the row states that a
// private channel exists, how large it is and who administers it, which is what
// the capability authorizes. It carries no message and no member name, so
// listing a private channel is not a way to read one — chat.channel_visible_to_user
// is still the only thing that decides that, and nothing here consults it
// because nothing here returns content.
func ListChannels(channels ChannelAdmin) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if channels == nil {
			writeUnavailable(w)
			return
		}
		filter, ok := parseChannelFilter(w, r)
		if !ok {
			return
		}
		page, err := channels.List(r.Context(), filter)
		if err != nil {
			writeDomainError(w, err)
			return
		}
		payload := make([]adminChannelPayload, 0, len(page.Items))
		for _, channel := range page.Items {
			payload = append(payload, newAdminChannelPayload(channel))
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{
			"channels":   payload,
			"pagination": newPagination(page.NextCursor),
		})
	})
}

func parseChannelFilter(w http.ResponseWriter, r *http.Request) (domain.AdminChannelFilter, bool) {
	query := r.URL.Query()
	limit, cursor, err := parsePageParams(query)
	if err != nil {
		writeInvalidQuery(w)
		return domain.AdminChannelFilter{}, false
	}
	workspaceID, err := parseUUIDFilter(query, "workspace_id")
	if err != nil {
		writeInvalidQuery(w)
		return domain.AdminChannelFilter{}, false
	}
	channelType, err := allowlisted(query, "type", domain.ChannelTypeFilter)
	if err != nil {
		writeInvalidQuery(w)
		return domain.AdminChannelFilter{}, false
	}
	status, err := allowlisted(query, "status", domain.ChannelStatusFilter)
	if err != nil {
		writeInvalidQuery(w)
		return domain.AdminChannelFilter{}, false
	}
	minMembers, err := parseBoundedInt(query, "min_members", maxMemberFilter)
	if err != nil {
		writeInvalidQuery(w)
		return domain.AdminChannelFilter{}, false
	}
	// "administered by" is a user id, validated as a UUID before it reaches the
	// query. It is a separate parameter and not part of `q` on purpose: a free
	// text search that also matched identifiers would make "who administers
	// this" a guess rather than a predicate.
	administeredBy, err := parseUUIDFilter(query, "administered_by")
	if err != nil {
		writeInvalidQuery(w)
		return domain.AdminChannelFilter{}, false
	}
	activeWithin, err := allowlisted(query, "active_within", domain.ChannelActivityFilter)
	if err != nil {
		writeInvalidQuery(w)
		return domain.AdminChannelFilter{}, false
	}
	term, err := parseSearchTerm(query)
	if err != nil {
		writeInvalidQuery(w)
		return domain.AdminChannelFilter{}, false
	}
	return domain.AdminChannelFilter{
		Query:          term,
		WorkspaceID:    workspaceID,
		Type:           channelType,
		Status:         status,
		MinMembers:     minMembers,
		ActiveWithin:   activeWithin,
		AdministeredBy: administeredBy,
		Limit:          limit,
		Cursor:         cursor,
	}, true
}

// GetChannel serves one channel with the people who administer it.
func GetChannel(channels ChannelAdmin) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if channels == nil {
			writeUnavailable(w)
			return
		}
		detail, err := channels.Get(r.Context(), r.PathValue("channelID"))
		if err != nil {
			writeDomainError(w, err)
			return
		}
		httputil.WriteJSON(w, http.StatusOK, adminChannelDetailPayload{
			adminChannelPayload: newAdminChannelPayload(detail.AdminChannelSummary),
			CategoryName:        detail.CategoryName,
			Moderators:          newChannelMemberPayloads(detail.Moderators),
			WorkspaceAdmins:     newChannelMemberPayloads(detail.WorkspaceAdmins),
			Members:             newChannelMemberPayloads(detail.Members),
			MessageCount:        detail.MessageCount,
		})
	})
}

type channelStatusRequest struct {
	Status string `json:"status"`
}

// UpdateChannelStatus archives or unarchives a channel.
//
// Requires admin.channels.manage. Archiving is the only lifecycle change this
// API offers, and it is reversible: a channel holds its members' history, and
// deleting one is not an operation an administrative console should be able to
// perform in a click. The workspace's #geral channel is refused, because
// chat-service treats it as immutable and this console must not become a second
// way around that.
func UpdateChannelStatus(channels ChannelAdmin) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if channels == nil {
			writeUnavailable(w)
			return
		}
		actor, ok := actorFrom(w, r)
		if !ok {
			return
		}
		var body channelStatusRequest
		if !decodeJSONBody(w, r, &body) {
			return
		}
		channel, err := channels.SetStatus(r.Context(), actor, r.PathValue("channelID"), body.Status)
		if err != nil {
			writeDomainError(w, err)
			return
		}
		httputil.WriteJSON(w, http.StatusOK, newAdminChannelPayload(channel))
	})
}

// memberCandidatePayload is a person the console may offer as a new member.
//
// A name, an e-mail, an avatar and the workspace role — the identifier travels
// because the mutation needs it, not because an operator should have to know
// it. Deliberately narrower than the user directory's row: a picker behind a
// channel capability must not double as a second, wider directory, so there is
// no admin role, no membership list, no session count and no identity-provider
// detail here.
type memberCandidatePayload struct {
	UserID        string `json:"user_id"`
	DisplayName   string `json:"display_name"`
	FullName      string `json:"full_name"`
	Email         string `json:"email"`
	AvatarURL     string `json:"avatar_url"`
	WorkspaceRole string `json:"workspace_role"`
}

// ListMemberCandidates searches the people who may be added to one channel.
//
// Requires admin.channels.manage — the capability of the mutation it feeds, not
// admin.channels.read. Seeing that a channel exists must not also enumerate the
// people in its workspace.
//
// The workspace is derived from the channel inside the query, so no request can
// point the search at another tenant. The search itself is a convenience and
// never a control: AddChannelMembers re-decides eligibility for whoever is
// actually submitted, under the shared rule, in the statement that writes.
func ListMemberCandidates(channels ChannelAdmin) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if channels == nil {
			writeUnavailable(w)
			return
		}
		term, err := parseSearchTerm(r.URL.Query())
		if err != nil {
			writeInvalidQuery(w)
			return
		}
		candidates, err := channels.MemberCandidates(r.Context(), r.PathValue("channelID"), term)
		if err != nil {
			writeDomainError(w, err)
			return
		}
		payload := make([]memberCandidatePayload, 0, len(candidates))
		for _, candidate := range candidates {
			payload = append(payload, memberCandidatePayload{
				UserID:        candidate.UserID,
				DisplayName:   candidate.DisplayName,
				FullName:      candidate.FullName,
				Email:         candidate.Email,
				AvatarURL:     candidate.AvatarURL,
				WorkspaceRole: candidate.WorkspaceRole,
			})
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"candidates": payload})
	})
}

// channelMembersRequest is the entire accepted body of a membership add.
//
// One field, and the decoder refuses every other. A body carrying "role",
// "workspace_id", "channel_id" or a whole membership object is a 400: this
// endpoint adds people as ordinary members of one named channel and cannot be
// talked into granting a role or aiming at another workspace.
type channelMembersRequest struct {
	UserIDs []string `json:"user_ids"`
}

// AddChannelMembers admits people to a channel.
//
// Requires admin.channels.manage — the same capability as archiving, and
// deliberately not admin.channels.read, which authorizes seeing that a channel
// exists and nothing more.
//
// The channel comes from the path and the workspace is derived from it
// server-side; no request names a workspace, so there is none to aim elsewhere.
// The role is not in the contract at all: every administratively added member
// joins as an ordinary member.
//
// This changes membership and grants the actor nothing. They do not become a
// member, and no message becomes readable anywhere in this service as a result.
func AddChannelMembers(channels ChannelAdmin) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if channels == nil {
			writeUnavailable(w)
			return
		}
		actor, ok := actorFrom(w, r)
		if !ok {
			return
		}
		var body channelMembersRequest
		if !decodeJSONBody(w, r, &body) {
			return
		}
		change, err := channels.AddMembers(r.Context(), actor, r.PathValue("channelID"), body.UserIDs)
		if err != nil {
			writeDomainError(w, err)
			return
		}
		httputil.WriteJSON(w, http.StatusOK, newMembershipPayload(change))
	})
}

// RemoveChannelMember takes one person out of a channel.
//
// The target is a path segment rather than a body field, so the operation has
// the idempotent shape a DELETE should have and cannot be re-aimed by a body
// the console did not send. Removing somebody who is not a member answers 200
// with removed=false: the caller's intent already holds, and a 404 would make a
// safe retry look like a failure.
func RemoveChannelMember(channels ChannelAdmin) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if channels == nil {
			writeUnavailable(w)
			return
		}
		actor, ok := actorFrom(w, r)
		if !ok {
			return
		}
		change, err := channels.RemoveMember(r.Context(), actor,
			r.PathValue("channelID"), r.PathValue("userID"))
		if err != nil {
			writeDomainError(w, err)
			return
		}
		httputil.WriteJSON(w, http.StatusOK, newMembershipPayload(change))
	})
}

func newMembershipPayload(change domain.ChannelMembershipChange) map[string]any {
	return map[string]any{
		"channel_id":      change.ChannelID,
		"workspace_id":    change.WorkspaceID,
		"added":           change.Added,
		"already_members": change.AlreadyMembers,
		"removed":         change.Removed,
		"member_count":    change.MemberCount,
	}
}

// adminConversationPayload is operational metadata about one private
// conversation.
//
// The absent fields are the contract. There is no body, no attachment, no
// quote, no reaction, no preview, no latest message, no title and no
// participant identity — a group's title is written by its participants and its
// membership is what decides who may read it, so neither belongs to an
// administrator who is not one. Being a platform administrator does not make
// somebody a participant, and chat.dm_members is still the only thing consulted
// for that decision anywhere in this platform.
type adminConversationPayload struct {
	ID               string     `json:"id"`
	WorkspaceID      string     `json:"workspace_id"`
	WorkspaceName    string     `json:"workspace_name"`
	Type             string     `json:"type"`
	Status           string     `json:"status"`
	ParticipantCount int        `json:"participant_count"`
	MessageCount     int64      `json:"message_count"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	LastActivityAt   *time.Time `json:"last_activity_at"`
}

// ListConversations serves private conversation metadata.
//
// It exists so an operator can see where traffic is concentrated and which
// conversations are still active; it is not, and must not become, a way to read
// them. There is no per-conversation detail endpoint and no message endpoint of
// any kind in this service.
func ListConversations(channels ChannelAdmin) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if channels == nil {
			writeUnavailable(w)
			return
		}
		query := r.URL.Query()
		limit, cursor, err := parsePageParams(query)
		if err != nil {
			writeInvalidQuery(w)
			return
		}
		workspaceID, err := parseUUIDFilter(query, "workspace_id")
		if err != nil {
			writeInvalidQuery(w)
			return
		}
		conversationType, err := allowlisted(query, "type", domain.ConversationTypeFilter)
		if err != nil {
			writeInvalidQuery(w)
			return
		}
		status, err := allowlisted(query, "status", domain.ConversationStatusFilter)
		if err != nil {
			writeInvalidQuery(w)
			return
		}
		page, err := channels.ListConversations(r.Context(), domain.AdminConversationFilter{
			WorkspaceID: workspaceID,
			Type:        conversationType,
			Status:      status,
			Limit:       limit,
			Cursor:      cursor,
		})
		if err != nil {
			writeDomainError(w, err)
			return
		}
		payload := make([]adminConversationPayload, 0, len(page.Items))
		for _, conversation := range page.Items {
			payload = append(payload, adminConversationPayload{
				ID:               conversation.ID,
				WorkspaceID:      conversation.WorkspaceID,
				WorkspaceName:    conversation.WorkspaceName,
				Type:             conversation.Type,
				Status:           conversation.Status,
				ParticipantCount: conversation.ParticipantCount,
				MessageCount:     conversation.MessageCount,
				CreatedAt:        conversation.CreatedAt,
				UpdatedAt:        conversation.UpdatedAt,
				LastActivityAt:   conversation.LastActivityAt,
			})
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{
			"conversations": payload,
			"pagination":    newPagination(page.NextCursor),
		})
	})
}

func newAdminChannelPayload(channel domain.AdminChannelSummary) adminChannelPayload {
	return adminChannelPayload{
		ID:             channel.ID,
		WorkspaceID:    channel.WorkspaceID,
		WorkspaceName:  channel.WorkspaceName,
		Slug:           channel.Slug,
		DisplayName:    channel.DisplayName,
		Type:           channel.Type,
		Status:         channel.Status,
		IsGeneral:      channel.IsGeneral,
		MemberCount:    channel.MemberCount,
		ModeratorCount: channel.ModeratorCount,
		CreatedByName:  channel.CreatedByName,
		CreatedByEmail: channel.CreatedByEmail,
		CreatedAt:      channel.CreatedAt,
		LastActivityAt: channel.LastActivityAt,
	}
}

func newChannelMemberPayloads(members []domain.ChannelMemberRef) []channelMemberPayload {
	payload := make([]channelMemberPayload, 0, len(members))
	for _, member := range members {
		payload = append(payload, channelMemberPayload{
			UserID:      member.UserID,
			DisplayName: member.DisplayName,
			Email:       member.Email,
			Role:        member.Role,
		})
	}
	return payload
}
