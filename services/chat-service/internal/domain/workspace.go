package domain

import (
	"strings"
	"time"
	"unicode/utf8"
)

type WorkspaceStatus string

const (
	WorkspaceStatusActive   WorkspaceStatus = "active"
	WorkspaceStatusDisabled WorkspaceStatus = "disabled"
)

type ChannelType string

const (
	ChannelTypePublic  ChannelType = "public"
	ChannelTypePrivate ChannelType = "private"
)

type ChannelStatus string

const (
	ChannelStatusActive   ChannelStatus = "active"
	ChannelStatusArchived ChannelStatus = "archived"
)

type DMConversationType string

const (
	DMConversationTypeDirect DMConversationType = "direct"
	DMConversationTypeGroup  DMConversationType = "group"
)

type DMConversationStatus string

const (
	DMConversationStatusActive   DMConversationStatus = "active"
	DMConversationStatusArchived DMConversationStatus = "archived"
)

type WorkspaceRole string

const (
	WorkspaceRoleOwner  WorkspaceRole = "owner"
	WorkspaceRoleAdmin  WorkspaceRole = "admin"
	WorkspaceRoleMember WorkspaceRole = "member"
	WorkspaceRoleGuest  WorkspaceRole = "guest"
)

type MemberStatus string

const (
	MemberStatusActive    MemberStatus = "active"
	MemberStatusSuspended MemberStatus = "suspended"
	MemberStatusLeft      MemberStatus = "left"
)

type ChannelRole string

const (
	ChannelRoleMember    ChannelRole = "member"
	ChannelRoleModerator ChannelRole = "moderator"
)

type DMMemberRole string

const (
	DMMemberRoleMember DMMemberRole = "member"
)

type DMMemberStatus string

const (
	DMMemberStatusActive DMMemberStatus = "active"
	DMMemberStatusLeft   DMMemberStatus = "left"
)

// Workspace represents a single team/organisation space.
type Workspace struct {
	ID                string
	Slug              string
	Name              string
	Status            WorkspaceStatus
	EditWindowSeconds *int
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// ChannelCategory is an organisational folder for channels within a workspace.
type ChannelCategory struct {
	ID          string
	WorkspaceID string
	Name        string
	Position    int
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Channel represents a public or private communication channel.
// CategoryID and CreatedBy are empty string when NULL in the database.
type Channel struct {
	ID          string
	WorkspaceID string
	CategoryID  string
	Slug        string
	DisplayName string
	Type        ChannelType
	Status      ChannelStatus
	IsGeneral   bool
	Position    int
	CreatedBy   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// MaxChannelDisplayNameCodePoints bounds a channel's display name.
//
// Security resource cap. Counted in Unicode code points to match Go
// utf8.RuneCountInString and PostgreSQL char_length.
const MaxChannelDisplayNameCodePoints = 100

// NormalizeChannelDisplayName trims a channel name and enforces the cap.
//
// The single rule behind every path that persists chat.channels.display_name —
// creation, update and the workspace bootstrap — so no writer can be left with a
// weaker one. Returns the value to store; never truncates, because silently
// storing something other than what the caller sent is worse than refusing it.
//
// Neither error carries the offending value: a rejected name can be tens of
// kilobytes of caller-controlled text, and it would otherwise reach every error
// body and every log line that wraps it.
func NormalizeChannelDisplayName(value string) (string, error) {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return "", ErrChannelDisplayNameRequired
	}
	if utf8.RuneCountInString(normalized) > MaxChannelDisplayNameCodePoints {
		return "", ErrChannelDisplayNameTooLong
	}
	return normalized, nil
}

// WorkspaceMember represents a user's membership in a workspace.
type WorkspaceMember struct {
	WorkspaceID string
	UserID      string
	Role        WorkspaceRole
	Status      MemberStatus
	JoinedAt    time.Time
}

// ChannelMember represents a user's membership in a specific channel.
type ChannelMember struct {
	ChannelID string
	UserID    string
	Role      ChannelRole
	JoinedAt  time.Time
}

// DMConversation represents a direct or ad-hoc group DM conversation.
// Title is empty string when NULL in the database.
type DMConversation struct {
	ID          string
	WorkspaceID string
	Type        DMConversationType
	Title       string
	Status      DMConversationStatus
	CreatedBy   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// DMMember represents membership in a DM conversation.
// LeftAt is zero when NULL in the database.
type DMMember struct {
	ConversationID string
	UserID         string
	Role           DMMemberRole
	Status         DMMemberStatus
	JoinedAt       time.Time
	LeftAt         time.Time
}

// DMConversationWithParticipantIDs extends DMConversation with the list of
// active member user IDs, used by the sidebar query to avoid N+1 fetches.
//
// The Counterpart* fields are viewer-scoped: they describe the other
// participant of a direct 1:1 conversation, as seen by the user the listing was
// performed for. They are never persisted on the conversation, since the same
// conversation resolves to a different counterpart for each participant.
// All three are empty for group conversations and whenever the counterpart
// cannot be resolved (removed member, missing user row).
//
// CounterpartDisplayName is already the resolved visual name: full_name when
// present, display_name otherwise. CounterpartAvatarURL is optional and carries
// whatever auth.users stores; the client decides whether it is renderable and
// falls back to initials. The counterpart's e-mail, status, auth source and
// external subject are never carried here.
type DMConversationWithParticipantIDs struct {
	DMConversation
	ParticipantIDs         []string
	CounterpartUserID      string
	CounterpartDisplayName string
	CounterpartAvatarURL   string
}

// DMCandidate is the minimal profile data exposed when starting a direct DM.
type DMCandidate struct {
	UserID      string
	DisplayName string
}

// CanReadChannel reports whether a user may read ch.
// wm is the workspace membership (nil = non-member).
// cm is the channel membership (nil = not a channel member).
// Public channels and #geral require active workspace membership only.
// Private channels additionally require channel membership.
func CanReadChannel(wm *WorkspaceMember, cm *ChannelMember, ch Channel) bool {
	if wm == nil || wm.Status != MemberStatusActive || wm.WorkspaceID != ch.WorkspaceID {
		return false
	}
	if ch.Status != ChannelStatusActive {
		return false
	}
	if ch.IsGeneral {
		return ch.Type == ChannelTypePublic
	}
	if ch.Type == ChannelTypePublic {
		return true
	}
	return ch.Type == ChannelTypePrivate && cm != nil && cm.ChannelID == ch.ID && cm.UserID == wm.UserID
}

// CanWriteChannel reports whether a user may post to ch.
// For MVP, write access follows the same rules as read access.
func CanWriteChannel(wm *WorkspaceMember, cm *ChannelMember, ch Channel) bool {
	return CanReadChannel(wm, cm, ch)
}

// CanManageWorkspace reports whether a user holds workspace management rights —
// the single predicate behind channel update and archival, and behind workspace
// settings. Channel *creation* is deliberately not one of them: it takes active
// membership only (BUG #393). A nil or inactive membership is never sufficient.
func CanManageWorkspace(wm *WorkspaceMember) bool {
	if wm == nil || wm.Status != MemberStatusActive {
		return false
	}
	return wm.Role == WorkspaceRoleOwner || wm.Role == WorkspaceRoleAdmin
}
