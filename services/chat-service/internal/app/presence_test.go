package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/observability"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
	"github.com/nicrepository/nchat/services/chat-service/internal/ws"
)

// Issue #798 wiring: the REST presence the details panels read obeys the same
// privacy rule as the realtime path, and Do Not Disturb reaches the realtime
// notification decision.

type fakePresenceReporterStore struct {
	contexts map[string]domain.PresenceContext
	err      error
	lastSeen time.Time
	found    bool
}

func (f *fakePresenceReporterStore) Contexts(
	context.Context, string, []string,
) (map[string]domain.PresenceContext, error) {
	return f.contexts, f.err
}

func (f *fakePresenceReporterStore) LastSeen(context.Context, string, string) (time.Time, bool, error) {
	return f.lastSeen, f.found, f.err
}

func connectedTracker(t *testing.T, users ...string) *ws.PresenceTracker {
	t.Helper()
	tracker := ws.NewPresenceTracker(time.Hour)
	t.Cleanup(tracker.Stop)
	for _, user := range users {
		tracker.Connect("ws-1", user, "conn-"+user)
	}
	return tracker
}

func TestPresenceReporter_HidesWhoChoseToAppearOffline(t *testing.T) {
	tracker := connectedTracker(t, "hidden", "visible", "busy")
	future := time.Now().Add(time.Hour)
	store := &fakePresenceReporterStore{contexts: map[string]domain.PresenceContext{
		"hidden": {Override: domain.PresenceOverride{State: domain.PresenceManualAppearOffline, ExpiresAt: future}},
		"busy":   {Override: domain.PresenceOverride{State: domain.PresenceManualBusy, ExpiresAt: future}},
	}}
	got := presenceReporter{tracker: tracker, store: store}.OnlineUserIDs("ws-1")
	if strings.Join(got, ",") != "busy,visible" {
		t.Fatalf("OnlineUserIDs = %v, want busy and visible only", got)
	}
}

func TestPresenceReporter_AnUnreadableChoiceWithholdsEveryone(t *testing.T) {
	tracker := connectedTracker(t, "a")
	store := &fakePresenceReporterStore{err: errors.New("database unavailable")}
	if got := (presenceReporter{tracker: tracker, store: store}).OnlineUserIDs("ws-1"); got != nil {
		t.Fatalf("OnlineUserIDs = %v, want nothing", got)
	}
}

func TestPresenceReporter_WithoutAStoreReportsTheTracker(t *testing.T) {
	tracker := connectedTracker(t, "a")
	if got := (presenceReporter{tracker: tracker}).OnlineUserIDs("ws-1"); len(got) != 1 {
		t.Fatalf("OnlineUserIDs = %v", got)
	}
	if got := (presenceReporter{}).OnlineUserIDs("ws-1"); got != nil {
		t.Fatalf("no tracker = %v", got)
	}
	if _, found := (presenceReporter{}).LastSeen(context.Background(), "ws-1", "a"); found {
		t.Fatal("no store invented a last seen")
	}
}

func TestPresenceReporter_LastSeen(t *testing.T) {
	at := time.Date(2026, 10, 1, 15, 42, 0, 0, time.UTC)
	reporter := presenceReporter{store: &fakePresenceReporterStore{lastSeen: at, found: true}}
	if got, found := reporter.LastSeen(context.Background(), "ws-1", "a"); !found || !got.Equal(at) {
		t.Fatalf("LastSeen = %v %v", got, found)
	}
	failing := presenceReporter{store: &fakePresenceReporterStore{err: errors.New("down")}}
	if _, found := failing.LastSeen(context.Background(), "ws-1", "a"); found {
		t.Fatal("a failed read reported a last seen")
	}
}

