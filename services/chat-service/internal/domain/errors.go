package domain

import "errors"

var (
	ErrNotFound                  = errors.New("not found")
	ErrForbidden                 = errors.New("forbidden")
	ErrInvalidInput              = errors.New("invalid input")
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
	// ErrPinLimitReached is returned when a channel already holds the maximum
	// number of pinned messages (RF-05 abuse ceiling).
	ErrPinLimitReached = errors.New("pin limit reached")
)
