package service

import (
	"context"
	"strconv"

	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
)

// ChannelDirectoryStore is the persistence the channel and conversation
// surfaces need.
type ChannelDirectoryStore interface {
	ListChannels(ctx context.Context, filter domain.AdminChannelFilter) (domain.Page[domain.AdminChannelSummary], error)
	GetChannel(ctx context.Context, channelID string) (domain.AdminChannelDetail, error)
	UpdateChannelStatus(ctx context.Context, channelID, newStatus string) (domain.AdminChannelSummary, error)
	ListMemberCandidates(ctx context.Context, channelID, query string, limit int) ([]domain.ChannelMemberCandidate, error)
	AddChannelMembers(ctx context.Context, channelID string, userIDs []string) (domain.ChannelMembershipChange, error)
	RemoveChannelMember(ctx context.Context, channelID, userID string) (domain.ChannelMembershipChange, error)
	ListConversations(ctx context.Context, filter domain.AdminConversationFilter) (domain.Page[domain.AdminConversationSummary], error)
}

// ChannelAdminService is the channel and group management surface.
//
// Five verbs: list, read, archive/unarchive, and add/remove a member. There is
// no delete — a channel carries its members' history, and removing it is not
// something an administrative click should be able to do.
//
// Membership is administered here without duplicating the chat domain's rule.
// Who may be added to a channel is a fact about the channel and the person, and
// that half lives once in libs/go/platform/channelmembership, embedded verbatim
// by both writers of chat.channel_members. What legitimately differs is who may
// ask: chat-service requires an active owner/admin/moderator membership of the
// workspace, this service requires the platform capability
// admin.channels.manage, and neither substitutes for the other.
//
// Private DM groups are not administered here and must not be. A platform
// administrator has no authority over a conversation they cannot read, and
// chat.dm_members remains the only thing that decides who participates in one —
// see docs/security/rbac-matrix.md. "Groups" in this console means the
// workspace's channels.
type ChannelAdminService struct {
	store ChannelDirectoryStore
	audit Recorder
}

func NewChannelAdminService(store ChannelDirectoryStore, audit Recorder) *ChannelAdminService {
	return &ChannelAdminService{store: store, audit: audit}
}

func (s *ChannelAdminService) List(ctx context.Context, filter domain.AdminChannelFilter) (domain.Page[domain.AdminChannelSummary], error) {
	if s == nil || s.store == nil {
		return domain.Page[domain.AdminChannelSummary]{}, domain.ErrUnavailable
	}
	return s.store.ListChannels(ctx, filter)
}

func (s *ChannelAdminService) Get(ctx context.Context, channelID string) (domain.AdminChannelDetail, error) {
	if s == nil || s.store == nil {
		return domain.AdminChannelDetail{}, domain.ErrUnavailable
	}
	if !domain.ValidUUID(channelID) {
		return domain.AdminChannelDetail{}, domain.ErrInvalidInput
	}
	return s.store.GetChannel(ctx, channelID)
}

func (s *ChannelAdminService) ListConversations(ctx context.Context, filter domain.AdminConversationFilter) (domain.Page[domain.AdminConversationSummary], error) {
	if s == nil || s.store == nil {
		return domain.Page[domain.AdminConversationSummary]{}, domain.ErrUnavailable
	}
	return s.store.ListConversations(ctx, filter)
}

// SetStatus archives or unarchives a channel.
//
// The state machine has two states and the store validates the transition under
// a row lock, so two operators clicking "archive" at the same moment produce one
// change and one conflict rather than two audit rows claiming the same thing.
func (s *ChannelAdminService) SetStatus(ctx context.Context, actor Actor, channelID, status string) (domain.AdminChannelSummary, error) {
	if s == nil || s.store == nil {
		return domain.AdminChannelSummary{}, domain.ErrUnavailable
	}
	channel, err := s.setStatus(ctx, channelID, status)
	record(ctx, s.audit, actor, domain.AuditActionChannelStatus, "admin.channel:"+channelID, resultFor(err), map[string]string{
		"channel_id":       channelID,
		"requested_status": status,
		"workspace_id":     channel.WorkspaceID,
	})
	return channel, err
}