func TestHubPresenceMetrics(t *testing.T) {
	if newHubPresenceMetrics(nil) != nil {
		t.Fatal("disabled metrics produced a collector")
	}
	registry := observability.NewMetrics(observability.Config{ServiceName: "chat-service", MetricsEnabled: true})
	metrics := newHubPresenceMetrics(registry)
	metrics.PresenceTransition("dnd")
	metrics.DisconnectGrace("recovered")
	metrics.PresenceDeferred("reach_read")

	recorder := httptest.NewRecorder()
	registry.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := recorder.Body.String()
	for _, want := range []string{
		`chat_presence_transitions_total{availability="dnd"} 1`,
		`chat_presence_disconnect_grace_total{outcome="recovered"} 1`,
		`chat_presence_deferred_total{reason="reach_read"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics missing %q", want)
		}
	}
}

type fakeDoNotDisturb struct {
	users map[string]bool
	err   error
}

func (f fakeDoNotDisturb) DoNotDisturbUsers(context.Context, string, []string) (map[string]bool, error) {
	return f.users, f.err
}

func TestRecipientPreferences_DoNotDisturbReplacesTheConversationPreference(t *testing.T) {
	prefs := &fakeMutedPrefs{stored: []storage.UserConversationNotificationPref{
		{UserID: "quiet", Level: storage.NotificationLevelMentionsReplies},
	}}
	policy := recipientPolicy{prefs: prefs, dnd: fakeDoNotDisturb{users: map[string]bool{"quiet": true}}}
	resolved, err := policy.RecipientPreferences(context.Background(), "ws-1", ws.TargetTypeChannel, "chan-1", []string{"quiet", "loud"})
	if err != nil {
		t.Fatalf("RecipientPreferences: %v", err)
	}
	if resolved["quiet"] != ws.RecipientPreferenceDoNotDisturb {
		t.Fatalf("quiet = %v, want do not disturb", resolved["quiet"])
	}
	if _, present := resolved["loud"]; present {
		t.Fatalf("loud = %v, want nothing", resolved["loud"])
	}

	failing := recipientPolicy{prefs: prefs, dnd: fakeDoNotDisturb{err: errors.New("down")}}
	if _, err := failing.RecipientPreferences(context.Background(), "ws-1", ws.TargetTypeChannel, "chan-1", []string{"quiet"}); err == nil {
		t.Fatal("an unreadable Do Not Disturb was reported as nobody")
	}
}

func TestDoNotDisturbDeniesEveryAlertSurfaceButNotTheMessage(t *testing.T) {
	payload := domainMessageToWSPayload(channelMessage("hello"))
	decision := recipientPolicy{}.PolicyFor(payload, "user-1", ws.RecipientPreferenceDoNotDisturb)
	if decision.InApp != ws.NotificationDeny || decision.Sound != ws.NotificationDeny || decision.WebPush != ws.NotificationDeny {
		t.Fatalf("Do Not Disturb kept a surface: %+v", decision)
	}
	if len(decision.Reasons) == 0 || decision.Reasons[0] != "do_not_disturb" {
		t.Fatalf("reasons = %v", decision.Reasons)
	}
	if facts := recipientFactsFrom("user-1", ws.RecipientPreferenceDoNotDisturb); !facts.dnd || facts.muted {
		t.Fatalf("facts = %+v", facts)
	}
	if got := withRecipientPolicyOption(nil, &fakeMutedPrefs{}, &storage.PGXPresenceStore{}); len(got) != 1 {
		t.Fatalf("options = %d", len(got))
	}
}

type fakePublicLastSeen struct {
	at      time.Time
	offline bool
	err     error
}

func (f fakePublicLastSeen) PublicLastSeen(context.Context, string, string) (time.Time, bool, error) {
	return f.at, f.offline, f.err
}

// The profile says what the realtime offline said (issue #798): for somebody
// appearing offline the projection is newer than the recorded departure, and
// answering with the record would tell them apart from somebody who left.
func TestPresenceReporter_LastSeenIsThePublicInstant(t *testing.T) {
	recorded := time.Date(2026, 9, 30, 18, 0, 0, 0, time.UTC)
	hid := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	store := &fakePresenceReporterStore{lastSeen: recorded, found: true}

	reporter := presenceReporter{store: store, projections: fakePublicLastSeen{at: hid, offline: true}}
	if got, found := reporter.LastSeen(context.Background(), "ws-1", "a"); !found || !got.Equal(hid) {
		t.Fatalf("with an offline projection LastSeen = %v %v, want %v", got, found, hid)
	}
	reporter.projections = fakePublicLastSeen{}
	if got, found := reporter.LastSeen(context.Background(), "ws-1", "a"); !found || !got.Equal(recorded) {
		t.Fatalf("with the projection expired LastSeen = %v %v, want the record", got, found)
	}
	reporter.projections = fakePublicLastSeen{err: errors.New("valkey down")}
	if _, found := reporter.LastSeen(context.Background(), "ws-1", "a"); found {
		t.Fatal("an unreadable projection fell back to a record that may disagree with it")
	}
}
