package domain

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var emailRE = regexp.MustCompile(`(?i)^[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}$`)

func ValidateEmail(email string) error {
	if email == "" {
		return fmt.Errorf("%w: email is required", ErrInvalidInput)
	}
	if !emailRE.MatchString(email) {
		return fmt.Errorf("%w: invalid email format", ErrInvalidInput)
	}
	return nil
}

func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func ValidatePassword(password string, policy PolicySettings) error {
	if utf8.RuneCountInString(password) < policy.MinPasswordLength {
		return fmt.Errorf("%w: minimum length is %d characters", ErrPasswordPolicy, policy.MinPasswordLength)
	}
	if policy.RequireUppercase && !containsUppercase(password) {
		return fmt.Errorf("%w: must contain at least one uppercase letter", ErrPasswordPolicy)
	}
	if policy.RequireLowercase && !containsLowercase(password) {
		return fmt.Errorf("%w: must contain at least one lowercase letter", ErrPasswordPolicy)
	}
	if policy.RequireNumber && !containsDigit(password) {
		return fmt.Errorf("%w: must contain at least one number", ErrPasswordPolicy)
	}
	if policy.RequireSymbol && !containsSymbol(password) {
		return fmt.Errorf("%w: must contain at least one symbol", ErrPasswordPolicy)
	}
	return nil
}

func containsUppercase(s string) bool {
	for _, r := range s {
		if unicode.IsUpper(r) {
			return true
		}
	}
	return false
}

func containsLowercase(s string) bool {
	for _, r := range s {
		if unicode.IsLower(r) {
			return true
		}
	}
	return false
}

func containsDigit(s string) bool {
	for _, r := range s {
		if unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

func containsSymbol(s string) bool {
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.IsSpace(r) {
			return true
		}
	}
	return false
}
