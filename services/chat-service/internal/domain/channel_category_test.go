package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

func TestNormalizeChannelCategoryName_TrimsAndAccepts(t *testing.T) {
	for _, test := range []struct {
		name  string
		input string
		want  string
	}{
		{name: "plain", input: "Projetos", want: "Projetos"},
		{name: "leading and trailing spaces are trimmed", input: "   Projetos   ", want: "Projetos"},
		{name: "inner spaces are preserved", input: " Projetos de 2026 ", want: "Projetos de 2026"},
		{name: "tabs and newlines around the value are trimmed", input: "\t Projetos \n", want: "Projetos"},
		{name: "at the cap", input: strings.Repeat("a", domain.MaxChannelCategoryNameCodePoints), want: strings.Repeat("a", domain.MaxChannelCategoryNameCodePoints)},
		// The cap counts code points, so a name of multi-byte characters gets the
		// same allowance as an ASCII one rather than a fraction of it.
		{name: "at the cap in accented characters", input: strings.Repeat("á", domain.MaxChannelCategoryNameCodePoints), want: strings.Repeat("á", domain.MaxChannelCategoryNameCodePoints)},
		{name: "at the cap in emoji", input: strings.Repeat("🙂", domain.MaxChannelCategoryNameCodePoints), want: strings.Repeat("🙂", domain.MaxChannelCategoryNameCodePoints)},
		// Reserved case-insensitively, but only as the whole name.
		{name: "reserved word as part of a longer name", input: "Geral e Avisos", want: "Geral e Avisos"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := domain.NormalizeChannelCategoryName(test.input)
			if err != nil {
				t.Fatalf("NormalizeChannelCategoryName(%q): %v", test.input, err)
			}
			if got != test.want {
				t.Fatalf("got %q, want %q", got, test.want)
			}
		})
	}
}

func TestNormalizeChannelCategoryName_Rejects(t *testing.T) {
	for _, test := range []struct {
		name  string
		input string
		want  error
	}{
		{name: "empty", input: "", want: domain.ErrChannelCategoryNameRequired},
		{name: "spaces only", input: "    ", want: domain.ErrChannelCategoryNameRequired},
		{name: "newline only", input: "\n\t", want: domain.ErrChannelCategoryNameRequired},
		{name: "one over the cap", input: strings.Repeat("a", domain.MaxChannelCategoryNameCodePoints+1), want: domain.ErrChannelCategoryNameTooLong},
		{name: "one over the cap in emoji", input: strings.Repeat("🙂", domain.MaxChannelCategoryNameCodePoints+1), want: domain.ErrChannelCategoryNameTooLong},
		{name: "inner newline", input: "Proj\netos", want: domain.ErrChannelCategoryNameInvalid},
		{name: "inner tab", input: "Proj\tetos", want: domain.ErrChannelCategoryNameInvalid},
		{name: "NUL byte", input: "Proj\x00etos", want: domain.ErrChannelCategoryNameInvalid},
		{name: "carriage return", input: "Proj\retos", want: domain.ErrChannelCategoryNameInvalid},
		{name: "DEL", input: "Proj\x7fetos", want: domain.ErrChannelCategoryNameInvalid},
		{name: "reserved name", input: "Geral", want: domain.ErrChannelCategoryNameReserved},
		{name: "reserved name lowercase", input: "geral", want: domain.ErrChannelCategoryNameReserved},
		{name: "reserved name uppercase", input: "GERAL", want: domain.ErrChannelCategoryNameReserved},
		{name: "reserved name mixed case", input: "GeRaL", want: domain.ErrChannelCategoryNameReserved},
		{name: "reserved name with surrounding spaces", input: "  Geral  ", want: domain.ErrChannelCategoryNameReserved},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := domain.NormalizeChannelCategoryName(test.input)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			// Every rejection maps to the endpoint's validation status without a
			// new branch, and none returns a partially normalized value.
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("error %v must wrap ErrInvalidInput", err)
			}
			if got != "" {
				t.Fatalf("rejected name must return no value, got %q", got)
			}
		})
	}
}

// A rejected name is caller-controlled text. It must not travel in the error, or
// it reaches every response body and log line that wraps it.
func TestNormalizeChannelCategoryName_ErrorsDoNotEchoTheValue(t *testing.T) {
	const secret = "SuperSecretCategoryName"
	for _, input := range []string{secret + strings.Repeat("x", 100), secret + "\n" + secret} {
		_, err := domain.NormalizeChannelCategoryName(input)
		if err == nil {
			t.Fatalf("expected %q to be rejected", input)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error message leaks the rejected name: %v", err)
		}
	}
}

// The management gate for RF-17. Owner and admin manage; member and guest do not;
// an inactive membership never does, whatever its role.
func TestCanManageChannelCategories(t *testing.T) {
	for _, test := range []struct {
		name   string
		member *domain.WorkspaceMember
		want   bool
	}{
		{name: "nil membership", member: nil, want: false},
		{name: "active owner", member: &domain.WorkspaceMember{Role: domain.WorkspaceRoleOwner, Status: domain.MemberStatusActive}, want: true},
		{name: "active admin", member: &domain.WorkspaceMember{Role: domain.WorkspaceRoleAdmin, Status: domain.MemberStatusActive}, want: true},
		{name: "active member", member: &domain.WorkspaceMember{Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusActive}, want: false},
		{name: "active guest", member: &domain.WorkspaceMember{Role: domain.WorkspaceRoleGuest, Status: domain.MemberStatusActive}, want: false},
		{name: "suspended admin", member: &domain.WorkspaceMember{Role: domain.WorkspaceRoleAdmin, Status: domain.MemberStatusSuspended}, want: false},
		{name: "left owner", member: &domain.WorkspaceMember{Role: domain.WorkspaceRoleOwner, Status: domain.MemberStatusLeft}, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := domain.CanManageChannelCategories(test.member); got != test.want {
				t.Fatalf("CanManageChannelCategories = %v, want %v", got, test.want)
			}
		})
	}
}

// The channel-level moderator role must not be mistakable for workspace
// management: it is a different type with a different value space, so it cannot be
// assigned to a WorkspaceMember at all, and no workspace role is named
// "moderator". This is the schema fact behind the RF-17 authorization decision
// recorded in SECURITY.md.
func TestChannelModeratorIsNotAWorkspaceRole(t *testing.T) {
	for _, role := range []domain.WorkspaceRole{
		domain.WorkspaceRoleOwner,
		domain.WorkspaceRoleAdmin,
		domain.WorkspaceRoleMember,
		domain.WorkspaceRoleGuest,
	} {
		if string(role) == string(domain.ChannelRoleModerator) {
			t.Fatalf("workspace role %q collides with the channel moderator role", role)
		}
	}
}
