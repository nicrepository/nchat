package app

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/nicrepository/nchat/libs/go/platform/notificationpolicy"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
	"github.com/nicrepository/nchat/services/chat-service/internal/ws"
)

// Issue #744: the realtime event carries the central decision.
//
// What is proved here is that a subscriber never has to work anything out for
// itself: the decision, the class and the naming are all on the wire, and they
// come from libs/go/platform/notificationpolicy rather than from a rule
// restated in this package.

const mentionOfBob = "hey @[Bob](mention:user:11111111-1111-1111-1111-111111111111)"

func channelMessage(body string) domain.Message {
	return domain.Message{
		ID:          "msg-1",
		WorkspaceID: "ws-1",
		ChannelID:   "chan-1",
		SenderID:    "user-9",
		Kind:        domain.MessageKindUser,
		BodyText:    body,
	}
}

func directMessage(body string) domain.Message {
	msg := channelMessage(body)
	msg.ChannelID = ""
	msg.DMConversationID = "dm-1"
	return msg
}

func TestRealtimePayloadCarriesTheCentralDecision(t *testing.T) {
	policy := domainMessageToWSPayload(channelMessage("hello")).NotificationPolicy
	if policy == nil {
		t.Fatal("a live channel message published no decision")
	}
	if policy.Sound != ws.NotificationAllow {
		t.Fatalf("sound = %q, want %q", policy.Sound, ws.NotificationAllow)
	}
	// The realtime evaluation runs on the foreground surface and declares no
	// push capability, so this channel is genuinely denied. It travels anyway,
	// because the client must be told rather than left to infer it from sound.
	if policy.WebPush != ws.NotificationDeny {
		t.Fatalf("web_push = %q, want %q", policy.WebPush, ws.NotificationDeny)
	}
	if policy.PolicyVersion != notificationpolicy.Version {
		t.Fatalf("policy_version = %d, want %d", policy.PolicyVersion, notificationpolicy.Version)
	}
	if len(policy.Reasons) != 0 {
		t.Fatalf("reasons = %v, want none on an allowed decision", policy.Reasons)
	}
	if policy.SoundClass != ws.SoundClassGeneral {
		t.Fatalf("sound_class = %q, want %q", policy.SoundClass, ws.SoundClassGeneral)
	}
}

type classificationCase struct {
	name         string
	message      domain.Message
	wantClass    string
	wantNamed    []string
	wantEveryone bool
}

func TestRealtimePayloadClassifiesTheEventForTheClient(t *testing.T) {
	cases := []classificationCase{
		{"a channel message is general", channelMessage("hello"), ws.SoundClassGeneral, nil, false},
		{"a direct conversation is direct", directMessage("hello"), ws.SoundClassDirect, nil, false},
		{
			"a mention names the user it names",
			channelMessage(mentionOfBob),
			ws.SoundClassGeneral,
			[]string{"11111111-1111-1111-1111-111111111111"},
			false,
		},
		{
			"an all-mention names everyone",
			channelMessage("@[all](mention:all:00000000-0000-0000-0000-000000000000)"),
			ws.SoundClassGeneral, nil, true,
		},
		{
			"a forged all-mention names nobody",
			channelMessage("@[all](mention:all:22222222-2222-2222-2222-222222222222)"),
			ws.SoundClassGeneral, nil, false,
		},
		{
			"plain @all text is not a mention",
			channelMessage("@all please look"),
			ws.SoundClassGeneral, nil, false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { tc.check(t) })
	}
}

func (tc classificationCase) check(t *testing.T) {
	t.Helper()
	policy := domainMessageToWSPayload(tc.message).NotificationPolicy
	if policy == nil {
		t.Fatal("no decision was published")
	}
	if policy.SoundClass != tc.wantClass {
		t.Fatalf("sound_class = %q, want %q", policy.SoundClass, tc.wantClass)
	}
	if !slices.Equal(policy.NamedUserIDs, tc.wantNamed) {
		t.Fatalf("named_user_ids = %v, want %v", policy.NamedUserIDs, tc.wantNamed)
	}
	if policy.NamesEveryone != tc.wantEveryone {
		t.Fatalf("names_everyone = %v, want %v", policy.NamesEveryone, tc.wantEveryone)
	}
}

