package notificationevent_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/libs/go/platform/notificationevent"
)

// validatable is what every closed vocabulary in this package has in common, so
// one pair of assertions covers all of them and each test stays a list of values
// rather than a loop wrapped around an if.
type validatable interface{ Valid() bool }

func assertDeclared(t *testing.T, values ...validatable) {
	t.Helper()
	for _, value := range values {
		if !value.Valid() {
			t.Errorf("declared value %v must be valid", value)
		}
	}
}

func assertRejected(t *testing.T, values ...validatable) {
	t.Helper()
	for _, value := range values {
		if value.Valid() {
			t.Errorf("value %v must not be accepted", value)
		}
	}
}

// Every event type the contract declares is accepted, and nothing else is. The
// list is written out rather than derived from the package's own map, so a type
// silently disappearing from the vocabulary fails here.
func TestEventTypeVocabulary(t *testing.T) {
	assertDeclared(t,
		notificationevent.EventTypeDirectMessage,
		notificationevent.EventTypeMention,
		notificationevent.EventTypeReply,
		notificationevent.EventTypeChannelMessage,
		notificationevent.EventTypeReaction,
		notificationevent.EventTypeCall,
	)
	assertRejected(t,
		notificationevent.EventType(""),
		notificationevent.EventType("Mention"),
		notificationevent.EventType("dm"),
		notificationevent.EventType("message"),
	)
}

func TestSourceTypeVocabulary(t *testing.T) {
	assertDeclared(t,
		notificationevent.SourceTypeMessage,
		notificationevent.SourceTypeReaction,
		notificationevent.SourceTypeCall,
	)
	assertRejected(t,
		notificationevent.SourceType(""),
		notificationevent.SourceType("messages"),
		notificationevent.SourceType("Message"),
	)
}

func TestPriorityVocabulary(t *testing.T) {
	assertDeclared(t,
		notificationevent.PriorityHigh,
		notificationevent.PriorityNormal,
		notificationevent.PriorityLow,
	)
	assertRejected(t,
		notificationevent.Priority(""),
		notificationevent.Priority("urgent"),
		notificationevent.Priority("HIGH"),
	)
}

func TestOriginVocabulary(t *testing.T) {
	assertDeclared(t,
		notificationevent.OriginLive,
		notificationevent.OriginImport,
		notificationevent.OriginReplay,
		notificationevent.OriginResync,
	)
	assertRejected(t,
		notificationevent.Origin(""),
		notificationevent.Origin("backfill"),
		notificationevent.Origin("LIVE"),
	)
}

func assertHistorical(t *testing.T, origin notificationevent.Origin, want bool) {
	t.Helper()
	if origin.IsHistorical() != want {
		t.Errorf("origin %q IsHistorical() = %t, want %t", origin, !want, want)
	}
}

// The historical marker is the whole reason Origin exists: an imported,
// replayed or resynced event must be distinguishable from something that just
// happened, deterministically and without consulting a timestamp.
func TestOriginSeparatesHistoricalFromLive(t *testing.T) {
	assertHistorical(t, notificationevent.OriginLive, false)
	assertHistorical(t, notificationevent.OriginImport, true)
	assertHistorical(t, notificationevent.OriginReplay, true)
	assertHistorical(t, notificationevent.OriginResync, true)

	// An unset or unknown origin is neither, and must never pass for live.
	assertHistorical(t, notificationevent.Origin(""), false)
	assertHistorical(t, notificationevent.Origin("backfill"), false)
}

func TestStateVocabulary(t *testing.T) {
	assertDeclared(t,
		notificationevent.StatePending,
		notificationevent.StateEligible,
		notificationevent.StateSuppressed,
		notificationevent.StateProcessing,
		notificationevent.StateSent,
		notificationevent.StateRetrying,
		notificationevent.StateFailed,
	)
	assertRejected(t,
		notificationevent.State(""),
		notificationevent.State("delivered"),
		notificationevent.State("queued"),
	)
}

// terminalStates are the three outcomes that must never be confused with one
// another: "nobody was told, on purpose", "somebody was told", "we tried and
// could not".
var terminalStates = []notificationevent.State{
	notificationevent.StateSuppressed,
	notificationevent.StateSent,
	notificationevent.StateFailed,
}

var openStates = []notificationevent.State{
	notificationevent.StatePending,
	notificationevent.StateEligible,
	notificationevent.StateProcessing,
	notificationevent.StateRetrying,
}

func assertTerminal(t *testing.T, state notificationevent.State, want bool) {
	t.Helper()
	if state.IsTerminal() != want {
		t.Errorf("state %q IsTerminal() = %t, want %t", state, !want, want)
	}
}

func TestTerminalStates(t *testing.T) {
	for _, state := range terminalStates {
		assertTerminal(t, state, true)
	}
	for _, state := range openStates {
		assertTerminal(t, state, false)
	}
	// An undeclared state is not a state at all, so it is not terminal either.
	assertTerminal(t, "unknown", false)
}

