package domain

import (
	"fmt"
	"regexp"
	"strings"
	"time"
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

// PasswordExpired reports whether a password set at changedAt is too old to
// authenticate with at the instant now.
//
// The rule is a pure function of three inputs and reads no clock of its own, so
// the boundary is testable exactly rather than approximately.
//
// Two states are never expired, and both matter:
//   - a policy of zero days, which is "passwords do not expire";
//   - a zero changedAt, which means the caller does not know when the password
//     was set. Treating an unknown age as infinitely old would lock out every
//     account the moment an expiry is configured.
func PasswordExpired(changedAt time.Time, now time.Time, policy PolicySettings) bool {
	if policy.PasswordExpirationDays <= 0 || changedAt.IsZero() {
		return false
	}
	// The password stops working once it is strictly older than the configured
	// window, so a password set exactly N days ago still authenticates on its
	// last day rather than one day early.
	return now.Sub(changedAt) > time.Duration(policy.PasswordExpirationDays)*24*time.Hour
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
