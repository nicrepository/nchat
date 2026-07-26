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
	// ErrChannelDisplayNameRequired and ErrChannelDisplayNameTooLong report the
	// two ways a channel name fails NormalizeChannelDisplayName.
	//
	// Both wrap ErrInvalidInput so every existing errors.Is check keeps mapping
	// them to the same validation status the endpoint already returns — the HTTP
	// contract does not change, only which payloads reach it. Neither message
	// repeats the rejected name.
	ErrChannelDisplayNameRequired = fmt.Errorf("%w: display_name is required", ErrInvalidInput)
	ErrChannelDisplayNameTooLong  = fmt.Errorf("%w: display_name must be %d characters or fewer", ErrInvalidInput, MaxChannelDisplayNameCodePoints)
)
