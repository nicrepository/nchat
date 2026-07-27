package domain

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxChannelCategoryNameCodePoints bounds a channel category's name.
//
// Security resource cap, and a narrower one than a channel's: a category name is
// a sidebar section header, not a title. Counted in Unicode code points to match
// Go utf8.RuneCountInString and PostgreSQL char_length, so an accented or emoji
// name gets the whole allowance rather than a fraction of it.
const MaxChannelCategoryNameCodePoints = 60

// MaxCategoriesPerWorkspace bounds how many categories one workspace may hold.
//
// Two things need it. It is an abuse ceiling on a write that only workspace
// management can perform but that creates permanent workspace-wide rows. And it
// bounds the reorder payload: reordering submits the workspace's complete
// category set, so without a ceiling on the set there is no honest ceiling on
// the request body either.
const MaxCategoriesPerWorkspace = 100

// UncategorizedGroupName is the label of the virtual group that holds channels
// with no category, and the one category name no workspace may persist.
//
// It is not a row. There is no "Geral" category in chat.channel_categories and
// no synthetic ID standing in for one — a channel is in this group exactly when
// its category_id is NULL. The name is reserved (case-insensitively, in the
// service and again in a database CHECK) so that a persisted category can never
// be confused with the virtual one by a client reading the API.
//
// The workspace's seeded #geral *channel* is a different object that happens to
// share the word: it is an ordinary channel with no category, so it appears
// inside this group like any other uncategorized channel.
const UncategorizedGroupName = "Geral"

// NormalizeChannelCategoryName trims a category name and enforces every rule
// the database also enforces, so the two can never disagree about what is
// storable: non-empty after trimming, no control characters, within the code
// point cap, and not the reserved uncategorized-group name.
//
// Returns the value to store; never truncates and never silently rewrites,
// because storing something other than what the caller sent is worse than
// refusing it.
//
// None of the errors carries the offending value: a rejected name is
// caller-controlled text that would otherwise reach every error body and every
// log line that wraps it.
func NormalizeChannelCategoryName(value string) (string, error) {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return "", ErrChannelCategoryNameRequired
	}
	if strings.IndexFunc(normalized, unicode.IsControl) >= 0 {
		return "", ErrChannelCategoryNameInvalid
	}
	if utf8.RuneCountInString(normalized) > MaxChannelCategoryNameCodePoints {
		return "", ErrChannelCategoryNameTooLong
	}
	if strings.EqualFold(normalized, UncategorizedGroupName) {
		return "", ErrChannelCategoryNameReserved
	}
	return normalized, nil
}

// CanManageChannelCategories reports whether a user may create, rename, reorder
// or delete channel categories.
//
// It is the workspace management gate — active owner or admin — reusing
// CanManageWorkspace rather than restating it, exactly as channel update and
// archive do. Reading categories is deliberately not covered: any active member
// may read, and the channels inside each group are filtered by the existing
// channel read policy, so listing categories never widens channel access.
//
// RF-17 was specified as "Admin and Moderator". There is no workspace-level
// moderator in this schema: chat.workspace_members.role is
// owner/admin/member/guest, and 'moderator' exists only on chat.channel_members
// as a per-channel role. Treating "moderates some channel" as "may restructure
// the whole workspace sidebar" would be an escalation, and inventing a workspace
// role nothing can assign would be dead code widening a security constraint.
// This predicate is the named seam to widen when RF-74 introduces a real
// workspace moderation role; the divergence is recorded in SECURITY.md.
func CanManageChannelCategories(wm *WorkspaceMember) bool {
	return CanManageWorkspace(wm)
}
