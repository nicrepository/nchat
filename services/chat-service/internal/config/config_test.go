package config

import (
	"strings"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/ws"
)

func TestLoadDefaults(t *testing.T) {
	cfg := Load()
	if cfg.ServiceName != "chat-service" || cfg.Env != "development" || cfg.Port != 8082 || cfg.ReadHeaderTimeoutSeconds != 5 {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if cfg.ReactionRateLimitMaxActions != 60 || cfg.ReactionRateLimitWindowSeconds != 60 {
		t.Fatalf("unexpected reaction rate-limit defaults: %+v", cfg)
	}
	wsDefaults := ws.DefaultHandlerConfig()
	if cfg.WSMaxConnectionsPerUser != wsDefaults.MaxConnectionsPerUser ||
		cfg.WSInboundMessagesPerMinute != wsDefaults.InboundMessagesPerMinute ||
		cfg.WSInboundBurst != wsDefaults.InboundBurst ||
		cfg.WSMaxInvalidMessages != wsDefaults.MaxInvalidMessages {
		t.Fatalf("unexpected websocket resource defaults: %+v", cfg)
	}
}

func TestLoadUsesEnvironmentOverrides(t *testing.T) {
	t.Setenv("APP_ENV", "test")
	t.Setenv("PORT", "18082")
	t.Setenv("WS_MAX_CONNECTIONS_PER_USER", "7")
	t.Setenv("WS_INBOUND_MESSAGES_PER_MINUTE", "120")
	t.Setenv("WS_INBOUND_BURST", "20")
	t.Setenv("WS_MAX_INVALID_MESSAGES", "3")
	t.Setenv("REACTION_RATE_LIMIT_MAX_ACTIONS", "7")
	t.Setenv("REACTION_RATE_LIMIT_WINDOW_SECONDS", "12")
	cfg := Load()
	if cfg.Env != "test" || cfg.Port != 18082 {
		t.Fatalf("expected env/port overrides, got %+v", cfg)
	}
	if cfg.WSMaxConnectionsPerUser != 7 ||
		cfg.WSInboundMessagesPerMinute != 120 ||
		cfg.WSInboundBurst != 20 ||
		cfg.WSMaxInvalidMessages != 3 {
		t.Fatalf("expected websocket resource env overrides, got %+v", cfg)
	}
	if cfg.ReactionRateLimitMaxActions != 7 || cfg.ReactionRateLimitWindowSeconds != 12 {
		t.Fatalf("expected reaction rate-limit env overrides, got %+v", cfg)
	}
}

func TestLoadInvalidReactionRateLimitFallsBackToDefaults(t *testing.T) {
	t.Setenv("REACTION_RATE_LIMIT_MAX_ACTIONS", "0")
	t.Setenv("REACTION_RATE_LIMIT_WINDOW_SECONDS", "invalid")
	cfg := Load()
	if cfg.ReactionRateLimitMaxActions != 60 || cfg.ReactionRateLimitWindowSeconds != 60 {
		t.Fatalf("expected invalid reaction rate limit to use defaults, got %+v", cfg)
	}
}

func TestLoad_InvalidWebSocketResourceControlsFallBackToDefaults(t *testing.T) {
	t.Setenv("WS_MAX_CONNECTIONS_PER_USER", "0")
	t.Setenv("WS_INBOUND_MESSAGES_PER_MINUTE", "-1")
	t.Setenv("WS_INBOUND_BURST", "not-an-int")
	t.Setenv("WS_MAX_INVALID_MESSAGES", "0")

	cfg := Load()
	wsDefaults := ws.DefaultHandlerConfig()
	if cfg.WSMaxConnectionsPerUser != wsDefaults.MaxConnectionsPerUser ||
		cfg.WSInboundMessagesPerMinute != wsDefaults.InboundMessagesPerMinute ||
		cfg.WSInboundBurst != wsDefaults.InboundBurst ||
		cfg.WSMaxInvalidMessages != wsDefaults.MaxInvalidMessages {
		t.Fatalf("expected invalid websocket resource env values to fall back to defaults, got %+v", cfg)
	}
}

// TestLoad_WSInboundBurst_IndependentOfSustainedRate is a regression test for
// issue #455: nchat-dev-server sets WS_INBOUND_BURST=60 so the web client's
// bootstrap burst (1 call.sync + 12 subscribe messages sent immediately after
// open) is not closed with 1008, while WS_INBOUND_MESSAGES_PER_MINUTE stays at
// its sustained-rate default because it is a separate, independent setting.
func TestLoad_WSInboundBurst_IndependentOfSustainedRate(t *testing.T) {
	t.Setenv("WS_INBOUND_BURST", "60")

	cfg := Load()
	wsDefaults := ws.DefaultHandlerConfig()
	if cfg.WSInboundBurst != 60 {
		t.Fatalf("expected WSInboundBurst=60 from env, got %d", cfg.WSInboundBurst)
	}
	if cfg.WSInboundMessagesPerMinute != wsDefaults.InboundMessagesPerMinute {
		t.Fatalf(
			"expected WSInboundMessagesPerMinute to stay at the sustained-rate default %d when only burst is overridden, got %d",
			wsDefaults.InboundMessagesPerMinute, cfg.WSInboundMessagesPerMinute,
		)
	}
}

func TestLoad_ValkeyURL(t *testing.T) {
	t.Setenv("VALKEY_URL", "valkey://localhost:6379")
	cfg := Load()
	if cfg.ValkeyURL != "valkey://localhost:6379" {
		t.Fatalf("expected ValkeyURL to be set from env, got %q", cfg.ValkeyURL)
	}
}

func TestLoad_ValkeyWSBroadcastEnabled(t *testing.T) {
	t.Setenv("VALKEY_WS_BROADCAST_ENABLED", "true")
	cfg := Load()
	if !cfg.ValkeyWSBroadcastEnabled {
		t.Fatal("expected ValkeyWSBroadcastEnabled=true when env is 'true'")
	}
}

func TestLoad_WSInstanceID(t *testing.T) {
	t.Setenv("WS_INSTANCE_ID", "my-pod-abc123")
	cfg := Load()
	if cfg.WSInstanceID != "my-pod-abc123" {
		t.Fatalf("expected WSInstanceID from env, got %q", cfg.WSInstanceID)
	}
}

func TestLoad_WSInstanceID_ValidChars(t *testing.T) {
	valid := []string{
		"pod-abc123",
		"instance.A",
		"my_host-01",
		"A",
		"a-b.c_d",
	}
	for _, id := range valid {
		t.Setenv("WS_INSTANCE_ID", id)
		cfg := Load()
		if cfg.WSInstanceID != id {
			t.Errorf("valid WSInstanceID %q was rejected, got %q", id, cfg.WSInstanceID)
		}
	}
}

func TestLoad_WSInstanceID_InvalidChars_FallsBackToEmpty(t *testing.T) {
	invalid := []string{
		"bad instance id", // spaces
		"id!@#$%",         // special chars
		"id/slash",        // slash
		"id:colon",        // colon
	}
	for _, id := range invalid {
		t.Setenv("WS_INSTANCE_ID", id)
		cfg := Load()
		if cfg.WSInstanceID != "" {
			t.Errorf("invalid WSInstanceID %q must fall back to empty, got %q", id, cfg.WSInstanceID)
		}
	}
}

func TestLoad_WSInstanceID_OversizedFallsBackToEmpty(t *testing.T) {
	safe64 := string(make([]byte, 64))
	for i := 0; i < 64; i++ {
		safe64 = safe64[:i] + "a" + safe64[i+1:]
	}
	safe65 := safe64 + "a"

	t.Setenv("WS_INSTANCE_ID", safe64)
	if cfg := Load(); cfg.WSInstanceID != safe64 {
		t.Errorf("64-char WSInstanceID must be accepted, got %q", cfg.WSInstanceID)
	}

	t.Setenv("WS_INSTANCE_ID", safe65)
	if cfg := Load(); cfg.WSInstanceID != "" {
		t.Errorf("65-char WSInstanceID must fall back to empty, got %q", cfg.WSInstanceID)
	}
}

func TestLoad_WSInstanceID_EmptyRemainsEmpty(t *testing.T) {
	t.Setenv("WS_INSTANCE_ID", "")
	cfg := Load()
	if cfg.WSInstanceID != "" {
		t.Fatalf("empty WS_INSTANCE_ID must stay empty, got %q", cfg.WSInstanceID)
	}
}

// --- RF-21 link safety ---------------------------------------------------

func TestLoadDefaultsLinkSafetyToDisabled(t *testing.T) {
	cfg := Load()

	if cfg.LinkSafetyEnabled {
		t.Fatal("the safe browsing check must be off unless a deployment asks for it")
	}
	if cfg.LinkSafetyCloudflareAccount != "" || cfg.LinkSafetyCloudflareToken != "" {
		t.Fatal("credentials must have no defaults")
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("an absent variable must take the default, not fail: %v", err)
	}
}

// The finding: an unparseable value must not fold into false. A control that
// switches itself off because someone wrote "enabled" has no symptom other than
// nothing ever being blocked.
func TestValidateRejectsUnparseableLinkSafetyFlag(t *testing.T) {
	for _, raw := range []string{"enabled", "on", "yes", "", " ", "2"} {
		t.Run(raw, func(t *testing.T) {
			t.Setenv("CHAT_LINK_SAFETY_ENABLED", raw)

			cfg := Load()
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("%q was accepted", raw)
			}
			if err.Error() != "CHAT_LINK_SAFETY_ENABLED must be a valid boolean" {
				t.Fatalf("message is not the deterministic one: %v", err)
			}
			if cfg.LinkSafetyEnabled {
				t.Fatal("an invalid value must not read as enabled either")
			}
		})
	}
}

