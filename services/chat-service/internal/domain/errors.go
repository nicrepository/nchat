package domain

import "errors"

var (
	ErrNotFound             = errors.New("not found")
	ErrForbidden            = errors.New("forbidden")
	ErrInvalidInput         = errors.New("invalid input")
	ErrDuplicateSlug        = errors.New("slug already in use")
	ErrAlreadyMember        = errors.New("already a member")
	ErrGeneralChannelExists = errors.New("workspace already has a general channel")
)
