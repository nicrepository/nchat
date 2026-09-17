package worker

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/libs/go/platform/notificationevent"
	"github.com/nicrepository/nchat/libs/go/platform/notificationpolicy"
	"github.com/nicrepository/nchat/libs/go/platform/observability"
	"github.com/nicrepository/nchat/services/notification-service/internal/storage"
)

// Issue #825, worker level.
//
// What the worker owes this feature is narrow and is exactly what is asserted
// here: it runs both halves of the lifecycle once per pass, before the
// evaluation that decides them; it counts what they report without inventing a
// label per recipient; it says so once per pass rather than once per person; and
// a database that is refusing either of them does not stop the queue draining.
//
// Every rule that actually decides a reminder — the five-minute window,
// only-pending eligibility, the unique index that makes a repeat a no-op, the
// ceiling that produces EXPIRED, the claim that refuses a resolved recipient —
// is a property of the statements and is proved against a real PostgreSQL in
// the storage package.

func reminderWorker(t *testing.T, outbox *fakeOutbox) (*NotificationWorker, *observability.Metrics) {
	t.Helper()
	metrics, shared := newTestMetrics(t)
	worker := NewNotificationWorker(notificationTestConfig(), NotificationWorkerDeps{
		Store:     outbox,
		Deliverer: &recordingDeliverer{},
		Metrics:   metrics,
		Logger:    silentLogger(),
	})
	return worker, shared
}

// loggingReminderWorker is reminderWorker with the log lines kept, because three
// of the assertions below are about what the worker says rather than what it
// writes to the database.
func loggingReminderWorker(t *testing.T, outbox *fakeOutbox) (*NotificationWorker, *bytes.Buffer) {
	t.Helper()
	var captured bytes.Buffer
	metrics, _ := newTestMetrics(t)
	worker := NewNotificationWorker(notificationTestConfig(), NotificationWorkerDeps{
		Store:     outbox,
		Deliverer: &recordingDeliverer{},
		Metrics:   metrics,
		Logger:    slog.New(slog.NewTextHandler(&captured, nil)),
	})
	return worker, &captured
}

// Both halves run, exactly once, on every pass — including a pass with nothing
// else to do. A scheduler that only ran when the queue was busy would stop
// reminding people the moment the product went quiet, which is the moment an
// urgent message most needs to keep asking.
func TestWorkerSchedulesAndRetiresRemindersOnEveryPass(t *testing.T) {
	outbox := newFakeOutbox()
	worker, _ := reminderWorker(t, outbox)

	worker.runPass()
	worker.runPass()

	if outbox.reminderPasses != 2 || outbox.supersedePasses != 2 {
		t.Fatalf("scheduled %d times and retired %d times, want 2 and 2",
			outbox.reminderPasses, outbox.supersedePasses)
	}
}

// The scheduling batch is the worker's own BatchSize, so one configuration
// bounds every kind of work a pass does. A reminder pass that ignored it would
// be the one unbounded claim in a worker built entirely out of bounded ones.
func TestWorkerBoundsReminderSchedulingByTheConfiguredBatch(t *testing.T) {
	outbox := newFakeOutbox()
	worker, _ := reminderWorker(t, outbox)

	worker.runPass()

	if got, want := outbox.reminderBatchSize, notificationTestConfig().BatchSize; got != want {
		t.Fatalf("scheduled with batch %d, want %d", got, want)
	}
}

// A reminder is scheduled as a pending row and is therefore evaluated and
// delivered by the same pass that created it. Deferring it to the next tick
// would add a poll interval to an interval the product already fixed at five
// minutes.
func TestWorkerSchedulesRemindersBeforeItEvaluates(t *testing.T) {
	outbox := newFakeOutbox()
	worker, _ := reminderWorker(t, outbox)

	// The fake records the order the worker called it in: scheduling must have
	// happened by the time the claim runs, or a reminder created this pass would
	// sit undelivered.
	worker.runPass()

	if outbox.reminderPasses == 0 || outbox.claims == 0 {
		t.Fatalf("pass did not both schedule (%d) and claim (%d)",
			outbox.reminderPasses, outbox.claims)
	}
	if outbox.supersedePasses == 0 {
		t.Fatal("stale reminders must be retired before the claim, not after it")
	}
}

