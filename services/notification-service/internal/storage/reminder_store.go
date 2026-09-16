package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/notificationevent"
)

// Persistent reminders for urgent messages (issue #825, parent #820).
//
// # Why this lives in the notification worker and not in chat-service
//
// chat-service records the *intent* — this message is urgent, it asked to keep
// asking, and these are the people it asked — in the same statement that makes
// the message observable, exactly as it records everything else notifiable.
// What happens afterwards is a queue with a clock, a claim, a lease, a retry and
// a delivery policy, and all five of those already exist here. Building the
// reminder loop anywhere else would mean a second scheduler alongside the one
// that already drains chat.notification_outbox, which is what #825 forbids
// outright.
//
// So a reminder is not a new mechanism. It is a new *row* in the outbox the
// worker already drains: same claim, same lease, same backoff, same policy
// evaluation, same push channel, same metrics. Everything this file adds is the
// decision of when one more of those rows should exist.
//
// # Why it writes to the chat schema
//
// One database, one source of truth — the same argument notification-outbox.md
// already makes for reading chat.notification_outbox and
// chat.conversation_notification_prefs from this service. The per-recipient
// reminder state has to be in the same commit as the outbox row it produces, or
// a crash between the two would either lose a reminder or repeat one forever;
// putting the state in a table only this service can reach would make that
// commit a distributed transaction. Migration 000049 named this worker as the
// owner of that lifecycle before it existed.

// ReminderScheduleResult is what one scheduling pass did.
//
// Three counts, because they answer three different operational questions and a
// single total would hide all of them: how much reminding the product is doing,
// how often a pass re-derived a reminder that already existed, and how many
// requests stopped being asked because they ran out of attempts.
type ReminderScheduleResult struct {
	// Scheduled is how many reminder rows this pass actually inserted.
	Scheduled int
	// Deduplicated is how many due reminders were advanced without inserting a
	// row, because the unique index already held one for that exact occurrence.
	//
	// It is the observable half of the idempotency guarantee, and it is expected
	// to be zero in steady state: a rising count means passes are being repeated
	// after their outbox write committed and before their bookkeeping did —
	// which is the crash this design is built to survive, not a fault.
	Deduplicated int
	// Expired is how many recipients stopped being reminded because they reached
	// notificationevent.MaxUrgentReminders.
	Expired int
}

// reminderRecipientPendingBody finds this outbox row's recipient while they are
// still waiting. It is written once because the rule — "only PENDING may produce
// a reminder" (#825) — is applied in three places, and a rule stated three times
// has three answers.
//
// The row is addressed by full primary key — (message_id, recipient_id) — so
// this is an index lookup and cannot match a row belonging to a different
// message or a different person. Tenant isolation comes with it: both
// identifiers were derived server-side in the workspace that produced them, and
// a message id is globally unique, so nothing here is an identifier a client
// chose.
const reminderRecipientPendingBody = `
		SELECT 1
		FROM chat.message_acknowledgements a
		WHERE a.message_id = o.message_id
		  AND a.recipient_id = o.recipient_user_id
		  AND a.state = 'pending'`

// reminderRecipientPending is the unlocked reading, for the cleanup that retires
// reminders nothing will ever claim. It decides nothing a delivery rests on, so
// taking a row lock there would be contention bought for no guarantee.
const reminderRecipientPending = `
	EXISTS (` + reminderRecipientPendingBody + `
	)`

