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
