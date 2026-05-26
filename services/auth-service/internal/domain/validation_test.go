package domain_test

import (
	"errors"
	"testing"

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