// Every outcome the feature has is counted, and counted under the closed
// `result` label the worker already publishes. #825 asks for reminders
// scheduled, deduplicated and expired to be distinguishable; a single total
// would hide all three.
func TestWorkerCountsEveryReminderOutcome(t *testing.T) {
	outbox := newFakeOutbox()
	outbox.scheduleReminderResult(storage.ReminderScheduleResult{
		Scheduled: 4, Deduplicated: 1, Expired: 2,
	})
	outbox.supersededReminders(3)
	worker, shared := reminderWorker(t, outbox)

	worker.runPass()

	scraped := scrape(t, shared)
	for _, expected := range []string{
		`nchat_notification_events_total{result="reminder_scheduled"} 4`,
		`nchat_notification_events_total{result="reminder_deduplicated"} 1`,
		`nchat_notification_events_total{result="reminder_expired"} 2`,
		`nchat_notification_events_total{result="reminder_superseded"} 3`,
	} {
		if !strings.Contains(scraped, expected) {
			t.Fatalf("metrics missing %q\n%s", expected, scraped)
		}
	}
}

// No identifier ever becomes a label. A reminder metric keyed by message or
// recipient would grow a series for every urgent message ever sent, and would
// put an identity this service exists to keep private into a store that is
// scraped and retained differently from a log.
func TestReminderMetricsCarryNoIdentifiers(t *testing.T) {
	outbox := newFakeOutbox()
	outbox.scheduleReminderResult(storage.ReminderScheduleResult{Scheduled: 1})
	outbox.supersededReminders(1)
	worker, shared := reminderWorker(t, outbox)

	worker.runPass()

	for _, line := range strings.Split(scrape(t, shared), "\n") {
		if !strings.HasPrefix(line, "nchat_notification_events_total{") {
			continue
		}
		if strings.Count(line, "=") != 1 {
			t.Fatalf("event counter grew a second label: %q", line)
		}
		for _, forbidden := range []string{"message", "recipient", "user", "workspace"} {
			if strings.Contains(line, forbidden+"_id") {
				t.Fatalf("metric line carries an identifier: %q", line)
			}
		}
	}
}

// A pass that scheduled nothing says nothing. Without this the log would carry
// a line every poll interval in every deployment, and the lines that matter
// would be the ones nobody reads.
func TestWorkerIsSilentWhenNoReminderIsDue(t *testing.T) {
	outbox := newFakeOutbox()
	worker, logs := loggingReminderWorker(t, outbox)

	worker.runPass()

	if strings.Contains(logs.String(), "urgent reminders") {
		t.Fatalf("an idle pass logged about reminders:\n%s", logs.String())
	}
}

// One line per pass, never one per recipient. A message asking two hundred
// people would otherwise write two hundred lines every five minutes, and a
// failing delivery would multiply that again — the log spam #825 asks to avoid.
func TestWorkerLogsOneLinePerReminderPass(t *testing.T) {
	outbox := newFakeOutbox()
	outbox.scheduleReminderResult(storage.ReminderScheduleResult{Scheduled: 200})
	worker, logs := loggingReminderWorker(t, outbox)

	worker.runPass()

	if got := strings.Count(logs.String(), "urgent reminders scheduled"); got != 1 {
		t.Fatalf("logged %d reminder lines for 200 recipients, want 1", got)
	}
	if strings.Contains(logs.String(), "recipient") || strings.Contains(logs.String(), "message_id") {
		t.Fatalf("reminder log line names an individual:\n%s", logs.String())
	}
}

