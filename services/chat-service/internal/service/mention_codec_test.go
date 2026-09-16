package service

import (
	"reflect"
	"testing"
)

func TestExtractMentionIDs_V3TokensOnly(t *testing.T) {
	const userID = "11111111-1111-1111-1111-111111111111"
	const channelID = "22222222-2222-2222-2222-222222222222"
	body := `literal \@[No](mention:user:33333333-3333-3333-3333-333333333333) ` +
		`@[Alice](mention:user:` + userID + `) ` +
		`@[Alice again](mention:user:` + userID + `) ` +
		`@[geral](mention:channel:` + channelID + `) ` +
		`@[bad](mention:user:not-a-uuid)`

	users, channels := extractMentionIDs(body)
	if !reflect.DeepEqual(users, []string{userID}) {
		t.Fatalf("unexpected user ids: %#v", users)
	}
	if !reflect.DeepEqual(channels, []string{channelID}) {
		t.Fatalf("unexpected channel ids: %#v", channels)
	}
}

func TestRewriteMentionLabels_UsesCanonicalNames(t *testing.T) {
	const userID = "11111111-1111-1111-1111-111111111111"
	const channelID = "22222222-2222-2222-2222-222222222222"
	body := `@[Spoofed](mention:user:` + userID + `) in ` +
		`@[old-name](mention:channel:` + channelID + `)`

	got := rewriteMentionLabels(body, map[string]string{
		"user:" + userID:       "Alice [Admin]",
		"channel:" + channelID: "novo-canal",
	})
	want := `@[Alice \[Admin\]](mention:user:` + userID + `) in ` +
		`@[novo-canal](mention:channel:` + channelID + `)`
	if got != want {
		t.Fatalf("rewriteMentionLabels() = %q, want %q", got, want)
	}
}

// NamedRecipients is what the realtime payload publishes as the authoritative
// answer to "was I named" (issue #744), so the browser stops reading the body
// with a grammar of its own. The rules that matter are the canonical "all"
// token and everything that only looks like a mention.
func TestNamedRecipients(t *testing.T) {
	const bob = "11111111-1111-1111-1111-111111111111"
	const allID = "00000000-0000-0000-0000-000000000000"

	cases := []struct {
		name         string
		body         string
		wantUsers    []string
		wantEveryone bool
	}{
		{"nothing to name", "hello there", nil, false},
		{"a user token", "hi @[Bob](mention:user:" + bob + ")", []string{bob}, false},
		{"the canonical all token", "@[all](mention:all:" + allID + ")", nil, true},
		{
			"a forged all token names nobody",
			"@[all](mention:all:" + bob + ")",
			nil, false,
		},
		{"plain @all text is not a token", "@all please look", nil, false},
		{
			"both at once",
			"@[Bob](mention:user:" + bob + ") @[all](mention:all:" + allID + ")",
			[]string{bob}, true,
		},
		{
			"a channel token names no person",
			"see @[general](mention:channel:" + bob + ")",
			nil, false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			users, everyone := NamedRecipients(tc.body)
			if len(users) != len(tc.wantUsers) || (len(users) > 0 && !reflect.DeepEqual(users, tc.wantUsers)) {
				t.Fatalf("users = %v, want %v", users, tc.wantUsers)
			}
			if everyone != tc.wantEveryone {
				t.Fatalf("everyone = %v, want %v", everyone, tc.wantEveryone)
			}
		})
	}
}