// TestEventsThatAreNotNotifiableDenyExplicitly is the rollout contract from the
// server's side: this build always says something, so a client can tell "not
// notifiable" from "an older server that cannot answer". A nil here would make
// those two indistinguishable on the wire and mute a client served ahead of its
// backend.
func TestEventsThatAreNotNotifiableDenyExplicitly(t *testing.T) {
	system := channelMessage("Alice added Bob")
	system.Kind = domain.MessageKindSystem

	removed := channelMessage("hello")
	removed.Status = domain.MessageStatusDeleted

	for name, msg := range map[string]domain.Message{"system": system, "removed": removed} {
		t.Run(name, func(t *testing.T) { assertNotNotifiable(t, msg) })
	}
}

func assertNotNotifiable(t *testing.T, msg domain.Message) {
	t.Helper()
	policy := domainMessageToWSPayload(msg).NotificationPolicy
	if policy == nil {
		t.Fatal("published no decision at all, which a client reads as a legacy server")
	}
	if policy.InApp != ws.NotificationDeny || policy.Sound != ws.NotificationDeny ||
		policy.WebPush != ws.NotificationDeny {
		t.Fatalf("channels = (%q, %q, %q), want all %q",
			policy.InApp, policy.Sound, policy.WebPush, ws.NotificationDeny)
	}
	if len(policy.Reasons) != 0 {
		t.Fatalf("reasons = %v, want none: there was no policy question to ask", policy.Reasons)
	}
}

// TestEveryMessagePayloadCarriesADecision is the invariant the client's legacy
// detection rests on. If any message this build publishes could omit the
// object, absence would stop meaning "older server".
func TestEveryMessagePayloadCarriesADecision(t *testing.T) {
	messages := []domain.Message{
		channelMessage("hello"),
		channelMessage(mentionOfBob),
		directMessage("hello"),
		func() domain.Message { m := channelMessage("x"); m.Kind = domain.MessageKindSystem; return m }(),
		func() domain.Message { m := channelMessage("x"); m.Status = domain.MessageStatusDeleted; return m }(),
	}
	for i, msg := range messages {
		if domainMessageToWSPayload(msg).NotificationPolicy == nil {
			t.Fatalf("message %d published no decision: %+v", i, msg)
		}
	}
}