func TestValidateAcceptsParseableLinkSafetyFlag(t *testing.T) {
	for raw, want := range map[string]bool{
		"true": true, "TRUE": true, "1": true,
		"false": false, "FALSE": false, "0": false,
	} {
		t.Run(raw, func(t *testing.T) {
			t.Setenv("CHAT_LINK_SAFETY_ENABLED", raw)
			// Credentials so this stays a test about *parsing*: enabling
			// without them is its own error, asserted separately below.
			t.Setenv("CHAT_LINK_SAFETY_CLOUDFLARE_ACCOUNT_ID", "acct-123")
			t.Setenv("CHAT_LINK_SAFETY_CLOUDFLARE_API_TOKEN", "token-abc")

			cfg := Load()
			if err := cfg.Validate(); err != nil {
				t.Fatalf("%q was rejected: %v", raw, err)
			}
			if cfg.LinkSafetyEnabled != want {
				t.Fatalf("%q read as %v", raw, cfg.LinkSafetyEnabled)
			}
		})
	}
}

func TestLoadReadsLinkSafetyCredentials(t *testing.T) {
	t.Setenv("CHAT_LINK_SAFETY_ENABLED", "true")
	t.Setenv("CHAT_LINK_SAFETY_CLOUDFLARE_ACCOUNT_ID", "acct-123")
	t.Setenv("CHAT_LINK_SAFETY_CLOUDFLARE_API_TOKEN", "token-abc")

	cfg := Load()

	if !cfg.LinkSafetyEnabled {
		t.Fatal("CHAT_LINK_SAFETY_ENABLED was not read")
	}
	if cfg.LinkSafetyCloudflareAccount != "acct-123" || cfg.LinkSafetyCloudflareToken != "token-abc" {
		t.Fatalf("credentials: %q / %q", cfg.LinkSafetyCloudflareAccount, cfg.LinkSafetyCloudflareToken)
	}
}

