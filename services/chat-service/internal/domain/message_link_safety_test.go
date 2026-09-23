package domain

import (
	"strings"
	"testing"
)

func TestMessageLinkSafetyPermissions(t *testing.T) {
	for _, test := range []struct {
		name         string
		state        MessageLinkSafety
		serverFetch  bool
		restrictLink bool
	}{
		{name: "none", state: MessageLinkSafetyNone},
		{name: "safe", state: MessageLinkSafetySafe, serverFetch: true},
		{name: "inconclusive", state: MessageLinkSafetyInconclusive},
		{name: "malicious", state: MessageLinkSafetyMalicious, restrictLink: true},
		{name: "unknown fails closed", state: MessageLinkSafety("future")},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := test.state.AllowsServerFetch(); got != test.serverFetch {
				t.Fatalf("AllowsServerFetch() = %v, want %v", got, test.serverFetch)
			}
			if got := test.state.RestrictsLinks(); got != test.restrictLink {
				t.Fatalf("RestrictsLinks() = %v, want %v", got, test.restrictLink)
			}
		})
	}
}

// The target key (issue #807 CQ follow-up) is the identity an occurrence keeps
// when its URL is withheld: stable for a URL, distinct across URLs, and never
// the URL itself.
func TestLinkTargetKeyIsAStableOpaqueIdentity(t *testing.T) {
	const url = "https://example.test/a?x=1"
	key := LinkTargetKey(url)
	if len(key) != LinkTargetKeyLength || key != LinkTargetKey(url) {
		t.Fatalf("key = %q, want %d stable hex characters", key, LinkTargetKeyLength)
	}
	for _, r := range key {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			t.Fatalf("key %q is not lowercase hex", key)
		}
	}
	if key == LinkTargetKey("https://example.test/a?x=2") || key == LinkTargetKey("") {
		t.Fatal("distinct URLs must not share a key")
	}
	if strings.Contains(key, "example") {
		t.Fatal("the key must not carry the URL")
	}
}