// ReminderRecipientPendingForClaim is the same reading taken *under a row lock*,
// and it is the linearization point of the whole feature.
//
// # The race it closes
//
// Without the lock this is a check-then-act across two tables. The claim
// evaluates the recipient's state from its own snapshot, and nothing stops an
// acknowledgement, a reply or a cancellation from committing between that
// evaluation and the UPDATE that moves the outbox row to 'processing'. The
// reminder is then claimed against a recipient who has already answered, and
// SuppressResolvedReminders cannot reach it any more — 'processing' has no path
// to 'suppressed' — so it is delivered. That is the defect this replaces, and
// TestReminderClaimSerializesWithTerminalTransitionPostgreSQL reproduces it.
//
// # Why FOR UPDATE is the fix and a second SELECT is not
//
// The lock makes the observation and the decision one event. Under READ
// COMMITTED, FOR UPDATE re-evaluates the predicate against the row version it
// actually locked, so a terminal transition that committed first is seen, and
// one that has not committed yet must wait behind this lock and therefore lands
// after the claim. Both orders are serial; neither is "PENDING observed, then
// terminal confirmed, then claim confirmed anyway".
//
// SKIP LOCKED rather than a wait, on the same terms as the outer claim: a
// recipient whose row is being transitioned right now is skipped for this pass
// and taken by the next one. That is the fail-closed direction — a reminder
// whose recipient cannot be *confirmed* pending is not claimed at all — and it
// keeps one contended recipient from serialising a whole batch behind them.
//
// # Lock order
//
// recipient-state row -> outbox row, and this expression is what establishes it:
// PostgreSQL evaluates the sublink in the scan's filter, below the outer
// LockRows that takes the outbox row, so the acknowledgement is always locked
// first. ScheduleDueReminders takes the same order (it locks the recipient row,
// then inserts the outbox row), and every chat-service transition —
// acknowledge, reply, cancel, delete — locks the recipient row alone. No path
// takes them in the opposite order, so no cycle exists.
//
// The lock lives for one statement. ClaimDue runs outside any transaction of the
// caller's and the delivery happens after it returns, so nothing here is held
// across a call to a push provider.
const ReminderRecipientPendingForClaim = `
		EXISTS (` + reminderRecipientPendingBody + `
		  FOR UPDATE SKIP LOCKED
		)`

