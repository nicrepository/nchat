package domain

import "errors"

var (
	ErrDuplicateEmail = errors.New("email already registered")
	ErrPasswordPolicy = errors.New("password does not meet policy requirements")
	ErrInvalidInput   = errors.New("invalid input")
)

var ErrInvalidRefreshToken = errors.New("invalid refresh token")
