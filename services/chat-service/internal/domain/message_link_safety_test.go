package domain

import "testing"

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
