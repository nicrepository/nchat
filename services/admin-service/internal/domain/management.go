package domain

import "time"

// Administrative management model (issue #579).
//
// Everything here is a read model or a validated command. None of it is a
// persisted row: the store maps columns onto these types and the HTTP layer
// maps these types onto response structs, so neither a database column nor a
// request body reaches the other side by being spread through.

// Page is one page of a keyset-paginated listing.
//
// NextCursor is empty on the last page, and HasMore is derived from it by the
// service rather than reported independently, so the two cannot disagree.
type Page[T any] struct {
	Items      []T
	NextCursor string
}

// HasMore reports whether another page exists.
func (p Page[T]) HasMore() bool { return p.NextCursor != "" }

// Pagination bounds shared by every administrative listing.
//
// The ceiling is what stops a caller turning `limit` into a request for the
// whole table; the default is what an unspecified limit means.
const (
	DefaultPageSize = 25
	MaxPageSize     = 100
)

// ClampPageSize normalises a requested page size.
//
// Zero or negative means "unspecified" and gets the default; anything above the
// ceiling is capped rather than refused, so an over-large value is a smaller
// page and never an error a caller can use to probe the bound.
func ClampPageSize(limit int) int {
	switch {
	case limit <= 0:
		return DefaultPageSize
	case limit > MaxPageSize:
		return MaxPageSize
	default:
		return limit
	}
}

// ---------------------------------------------------------------------------
// Users
// ---------------------------------------------------------------------------

// UserStatusFilter is the closed set of account statuses a caller may filter
// on.
//
// It is an allowlist and not a passthrough: the value reaches a parameterised
// comparison, but accepting an arbitrary string would still let a caller
// discover which statuses exist by which ones return rows.
//
// 'deleted' is in the CHECK on auth.users.status and is deliberately NOT here.
// The directory excludes soft-deleted accounts unconditionally — every reader of
// auth.users on this platform does, and nothing in the repository ever writes
// that status or deleted_at in the first place. Accepting the value would
// publish a filter that can never return a row, and making it return rows would
// turn a contract fix into administrative access to erased and anonymized
// people, which is retention work this issue explicitly does not do.
var UserStatusFilter = map[string]struct{}{
	"active":    {},
	"invited":   {},
	"suspended": {},
	"locked":    {},
}

// UserAuthSourceFilter mirrors the CHECK on auth.users.auth_source.
var UserAuthSourceFilter = map[string]struct{}{
	"manual":   {},
	"oidc":     {},
	"imported": {},
}

// WorkspaceRoleFilter is the closed set of workspace roles a caller may filter
// the directory on. It mirrors chat.workspace_members_role_check as migration
// 000022 widened it.
//
// These are workspace roles and nothing else. They are not the platform's
// administrative roles and they are not capabilities: PlatformAdmin below is a
// separate question with a separate filter, and the two must stay separate
// because "owner of a workspace" confers no platform authority whatsoever (see
// docs/security/rbac-matrix.md).
var WorkspaceRoleFilter = map[string]struct{}{
	"owner":     {},
	"admin":     {},
	"moderator": {},
	"member":    {},
	"guest":     {},
}

// UserActivityFilter names the "when did they last sign in" buckets the
// directory offers. Each maps to a fixed interval below, never to a
// caller-supplied one.
var UserActivityFilter = map[string]time.Duration{
	"7d":  7 * 24 * time.Hour,
	"30d": 30 * 24 * time.Hour,
	"90d": 90 * 24 * time.Hour,
}

// ActivityFilterNever selects accounts that have never signed in. It is not a
// duration, so it is its own constant rather than a zero entry in the map
// above, where zero would be indistinguishable from "unset".
const ActivityFilterNever = "never"

// AdminUserFilter is the validated shape of a user directory query.
//
// Every field is either a bounded scalar or a value drawn from one of the
// allowlists above. There is no sort parameter at all: the directory has one
// order (newest first), so there is no ordering expression for a caller to
// influence.
type AdminUserFilter struct {
	// Query matches display name, full name or e-mail, case-insensitively.
	Query string
	// Status, when set, is a key of UserStatusFilter.
	Status string
	// AuthSource, when set, is a key of UserAuthSourceFilter.
	AuthSource string
	// PlatformAdmin narrows to (or excludes) active platform administrators.
	// Nil means "either", which is not the same as false.
	PlatformAdmin *bool
	// WorkspaceRole, when set, is a key of WorkspaceRoleFilter.
	//
	// The directory is platform-wide — no request names a workspace — so the
	// filter reads as "holds at least one active membership with this role, in
	// any active workspace". It cannot mean "in workspace X" because there is
	// no X in the request, and inventing one would be a scope the endpoint does
	// not have.
	WorkspaceRole string
	// Inactivity, when set, is ActivityFilterNever or a key of
	// UserActivityFilter, and selects accounts with no login since that window.
	Inactivity string

	Limit  int
	Cursor Cursor
}