// scheduleDueRemindersQuery is one atomic scheduling pass.
//
// # The claim
//
// FOR UPDATE ... SKIP LOCKED over the due rows, exactly as the delivery claim
// does and for the same reason: several replicas drain the same queue without
// coordinating, each taking rows nobody else holds. The two statements that
// follow read that locked set, so the row that is counted and the row that is
// written are the same row under the same lock — a concurrent acknowledgement
// either committed before this statement's snapshot, in which case `due` does
// not select it, or waits behind the lock and finds the schedule already
// advanced.
//
// # Why the insert and the bookkeeping are one statement
//
// They are one fact: "this reminder now exists, and the next one is due at T".
// Split across two statements, a crash between them either produces a reminder
// nothing will ever advance past — the same push every five minutes forever — or
// advances a schedule for a reminder nobody was sent. Here the statement either
// commits both or neither.
//
// # Why ON CONFLICT DO NOTHING is the deduplication
//
// The dedupe key carries the occurrence number, so the nth reminder for one
// recipient has one identity and notification_outbox_dedupe_uq is what decides
// it exists. A pass repeated after a crash recomputes the same key, inserts
// nothing, and advances the schedule anyway — which is correct, because the
// reminder it was about to create is already in the queue. No SELECT precedes
// the INSERT and no flag in memory takes part, so the invariant holds between
// processes and across restarts.
//
// No arbiter is named, on the same terms as chat-service's producers: DO NOTHING
// without one absorbs unique violations only, so a foreign key or check failure
// still aborts the statement rather than being silently swallowed.
//
// # The message predicates
//
// status = 'active' and deleted_at IS NULL are the fail-closed direction. A
// message withheld by a link scan has no schedule yet — CreateMessage leaves it
// NULL and the promotion starts it — and a deleted one had its rows cancelled in
// the same transaction as the delete. Re-checking both here means a reminder can
// never be produced for a message its recipients are not entitled to see, even
// if some future path forgets to clear the schedule.
//
// persistent_notifications is re-read rather than assumed: it is the authority
// for whether this message may remind anybody at all, and the schedule column is
// a consequence of it, not a substitute for it.
//
// # occurred_at
//
// The message's own created_at, not the reference instant. The event a reminder
// announces is the urgent message, which happened when it was sent; a client
// that ordered its notifications by this column must not be told a new message
// arrived every five minutes. What repeats is the asking, and the occurrence
// number in the dedupe key is where that is recorded.
//
// # The reference instant
//
// $2 is the instant the whole statement reasons about: which reminders are due,
// when the next one falls, and when a recipient who ran out was resolved. It is
// a parameter rather than now() because the five-minute boundary is a functional
// rule and a rule tested against the wall clock can only be tested with a
// tolerance — "about five minutes" is not the contract #820 wrote down.
//
// One instant for all three, deliberately. Reading `due` against a parameter and
// computing the next window from now() would put two clocks in one statement and
// make the window drift by however long the statement took; here the next
// occurrence is exactly UrgentReminderInterval after the one this pass consumed.
var scheduleDueRemindersQuery = `
	WITH due AS (
		SELECT a.message_id, a.recipient_id, a.reminder_count,
		       m.workspace_id, m.created_at, m.acknowledgement_required
		FROM chat.message_acknowledgements a
		JOIN chat.messages m ON m.id = a.message_id
		WHERE a.state = 'pending'
		  AND a.next_reminder_at IS NOT NULL
		  AND a.next_reminder_at <= $2::timestamptz
		  AND m.persistent_notifications
		  AND m.status = 'active'
		  AND m.deleted_at IS NULL
		ORDER BY a.next_reminder_at
		LIMIT $1
		FOR UPDATE OF a SKIP LOCKED
	),
	scheduled AS (
		INSERT INTO chat.notification_outbox
			(workspace_id, message_id, recipient_user_id, kind, status,
			 source_type, occurred_at, priority, origin, dedupe_key)
		SELECT due.workspace_id, due.message_id, due.recipient_id,
		       '` + string(notificationevent.EventTypeUrgentReminder) + `', 'pending',
		       '` + string(notificationevent.SourceTypeMessage) + `', due.created_at,
		       '` + string(notificationevent.PriorityHigh) + `',
		       '` + string(notificationevent.OriginLive) + `',
		       ` + notificationevent.UrgentReminderDedupeKeySQL(
	"due.message_id", "(due.reminder_count + 1)") + `
		FROM due
		ON CONFLICT DO NOTHING
		RETURNING id
	),
	advanced AS (
		UPDATE chat.message_acknowledgements a
		SET reminder_count = due.reminder_count + 1,
		    -- The next occurrence, or the end of them. NULL is what takes the
		    -- row out of idx_message_acknowledgements_due, so the ceiling is
		    -- enforced by the schedule itself rather than by a predicate every
		    -- future reader would have to remember.
		    next_reminder_at = CASE WHEN due.reminder_count + 1 < $4
		                            THEN $2::timestamptz + ($3 * interval '1 second') END,
		    -- Running out of reminders resolves the recipient as EXPIRED — the
		    -- state migration 000049 declared and left without a producer — but
		    -- only where reminding was the only thing this message asked of them.
		    -- On a message that also asked for confirmation the question is still
		    -- open and somebody may still answer it; marking them expired would
		    -- tell the sender nobody will, which is not something running out of
		    -- pushes establishes.
		    state = CASE WHEN due.reminder_count + 1 >= $4
		                  AND NOT due.acknowledgement_required
		                 THEN 'expired' ELSE a.state END,
		    resolved_at = CASE WHEN due.reminder_count + 1 >= $4
		                        AND NOT due.acknowledgement_required
		                       THEN $2::timestamptz ELSE a.resolved_at END
		FROM due
		WHERE a.message_id = due.message_id
		  AND a.recipient_id = due.recipient_id
		RETURNING (a.next_reminder_at IS NULL) AS exhausted
	)
	SELECT (SELECT count(*) FROM scheduled)::int,
	       (SELECT count(*) FROM advanced)::int,
	       (SELECT count(*) FROM advanced WHERE exhausted)::int`