// MemberCandidates searches the people who may be added to one channel.
//
// It exists so an operator can find somebody by name instead of knowing an
// identifier, and it is a convenience and not a control: the add endpoint
// re-decides eligibility for whoever is actually submitted, so a client that
// skips this search entirely gains nothing.
//
// The limit is clamped rather than refused. This is a picker; asking for a
// thousand results is a client bug, and answering with ten is the useful reply.
func (s *ChannelAdminService) MemberCandidates(ctx context.Context, channelID, query string) ([]domain.ChannelMemberCandidate, error) {
	if s == nil || s.store == nil {
		return nil, domain.ErrUnavailable
	}
	if !domain.ValidUUID(channelID) {
		return nil, domain.ErrInvalidInput
	}
	return s.store.ListMemberCandidates(ctx, channelID, query, domain.MaxMemberCandidates)
}

// AddMembers admits people to a channel.
//
// The list is validated here — non-empty, bounded, every entry a well-formed
// and distinct UUID — before any statement runs, so a malformed request is a
// 400 rather than a rolled-back transaction. Eligibility of the targets is not
// validated here: that is the shared rule, and it is decided by the same
// statement that writes, under the channel's row lock.
func (s *ChannelAdminService) AddMembers(ctx context.Context, actor Actor, channelID string, userIDs []string) (domain.ChannelMembershipChange, error) {
	if s == nil || s.store == nil {
		return domain.ChannelMembershipChange{}, domain.ErrUnavailable
	}
	change, err := s.addMembers(ctx, channelID, userIDs)
	record(ctx, s.audit, actor, domain.AuditActionChannelMemberAdd, "admin.channel:"+channelID, resultFor(err), map[string]string{
		"channel_id":      channelID,
		"workspace_id":    change.WorkspaceID,
		"target_count":    strconv.Itoa(len(userIDs)),
		"added":           strconv.Itoa(change.Added),
		"already_members": strconv.Itoa(change.AlreadyMembers),
	})
	return change, err
}

func (s *ChannelAdminService) addMembers(ctx context.Context, channelID string, userIDs []string) (domain.ChannelMembershipChange, error) {
	if !domain.ValidUUID(channelID) {
		return domain.ChannelMembershipChange{}, domain.ErrInvalidInput
	}
	if len(userIDs) == 0 || len(userIDs) > domain.MaxChannelMembersPerRequest {
		return domain.ChannelMembershipChange{}, domain.ErrInvalidInput
	}
	seen := make(map[string]struct{}, len(userIDs))
	for _, userID := range userIDs {
		if !domain.ValidUUID(userID) {
			return domain.ChannelMembershipChange{}, domain.ErrInvalidInput
		}
		// A repeated id would make the eligible count disagree with the
		// requested count and turn a harmless duplicate into a refusal, so it
		// is refused here where the reason can be stated.
		if _, duplicate := seen[userID]; duplicate {
			return domain.ChannelMembershipChange{}, domain.ErrInvalidInput
		}
		seen[userID] = struct{}{}
	}
	return s.store.AddChannelMembers(ctx, channelID, userIDs)
}

// RemoveMember takes one person out of a channel.
//
// Idempotent: removing somebody who is not a member succeeds and reports that
// nothing changed. The audit row records which of the two happened, so the
// trail does not claim a removal that did not occur.
func (s *ChannelAdminService) RemoveMember(ctx context.Context, actor Actor, channelID, userID string) (domain.ChannelMembershipChange, error) {
	if s == nil || s.store == nil {
		return domain.ChannelMembershipChange{}, domain.ErrUnavailable
	}
	change, err := s.removeMember(ctx, channelID, userID)
	record(ctx, s.audit, actor, domain.AuditActionChannelMemberKick, "admin.channel:"+channelID, resultFor(err), map[string]string{
		"channel_id":     channelID,
		"workspace_id":   change.WorkspaceID,
		"target_user_id": userID,
		"removed":        strconv.FormatBool(change.Removed),
	})
	return change, err
}

func (s *ChannelAdminService) removeMember(ctx context.Context, channelID, userID string) (domain.ChannelMembershipChange, error) {
	if !domain.ValidUUID(channelID) || !domain.ValidUUID(userID) {
		return domain.ChannelMembershipChange{}, domain.ErrInvalidInput
	}
	return s.store.RemoveChannelMember(ctx, channelID, userID)
}

func (s *ChannelAdminService) setStatus(ctx context.Context, channelID, status string) (domain.AdminChannelSummary, error) {
	if !domain.ValidUUID(channelID) {
		return domain.AdminChannelSummary{}, domain.ErrInvalidInput
	}
	if status != domain.ChannelStatusActive && status != domain.ChannelStatusArchived {
		return domain.AdminChannelSummary{}, domain.ErrInvalidInput
	}
	return s.store.UpdateChannelStatus(ctx, channelID, status)
}