// AdminUserSummary is one row of the platform user directory.
type AdminUserSummary struct {
	ID          string
	Email       string
	DisplayName string
	FullName    string
	AvatarURL   string
	Status      string
	// AuthSource is where the identity comes from: 'manual' (NChat-local),
	// 'oidc' (an external identity provider) or 'imported'.
	AuthSource string
	// ExternalProvider names the identity provider for an OIDC account, e.g.
	// "keycloak". Empty for a local account. The console uses it to say which
	// fields NChat is not the source of truth for.
	ExternalProvider string
	LastLoginAt      *time.Time
	CreatedAt        time.Time
	// PlatformAdmin is whether the person holds an *active* administrative
	// principal. It is a fact about auth.admin_principals, never a role claim.
	PlatformAdmin  bool
	AdminRoles     []string
	WorkspaceRoles []WorkspaceRoleRef
	ActiveSessions int
}

// WorkspaceRoleRef is one workspace membership as the directory shows it.
type WorkspaceRoleRef struct {
	WorkspaceID   string
	WorkspaceName string
	Role          string
	Status        string
	JoinedAt      time.Time
}

// ChannelMemberCandidate is somebody the console may offer as a new member of
// one channel.
//
// It is a person as a person: a name, an e-mail, an avatar. The identifier
// travels because the mutation needs it, not because an operator should have to
// know it.
//
// The projection is deliberately narrower than AdminUserSummary. A picker needs
// to show who somebody is; it does not need their administrative roles, their
// workspace memberships, their session count or where their identity comes
// from, and a search endpoint that returned those would be a second, wider user
// directory behind a channel capability.
type ChannelMemberCandidate struct {
	UserID      string
	DisplayName string
	FullName    string
	Email       string
	AvatarURL   string
	// WorkspaceRole is the person's role in the channel's workspace. It is shown
	// so an operator adding somebody to a private channel can see they are about
	// to add a guest.
	WorkspaceRole string
}

// MaxMemberCandidates bounds one candidate search.
//
// Small on purpose: this is a picker, not a directory. An operator narrows by
// typing, and a list long enough to scroll is a list nobody reads.
const MaxMemberCandidates = 10

// AdminRoleGrant is one administrative role held by a principal.
type AdminRoleGrant struct {
	Slug         string
	Description  string
	GrantedAt    time.Time
	GrantedBy    string
	Capabilities []string
}

// AdminRoleDescriptor is one role an operator may grant, with what it confers.
type AdminRoleDescriptor struct {
	Slug         string
	Description  string
	Capabilities []string
}

// AdminUserDetail is the record behind one directory row.
//
// It is loaded only when an operator opens a person, because it costs several
// aggregates the listing has no use for.
type AdminUserDetail struct {
	AdminUserSummary
	Memberships  []WorkspaceRoleRef
	ChannelCount int
	RoleGrants   []AdminRoleGrant
	// AvailableRoles is the catalogue of grantable roles. It travels with the
	// detail so the console does not need a second endpoint for two seeded
	// rows; it names roles and capabilities, which are public policy, and no
	// principal.
	AvailableRoles []AdminRoleDescriptor
}

// UserStatusChange is the validated command behind a status transition.
type UserStatusChange struct {
	TargetUserID string
	FromStatus   string
	ToStatus     string
	// RevokedSessions is how many live sessions the transition ended. It is
	// recorded in the audit trail so "suspended" and "and their sessions were
	// closed" are one fact rather than two hopes.
	RevokedSessions int
}

// Administrative statuses this console may move an account between.
//
// Deliberately two, and deliberately not the full CHECK list on auth.users:
// 'invited', 'locked' and 'deleted' are produced by other flows (invitation,
// brute-force lockout, erasure) and are not a switch an operator flips. This
// mirrors domain.ValidateStatusTransition in auth-service.
const (
	UserStatusActive    = "active"
	UserStatusSuspended = "suspended"
)