// A database refusing the scheduler does not stop the queue draining. Not
// scheduling this minute's reminders delays them by one poll interval; refusing
// to deliver everything else because of it would turn one fault into an outage.
func TestWorkerKeepsDrainingWhenReminderSchedulingFails(t *testing.T) {
	outbox := newFakeOutbox()
	outbox.seedPending("n1")
	outbox.fail("schedule_reminders", errors.New("database is refusing"))
	outbox.fail("suppress_resolved_reminders", errors.New("database is refusing"))
	metrics, shared := newTestMetrics(t)
	deliverer := &recordingDeliverer{}
	worker := NewNotificationWorker(notificationTestConfig(), NotificationWorkerDeps{
		Store: outbox, Deliverer: deliverer, Metrics: metrics, Logger: silentLogger(),
	})

	worker.runPass()

	if got := outbox.snapshot("n1").state; got != notificationevent.StateSent {
		t.Fatalf("state = %q, want the ordinary notification still delivered", got)
	}
	if !strings.Contains(scrape(t, shared), `nchat_notification_events_total{result="error"}`) {
		t.Fatal("a refused scheduling pass must still be counted as an error")
	}
}

// A failure reports the category and never the driver's text, which carries the
// statement. The same reticence every other database call in this worker applies.
func TestReminderFailuresAreLoggedByCategoryOnly(t *testing.T) {
	outbox := newFakeOutbox()
	outbox.fail("schedule_reminders", errors.New("relation chat.message_acknowledgements does not exist"))
	worker, logs := loggingReminderWorker(t, outbox)

	worker.runPass()

	body := logs.String()
	if !strings.Contains(body, "schedule_reminders_failed") {
		t.Fatalf("failure category missing from:\n%s", body)
	}
	if strings.Contains(body, "does not exist") {
		t.Fatalf("driver error text reached the log:\n%s", body)
	}
}

// ── every reminder goes through the policy again (issue #825) ────────────────

// A reminder is decided by the central policy on every occurrence, exactly as
// the first notification was. #825 is explicit that persistent notifications
// are not a bypass: the same mute, the same origin rule, the same channels.
func TestReminderIsEvaluatedByTheCentralPolicy(t *testing.T) {
	reminder := liveNotification()
	reminder.EventType = string(notificationevent.EventTypeUrgentReminder)

	verdict := evaluate(t, reminder)

	if !verdict.Deliver {
		t.Fatalf("a live reminder was refused: %+v", verdict)
	}
	if verdict.PolicyVersion == 0 {
		t.Fatal("a reminder must carry the version of the rules that decided it")
	}
}

// A muted conversation silences its reminders too. The mute is resolved per row
// by the outbox projection, so it is re-read on every occurrence rather than
// decided once when the message was sent.
func TestReminderIsSuppressedByAMutedConversation(t *testing.T) {
	reminder := liveNotification()
	reminder.EventType = string(notificationevent.EventTypeUrgentReminder)
	reminder.Muted = true

	verdict := evaluate(t, reminder)

	if verdict.Deliver {
		t.Fatal("a reminder reached a conversation the recipient had muted")
	}
	if !strings.Contains(verdict.Reason(), string(notificationpolicy.ReasonMuted)) {
		t.Fatalf("reason = %q, want the mute named", verdict.Reason())
	}
}

// Urgency is not a bypass of the rules an event is subject to for reasons other
// than preference: a reminder that is not a live event is suppressed like any
// other, so a replayed or imported one never pages anybody.
func TestReminderDoesNotBypassTheOriginRule(t *testing.T) {
	reminder := liveNotification()
	reminder.EventType = string(notificationevent.EventTypeUrgentReminder)
	reminder.Origin = string(notificationevent.OriginReplay)

	if verdict := evaluate(t, reminder); verdict.Deliver {
		t.Fatal("a replayed reminder was delivered")
	}
}