// ScheduleDueReminders produces at most batchSize reminders due at or before
// `now`, and advances their schedules from the same instant.
//
// One pass, never a drain loop: the same restraint the delivery pass applies, so
// a backlog of reminders cannot become one burst of pushes against a provider
// that is already busy. Throughput is BatchSize per poll interval, which are the
// two numbers an operator already reasons about for this worker.
//
// `now` is the caller's, not the database's, and that is the whole of the clock
// seam this feature needs. The worker passes time.Now().UTC() on every pass, so
// production behaviour is unchanged; a test passes a fixed instant and can then
// assert the five-minute boundary exactly — 4m59s is not due, 5m00s is — instead
// of asserting "about five minutes" against a wall clock it does not control.
// Nothing else is abstracted: there is no Clock interface, because one seam is
// what the rule needs and a second would be a framework nobody asked for.
//
// A zero instant is refused rather than defaulted. Defaulting it would make a
// caller that forgot to pass one behave correctly in production and silently
// differently in a test, which is the failure this parameter exists to prevent.
func (s *PGXNotificationOutboxStore) ScheduleDueReminders(
	ctx context.Context, now time.Time, batchSize int,
) (ReminderScheduleResult, error) {
	if batchSize <= 0 {
		return ReminderScheduleResult{}, nil
	}
	if now.IsZero() {
		return ReminderScheduleResult{}, fmt.Errorf("schedule due reminders: reference instant is required")
	}
	var scheduled, advanced, expired int
	err := s.pool.QueryRow(ctx, scheduleDueRemindersQuery, batchSize, now.UTC(),
		notificationevent.UrgentReminderInterval.Seconds(),
		notificationevent.MaxUrgentReminders,
	).Scan(&scheduled, &advanced, &expired)
	if err != nil {
		return ReminderScheduleResult{}, fmt.Errorf("schedule due reminders: %w", err)
	}
	return ReminderScheduleResult{
		Scheduled: scheduled,
		// Advanced counts every due row this pass moved on; the ones that
		// inserted nothing are the ones the unique index already held.
		Deduplicated: advanced - scheduled,
		Expired:      expired,
	}, nil
}

// suppressResolvedRemindersQuery retires reminders whose recipient stopped being
// pending after the reminder was scheduled.
//
// The window it closes is real and small: a reminder is scheduled, and before
// the worker claims it the recipient acknowledges, replies, is cancelled by the
// sender, or has the message deleted underneath them. #825 requires the terminal
// state to win, so the row is suppressed rather than delivered.
//
// # It is the cleanup, not the guarantee
//
// What actually prevents a delivery to somebody who has answered is
// ReminderRecipientPendingForClaim, inside the claim, under a row lock. This
// statement exists so a reminder that will never be claimed does not sit in the
// backlog for ever, and it runs before the claim in each pass so the ordinary
// case is retired in the same pass it went stale rather than counted as queue
// depth until somebody notices.
//
// # Why it deliberately cannot reach a claimed reminder
//
// 'processing' has no transition to 'suppressed', and that is correct rather
// than a gap. The claim is the point at which a reminder becomes *logically
// acquired for delivery*, and the lock it takes proves the recipient was still
// pending at that instant. An acknowledgement arriving afterwards is an
// acknowledgement that arrived after the notification was already on its way —
// indistinguishable, from the recipient's side, from one that arrived after the
// push had been handed to the provider.
//
// Closing that window would mean holding a PostgreSQL transaction across the
// delivery, which this worker does not do for any notification and must not
// start doing for this one: a provider that stops answering would then hold row
// locks for the whole delivery timeout. The at-least-once contract the outbox
// has always had is preserved unchanged; see the package comment in
// notification_worker.go.
//
// pending -> suppressed and eligible -> suppressed are both transitions the
// machine and the trigger allow. A row already claimed is deliberately out of
// scope: 'processing' has no path to suppressed, and taking a claim away from a
// worker that is mid-delivery is not something this statement may do.
//
// suppressed_reason is a closed operational code, as every suppression reason in
// this service is. It says nothing about the message, the recipient or the
// conversation.
const suppressResolvedRemindersReason = "recipient_resolved"

var suppressResolvedRemindersQuery = `
	UPDATE chat.notification_outbox o
	SET status = 'suppressed',
	    suppressed_reason = '` + suppressResolvedRemindersReason + `',
	    processed_at = now(),
	    next_attempt_at = NULL,
	    updated_at = now()
	WHERE o.kind = '` + string(notificationevent.EventTypeUrgentReminder) + `'
	  AND o.status IN ('pending', 'eligible')
	  AND NOT ` + reminderRecipientPending

// SuppressResolvedReminders retires every scheduled reminder whose recipient is
// no longer pending, and reports how many.
//
// Set-based and unbounded by design: it touches only reminder rows that are
// already provably undeliverable, the predicate is an index lookup per row, and
// leaving any of them behind would mean a backlog gauge that never returns to
// zero after a busy message is answered.
func (s *PGXNotificationOutboxStore) SuppressResolvedReminders(ctx context.Context) (int, error) {
	tag, err := s.pool.Exec(ctx, suppressResolvedRemindersQuery)
	if err != nil {
		return 0, fmt.Errorf("suppress resolved reminders: %w", err)
	}
	return int(tag.RowsAffected()), nil
}