// The distinction issue #741 exists to guarantee, asserted directly rather than
// implied by the transition table.
func TestTerminalStatesNeverConvertIntoOneAnother(t *testing.T) {
	for i, from := range terminalStates {
		for _, to := range terminalStates[i+1:] {
			assertTransition(t, from, to, false)
			assertTransition(t, to, from, false)
		}
	}
}

func assertTransition(t *testing.T, from, to notificationevent.State, want bool) {
	t.Helper()
	if from.CanTransitionTo(to) != want {
		t.Errorf("%q -> %q allowed = %t, want %t", from, to, !want, want)
	}
}

func TestAllowedStateTransitions(t *testing.T) {
	allowed := [][2]notificationevent.State{
		{notificationevent.StatePending, notificationevent.StateEligible},
		{notificationevent.StatePending, notificationevent.StateSuppressed},
		{notificationevent.StateEligible, notificationevent.StateProcessing},
		{notificationevent.StateEligible, notificationevent.StateSuppressed},
		{notificationevent.StateProcessing, notificationevent.StateSent},
		{notificationevent.StateProcessing, notificationevent.StateRetrying},
		{notificationevent.StateProcessing, notificationevent.StateFailed},
		{notificationevent.StateRetrying, notificationevent.StateProcessing},
		{notificationevent.StateRetrying, notificationevent.StateFailed},
	}
	for _, pair := range allowed {
		assertTransition(t, pair[0], pair[1], true)
	}
}

func TestRefusedStateTransitions(t *testing.T) {
	// Each pair is refused for a stated reason: delivery cannot be claimed
	// without evaluation; an unevaluated event has not been attempted; a state
	// never transitions to itself; and an undeclared state has no edges at all.
	refused := [][2]notificationevent.State{
		{notificationevent.StatePending, notificationevent.StateProcessing},
		{notificationevent.StatePending, notificationevent.StateSent},
		{notificationevent.StatePending, notificationevent.StateFailed},
		{notificationevent.StatePending, notificationevent.StateRetrying},
		{notificationevent.StateEligible, notificationevent.StateSent},
		{notificationevent.StatePending, notificationevent.StatePending},
		{notificationevent.StateEligible, "unknown"},
		{"unknown", notificationevent.StateEligible},
	}
	for _, pair := range refused {
		assertTransition(t, pair[0], pair[1], false)
	}
}

var baseIdentity = notificationevent.Identity{
	WorkspaceID: "00000000-0000-4000-8000-000000000001",
	RecipientID: "00000000-0000-4000-8000-000000000002",
	EventType:   notificationevent.EventTypeMention,
	SourceType:  notificationevent.SourceTypeMessage,
	SourceID:    "00000000-0000-4000-8000-000000000003",
}

func mustDedupeKey(t *testing.T, identity notificationevent.Identity) string {
	t.Helper()
	key, err := identity.DedupeKey()
	if err != nil {
		t.Fatalf("dedupe key: %v", err)
	}
	return key
}

func TestDedupeKeyFormat(t *testing.T) {
	const want = "message:00000000-0000-4000-8000-000000000003:mention"
	if got := mustDedupeKey(t, baseIdentity); got != want {
		t.Fatalf("dedupe key = %q, want %q", got, want)
	}
}

// The same source entity reached by a different rule is a different logical
// notification, and must not be collapsed into the first one.
func TestDedupeKeySeparatesEventTypesOnOneSource(t *testing.T) {
	reply := baseIdentity
	reply.EventType = notificationevent.EventTypeReply
	if mustDedupeKey(t, reply) == mustDedupeKey(t, baseIdentity) {
		t.Fatal("a mention and a reply on the same message are two events")
	}
}

// Two reactions on one message share the message id; the discriminator is what
// stops one of them from silently swallowing the other.
func TestDedupeKeyDiscriminatorSeparatesOperations(t *testing.T) {
	first := notificationevent.Identity{
		WorkspaceID:   baseIdentity.WorkspaceID,
		RecipientID:   baseIdentity.RecipientID,
		EventType:     notificationevent.EventTypeReaction,
		SourceType:    notificationevent.SourceTypeReaction,
		SourceID:      baseIdentity.SourceID,
		Discriminator: "actor-a.thumbsup",
	}
	second := first
	second.Discriminator = "actor-b.thumbsup"

	firstKey, secondKey := mustDedupeKey(t, first), mustDedupeKey(t, second)
	if firstKey == secondKey {
		t.Fatal("two distinct reactions must not share a dedupe key")
	}
	if want := "reaction:" + baseIdentity.SourceID + ":reaction:actor-a.thumbsup"; firstKey != want {
		t.Fatalf("discriminated key = %q, want %q", firstKey, want)
	}
}

// A retry of one logical operation produces one key. This is the property the
// unique index turns into the idempotency guarantee.
func TestDedupeKeyIsStableAcrossRetries(t *testing.T) {
	if first, second := mustDedupeKey(t, baseIdentity), mustDedupeKey(t, baseIdentity); first != second {
		t.Fatalf("retry produced %q then %q", first, second)
	}
}

