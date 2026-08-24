package domain

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nicrepository/nchat/libs/go/platform/antispampolicy"
	"github.com/nicrepository/nchat/libs/go/platform/uploadpolicy"
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

// The RF-74 workspace roles, in descending authority.
//
// WorkspaceRoleModerator is a workspace-scoped role and has nothing to do with
// ChannelRoleModerator below, which is per-channel. Neither is ever read as the
// other: moderating one channel does not moderate the workspace, and moderating
// the workspace does not by itself grant access to a private channel.
//
// WorkspaceRoleGuest is a membership that grants no channel access on its own.
// Every other role reaches the workspace's public channels; a guest reaches
// exactly the channels it holds a chat.channel_members row for.
const (
	WorkspaceRoleOwner     WorkspaceRole = "owner"
	WorkspaceRoleAdmin     WorkspaceRole = "admin"
	WorkspaceRoleModerator WorkspaceRole = "moderator"
	WorkspaceRoleMember    WorkspaceRole = "member"
	WorkspaceRoleGuest     WorkspaceRole = "guest"
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
	// MessageRateLimitPerMinute is the RF-19 anti-spam policy: how many
	// messages one user may send in this workspace per minute. Never zero on a
	// value read from the database — the column is NOT NULL with a CHECK.
	MessageRateLimitPerMinute int
	// MaxUploadBytes is the RF-32 attachment size policy: the largest single
	// file that may be attached in this workspace, in bytes. chat-service owns
	// the value; file-service is what enforces it. Never zero on a value read
	// from the database — the column is NOT NULL with a CHECK.
	MaxUploadBytes int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// RF-32 attachment size bounds (issue #458).
//
// They are re-exported from libs/go/platform/uploadpolicy rather than restated:
// the value is decided here and enforced in file-service, so a second copy of
// the numbers is exactly the drift the requirement forbids. The database CHECK
// in migration 000020 mirrors the same two bounds.
const (
	DefaultMaxUploadBytes = uploadpolicy.DefaultMaxUploadBytes
	MinMaxUploadBytes     = uploadpolicy.MinMaxUploadBytes
	MaxMaxUploadBytes     = uploadpolicy.MaxMaxUploadBytes
)

// ValidMaxUploadBytes reports whether value is an acceptable RF-32 policy. Used
// by the admin endpoint before touching the database, which enforces the same
// bounds as a backstop.
func ValidMaxUploadBytes(value int64) bool { return uploadpolicy.Valid(value) }

// EffectiveMaxUploadBytes normalises a persisted policy into a value safe to
// publish and enforce with. A zero or out-of-range value can only come from a
// row written before migration 000020 or from a struct that was never
// populated; in both cases the answer is the default, never "no limit".
func EffectiveMaxUploadBytes(value int64) int64 { return uploadpolicy.Effective(value) }

// RF-19 anti-spam bounds (issue #419).
//
// They are re-exported from libs/go/platform/antispampolicy rather than
// restated, for the same reason the RF-32 bounds below are: the column is
// enforced here and is now also edited from the Admin Console (issue #579), in
// another module. A second copy of "1, 60, 600" is exactly the drift the
// requirement forbids. The database CHECK in migration 000018 mirrors the same
// two bounds.
const (
	DefaultMessageRateLimitPerMinute = antispampolicy.Default
	MinMessageRateLimitPerMinute     = antispampolicy.Min
	MaxMessageRateLimitPerMinute     = antispampolicy.Max
)

// ValidMessageRateLimitPerMinute reports whether value is an acceptable RF-19
// policy. Used by the admin endpoint before touching the database, which
// enforces the same bounds as a backstop.
func ValidMessageRateLimitPerMinute(value int) bool { return antispampolicy.Valid(value) }

// EffectiveMessageRateLimitPerMinute normalises a persisted policy into a value
// safe to hand to the rate limiter. A zero or out-of-range value can only come
// from a row written before migration 000018 or from a struct that was never
// populated; in both cases the answer is the default, never "no limit".
func EffectiveMessageRateLimitPerMinute(value int) int { return antispampolicy.Effective(value) }

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

// ChannelMemberProfile is one member of a channel as the channel-details panel
// needs to render them (issue #435).
//
// It carries a stable ID (for a deterministic avatar colour and for "this is
// you"), the already-resolved visual name, an optional avatar URL and the
// channel role. E-mail, auth source, workspace role, join date and every other
// profile attribute are deliberately absent: none of them is displayed, and a
// member list is the wrong place to hand out a directory.
type ChannelMemberProfile struct {
	UserID      string
	DisplayName string
	AvatarURL   string
	Role        ChannelRole
}

// MaxChannelDetailsMembers bounds the member page the details endpoint returns.
//
// The panel shows a short preview, not the roster, so the response is capped
// server-side and the total is reported separately. A client asking for more
// gets this many.
const MaxChannelDetailsMembers = 30

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

// DMParticipantProfile is one participant of a group conversation as the
// group-details panel renders them (issue #441).
//
// It carries a stable ID (for a deterministic avatar colour and for "this is
// you"), the already-resolved visual name, an optional avatar URL and the live
// presence status. E-mail, workspace role, join date and every other profile
// attribute are deliberately absent.
//
// There is no Role field, unlike ChannelMemberProfile: chat.dm_members.role is
// closed by CHECK to the single value 'member', so it carries no information a
// panel could show. A group has no owner or moderator in this domain.
//
// Presence is decoration here, not a filter: unlike the channel panel — whose
// list is defined as "the members who are online" — a group's participant list
// is every active participant, and someone being offline never removes them
// from it. Presence is therefore resolved for the returned page rather than
// used to select it.
type DMParticipantProfile struct {
	UserID      string
	DisplayName string
	AvatarURL   string
	Presence    string
}

// DMDirectProfile is the other participant of a 1:1 conversation, as the
// profile panel renders them (issue #443).
//
// It is a strictly larger projection than DMParticipantProfile by exactly one
// field — Email — because the prototype's profile card shows the corporate
// address and a participant row does not. Everything else auth.users holds
// (auth_source, external_subject, status, last_login_at, email_verified_at,
// the password credential, every session and device row) stays out: this is a
// profile summary, not a directory record.
//
// Job title, department and time zone are deliberately absent as fields. No
// column anywhere in auth.users, chat.workspace_members or any other table
// stores them today, so there is nothing to project; inventing a field the
// database cannot fill would only move the fabrication one layer down. The
// panel renders "Não informado" for those rows, which is the truth.
type DMDirectProfile struct {
	UserID      string
	DisplayName string
	AvatarURL   string
	Email       string
}

// MaxDMDetailsParticipants bounds the participant page the group-details
// endpoint returns.
//
// The panel shows a short preview, not the roster, so the response is capped
// server-side and the total is reported separately. A client asking for more
// gets this many.
const MaxDMDetailsParticipants = 30

// CallParticipantProfile is the presentation-only identity of one call
// participant: canonical user ID, display name, avatar URL. Deliberately
// slimmer than ChannelMemberProfile/DMParticipantProfile — it carries no
// role and no presence, because a call tile needs neither and the batch
// response should return only what issue #612 asks for.
type CallParticipantProfile struct {
	UserID      string
	DisplayName string
	AvatarURL   string
}

// MaxCallParticipantProfileIDs bounds one call-participant-profile batch
// request (issue #612).
//
// A LiveKit room in this product has no enforced participant ceiling, so
// this is a defensive cap on the request payload, not a room-size limit:
// it stops a caller from turning "resolve who's already in the room" into
// an unbounded IN-list. 50 comfortably covers any real call while staying
// far below anything that could be used to fish for identities.
const MaxCallParticipantProfileIDs = 50

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
// LastMessageAt is the created_at of the newest message persisted in the
// conversation, or nil when it has none (issue #414). It is the conversation's
// activity instant and nothing else: a timestamp the database assigned when the
// message row was written, never a client-supplied value, never the moment an
// event was published or received, and never touched by an edit, a reaction or
// a rename. Nil is a real answer — "no activity yet" — and is what makes an
// empty conversation orderable *after* every conversation that has activity,
// rather than being folded into created_at and competing with it.
//
// It carries no content: not the body, the author, the kind or the message id.
// Its visibility is the conversation's own — it is produced by the same
// authorized listing query, so a conversation the caller may not read never
// reaches this struct to have an activity instant read off it.
type DMConversationWithParticipantIDs struct {
	DMConversation
	ParticipantIDs         []string
	CounterpartUserID      string
	CounterpartDisplayName string
	CounterpartAvatarURL   string
	LastMessageAt          *time.Time
	PinnedAt               *time.Time
	UnreadCount            int
}

// DMCandidate is the minimal profile data exposed when starting a direct DM.
type DMCandidate struct {
	UserID      string
	DisplayName string
}

// CanReachPublicChannels reports whether wm's role reaches the workspace's
// public channels without an explicit channel membership (RF-74).
//
// This is the guest boundary, stated once. Owner, admin, moderator and member
// are workspace-wide roles: belonging to the workspace is what gives them
// #geral and every public channel. A guest is not — its membership grants no
// channel on its own, so it reaches exactly the channels it was added to.
//
// The role test is an allowlist rather than "not a guest" so that an
// unrecognised role — a row written before the CHECK constraint was widened, a
// value from a future migration, a zero-valued struct in a test — is denied
// instead of being treated as a full member. chat.channel_visible_to_user
// applies the same allowlist in SQL and the two must stay identical.
func CanReachPublicChannels(wm *WorkspaceMember) bool {
	if wm == nil || wm.Status != MemberStatusActive {
		return false
	}
	switch wm.Role {
	case WorkspaceRoleOwner, WorkspaceRoleAdmin, WorkspaceRoleModerator, WorkspaceRoleMember:
		return true
	default:
		return false
	}
}

// CanReadChannel reports whether a user may read ch.
// wm is the workspace membership (nil = non-member).
// cm is the channel membership (nil = not a channel member).
//
// An explicit channel membership is sufficient for any role, and for a guest it
// is the only thing that is: public channels and #geral are reachable by
// workspace membership alone only for the roles CanReachPublicChannels admits.
// Private channels always require channel membership, for every role — a
// workspace admin does not read a private channel it does not belong to.
//
// This is the Go statement of chat.channel_visible_to_user. The SQL is the
// authority (it is what the listing, the message reads and the WebSocket
// authorizer actually run); this predicate must agree with it exactly.
func CanReadChannel(wm *WorkspaceMember, cm *ChannelMember, ch Channel) bool {
	if wm == nil || wm.Status != MemberStatusActive || wm.WorkspaceID != ch.WorkspaceID {
		return false
	}
	if ch.Status != ChannelStatusActive {
		return false
	}
	if cm != nil && cm.ChannelID == ch.ID && cm.UserID == wm.UserID {
		return true
	}
	if !CanReachPublicChannels(wm) {
		return false
	}
	return ch.Type == ChannelTypePublic
}

// CanWriteChannel reports whether a user may post to ch.
// For MVP, write access follows the same rules as read access.
func CanWriteChannel(wm *WorkspaceMember, cm *ChannelMember, ch Channel) bool {
	return CanReadChannel(wm, cm, ch)
}

// CanManageWorkspace reports whether a user holds workspace management rights —
// the single predicate behind channel update and archival, and behind workspace
// settings. Channel *creation* is deliberately not one of them: it takes active
// membership that reaches public channels (BUG #393, narrowed for guests by
// CanCreateChannel below). A nil or inactive membership is never sufficient.
//
// RF-74 deliberately does not add the moderator here. A moderator moderates
// channel structure and channel membership; changing what a channel *is*,
// archiving it, or changing the workspace's anti-spam and upload policies is
// administration. Widening this predicate would also widen
// auth-service's admin API, whose workspace is resolved by the same
// owner/admin test in PGXUserStore.GetAdminWorkspaceID.
func CanManageWorkspace(wm *WorkspaceMember) bool {
	if wm == nil || wm.Status != MemberStatusActive {
		return false
	}
	return wm.Role == WorkspaceRoleOwner || wm.Role == WorkspaceRoleAdmin
}

// CanModerateWorkspace reports whether a user holds workspace moderation rights
// (RF-74): every administrator, plus the workspace moderator role.
//
// This is the seam SECURITY.md reserved. RF-17 specified "Admin and Moderator"
// for channel categories and issue #398 inherited the same shape for channel
// membership; both had to settle for owner/admin because no workspace moderator
// existed to name. It exists now, and the two capabilities below delegate here
// instead of to CanManageWorkspace.
//
// It is emphatically not chat.channel_members.role — moderating one channel
// still confers nothing at workspace scope, and no code path reads that column
// for an authorization decision.
func CanModerateWorkspace(wm *WorkspaceMember) bool {
	if CanManageWorkspace(wm) {
		return true
	}
	return wm != nil && wm.Status == MemberStatusActive && wm.Role == WorkspaceRoleModerator
}

// CanCreateChannel reports whether a user may create a channel in the workspace.
//
// Creating a channel still takes no management role (BUG #393): a plain member
// and an owner take the same path, and the role is not otherwise consulted. The
// one role this excludes is guest, and for the same reason it excludes a guest
// from reading a public channel — a guest's reach is the channels it was added
// to, and letting it mint channels of its own would hand it back the
// workspace-wide scope RF-74 removes. It would also let a guest create a public
// channel that every real member sees.
func CanCreateChannel(wm *WorkspaceMember) bool { return CanReachPublicChannels(wm) }

// MaxAddMembersPerRequest bounds one add-members call (issue #398).
//
// A batch ceiling, not a conversation ceiling: it caps how many membership rows
// and per-user eligibility joins a single accepted request can cost, so an
// oversized payload is refused by one comparison instead of by the database.
// It is deliberately below the 50-participant group maximum, so no single
// request can fill a group from empty, and it is comfortably above what the
// panel's selector produces in one human confirmation.
const MaxAddMembersPerRequest = 25

// CanManageChannelMembers reports whether a user may add participants to a
// channel (issue #398).
//
// It is the workspace moderation gate — active owner, admin or moderator —
// reusing CanModerateWorkspace rather than restating it, exactly as channel
// categories do. The choice is not a new policy: removing a member from a
// channel is the same authority (MemberService.RemoveMemberFromChannel), and
// docs/runbooks/task-chat-channel-join-leave.md names the addition its
// "manager-add flow". Adding and removing the same row are the same authority.
//
// RF-74 is what widened this from owner/admin to include the moderator: this
// predicate was written as the named seam for exactly that, and the seam is now
// spent. The widening reaches the store as well — PGXMemberStore.AddChannelMembers
// re-derives the same role list inside its transaction.
//
// Deliberately *not* "any member of the channel": that would let anyone with
// read access to a private channel widen its audience, which is precisely the
// property a private channel has. Deliberately not the per-channel 'moderator'
// role either — that role is per channel and is never read as workspace
// authority.
//
// Group DM participation is a different question with a different answer and is
// deliberately not routed through here: chat.dm_members.role is closed by CHECK
// to the single value 'member', so a group has no manager to be, and a
// workspace admin is not even a participant. See DMService.AddGroupParticipants.
func CanManageChannelMembers(wm *WorkspaceMember) bool {
	return CanModerateWorkspace(wm)
}
