//nolint:gosec // Test fixtures intentionally use example token strings.
package worker

import (
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/emailcrypto"
)

func TestRenderTemplatePasswordResetUsesConstructedLink(t *testing.T) {
	expiresAt := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	plaintext := emailcrypto.Plaintext{
		Kind:      "password_reset",
		Token:     "raw-token-must-not-render-directly",
		LinkPath:  "/auth/password/reset?handoff=opaque",
		ToEmail:   "user@example.com",
		ExpiresAt: expiresAt,
	}
	resetLink := "https://app.example.com" + plaintext.LinkPath

	subject, textBody, htmlBody, err := RenderTemplate(plaintext.Kind, TemplateData{
		ResetLink: resetLink,
		ExpiresAt: plaintext.ExpiresAt,
	})
	if err != nil {
		t.Fatalf("RenderTemplate returned error: %v", err)
	}

	if subject != passwordResetSubject {
		t.Fatalf("expected subject %q, got %q", passwordResetSubject, subject)
	}
	if !strings.Contains(textBody, resetLink) {
		t.Fatalf("expected text body to contain %q, got %q", resetLink, textBody)
	}
	if !strings.Contains(htmlBody, resetLink) {
		t.Fatalf("expected html body to contain %q, got %q", resetLink, htmlBody)
	}
	if strings.Contains(textBody, plaintext.Token) || strings.Contains(htmlBody, plaintext.Token) {
		t.Fatalf("expected rendered bodies to avoid raw plaintext token %q", plaintext.Token)
	}
}

func TestRenderTemplateInviteUsesAcceptLink(t *testing.T) {
	expiresAt := time.Date(2026, time.February, 3, 4, 5, 6, 0, time.UTC)
	plaintext := emailcrypto.Plaintext{
		Kind:      "invite",
		Token:     "secret-token",
		LinkPath:  "/auth/invites/accept?invite=opaque",
		ToEmail:   "invitee@example.com",
		ExpiresAt: expiresAt,
	}
	acceptLink := "https://app.example.com" + plaintext.LinkPath

	subject, textBody, htmlBody, err := RenderTemplate(plaintext.Kind, TemplateData{
		AcceptLink: acceptLink,
		ExpiresAt:  plaintext.ExpiresAt,
	})
	if err != nil {
		t.Fatalf("RenderTemplate returned error: %v", err)
	}

	if subject != inviteSubject {
		t.Fatalf("expected subject %q, got %q", inviteSubject, subject)
	}
	if !strings.Contains(textBody, acceptLink) {
		t.Fatalf("expected text body to contain %q, got %q", acceptLink, textBody)
	}
	if !strings.Contains(htmlBody, acceptLink) {
		t.Fatalf("expected html body to contain %q, got %q", acceptLink, htmlBody)
	}
}

func TestRenderTemplateUnknownKind(t *testing.T) {
	_, _, _, err := RenderTemplate("unknown", TemplateData{})
	if err == nil {
		t.Fatal("expected error for unknown kind")
	}
}

func TestRenderText_ParseError(t *testing.T) {
	_, err := renderText("{{", TemplateData{})
	if err == nil {
		t.Fatal("expected parse error")
	}
}

func TestRenderText_ExecuteError(t *testing.T) {
	_, err := renderText("{{.Missing}}", TemplateData{})
	if err == nil {
		t.Fatal("expected execute error")
	}
}

func TestRenderHTML_ParseError(t *testing.T) {
	_, err := renderHTML("{{", TemplateData{})
	if err == nil {
		t.Fatal("expected parse error")
	}
}

func TestRenderHTML_ExecuteError(t *testing.T) {
	_, err := renderHTML("{{.Missing}}", TemplateData{})
	if err == nil {
		t.Fatal("expected execute error")
	}
}
