package domain_test

import (
	"testing"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

// The context is the only client input in the whole redirect decision, so the
// set it can resolve to has to be exactly two labels and nothing else.
func TestParseOIDCAppContext_AcceptsOnlyTheKnownLabels(t *testing.T) {
	tests := map[string]domain.OIDCAppContext{
		"":      domain.OIDCAppChat,
		"chat":  domain.OIDCAppChat,
		"admin": domain.OIDCAppAdmin,
	}
	for raw, want := range tests {
		got, ok := domain.ParseOIDCAppContext(raw)
		if !ok || got != want {
			t.Fatalf("ParseOIDCAppContext(%q) = (%q, %v), want (%q, true)", raw, got, ok, want)
		}
	}
}

// Everything else is refused rather than coerced. In particular, nothing that
// looks like a URL is accepted: a label is not a destination, and this is the
// boundary that keeps an attacker from proposing one.
func TestParseOIDCAppContext_RefusesEverythingElse(t *testing.T) {
	for _, raw := range []string{
		"Chat", "ADMIN", " admin", "admin ", "administrator", "chat\n",
		"https://evil.test", "//evil.test", "/oidc-callback", "chat,admin", "0",
	} {
		if got, ok := domain.ParseOIDCAppContext(raw); ok {
			t.Fatalf("ParseOIDCAppContext(%q) accepted and returned %q", raw, got)
		}
	}
}
