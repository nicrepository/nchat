package domain

import (
	"errors"
	"fmt"
)

var (
	ErrNotFound                  = errors.New("not found")
	ErrForbidden                 = errors.New("forbidden")
	ErrInvalidInput              = errors.New("invalid input")
	ErrConflict                  = errors.New("conflict")
	ErrInvalidToken              = errors.New("invalid token")
	ErrDuplicateSlug             = errors.New("slug already in use")
	ErrAlreadyMember             = errors.New("already a member")
	ErrMemberInactive            = errors.New("workspace member is inactive")
	ErrGeneralChannelExists      = errors.New("workspace already has a general channel")
	ErrGeneralChannelMissing     = errors.New("workspace general channel not found")
	ErrCannotLeaveGeneralChannel = errors.New("cannot leave general channel")
	ErrInvalidMessageTarget      = errors.New("invalid message target")
	// ErrCallParticipantBusy reports that a direct or resource-call admission
	// was refused because the affected user already holds an active/ringing
	// direct call or a live resource-call participant lease. It wraps
	// ErrConflict so existing generic conflict handling still matches it, but
	// callers that need to tell "someone is busy" apart from a real
	// lifecycle/version conflict (issue #575) can check for it first.
	ErrCallParticipantBusy = fmt.Errorf("%w: call participant already in another call", ErrConflict)
	// ErrCallParticipationStale reports that a call.leave or call.presence
	// carried a participation_id fencing token that no longer matches the
	// actor's current lease on that call (issue #622 round 3) — a newer
	// admission (a rejoin, a handoff, a second tab) already rotated it, or
	// the actor never held a fenced lease with that identity in the first
	// place. Wraps ErrConflict for the same reason ErrCallParticipantBusy
	// does: existing generic conflict handling still matches it, but a
	// caller that must never treat this as "my current participation ended"
	// (issue #622 round 3's cross-tab false-left guard) checks for it first.
	// Never reveals the actual current participation_id or any other
	// participant's identity — the caller learns only that its own claimed
	// fencing token is no longer authoritative.
	ErrCallParticipationStale = fmt.Errorf("%w: call participation is no longer current", ErrConflict)
	// ErrInconsistentDirectConversation reports a chat.dm_conversations row of
	// type 'direct' that does not resolve to exactly one other active
	// participant. The domain says a direct conversation is a pair, so this is
	// corrupt data, not a denial: it must surface as a server error an operator
	// can see rather than as a 404 that would hide it among the legitimate
	// refusals.
	ErrInconsistentDirectConversation = errors.New("direct conversation does not have exactly one counterpart")
	// ErrInvalidMessageReference is returned when a parent, forwarded_from, or
	// referenced message ID fails validation. The error is intentionally generic so
	// that callers cannot determine whether the referenced message exists.
	ErrInvalidMessageReference = errors.New("invalid message reference")
	// ErrInvalidCursor is returned when a pagination cursor cannot be decoded or
	// contains values that fail validation (malformed timestamp, invalid UUID).
	ErrInvalidCursor = errors.New("invalid pagination cursor")
	// ErrEditForbidden covers author-only and deleted-message edit denials.
	ErrEditForbidden = errors.New("message edit forbidden")
	// ErrEditWindowExpired reports an edit outside the workspace-configured window.
	ErrEditWindowExpired = errors.New("message edit window expired")
	// ErrPinLimitReached is returned when a channel already holds the maximum
	// number of pinned messages (RF-05 abuse ceiling).
	ErrPinLimitReached = errors.New("pin limit reached")
	// ErrMaliciousURL reports a message body carrying a link the Safe Browsing
	// provider considers a security threat, or one whose host has no reputation
	// that could be consulted at all (RF-21). Both are permanent: the same body
	// will be refused again.
	//
	// It is its own error and not an ErrInvalidInput, because a client must be
	// able to tell "this link is dangerous" from "your request was malformed"
	// without reading the message text.
	ErrMaliciousURL = errors.New("message carries a malicious url")
	// ErrURLCheckUnavailable reports that a link's safety could not be
	// established. It is separate from ErrMaliciousURL because the two mean
	// opposite things to the person typing: one says the link is bad, the other
	// says to try again.
	ErrURLCheckUnavailable = errors.New("url safety check unavailable")
	// ErrURLCheckPending reports that a link is being scanned and no verdict
	// exists yet (RF-21).
	//
	// It is separate from the other two because it means neither "bad" nor
	// "broken": the scan was queued by this very request and will finish. Only
	// editing uses it — creating and forwarding withhold the message instead,
	// which is the better answer when there is no already-published version to
	// preserve.
	ErrURLCheckPending = errors.New("url safety check pending")
	// ErrLinkScanCapacity reports that the operation would require new provider
	// work this deployment is not currently willing to spend.
	//
	// It is its own error, and keeping it apart from ErrMaliciousURL is the whole
	// point: a full queue or a spent window says nothing whatsoever about the
	// links in the message. Reporting one as the other would show a sender a
	// security warning for an operational condition, and would teach an operator
	// to read a spike in refusals as an attack on their users rather than as a
	// provider that has stopped keeping up.
	//
	// It is also not ErrURLCheckUnavailable. Unavailable means the check failed;
	// this means the check was never attempted, on purpose, and retrying shortly
	// is the right response.
	ErrLinkScanCapacity = errors.New("link scan capacity exceeded")
	// ErrChannelDisplayNameRequired and ErrChannelDisplayNameTooLong report the
	// two ways a channel name fails NormalizeChannelDisplayName.
	//
	// Both wrap ErrInvalidInput so every existing errors.Is check keeps mapping
	// them to the same validation status the endpoint already returns — the HTTP
	// contract does not change, only which payloads reach it. Neither message
	// repeats the rejected name.
	ErrChannelDisplayNameRequired = fmt.Errorf("%w: display_name is required", ErrInvalidInput)
	ErrChannelDisplayNameTooLong  = fmt.Errorf("%w: display_name must be %d characters or fewer", ErrInvalidInput, MaxChannelDisplayNameCodePoints)
	// The four ways a channel category name fails
	// NormalizeChannelCategoryName (RF-17), and the two ways a category write
	// conflicts with existing state.
	//
	// The name errors wrap ErrInvalidInput and the conflicts wrap ErrConflict, so
	// each maps to the status the HTTP layer already returns for its family
	// without a new branch. None of the messages repeats the rejected name.
	ErrChannelCategoryNameRequired = fmt.Errorf("%w: name is required", ErrInvalidInput)
	ErrChannelCategoryNameInvalid  = fmt.Errorf("%w: name must not contain control characters", ErrInvalidInput)
	ErrChannelCategoryNameTooLong  = fmt.Errorf("%w: name must be %d characters or fewer", ErrInvalidInput, MaxChannelCategoryNameCodePoints)
	ErrChannelCategoryNameReserved = fmt.Errorf("%w: %q is reserved for uncategorized channels", ErrInvalidInput, UncategorizedGroupName)
	// ErrDuplicateChannelCategoryName reports a name already used by another
	// category in the same workspace, compared case-insensitively. The collision
	// is never silenced: the write fails and the caller is told.
	ErrDuplicateChannelCategoryName = fmt.Errorf("%w: category name already in use", ErrConflict)
	// ErrChannelCategoryLimitReached reports the per-workspace category ceiling.
	ErrChannelCategoryLimitReached = fmt.Errorf("%w: workspace category limit reached", ErrConflict)
	// ErrTooManyMembersRequested reports a batch above MaxAddMembersPerRequest.
	//
	// This is a bound on the *request*, decided before any database work, and it
	// never depends on who the caller is or what the conversation contains.
	// There is deliberately no companion error for a conversation being "full":
	// channels and groups have no fixed participant capacity in this product.
	ErrTooManyMembersRequested = fmt.Errorf("%w: at most %d users may be added per request", ErrInvalidInput, MaxAddMembersPerRequest)
	// ErrNoMembersRequested reports an empty or all-blank user list. An add that
	// adds nobody is a client bug, not a no-op success.
	ErrNoMembersRequested = fmt.Errorf("%w: user_ids must contain at least one user", ErrInvalidInput)
	// ErrInvalidChannelCategoryOrder reports a reorder payload that is not
	// exactly the workspace's category set: a duplicate ID, a missing one, or one
	// belonging to another workspace. It says which rule was broken but never
	// which IDs exist, so it cannot be used to enumerate another workspace.
	ErrInvalidChannelCategoryOrder = fmt.Errorf("%w: order must list every category of the workspace exactly once", ErrInvalidInput)
)
