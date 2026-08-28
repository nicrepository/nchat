package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

func TestValidateEmail_Valid(t *testing.T) {
	cases := []string{
		"user@example.com",
		"USER@EXAMPLE.COM",
		"user+tag@sub.domain.io",
		"a@b.co",
	}
	for _, email := range cases {
		if err := domain.ValidateEmail(email); err != nil {
			t.Errorf("ValidateEmail(%q): unexpected error %v", email, err)
		}
	}
}

func TestValidateEmail_Invalid(t *testing.T) {
	cases := []string{"", "notanemail", "@nodomain", "no@", "no@domain"}
	for _, email := range cases {
		err := domain.ValidateEmail(email)
		if err == nil {
			t.Errorf("ValidateEmail(%q): expected error, got nil", email)
		}
		if !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("ValidateEmail(%q): expected ErrInvalidInput, got %v", email, err)
		}
	}
}

func TestNormalizeEmail(t *testing.T) {
	got := domain.NormalizeEmail("  USER@EXAMPLE.COM  ")
	if got != "user@example.com" {
		t.Fatalf("expected user@example.com, got %q", got)
	}
}

func TestValidatePassword_MeetsPolicy(t *testing.T) {
	policy := domain.PolicySettings{
		MinPasswordLength: 8,
		RequireUppercase:  true,
		RequireLowercase:  true,
		RequireNumber:     true,
		RequireSymbol:     true,
	}
	if err := domain.ValidatePassword("Abcdef1!", policy); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestValidatePassword_MinLengthCountsCharacters(t *testing.T) {
	policy := domain.PolicySettings{MinPasswordLength: 9}
	err := domain.ValidatePassword("Áb1!Áb1!", policy)
	if !errors.Is(err, domain.ErrPasswordPolicy) {
		t.Fatalf("expected ErrPasswordPolicy for 8-character password, got %v", err)
	}
}

func TestValidatePassword_TooShort(t *testing.T) {
	policy := domain.PolicySettings{MinPasswordLength: 12}
	err := domain.ValidatePassword("short", policy)
	if !errors.Is(err, domain.ErrPasswordPolicy) {
		t.Fatalf("expected ErrPasswordPolicy, got %v", err)
	}
}

func TestValidatePassword_MissingUppercase(t *testing.T) {
	policy := domain.PolicySettings{MinPasswordLength: 1, RequireUppercase: true}
	err := domain.ValidatePassword("nouppercase1!", policy)
	if !errors.Is(err, domain.ErrPasswordPolicy) {
		t.Fatalf("expected ErrPasswordPolicy, got %v", err)
	}
}

func TestValidatePassword_MissingLowercase(t *testing.T) {
	policy := domain.PolicySettings{MinPasswordLength: 1, RequireLowercase: true}
	err := domain.ValidatePassword("NOLOWERCASE1!", policy)
	if !errors.Is(err, domain.ErrPasswordPolicy) {
		t.Fatalf("expected ErrPasswordPolicy, got %v", err)
	}
}

func TestValidatePassword_MissingNumber(t *testing.T) {
	policy := domain.PolicySettings{MinPasswordLength: 1, RequireNumber: true}
	err := domain.ValidatePassword("NoNumber!", policy)
	if !errors.Is(err, domain.ErrPasswordPolicy) {
		t.Fatalf("expected ErrPasswordPolicy, got %v", err)
	}
}

func TestValidatePassword_MissingSymbol(t *testing.T) {
	policy := domain.PolicySettings{MinPasswordLength: 1, RequireSymbol: true}
	err := domain.ValidatePassword("NoSymbol1", policy)
	if !errors.Is(err, domain.ErrPasswordPolicy) {
		t.Fatalf("expected ErrPasswordPolicy, got %v", err)
	}
}

func TestValidatePassword_NoRequirements(t *testing.T) {
	policy := domain.PolicySettings{MinPasswordLength: 1}
	if err := domain.ValidatePassword("a", policy); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestValidateStatusTransition(t *testing.T) {
	tests := []struct {
		from    string
		to      string
		wantErr bool
	}{
		{"active", "suspended", false},
		{"suspended", "active", false},
		{"active", "active", true},
		{"suspended", "suspended", true},
		{"active", "locked", true},
		{"locked", "active", true},
		{"active", "deleted", true},
		{"invited", "active", true},
		{"", "active", true},
		{"active", "", true},
	}
	for _, tt := range tests {
		name := tt.from + "->" + tt.to
		t.Run(name, func(t *testing.T) {
			err := domain.ValidateStatusTransition(tt.from, tt.to)
			if tt.wantErr && err == nil {
				t.Fatalf("expected error for %s->%s", tt.from, tt.to)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error for %s->%s: %v", tt.from, tt.to, err)
			}
			if tt.wantErr && err != nil && !errors.Is(err, domain.ErrStatusTransitionNotAllowed) {
				t.Fatalf("expected ErrStatusTransitionNotAllowed, got %v", err)
			}
		})
	}
}

// The expiry boundary, asserted exactly. A password set N days ago must still
// work on its last day: expiring one day early would lock people out of a
// window the policy told them they had.
func TestPasswordExpired_BoundaryIsTheLastDayOfTheWindow(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	policy := domain.PolicySettings{PasswordExpirationDays: 90}

	cases := []struct {
		name    string
		age     time.Duration
		expired bool
	}{
		{"just set", 0, false},
		{"one day short of the window", 89 * 24 * time.Hour, false},
		{"exactly at the window", 90 * 24 * time.Hour, false},
		{"one second past the window", 90*24*time.Hour + time.Second, true},
		{"long past the window", 400 * 24 * time.Hour, true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			changedAt := now.Add(-testCase.age)
			if got := domain.PasswordExpired(changedAt, now, policy); got != testCase.expired {
				t.Fatalf("age %s: expected expired=%t, got %t", testCase.age, testCase.expired, got)
			}
		})
	}
}

// Zero days is the platform default and means passwords do not expire. A
// password of any age must keep working, or enabling the feature by accident
// would be indistinguishable from disabling every account.
func TestPasswordExpired_ZeroDaysNeverExpires(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)

	for _, days := range []int{0, -1} {
		policy := domain.PolicySettings{PasswordExpirationDays: days}
		if domain.PasswordExpired(now.Add(-10*365*24*time.Hour), now, policy) {
			t.Fatalf("expiration_days=%d must never expire a password", days)
		}
	}
}

// An unknown password age must not be read as infinitely old. Treating the zero
// time as expired would refuse every login the moment an expiry is configured.
func TestPasswordExpired_UnknownAgeIsNotExpired(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	policy := domain.PolicySettings{PasswordExpirationDays: 1}

	if domain.PasswordExpired(time.Time{}, now, policy) {
		t.Fatal("a password with no recorded change time must not be treated as expired")
	}
}