func assertIdentityRefused(t *testing.T, identity notificationevent.Identity) {
	t.Helper()
	key, err := identity.DedupeKey()
	if !errors.Is(err, notificationevent.ErrInvalidIdentity) {
		t.Fatalf("error = %v, want ErrInvalidIdentity", err)
	}
	if key != "" {
		t.Fatalf("a refused identity must produce no key, got %q", key)
	}
}

func TestDedupeKeyRejectsIncompleteIdentities(t *testing.T) {
	mutations := map[string]func(*notificationevent.Identity){
		"missing workspace":  func(i *notificationevent.Identity) { i.WorkspaceID = "" },
		"missing recipient":  func(i *notificationevent.Identity) { i.RecipientID = "" },
		"unknown event type": func(i *notificationevent.Identity) { i.EventType = "gossip" },
		"empty event type":   func(i *notificationevent.Identity) { i.EventType = "" },
		"unknown source":     func(i *notificationevent.Identity) { i.SourceType = "thread" },
		"missing source id":  func(i *notificationevent.Identity) { i.SourceID = "" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			identity := baseIdentity
			mutate(&identity)
			assertIdentityRefused(t, identity)
		})
	}
}

// The separator is the attack: a segment carrying ':' could be shaped into the
// key of a different event and suppress it.
func TestDedupeKeyRejectsUnsafeSegments(t *testing.T) {
	mutations := map[string]func(*notificationevent.Identity){
		"separator in source id":      func(i *notificationevent.Identity) { i.SourceID = "a:b" },
		"separator in discriminator":  func(i *notificationevent.Identity) { i.Discriminator = "actor:mention" },
		"whitespace in discriminator": func(i *notificationevent.Identity) { i.Discriminator = "actor mention" },
		"control in discriminator":    func(i *notificationevent.Identity) { i.Discriminator = "actor\nmention" },
		"oversized discriminator": func(i *notificationevent.Identity) {
			i.Discriminator = strings.Repeat("x", 65)
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			identity := baseIdentity
			mutate(&identity)
			assertIdentityRefused(t, identity)
		})
	}
}

func assertReasonRefused(t *testing.T, state notificationevent.State, reason string) {
	t.Helper()
	if err := notificationevent.ValidateSuppressedReason(state, reason); !errors.Is(err, notificationevent.ErrInvalidSuppressedReason) {
		t.Fatalf("ValidateSuppressedReason(%q, %d bytes) = %v, want ErrInvalidSuppressedReason",
			state, len(reason), err)
	}
}

func assertReasonAccepted(t *testing.T, state notificationevent.State, reason string) {
	t.Helper()
	if err := notificationevent.ValidateSuppressedReason(state, reason); err != nil {
		t.Fatalf("ValidateSuppressedReason(%q, %d bytes) = %v, want nil", state, len(reason), err)
	}
}

// Exactly the suppressed state carries a reason. A reason on a delivered or
// failed notification would claim a suppression that never happened.
func TestSuppressedReasonBelongsOnlyToSuppression(t *testing.T) {
	assertReasonAccepted(t, notificationevent.StateSuppressed, "quiet_hours")
	assertReasonAccepted(t, notificationevent.StateSent, "")
	assertReasonAccepted(t, notificationevent.StateFailed, "")
	assertReasonAccepted(t, notificationevent.StatePending, "")

	assertReasonRefused(t, notificationevent.StateSuppressed, "")
	assertReasonRefused(t, notificationevent.StateSent, "quiet_hours")
	assertReasonRefused(t, notificationevent.StateFailed, "quiet_hours")
	assertReasonRefused(t, notificationevent.StateEligible, "quiet_hours")
}

// The bound is a bound: the longest permitted reason is accepted and one byte
// more is not.
func TestSuppressedReasonIsBounded(t *testing.T) {
	assertReasonAccepted(t, notificationevent.StateSuppressed,
		strings.Repeat("x", notificationevent.SuppressedReasonMaxLen))
	assertReasonRefused(t, notificationevent.StateSuppressed,
		strings.Repeat("x", notificationevent.SuppressedReasonMaxLen+1))
}

// The SQL expression and the Go builder are the same format. This checks the
// shape without a database; the value they produce is compared against a real
// one in TestNotificationOutboxCommitsWithMessagePostgreSQL.
func TestMessageDedupeKeySQLMatchesTheContractFormat(t *testing.T) {
	got := notificationevent.MessageDedupeKeySQL("inserted.id", "r.kind")
	const want = "'message:' || inserted.id::text || ':' || r.kind"
	if got != want {
		t.Fatalf("MessageDedupeKeySQL = %q, want %q", got, want)
	}
	if !strings.HasPrefix(got, "'"+string(notificationevent.SourceTypeMessage)+":'") {
		t.Fatalf("the expression must open with the source type segment: %q", got)
	}
}
