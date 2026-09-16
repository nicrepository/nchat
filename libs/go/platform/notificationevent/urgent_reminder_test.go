package notificationevent_test

import (
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/notificationevent"
)

// Issue #825: the shared half of the persistent reminder contract.
//
// chat-service starts a reminder schedule, notification-service advances it, and
// the browser renders what comes out. All three read the constants and the key
// format from this package, so what is asserted here is the agreement itself —
// not an implementation, which neither service has in Go.

// The event type has to be declared, because an outbox row carrying a type this
// package does not know is a row no policy can decide about.
func TestUrgentReminderIsADeclaredEventType(t *testing.T) {
	if !notificationevent.EventTypeUrgentReminder.Valid() {
		t.Fatal("urgent_reminder must be a declared event type")
	}
	if got := string(notificationevent.EventTypeUrgentReminder); got != "urgent_reminder" {
		t.Fatalf("wire value = %q, want urgent_reminder", got)
	}
}

// It is its own type and not a spelling of an existing one. Sharing a value with
// mention or direct_message would make a reminder indistinguishable from the
// message it is reminding about, in the metric, in the log and in the payload.
func TestUrgentReminderIsDistinctFromEveryOtherEventType(t *testing.T) {
	for _, other := range []notificationevent.EventType{
		notificationevent.EventTypeDirectMessage,
		notificationevent.EventTypeMention,
		notificationevent.EventTypeReply,
		notificationevent.EventTypeChannelMessage,
		notificationevent.EventTypeReaction,
		notificationevent.EventTypeCall,
	} {
		if other == notificationevent.EventTypeUrgentReminder {
			t.Fatalf("%q collides with urgent_reminder", other)
		}
	}
}

// The interval #820 specifies, and the fact that this package is where it is
// written down. A test that recomputed it would assert nothing; this asserts the
// product decision.
func TestUrgentReminderIntervalIsFiveMinutes(t *testing.T) {
	if got := notificationevent.UrgentReminderInterval; got != 5*time.Minute {
		t.Fatalf("interval = %v, want 5m", got)
	}
}

// #825 requires reminders to stop. A ceiling that is zero or negative would stop
// them before they started; one that is absurdly large would not stop them at
// all. Both directions are asserted because both are ways to get this wrong.
func TestUrgentReminderCeilingIsFiniteAndPositive(t *testing.T) {
	ceiling := notificationevent.MaxUrgentReminders
	if ceiling <= 0 {
		t.Fatalf("ceiling = %d, want a positive number of reminders", ceiling)
	}
	span := time.Duration(ceiling) * notificationevent.UrgentReminderInterval
	if span > 4*time.Hour {
		t.Fatalf("reminders would run for %v, which is not a bounded ask", span)
	}
}

// The key the scheduler builds in SQL is the key this package would build in Go.
//
// The two are written separately — the scheduler's fan-out is set-based and Go
// never sees an individual row — so this asserts they agree on the format. That
// they agree when a real PostgreSQL evaluates the expression is proved by
// TestUrgentReminderDedupeKeyMatchesSQLPostgreSQL in the notification-service
// storage package; this is the cheaper half that runs everywhere.
func TestUrgentReminderDedupeKeySQLMatchesTheGoBuilder(t *testing.T) {
	expression := notificationevent.UrgentReminderDedupeKeySQL("m.id", "(a.reminder_count + 1)")
	for _, fragment := range []string{
		"'message:'",
		"m.id::text",
		":urgent_reminder:'",
		"(a.reminder_count + 1)::text",
	} {
		if !strings.Contains(expression, fragment) {
			t.Fatalf("expression %q is missing %q", expression, fragment)
		}
	}

	key, err := notificationevent.Identity{
		WorkspaceID: "ws", RecipientID: "user",
		EventType:  notificationevent.EventTypeUrgentReminder,
		SourceType: notificationevent.SourceTypeMessage,
		SourceID:   "11111111-1111-4111-8111-111111111111",
		// The occurrence is the discriminator, and it is what makes the nth
		// reminder a different logical event from the (n-1)th.
		Discriminator: "3",
	}.DedupeKey()
	if err != nil {
		t.Fatalf("DedupeKey: %v", err)
	}
	if want := "message:11111111-1111-4111-8111-111111111111:urgent_reminder:3"; key != want {
		t.Fatalf("key = %q, want %q", key, want)
	}
}

// Two occurrences of the same message and recipient are two keys. Without this
// the unique index would collapse every reminder after the first into a
// duplicate of it, and the product would send exactly one.
func TestUrgentReminderOccurrencesAreDistinctIdentities(t *testing.T) {
	identity := notificationevent.Identity{
		WorkspaceID: "ws", RecipientID: "user",
		EventType:  notificationevent.EventTypeUrgentReminder,
		SourceType: notificationevent.SourceTypeMessage,
		SourceID:   "11111111-1111-4111-8111-111111111111",
	}
	seen := map[string]struct{}{}
	for occurrence := 1; occurrence <= notificationevent.MaxUrgentReminders; occurrence++ {
		identity.Discriminator = string(rune('0'+occurrence%10)) + string(rune('a'+occurrence/10))
		key, err := identity.DedupeKey()
		if err != nil {
			t.Fatalf("DedupeKey(%d): %v", occurrence, err)
		}
		if _, duplicate := seen[key]; duplicate {
			t.Fatalf("occurrence %d produced a key already seen: %q", occurrence, key)
		}
		seen[key] = struct{}{}
	}
}