// TestDecisionIsOnTheWire checks the JSON contract itself: the field names a
// browser reads, and that an allowed decision does not ship an empty reasons
// list to every subscriber of every message.
func TestDecisionIsOnTheWire(t *testing.T) {
	encoded, err := json.Marshal(domainMessageToWSPayload(channelMessage(mentionOfBob)))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded struct {
		NotificationPolicy *struct {
			PolicyVersion int      `json:"policy_version"`
			Sound         string   `json:"sound"`
			Reasons       []string `json:"reasons"`
			SoundClass    string   `json:"sound_class"`
			NamedUserIDs  []string `json:"named_user_ids"`
			NamesEveryone bool     `json:"names_everyone"`
		} `json:"notification_policy"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.NotificationPolicy == nil {
		t.Fatalf("notification_policy is absent from %s", encoded)
	}
	if decoded.NotificationPolicy.Sound != ws.NotificationAllow ||
		decoded.NotificationPolicy.PolicyVersion != notificationpolicy.Version ||
		len(decoded.NotificationPolicy.NamedUserIDs) != 1 {
		t.Fatalf("wire decision = %+v", *decoded.NotificationPolicy)
	}
	if decoded.NotificationPolicy.Reasons != nil {
		t.Fatalf("reasons = %v, want omitted on an allow", decoded.NotificationPolicy.Reasons)
	}
}

// TestRealtimeDenialIsExplained proves the other half of the contract: when the
// engine denies, the client is told so and told why, in the policy's own
// vocabulary. Reaching a denial through the engine rather than through a
// condition in this package is the point.
func TestRealtimeDenialIsExplained(t *testing.T) {
	denied := notificationpolicy.Evaluate(notificationpolicy.Context{
		Presence: notificationpolicy.PresenceForeground,
	})
	if denied.Channels.Sound {
		t.Fatal("the fixture is not a denial")
	}
	if got := channelVerdict(denied.Channels.Sound); got != ws.NotificationDeny {
		t.Fatalf("sound = %q, want %q", got, ws.NotificationDeny)
	}
	reasons := deniedReasons(denied)
	if len(reasons) == 0 {
		t.Fatal("a denial reached the wire with nothing to explain it")
	}
	for _, reason := range reasons {
		if reason == "" {
			t.Fatalf("reasons = %v, want the policy's own vocabulary", reasons)
		}
	}
}

// Issue #744, round 4: one channel's decision must never be produced from
// another's.
//
// The realtime context cannot itself produce a divergence in both directions —
// it evaluates on the foreground surface, where push is never allowed — so the
// projection is driven directly with decisions that disagree. A projection that
// copied one channel into the other passes every same-value fixture and fails
// exactly here.
func TestChannelsAreProjectedIndependently(t *testing.T) {
	cases := []struct {
		name     string
		channels notificationpolicy.Channels
		want     [3]string
	}{
		{"in-app only", notificationpolicy.Channels{InApp: true},
			[3]string{ws.NotificationAllow, ws.NotificationDeny, ws.NotificationDeny}},
		{"sound only", notificationpolicy.Channels{Sound: true},
			[3]string{ws.NotificationDeny, ws.NotificationAllow, ws.NotificationDeny}},
		{"push only", notificationpolicy.Channels{WebPush: true},
			[3]string{ws.NotificationDeny, ws.NotificationDeny, ws.NotificationAllow}},
		// The two the reviewer named: every neighbouring pair disagrees, so a
		// projection that copied one field into another cannot pass.
		{"in-app and push, no sound", notificationpolicy.Channels{InApp: true, WebPush: true},
			[3]string{ws.NotificationAllow, ws.NotificationDeny, ws.NotificationAllow}},
		{"sound only, in-app and push denied", notificationpolicy.Channels{Sound: true},
			[3]string{ws.NotificationDeny, ws.NotificationAllow, ws.NotificationDeny}},
		{"all allowed", notificationpolicy.Channels{InApp: true, Sound: true, WebPush: true},
			[3]string{ws.NotificationAllow, ws.NotificationAllow, ws.NotificationAllow}},
		{"none allowed", notificationpolicy.Channels{},
			[3]string{ws.NotificationDeny, ws.NotificationDeny, ws.NotificationDeny}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inApp, sound, webPush := channelsOnTheWire(tc.channels)
			if got := [3]string{inApp, sound, webPush}; got != tc.want {
				t.Fatalf("(in_app, sound, web_push) = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestEveryChannelIsOnTheWire checks the JSON a browser actually reads: three
// fields, decided separately, none of them optional.
//
// A live channel message is deliberately the subject, because its plan is not
// uniform — the foreground surface allows the toast and the chime while push is
// denied — so a field that had been dropped or copied from a neighbour shows up
// here as a wrong value rather than as a missing one.
func TestEveryChannelIsOnTheWire(t *testing.T) {
	encoded, err := json.Marshal(domainMessageToWSPayload(channelMessage("hello")))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded struct {
		NotificationPolicy struct {
			InApp   string `json:"in_app"`
			Sound   string `json:"sound"`
			WebPush string `json:"web_push"`
		} `json:"notification_policy"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	policy := decoded.NotificationPolicy
	if policy.InApp != ws.NotificationAllow {
		t.Fatalf("in_app = %q, want %q", policy.InApp, ws.NotificationAllow)
	}
	if policy.Sound != ws.NotificationAllow {
		t.Fatalf("sound = %q, want %q", policy.Sound, ws.NotificationAllow)
	}
	if policy.WebPush != ws.NotificationDeny {
		t.Fatalf("web_push = %q, want %q", policy.WebPush, ws.NotificationDeny)
	}
}

// ---------------------------------------------------------------------------
// Per-recipient authority (issue #744, review round 6)
// ---------------------------------------------------------------------------

// The decision is about a person, so two people who disagree about a
// conversation must get two decisions. This is the property a single payload
// reused for every subscriber could not have.
func TestDecisionDivergesBetweenRecipientsOfTheSameEvent(t *testing.T) {
	policy := recipientPolicy{}
	payload := domainMessageToWSPayload(channelMessage("hello"))

	muted := policy.PolicyFor(payload, "user-muted", ws.RecipientPreferenceMuted)
	unmuted := policy.PolicyFor(payload, "user-plain", ws.RecipientPreferenceNone)

	if unmuted.InApp != ws.NotificationAllow || unmuted.Sound != ws.NotificationAllow {
		t.Fatalf("the recipient with no preference lost a surface: %+v", unmuted)
	}
	if muted.InApp != ws.NotificationDeny || muted.Sound != ws.NotificationDeny {
		t.Fatalf("the muted recipient was still offered a surface: %+v", muted)
	}
	// The suppression names the rule it rests on, in the engine's vocabulary —
	// this package restates no rule of its own.
	if len(muted.Reasons) != 1 || muted.Reasons[0] != string(notificationpolicy.ReasonMuted) {
		t.Fatalf("reasons = %v, want exactly [%s]", muted.Reasons, notificationpolicy.ReasonMuted)
	}
	if len(unmuted.Reasons) != 0 {
		t.Fatalf("an allowed decision explained itself: %v", unmuted.Reasons)
	}
}

// The recipient the decision is about has to reach the engine. An empty
// RecipientID on a decision made for somebody specific is an audit record that
// names nobody.
func TestRecipientIdentityReachesTheEngine(t *testing.T) {
	ctx := realtimeContext(channelMessage("hello"), recipientFacts{id: "user-7", muted: true})
	if ctx.RecipientID != "user-7" {
		t.Fatalf("RecipientID = %q, want the recipient the decision is for", ctx.RecipientID)
	}
	if !ctx.Preferences.Muted {
		t.Fatal("the recipient's own mute did not reach the engine")
	}
	if ctx.WorkspaceID != "ws-1" {
		t.Fatalf("WorkspaceID = %q, want the message's own", ctx.WorkspaceID)
	}
}

// What the server does not know must not be stated as though it did. Presence is
// the case that matters: this path can see the socket and cannot see the reader.
func TestRealtimeContextDoesNotClaimFactsItCannotObserve(t *testing.T) {
	ctx := realtimeContext(channelMessage("hello"), recipientFacts{id: "user-1"})

	if ctx.Presence != notificationpolicy.PresenceConnected {
		t.Fatalf("Presence = %q, want %q: an open socket is not an observed focus",
			ctx.Presence, notificationpolicy.PresenceConnected)
	}
	// ConversationOpen is left false and is inert under Connected — the rule that
	// reads it requires an observed focus. Proving inertness rather than the
	// literal value is the point: false must not be doing work here.
	open := ctx
	open.ConversationOpen = true
	if notificationpolicy.Evaluate(open).Channels != notificationpolicy.Evaluate(ctx).Channels {
		t.Fatal("ConversationOpen decided something on a path that cannot observe it")
	}
	// The chime preference has no server-side source of truth, so it stays unset
	// rather than being guessed at.
	if ctx.Preferences.SoundMode != "" {
		t.Fatalf("SoundMode = %q, want unset: it lives in the browser", ctx.Preferences.SoundMode)
	}
}

// The published payload is the decision for somebody with no preference — never
// a claim that the recipient it reaches has none.
func TestPublishedPayloadIsTheNoPreferenceDecision(t *testing.T) {
	published := domainMessageToWSPayload(channelMessage("hello")).NotificationPolicy
	forPlain := recipientPolicy{}.PolicyFor(
		domainMessageToWSPayload(channelMessage("hello")), "user-1", ws.RecipientPreferenceNone)
	if published.InApp != forPlain.InApp || published.Sound != forPlain.Sound ||
		published.WebPush != forPlain.WebPush {
		t.Fatalf("published %+v disagrees with the resolved no-preference decision %+v",
			published, forPlain)
	}
}

// fakeMutedPrefs records what the fan-out asked the preference store for.
type fakeMutedPrefs struct {
	storage.NotificationPrefStore
	targetType string
	users      []string
	muted      []string
	err        error
}

func (f *fakeMutedPrefs) FilterMutedUsers(
	_ context.Context, _, targetType, _ string, userIDs []string,
) ([]string, error) {
	f.targetType = targetType
	f.users = userIDs
	return f.muted, f.err
}

// The fan-out's read has to reach the preference table under the target kind
// that table actually uses — a channel event must not be looked up as a DM.
func TestMutedUsersAsksThePreferenceStoreForTheRightTargetKind(t *testing.T) {
	for targetType, want := range map[ws.TargetType]string{
		ws.TargetTypeChannel: storage.NotificationPrefTargetChannel,
		ws.TargetTypeDM:      storage.NotificationPrefTargetDM,
	} {
		prefs := &fakeMutedPrefs{muted: []string{"user-2"}}
		muted, err := recipientPolicy{prefs: prefs}.MutedUsers(
			context.Background(), "ws-1", targetType, "target-1", []string{"user-1", "user-2"})
		if err != nil {
			t.Fatalf("MutedUsers: %v", err)
		}
		if prefs.targetType != want {
			t.Fatalf("asked for %q, want %q", prefs.targetType, want)
		}
		if len(muted) != 1 || muted[0] != "user-2" {
			t.Fatalf("muted = %v, want [user-2]", muted)
		}
		if len(prefs.users) != 2 {
			t.Fatalf("passed %v, want the whole subscriber list in one call", prefs.users)
		}
	}
}

// A target kind the preference table cannot describe has no preferences to
// report, and saying so is not the same as failing.
func TestMutedUsersReportsNobodyForATargetThatCannotBeMuted(t *testing.T) {
	prefs := &fakeMutedPrefs{}
	muted, err := recipientPolicy{prefs: prefs}.MutedUsers(
		context.Background(), "ws-1", ws.TargetTypeUser, "user-9", []string{"user-1"})
	if err != nil || muted != nil {
		t.Fatalf("MutedUsers = (%v, %v), want (nil, nil)", muted, err)
	}
	if prefs.users != nil {
		t.Fatal("queried the preference table for a target it does not describe")
	}
}

// Without a store there is nothing to personalise with, and the hub must be
// left delivering the published decision rather than handed a policy that
// cannot answer.
func TestRecipientPolicyOptionIsOmittedWithoutAStore(t *testing.T) {
	if got := withRecipientPolicyOption(nil, nil); got != nil {
		t.Fatalf("added %d options with no preference store", len(got))
	}
	if got := withRecipientPolicyOption(nil, &fakeMutedPrefs{}); len(got) != 1 {
		t.Fatalf("added %d options, want exactly 1", len(got))
	}
}

// The engine's own answer for a recipient whose preferences could not be read
// (issue #744, review round 7). Asserted through the real adapter, so what the
// fan-out's fake stands in for is proved here against the actual rules.
func TestUnreadablePreferencesDenyEveryAlertSurface(t *testing.T) {
	payload := domainMessageToWSPayload(channelMessage("hello"))
	decision := recipientPolicy{}.PolicyFor(payload, "user-1", ws.RecipientPreferenceUnavailable)

	if decision.InApp != ws.NotificationDeny || decision.Sound != ws.NotificationDeny ||
		decision.WebPush != ws.NotificationDeny {
		t.Fatalf("an unreadable preference kept a surface: %+v", decision)
	}
	// Named as the fault it is, never borrowed from a choice the recipient did
	// not make.
	if len(decision.Reasons) != 1 ||
		decision.Reasons[0] != string(notificationpolicy.ReasonPreferencesUnavailable) {
		t.Fatalf("reasons = %v, want [%s]",
			decision.Reasons, notificationpolicy.ReasonPreferencesUnavailable)
	}
	for _, wrong := range []notificationpolicy.Reason{
		notificationpolicy.ReasonMuted,
		notificationpolicy.ReasonUserPreference,
		notificationpolicy.ReasonUnsupportedChannel,
	} {
		if decision.Reasons[0] == string(wrong) {
			t.Fatalf("borrowed %q for a preference that could not be read", wrong)
		}
	}
	// The message identity is untouched: only the decision differs.
	if decision.SoundClass != ws.SoundClassGeneral || decision.PolicyVersion == 0 {
		t.Fatalf("the decision lost its classification or version: %+v", decision)
	}
}

// The three states the fan-out can report must map onto three engine inputs,
// and "unavailable" must not be collapsed into "muted".
func TestRecipientFactsKeepUnavailableDistinctFromMuted(t *testing.T) {
	none := recipientFactsFrom("user-1", ws.RecipientPreferenceNone)
	muted := recipientFactsFrom("user-1", ws.RecipientPreferenceMuted)
	unavailable := recipientFactsFrom("user-1", ws.RecipientPreferenceUnavailable)

	if none.muted || none.status != notificationpolicy.PreferenceStatusResolved {
		t.Fatalf("a completed read with nothing expressed became %+v", none)
	}
	if !muted.muted || muted.status != notificationpolicy.PreferenceStatusResolved {
		t.Fatalf("a mute became %+v", muted)
	}
	if unavailable.muted {
		t.Fatal("an unreadable preference was recorded as a mute the recipient chose")
	}
	if unavailable.status != notificationpolicy.PreferenceStatusUnavailable {
		t.Fatalf("status = %q, want unavailable", unavailable.status)
	}
	if id := unavailable.id; id != "user-1" {
		t.Fatalf("recipient identity lost: %q", id)
	}
}