// The other half of the finding: the flag parsed fine, but nothing was there to
// build a checker from. Accepting that configuration is what produced the
// enabled-but-unchecked state — the checker could not be created, the gate was
// absent, and every message went through.
func TestValidateRejectsEnabledLinkSafetyWithoutCredentials(t *testing.T) {
	for name, testCase := range map[string]struct {
		account string
		token   string
		want    string
	}{
		"no account": {
			account: "", token: "token-abc",
			want: "CHAT_LINK_SAFETY_CLOUDFLARE_ACCOUNT_ID is required when CHAT_LINK_SAFETY_ENABLED is true",
		},
		"no token": {
			account: "acct-123", token: "",
			want: "CHAT_LINK_SAFETY_CLOUDFLARE_API_TOKEN is required when CHAT_LINK_SAFETY_ENABLED is true",
		},
		"neither": {
			account: "", token: "",
			want: "CHAT_LINK_SAFETY_CLOUDFLARE_ACCOUNT_ID is required when CHAT_LINK_SAFETY_ENABLED is true",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("CHAT_LINK_SAFETY_ENABLED", "true")
			t.Setenv("CHAT_LINK_SAFETY_CLOUDFLARE_ACCOUNT_ID", testCase.account)
			t.Setenv("CHAT_LINK_SAFETY_CLOUDFLARE_API_TOKEN", testCase.token)

			err := Load().Validate()
			if err == nil {
				t.Fatal("an enabled check with no way to run it was accepted")
			}
			if err.Error() != testCase.want {
				t.Fatalf("message is not the deterministic one: %v", err)
			}
			if strings.Contains(err.Error(), testCase.token) && testCase.token != "" {
				t.Fatal("the error repeated the token")
			}
		})
	}
}

// Disabled is the one state in which absent credentials are correct, and it has
// to stay valid: it is how an environment without a Cloudflare account runs.
func TestValidateAcceptsDisabledLinkSafetyWithoutCredentials(t *testing.T) {
	t.Setenv("CHAT_LINK_SAFETY_ENABLED", "false")
	t.Setenv("CHAT_LINK_SAFETY_CLOUDFLARE_ACCOUNT_ID", "")
	t.Setenv("CHAT_LINK_SAFETY_CLOUDFLARE_API_TOKEN", "")

	if err := Load().Validate(); err != nil {
		t.Fatalf("an explicitly disabled check needs no credentials: %v", err)
	}
}
