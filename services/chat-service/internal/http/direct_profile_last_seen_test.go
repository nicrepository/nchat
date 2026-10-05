package httpapi_test

import (
	"context"
	"testing"
	"time"
)

// lastSeenPresence is a presence source that can also say when somebody was
// last seen (issue #798).
type lastSeenPresence struct {
	fakePresence
	at    time.Time
	found bool
}

func (p *lastSeenPresence) LastSeen(context.Context, string, string) (time.Time, bool) {
	return p.at, p.found
}

func TestDMHandler_DirectProfile_CarriesLastSeenOnlyWhileOffline(t *testing.T) {
	seen := time.Date(2026, 10, 1, 15, 42, 0, 0, time.UTC)
	offline := &lastSeenPresence{fakePresence: fakePresence{online: []string{"someone-else"}}, at: seen, found: true}
	handler := directProfileHandler(&fakeDMProvider{directProfile: directProfileResult()}).WithPresence(offline)

	profile := profileOf(t, detailsData(t, serveDirectProfile(t, handler, testConversationID)))
	if profile["presence"] != "offline" || profile["last_seen_at"] != "2026-10-01T15:42:00Z" {
		t.Fatalf("offline profile = %v", profile)
	}

	online := &lastSeenPresence{fakePresence: fakePresence{online: []string{dmOtherUserID}}, at: seen, found: true}
	handler = directProfileHandler(&fakeDMProvider{directProfile: directProfileResult()}).WithPresence(online)
	profile = profileOf(t, detailsData(t, serveDirectProfile(t, handler, testConversationID)))
	if _, present := profile["last_seen_at"]; present {
		t.Fatalf("a present person's last seen was disclosed: %v", profile)
	}

	never := &lastSeenPresence{fakePresence: fakePresence{}}
	handler = directProfileHandler(&fakeDMProvider{directProfile: directProfileResult()}).WithPresence(never)
	profile = profileOf(t, detailsData(t, serveDirectProfile(t, handler, testConversationID)))
	if _, present := profile["last_seen_at"]; present {
		t.Fatalf("an unknown last seen was invented: %v", profile)
	}
}