// ValidUserStatusTransition reports whether an administrator may move an
// account from one status to the other.
//
// Both directions between active and suspended are allowed; everything else —
// including a no-op onto the same status — is refused, because a "change" that
// changes nothing would still write an audit row claiming something happened.
func ValidUserStatusTransition(from, to string) bool {
	switch {
	case from == UserStatusActive && to == UserStatusSuspended:
		return true
	case from == UserStatusSuspended && to == UserStatusActive:
		return true
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// Channels
// ---------------------------------------------------------------------------

// ChannelTypeFilter and ChannelStatusFilter mirror the CHECKs on chat.channels.
var (
	ChannelTypeFilter   = map[string]struct{}{"public": {}, "private": {}}
	ChannelStatusFilter = map[string]struct{}{"active": {}, "archived": {}}
)

// ChannelActivityFilter names the "used recently" buckets the directory offers.
var ChannelActivityFilter = map[string]time.Duration{
	"7d":  7 * 24 * time.Hour,
	"30d": 30 * 24 * time.Hour,
	"90d": 90 * 24 * time.Hour,
}

// AdminChannelFilter is the validated shape of a channel directory query.
type AdminChannelFilter struct {
	// Query matches the channel's display name or slug, case-insensitively.
	Query       string
	WorkspaceID string
	Type        string
	Status      string
	// MinMembers selects channels with at least this many members. Zero means
	// unset.
	MinMembers int
	// ActiveWithin, when set, is a key of ChannelActivityFilter and selects
	// channels with a message inside that window.
	ActiveWithin string
	// AdministeredBy, when set, is a user id, and selects the channels that
	// person administers.
	//
	// "Administers a channel" is deliberately narrow, because the chat domain
	// has no channel owner and no channel admin. What it has is a creator
	// (chat.channels.created_by) and per-channel moderators
	// (chat.channel_members.role = 'moderator'), and this filter is exactly
	// their union.
	//
	// It does NOT include the workspace's owners and admins. Those govern the
	// workspace, not one channel, and folding them in would make every channel
	// in a workspace match its owner — which answers a different question and
	// is precisely the collapse docs/security/rbac-matrix.md exists to prevent.
	AdministeredBy string

	Limit  int
	Cursor Cursor
}

// AdminChannelSummary is one row of the platform channel directory.
//
// It carries no message and no message excerpt. "Last activity" is a
// timestamp, deliberately not a preview: the console lists where conversation
// happens, never what was said.
type AdminChannelSummary struct {
	ID             string
	WorkspaceID    string
	WorkspaceName  string
	Slug           string
	DisplayName    string
	Type           string
	Status         string
	IsGeneral      bool
	MemberCount    int
	ModeratorCount int
	CreatedByName  string
	CreatedByEmail string
	CreatedAt      time.Time
	LastActivityAt *time.Time
}

// AdminChannelDetail is the record behind one channel row.
type AdminChannelDetail struct {
	AdminChannelSummary
	CategoryName string
	// Moderators are the channel's own moderators
	// (chat.channel_members.role = 'moderator'), which is a per-channel role.
	Moderators []ChannelMemberRef
	// WorkspaceAdmins are the owners and admins of the channel's workspace.
	// They are listed because "who can administer this" is the question the
	// screen exists to answer — not because a channel moderator and a workspace
	// admin are the same authority. They are not, and the payload keeps them in
	// separate fields for that reason.
	WorkspaceAdmins []ChannelMemberRef
	// Members is a bounded preview of the channel's membership, so the console
	// has something to administer. MemberCount on the summary is the real
	// total; this list is capped, and the cap is what keeps a detail view from
	// becoming a membership export.
	Members      []ChannelMemberRef
	MessageCount int64
}

// ChannelMemberRef identifies a person against a channel or workspace role.
type ChannelMemberRef struct {
	UserID      string
	DisplayName string
	Email       string
	Role        string
}

// ChannelMembershipChange is what an applied membership mutation reports back.
//
// Added and AlreadyMembers are separate because they are separate outcomes: a
// retry of the same add is a success that added nobody, and an operator reading
// "2 added" when nothing changed would be told something false.
type ChannelMembershipChange struct {
	ChannelID   string
	WorkspaceID string
	Added       int
	// AlreadyMembers counts eligible targets that were already in the channel.
	AlreadyMembers int
	// Removed is false when the person was not a member. Removal is idempotent:
	// a retry is a success, not a 404, because the caller's intent — "this
	// person is not in this channel" — already holds.
	Removed     bool
	MemberCount int
}

// MaxChannelMembersPerRequest bounds one administrative add.
//
// The statement is all-or-nothing over the whole list, so an unbounded list
// would be an unbounded transaction. Fifty is far more than an operator adds by
// hand and far less than a workspace.
const MaxChannelMembersPerRequest = 50

// Channel statuses an administrator may move a channel between.
const (
	ChannelStatusActive   = "active"
	ChannelStatusArchived = "archived"
)

// ValidChannelStatusTransition reports whether the archive/unarchive command is
// a real change. A transition onto the current status is refused for the same
// reason a no-op user status change is.
func ValidChannelStatusTransition(from, to string) bool {
	switch {
	case from == ChannelStatusActive && to == ChannelStatusArchived:
		return true
	case from == ChannelStatusArchived && to == ChannelStatusActive:
		return true
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// Conversations (DM metadata)
// ---------------------------------------------------------------------------

// ConversationTypeFilter and ConversationStatusFilter mirror the CHECKs on
// chat.dm_conversations.
var (
	ConversationTypeFilter   = map[string]struct{}{"direct": {}, "group": {}}
	ConversationStatusFilter = map[string]struct{}{"active": {}, "archived": {}}
)

// AdminConversationFilter is the validated shape of a conversation metadata
// query. There is no search field: searching conversations would mean searching
// their titles or their content, and neither leaves the chat.
type AdminConversationFilter struct {
	WorkspaceID string
	Type        string
	Status      string

	Limit  int
	Cursor Cursor
}

// AdminConversationSummary is operational metadata about one private
// conversation, and nothing else.
//
// What is absent is the contract: no body, no rich text, no attachment, no
// quote, no reaction, no preview, no "most recent message", no title (a group
// title is written by its participants), and no participant identities. A
// platform administrator does not become a participant by being an
// administrator, and chat.dm_members remains the only thing that decides who
// may read a conversation.
//
// What is present is what operating the platform needs: which conversation,
// where, of what kind, in what state, how many people, how much traffic, and
// when it was last used.
type AdminConversationSummary struct {
	ID               string
	WorkspaceID      string
	WorkspaceName    string
	Type             string
	Status           string
	ParticipantCount int
	MessageCount     int64
	CreatedAt        time.Time
	UpdatedAt        time.Time
	LastActivityAt   *time.Time
}

// ---------------------------------------------------------------------------
// Operational policies
// ---------------------------------------------------------------------------

// WorkspaceRef identifies the workspace a policy belongs to.
type WorkspaceRef struct {
	ID     string
	Slug   string
	Name   string
	Status string
}

// AntiSpamPolicy is the RF-19 per-user message budget of one workspace.
type AntiSpamPolicy struct {
	Workspace                 WorkspaceRef
	MessageRateLimitPerMinute int
}

// UploadPolicy is the RF-32 attachment size limit of one workspace.
type UploadPolicy struct {
	Workspace      WorkspaceRef
	MaxUploadBytes int64
}

// PolicyChange is what an applied policy update reports back, so the audit
// trail can record a diff instead of only a new value.
type PolicyChange struct {
	WorkspaceID string
	From        int64
	To          int64
}

// ---------------------------------------------------------------------------
// Audit
// ---------------------------------------------------------------------------

// AuditResourceUserPrefix is the canonical way an audit event names the user it
// was performed on.
//
// Every producer that acts *on* an account already writes
// "admin.user:<uuid>" into auth.admin_audit_events.resource, and this constant
// is where that string is now defined so the producers and the reader cannot
// drift into two spellings of the same key.
//
// It is a resource key and not a search term: the filter compares it for
// equality against an indexed column. Nothing anywhere matches it against
// metadata, and there is no path by which a caller supplies a fragment of SQL,
// a JSON path or a pattern.
const AuditResourceUserPrefix = "admin.user:"

// AuditUserResource builds the resource key for one account.
func AuditUserResource(userID string) string { return AuditResourceUserPrefix + userID }

// AuditFilter narrows the audit trail.
//
// The zero value is the platform-wide trail, which is what the console's audit
// section has always shown. Resource is the only narrowing this API offers, and
// it is not a free-text field: the HTTP layer builds it from a validated user
// id and nothing else reaches it.
//
// Deliberately not a query language. "Show me this person's history" is the one
// question the console asks, so it is the one question the contract can
// express — there is no metadata key, no operator and no expression for a
// caller to compose.
type AuditFilter struct {
	Resource string
	Limit    int
}

// ---------------------------------------------------------------------------
// Audit actions
// ---------------------------------------------------------------------------

// Audit action names for the management surface. Constants for the same reason
// the foundation's are: a rename must not silently split one action into two in
// the trail.
const (
	AuditActionUserStatusUpdate   = "admin.user.status.update"
	AuditActionUserSessionsRevoke = "admin.user.sessions.revoke"
	AuditActionUserRoleGrant      = "admin.user.role.grant"
	AuditActionUserRoleRevoke     = "admin.user.role.revoke"
	AuditActionChannelStatus      = "admin.channel.status.update"
	AuditActionChannelMemberAdd   = "admin.channel.member.add"
	AuditActionChannelMemberKick  = "admin.channel.member.remove"
	AuditActionPolicyAntiSpam     = "admin.policy.antispam.update"
	AuditActionPolicyUpload       = "admin.policy.upload.update"
)
