package domain

import "errors"

var (
	ErrNotFound                  = errors.New("not found")
	ErrForbidden                 = errors.New("forbidden")
	ErrInvalidInput              = errors.New("invalid input")
	ErrDuplicateSlug             = errors.New("slug already in use")
	ErrAlreadyMember             = errors.New("already a member")
	ErrMemberInactive            = errors.New("workspace member is inactive")
	ErrGeneralChannelExists      = errors.New("workspace already has a general channel")
	ErrGeneralChannelMissing     = errors.New("workspace general channel not found")
	ErrCannotLeaveGeneralChannel = errors.New("cannot leave general channel")
	ErrInvalidMessageTarget      = errors.New("invalid message target")
)
