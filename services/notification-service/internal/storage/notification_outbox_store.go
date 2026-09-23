package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/nicrepository/nchat/libs/go/platform/notificationevent"
)

// ErrNotificationStateConflict reports that a notification was not in the state
// the caller believed it was in, so nothing was written.
//
// It is a conflict and not a failure. Two workers racing for one row is the
// normal outcome of running more than one replica, and a worker that could not
// tell that apart from a broken database would log an incident every time
// Blue/Green ran two slots at once.
var ErrNotificationStateConflict = errors.New("notification outbox state conflict")

// ErrInvalidNotificationTransition reports a state change the machine in
// libs/go/platform/notificationevent does not allow, or a suppression reason
// that does not agree with the state it was given.
//
// Distinct from a conflict on purpose: a conflict means the row moved, this
// means the caller asked for something no row could ever do.
var ErrInvalidNotificationTransition = errors.New("invalid notification outbox transition")

// NotificationEvent is one row of chat.notification_outbox, in the only shape
// the worker needs.
//
// The persisted row holds references only. ClaimDue can additionally resolve an
// ephemeral presentation authorized at the claim's PostgreSQL statement snapshot.
type NotificationEvent struct {
	// ID is the notification's identity for its whole life, across every
	// attempt. It is what the worker hands a delivery adapter as an idempotency
	// key, and the unique index on (workspace_id, recipient_user_id, dedupe_key)
	// is what guarantees one logical event has exactly one of them.
	ID          string
	WorkspaceID string
	RecipientID string
	EventType   string
	Priority    string
	SourceType  string
	SourceID    string
	// Origin is where the event came from: live, import, replay or resync. It is
	// read because the policy engine refuses to alert for anything that did not
	// just happen, and a timestamp cannot substitute for it — an import writes
	// old occurred_at values and a replay writes new ones.
	Origin    string
	DedupeKey string
	// Attempts is how many times this event has been claimed, counting the
	// claim that produced this struct. It is therefore also the *identity of
	// that claim*, and every finalisation carries it back as a predicate.
	//
	// That is what stops a stale worker from finalising somebody else's claim.
	// A worker whose lease expired mid-delivery still holds the attempts value
	// its own claim returned; the reclaim that took the row from it incremented
	// the column, so the stale worker's compare-and-set matches nothing and it
	// is told it lost the row. Without this predicate "still processing" was the
	// only condition checked, and the two claims were indistinguishable.
	Attempts   int
	OccurredAt time.Time
	// Muted is this recipient's own mute preference for the conversation the
	// event happened in, resolved by the projection below rather than by a
	// lookup per row (issue #744).
	//
	// It is a resolved fact and not a decision: what a mute *does* is decided by
	// libs/go/platform/notificationpolicy, which is the only place that may
	// suppress. False means "this recipient has not silenced this conversation",
	// which covers both the absence of a row and a row that expresses only a
	// level — see mutedProjection for why the test is the timestamp and no
	// longer the row.
	Muted bool
	// NotificationLevel is the other half of that preference (issue #136): which
	// events of this conversation the recipient wants alerts for, from the same
	// row and the same single statement.
	//
	// Also a resolved fact and also not a decision. The empty string is what a
	// recipient with no preference row has, and the engine normalises it to the
	// product default rather than guessing here.
	NotificationLevel string
	// Presentation is what a push banner may say about this event (issue #870),
	// resolved by presentationProjection in the same statement and against this
	// recipient's access at the claim snapshot. The zero value means "nothing may be said",
	// which every delivery channel renders as the generic notification it
	// rendered before this field existed.
	Presentation MessagePresentation
}

// NotificationOutboxStore is every persistent operation the notification worker
// performs. An interface because the worker's own tests drive it with a fake,
// while the claim semantics it describes are proved against a real PostgreSQL.
type NotificationOutboxStore interface {
	ListPending(ctx context.Context, limit int) ([]NotificationEvent, error)
	MarkEvaluated(ctx context.Context, id string, state notificationevent.State, reason string) error
	ClaimDue(ctx context.Context, batchSize, maxAttempts int, lease time.Duration) ([]NotificationEvent, error)
	// The three finalisations take the attempts value their claim returned, and
	// apply only while the row still carries it. See NotificationEvent.Attempts.
	MarkDelivered(ctx context.Context, id string, attempt int) error
	ScheduleRetry(ctx context.Context, id string, attempt int, delay time.Duration, category string) error
	MarkFailed(ctx context.Context, id string, attempt int, category string) error
	FailExhausted(ctx context.Context, maxAttempts int) (int, error)
	Backlog(ctx context.Context) (int, error)

	// The persistent reminder lifecycle (issue #825). See reminder_store.go for
	// why it is the same queue rather than a second one.
	//
	// `now` is the instant the scheduling rule reasons about — which reminders
	// are due, and when the next one falls. It is the caller's so the five-minute
	// boundary can be asserted exactly rather than with a tolerance.
	ScheduleDueReminders(ctx context.Context, now time.Time, batchSize int) (ReminderScheduleResult, error)
	SuppressResolvedReminders(ctx context.Context) (int, error)
}

// PGXNotificationOutboxStore implements NotificationOutboxStore over a pgx pool.
type PGXNotificationOutboxStore struct {
	pool           Pool
	previewEnabled bool
}

// NewPGXNotificationOutboxStore uses the delivery adapter's rollout flag so a
// v1 claim never reads presentation. ListPending never needs it in either mode.
func NewPGXNotificationOutboxStore(pool Pool, previewEnabled bool) *PGXNotificationOutboxStore {
	return &PGXNotificationOutboxStore{pool: pool, previewEnabled: previewEnabled}
}

// mutedProjection resolves the recipient's own mute preference for the
// conversation the event happened in (issue #744).
//
// # Why it is part of the projection
//
// The policy engine reads Preferences.Muted, and the only server-side authority
// for it is chat.conversation_notification_prefs — the same table the sidebar's
// ListMuted reads and the mute endpoint writes. No table, column, cache or
// configuration is added here: this is a read of the source of truth that
// already exists.
//
// It is a correlated subquery inside the batch read rather than a lookup the
// worker performs per event, and that is the whole point. A per-row resolution
// would be one query per notification — the N+1 the issue forbids — while here
// a batch of BatchSize events costs exactly the one statement it already cost.
//
// # Why the join goes through chat.messages
//
// An outbox row names its source (message_id) and not its conversation, so the
// conversation is the message's: channel_id XOR dm_conversation_id, the
// invariant 000004 enforces. Each side is matched against the preference column
// of its own kind, so a preference can only ever match the kind of target it was
// written for.
//
// # Scoping, which is the security-relevant part
//
// Three predicates, and all three are required:
//
//   - p.user_id = o.recipient_user_id, so one member muting a conversation can
//     never silence it for another. The table is keyed by user for exactly this
//     reason;
//   - p.workspace_id = o.workspace_id, so a preference row cannot reach an event
//     in another tenant. The prefs table has separate foreign keys to workspaces
//     and to the target rather than a composite one, so a row naming a workspace
//     that does not own the target is insertable — this predicate is what makes
//     it unusable;
//   - m.workspace_id = o.workspace_id, so the conversation the preference is
//     matched against is one this event's tenant owns.
//
// Every identifier compared here comes from the persisted row or from the
// message it names. Nothing a client asserted takes part.
//
// Membership is deliberately not re-checked. The write path already established
// it — NotificationPrefStore.Mute admits only a conversation the user could see
// — and re-applying visibility on read would make a revoked membership *undo* a
// mute, which is the direction that alerts someone who asked not to be.
// preferenceRowJoin is the correlated lookup both projections below are built
// from, written once so the scoping predicates above cannot drift apart between
// them.
const preferenceRowJoin = `
		FROM chat.messages m
		JOIN chat.conversation_notification_prefs p
		  ON p.user_id = o.recipient_user_id
		 AND p.workspace_id = o.workspace_id
		 AND ((p.channel_id IS NOT NULL AND p.channel_id = m.channel_id)
		   OR (p.dm_conversation_id IS NOT NULL AND p.dm_conversation_id = m.dm_conversation_id))
		WHERE m.id = o.message_id
		  AND m.workspace_id = o.workspace_id`

// mutedProjection asks whether the recipient has *silenced* this conversation,
// which since issue #136 is the timestamp and no longer the existence of the
// row.
//
// That change is the load-bearing part of this migration for delivery. A row
// with a NULL muted_at is "mentions and replies, not silenced" — a preference
// somebody expressed in order to keep hearing about mentions — and reading its
// presence as a mute would have silenced every one of those recipients the
// moment 000050 shipped.
const mutedProjection = `
	EXISTS (
		SELECT 1` + preferenceRowJoin + `
		  AND p.muted_at IS NOT NULL
	)`

// levelProjection reads the level from the same row.
//
// A second correlated subquery over the same join rather than one returning a
// composite, because PostgreSQL has no way to spread a single subselect across
// two output columns. Both resolve through the (user_id, target) partial unique
// index, so this is one extra index lookup per row of the batch and still no
// query per event.
//
// COALESCE to the default level, so a recipient with no preference row at all
// reads as the product default rather than as an empty string the engine would
// have to interpret.
const levelProjection = `
	COALESCE((
		SELECT p.notification_level` + preferenceRowJoin + `
		LIMIT 1
	), 'all')`

// MessagePresentation is ephemeral content authorized at the ClaimDue statement
// snapshot, never persisted in the outbox. Changes after that snapshot cannot
// retract a claimed or provider-accepted payload. A retry resolves access again.
// The zero value renders a generic notification without message content.
type MessagePresentation struct {
	// Sender is auth.users.display_name of the author. Never an e-mail, never
	// a username, never an id.
	Sender string `json:"sender"`
	// Context is where it was said: "#" plus the channel display name, the group
	// conversation's title for a group DM, empty for a 1:1 DM — where the
	// sender is already the whole context — and empty for a group nobody named.
	Context string `json:"context"`
	// GroupDM preserves the conversation kind when its title is blank. The
	// presentation layer supplies the server-owned fallback after sanitizing.
	GroupDM bool `json:"group_dm"`
	// Body is the message text, already bounded by the projection to
	// presentationBodyChars. It is raw: no sanitisation, no truncation to the
	// final limit and no assumption that it is safe to present. That happens in
	// the worker, in Go, where UTF-8 and control characters are testable.
	Body string `json:"body"`
	// Attachment reports that the message carries at least one file, which is
	// what lets an attachment-only message say something instead of nothing.
	Attachment bool `json:"attachment"`
}

// presentationBodyChars bounds what the database sends back per notification.
//
// Characters, not bytes: left() counts characters, so this cannot split a
// multi-byte sequence. It is far above the limit the push preview is finally
// truncated to, because sanitisation removes characters and collapsing runs of
// whitespace removes more, and a message that is mostly newlines should still
// have something left to show. It is far below the 40000 a message may carry,
// so a batch claim never pulls a megabyte of message text it will discard.
const presentationBodyChars = "500"

// presentationNameChars and presentationTitleChars bound the other two strings
// the projection carries, for the same reason the body is bounded: nothing here
// should transport a pathological value across the wire only for Go to throw
// almost all of it away.
//
// They are ceilings, not the presentation limit — webpush_preview.go still cuts
// a title to 80 characters and 200 bytes, and that is the limit the contract
// states. Each of these is set to the bound the schema or the domain already
// imposes, so a conforming row is returned untouched and only a value that
// should not exist is shortened:
//
//   - chat.channels.display_name is 1..100 by channels_display_name_length_check;
//   - chat.dm_conversations.title is <= 120 by dm_conversations_title_length_check;
//   - auth.users.display_name has *no* database CHECK. The self-service path
//     bounds it at 80 runes (auth-service's selfDisplayNameMaxLen), but that is
//     one writer among several, so the same 100 the channels column enforces is
//     applied here rather than trusted.
const (
	presentationNameChars  = "100"
	presentationTitleChars = "120"
)

// presentationProjection resolves all presentation fields under one authorization
// predicate at claim time. Only active, undeleted user messages without malicious
// links qualify; pending scans and system messages fall back to generic content.
// CASE in ClaimDue skips this subquery when preview is disabled. ListPending
// never runs it. This is one statement per batch, not a query per recipient.
//
// Keep the workspace, membership and target-state predicates aligned with
// chat-service's ListDMMessages and ListChannelMessages (message_store.go).
// channel_visible_to_user supplies channel visibility, not target/workspace
// status. There is no shared SQL function for DM read access; importing the
// chat service's internal Go helpers would cross the service boundary.
const presentationProjection = `
	(
		SELECT jsonb_build_object(
			'sender', left(sender_user.display_name, ` + presentationNameChars + `),
			'context', CASE
				WHEN m.channel_id IS NOT NULL
					THEN '#' || left(c.display_name, ` + presentationNameChars + `)
				WHEN d.type = 'group' THEN left(COALESCE(d.title, ''), ` + presentationTitleChars + `)
				ELSE ''
			END,
			'group_dm', COALESCE(d.type = 'group', false),
			'body', left(m.body_text, ` + presentationBodyChars + `),
			'attachment', EXISTS (
				SELECT 1 FROM chat.message_attachments ma
				WHERE ma.message_id = m.id
			)
		)
		FROM chat.messages m
		JOIN chat.workspaces w
		  ON w.id = m.workspace_id AND w.status = 'active'
		JOIN chat.workspace_members wm
		  ON wm.workspace_id = m.workspace_id
		 AND wm.user_id = o.recipient_user_id AND wm.status = 'active'
		JOIN auth.users sender_user
		  ON sender_user.id = m.sender_id
		LEFT JOIN chat.channels c
		  ON c.id = m.channel_id AND c.workspace_id = m.workspace_id
		LEFT JOIN chat.dm_conversations d
		  ON d.id = m.dm_conversation_id AND d.workspace_id = m.workspace_id
		WHERE m.id = o.message_id
		  AND m.workspace_id = o.workspace_id
		  -- SR-001. The recipient's own account, which is not their membership.
		  --
		  -- Suspending somebody globally revokes their sessions — every read in
		  -- this product goes through authsession.ActiveSessionCTE, which
		  -- refuses any session whose user is not active and not soft-deleted —
		  -- but it leaves chat.workspace_members and chat.push_subscriptions
		  -- exactly where they were, and it leaves every outbox row already
		  -- written. Without this predicate a claim kept resolving message text
		  -- for a person the authenticated UI had already stopped serving.
		  --
		  -- EXISTS and not a JOIN: this is a filter on o.recipient_user_id, it
		  -- projects nothing, and it cannot widen the result the way a join on a
		  -- table with a non-unique key could. The alias is spelled out because
		  -- the other auth.users in this statement is the *sender*, and reading
		  -- one as the other is the defect this closes.
		  --
		  -- The two columns are the ones ActiveSessionCTE names, in the same
		  -- order, so the question this asks and the question a page load asks
		  -- are the same question.
		  AND EXISTS (
		    SELECT 1
		    FROM auth.users recipient_user
		    WHERE recipient_user.id = o.recipient_user_id
		      AND recipient_user.status = 'active'
		      AND recipient_user.deleted_at IS NULL
		  )
		  AND m.kind = 'user'
		  AND m.status = 'active'
		  AND m.deleted_at IS NULL
		  AND m.link_safety_state <> 'malicious'
		  AND (
		    (m.channel_id IS NOT NULL AND c.status = 'active'
		      AND chat.channel_visible_to_user(m.channel_id, o.recipient_user_id))
		    OR (m.dm_conversation_id IS NOT NULL AND d.status = 'active' AND EXISTS (
		      SELECT 1 FROM chat.dm_members dm
		      WHERE dm.conversation_id = m.dm_conversation_id
		        AND dm.user_id = o.recipient_user_id
		        AND dm.status = 'active'
		    ))
		  )
	)`

// notificationColumns is the projection both reads share, so a column added to
// one can never be forgotten in the other.
const notificationColumns = `
	o.id::text, o.workspace_id::text, o.recipient_user_id::text,
	o.kind, o.priority, o.source_type, o.message_id::text, o.origin,
	COALESCE(o.dedupe_key, ''), o.attempts, o.occurred_at,` +
	mutedProjection + `,` + levelProjection

// listPendingQuery reads events no policy has looked at yet.
//
// No lock and no lease. Evaluation reaches nothing outside this process, so the
// worst a race costs is two workers deciding the same row and one of them
// finding its compare-and-set already applied — cheap, and cheaper than holding
// a transaction open across a policy call. The claim that precedes a *delivery*
// is a different matter and is locked accordingly.
const listPendingQuery = `
	SELECT` + notificationColumns + `, NULL::jsonb
	FROM chat.notification_outbox o
	WHERE o.status = 'pending'
	ORDER BY o.occurred_at, o.id
	LIMIT $1`

// ListPending returns at most limit events awaiting a policy decision, oldest
// occurrence first.
func (s *PGXNotificationOutboxStore) ListPending(ctx context.Context, limit int) ([]NotificationEvent, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, listPendingQuery, limit)
	if err != nil {
		return nil, fmt.Errorf("list pending notifications: %w", err)
	}
	return scanNotificationEvents(rows, "list pending notifications")
}

// markEvaluatedQuery records a policy's decision about a pending event.
//
// The WHERE names the state the caller believes the row is in, which makes the
// statement a compare-and-set rather than a read followed by a write with the
// race left in. processed_at is written only for suppression, because that is
// the only outcome here a row never leaves.
//
// # Why this writes next_attempt_at
//
// Becoming eligible is the moment the event becomes *available to the worker*,
// and that instant has to be recorded, because it is not the instant the event
// happened. A message from three weeks ago that sat pending until today became
// available today; occurred_at still says three weeks ago.
//
// Leaving the column NULL and letting the claim fall back to occurred_at merged
// the two facts, and the merge had a consequence: every historical event
// promoted to eligible entered the queue with a decades-old priority and
// overtook retries that had genuinely been waiting. Under a steady trickle of
// old pending rows being evaluated, a due retry could be overtaken for ever.
//
// now() is the database's clock — the same one the lease deadline, the retry
// schedule and the claim's due predicate are all written and compared against.
// Taking it from the worker's clock instead would make availability the only
// value in the protocol subject to skew between replicas.
//
// Suppression deliberately writes nothing here: a suppressed event is terminal
// and must never become claimable.
const markEvaluatedQuery = `
	UPDATE chat.notification_outbox
	SET status = $2::text,
	    suppressed_reason = NULLIF($3::text, ''),
	    next_attempt_at = CASE WHEN $2::text = 'eligible' THEN now() ELSE next_attempt_at END,
	    processed_at = CASE WHEN $2::text = 'suppressed' THEN now() ELSE processed_at END,
	    updated_at = now()
	WHERE id = $1::uuid
	  AND status = 'pending'`

// MarkEvaluated moves a pending event to the state a policy chose for it.
//
// Only the two transitions the machine allows out of pending are accepted, and
// the reason contract — exactly the suppressed state carries one — is checked
// here so a caller gets a domain error instead of a constraint violation.
func (s *PGXNotificationOutboxStore) MarkEvaluated(
	ctx context.Context, id string, state notificationevent.State, reason string,
) error {
	if !notificationevent.StatePending.CanTransitionTo(state) {
		return fmt.Errorf("%w: pending -> %q", ErrInvalidNotificationTransition, state)
	}
	if err := notificationevent.ValidateSuppressedReason(state, reason); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidNotificationTransition, err)
	}
	return s.applyTransition(ctx, markEvaluatedQuery, id, string(state), reason)
}

// claimDueQuery takes a batch of due events and leases them in one statement.
//
// Every clause is load-bearing:
//
//   - FOR UPDATE SKIP LOCKED is what lets several replicas drain the queue
//     without coordinating. Each takes rows nobody else holds, so no two workers
//     can ever be delivering the same notification because of this query;
//   - the UPDATE *is* the claim. There is no window between choosing a row and
//     owning it, because they are the same write;
//   - next_attempt_at is when the row next becomes available, in every claimable
//     state: the instant a policy made it eligible, the instant a backoff ends,
//     the instant a lease lapses. A worker that died leaves its rows in
//     'processing' with a deadline in the past, which is exactly what the
//     predicate calls due, so a crash costs one lease and not a lost event;
//   - 'processing' is in the predicate for that reason alone. A row still inside
//     its lease is not due and cannot be taken;
//   - attempts is counted here rather than on failure. A notification that kills
//     the worker before anything can be recorded still burns an attempt each
//     time it is reclaimed, so a poison event reaches the ceiling instead of
//     cycling forever;
//   - the ORDER BY matches idx_notification_outbox_claimable exactly, so the
//     LIMIT stops the scan after a batch instead of sorting the backlog.
//
// # Why the order is next_attempt_at, and why occurred_at only breaks ties
//
// next_attempt_at is the availability instant — when the row became, or becomes,
// something this worker may take. Every claimable state writes it: MarkEvaluated
// on promotion to eligible, ScheduleRetry when a backoff is computed, this
// statement when a lease is granted. So the queue is FIFO by *availability*,
// which is the only ordering that guarantees liveness.
//
// occurred_at is a different fact — when the thing happened in the product — and
// it is deliberately demoted to a tie-break. Two earlier versions of this query
// let it decide priority and both starved retries:
//
//	next_attempt_at NULLS FIRST     put every fresh eligible row, which had no
//	                                next_attempt_at, ahead of every retry;
//	COALESCE(next_attempt_at,       fell back to occurred_at for the same rows,
//	         occurred_at)           so a three-week-old message evaluated today
//	                                entered the queue with a three-week-old
//	                                priority and overtook a retry due a minute
//	                                ago. A trickle of historical pending rows
//	                                being evaluated could hold a due retry back
//	                                indefinitely.
//
// With availability recorded rather than inferred, a retry that became due at T
// is ahead of everything that became available after T, and nothing can become
// available before T once T has passed. The queue ahead of it is therefore
// finite and drains, which is what makes progress guaranteed rather than likely.
//
// # Why NULL is not due
//
// The predicate is a plain comparison, so a NULL next_attempt_at is not due —
// which is exactly right for pending, the one state that carries NULL by design
// and is not claimable anyway. It also picks the safe direction if a future
// writer ever forgets to stamp availability: the row waits and shows up in the
// backlog gauge, instead of being claimed with an unknown priority and
// overtaking everything else. 000044 backfills the same invariant onto any row
// that predates it.
//
// Nothing external is called while this runs, and it holds no transaction of the
// caller's: the row is leased, the statement commits, and only then does a
// delivery begin.
const claimDueQuery = `
	WITH due AS (
		SELECT o.id
		FROM chat.notification_outbox o
		WHERE o.status IN ('eligible', 'retrying', 'processing')
		  AND o.attempts < $3
		  AND o.next_attempt_at <= now()
		  -- Issue #825: a reminder is claimable only while its recipient is still
		  -- pending, and this is where that becomes a guarantee rather than a
		  -- hope. The sublink takes a row lock on the recipient's state, so the
		  -- claim and a PENDING -> terminal transition are serialized on the same
		  -- persisted row; see ReminderRecipientPendingForClaim for the race this
		  -- closes, the lock order it establishes, and why an unlocked read here
		  -- was a check-then-act.
		  --
		  -- The kind test short-circuits, so an ordinary notification never
		  -- performs the lookup, takes no second lock, and follows exactly the
		  -- path it followed before this feature existed.
		  AND (o.kind <> '` + string(notificationevent.EventTypeUrgentReminder) + `'
		       OR ` + ReminderRecipientPendingForClaim + `)
		ORDER BY o.next_attempt_at, o.occurred_at, o.id
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	)
	UPDATE chat.notification_outbox o
	SET status = 'processing',
	    attempts = LEAST(o.attempts + 1, 32767),
	    next_attempt_at = now() + ($2 * interval '1 second'),
	    updated_at = now()
	FROM due
	WHERE o.id = due.id
	RETURNING` + notificationColumns + `,
	CASE WHEN $4::boolean THEN ` + presentationProjection + ` ELSE NULL::jsonb END`

// ClaimDue leases up to batchSize events for delivery.
//
// An event whose attempts already reached maxAttempts is not claimed: it is work
// that can no longer succeed, and FailExhausted is what retires it.
func (s *PGXNotificationOutboxStore) ClaimDue(
	ctx context.Context, batchSize, maxAttempts int, lease time.Duration,
) ([]NotificationEvent, error) {
	if batchSize <= 0 || maxAttempts <= 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, claimDueQuery, batchSize, lease.Seconds(), maxAttempts, s.previewEnabled)
	if err != nil {
		return nil, fmt.Errorf("claim due notifications: %w", err)
	}
	return scanNotificationEvents(rows, "claim due notifications")
}

// markDeliveredQuery records that a channel accepted the notification.
//
// last_error is cleared: a delivered notification that still names the failure
// of an earlier attempt would read, months later, as one that failed.
//
// The attempts predicate is what makes this the finalisation of *this* claim.
// Without it a worker whose lease expired mid-delivery would mark the row sent
// under the claim another worker now holds — cancelling a delivery that is still
// in flight and recording an outcome for an attempt that never reported one.
const markDeliveredQuery = `
	UPDATE chat.notification_outbox
	SET status = 'sent',
	    processed_at = now(),
	    next_attempt_at = NULL,
	    last_error = NULL,
	    updated_at = now()
	WHERE id = $1::uuid
	  AND status = 'processing'
	  AND attempts = $2`

// MarkDelivered ends a claim in the terminal state that means somebody was told.
//
// attempt is the value the claim returned. A mismatch means this worker no
// longer owns the row and is reported as ErrNotificationStateConflict.
func (s *PGXNotificationOutboxStore) MarkDelivered(ctx context.Context, id string, attempt int) error {
	return s.applyTransition(ctx, markDeliveredQuery, id, attempt)
}

// scheduleRetryQuery hands the event back to the queue with its next attempt
// already decided.
//
// The delay is computed by the worker's retry policy and persisted here, so a
// restart resumes the same schedule rather than retrying everything at once.
const scheduleRetryQuery = `
	UPDATE chat.notification_outbox
	SET status = 'retrying',
	    next_attempt_at = now() + ($3 * interval '1 second'),
	    last_error = $4::text,
	    updated_at = now()
	WHERE id = $1::uuid
	  AND status = 'processing'
	  AND attempts = $2`

// ScheduleRetry releases a claim for another attempt at a stated time.
//
// Guarded by the claim's attempts for the same reason as MarkDelivered, and it
// matters most here: a stale worker rescheduling would push the *live* claim's
// lease out into the future and overwrite its error category, so the delivery
// actually in flight would be finalised against a row that had already been
// handed back to the queue.
func (s *PGXNotificationOutboxStore) ScheduleRetry(
	ctx context.Context, id string, attempt int, delay time.Duration, category string,
) error {
	return s.applyTransition(ctx, scheduleRetryQuery, id, attempt, delay.Seconds(), category)
}

// markFailedQuery ends a claim in the terminal state that means we tried and
// could not.
//
// Emphatically not the same row state as suppression: suppressed_reason is left
// untouched and stays NULL, so "nobody was told, on purpose" and "we tried and
// could not" remain two different, queryable facts.
const markFailedQuery = `
	UPDATE chat.notification_outbox
	SET status = 'failed',
	    processed_at = now(),
	    next_attempt_at = NULL,
	    last_error = $3::text,
	    updated_at = now()
	WHERE id = $1::uuid
	  AND status = 'processing'
	  AND attempts = $2`

// MarkFailed retires a claim that will not be attempted again.
//
// Guarded by the claim's attempts: a stale worker must not be able to retire an
// event permanently on the strength of an attempt that has already been
// superseded.
func (s *PGXNotificationOutboxStore) MarkFailed(
	ctx context.Context, id string, attempt int, category string,
) error {
	return s.applyTransition(ctx, markFailedQuery, id, attempt, category)
}

// failExhaustedQuery retires abandoned claims that can no longer succeed.
//
// It exists for one case, and the case is real: a notification whose delivery
// kills the worker is never finalised by anybody, so it is reclaimed, counted,
// abandoned, reclaimed again. Without this it would do that forever. The
// predicate is narrow on purpose — only a claim whose lease has already expired
// and whose attempts are spent — so it can never overtake a delivery that is
// still in flight, and processing -> failed is a transition the trigger allows.
const failExhaustedQuery = `
	UPDATE chat.notification_outbox
	SET status = 'failed',
	    processed_at = now(),
	    next_attempt_at = NULL,
	    last_error = 'attempts_exhausted',
	    updated_at = now()
	WHERE status = 'processing'
	  AND attempts >= $1
	  AND next_attempt_at IS NOT NULL
	  AND next_attempt_at <= now()`

// FailExhausted retires every abandoned claim that has spent its attempts, and
// reports how many. It is the bound on a poison event.
//
// It is the one path that finalises a 'processing' row without naming a claim
// generation, and it does not need to: its predicate already requires a lease
// that has lapsed, and a claim taken by any live worker sets next_attempt_at
// into the future. So a row this statement can touch is by definition one no
// worker holds. It is also unreachable for a row a worker could still claim,
// because the claim refuses attempts >= maxAttempts.
func (s *PGXNotificationOutboxStore) FailExhausted(ctx context.Context, maxAttempts int) (int, error) {
	if maxAttempts <= 0 {
		return 0, nil
	}
	tag, err := s.pool.Exec(ctx, failExhaustedQuery, maxAttempts)
	if err != nil {
		return 0, fmt.Errorf("fail exhausted notifications: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// backlogQuery counts the work that is neither terminal nor in flight. Its
// predicate is idx_notification_outbox_open's, so the count is an index scan
// over the backlog rather than over the history.
const backlogQuery = `
	SELECT count(*)
	FROM chat.notification_outbox
	WHERE status IN ('pending', 'eligible', 'retrying')`

// Backlog reports how many notifications are waiting to be delivered.
func (s *PGXNotificationOutboxStore) Backlog(ctx context.Context) (int, error) {
	var backlog int
	if err := s.pool.QueryRow(ctx, backlogQuery).Scan(&backlog); err != nil {
		return 0, fmt.Errorf("count notification backlog: %w", err)
	}
	return backlog, nil
}

// applyTransition executes a compare-and-set and turns its result into the
// error the caller can act on.
//
// Every statement in this file is the same shape — a SET clause over an id and
// an expected status — so the reading of the result lives here once. A claim
// this worker no longer holds, because its lease expired and another worker took
// the row, matches nothing and is reported as a conflict: a thing to count, not
// a thing to fail on.
func (s *PGXNotificationOutboxStore) applyTransition(
	ctx context.Context, query, id string, args ...any,
) error {
	if id == "" {
		return fmt.Errorf("%w: notification id is required", ErrInvalidNotificationTransition)
	}
	tag, err := s.pool.Exec(ctx, query, append([]any{id}, args...)...)
	if err != nil {
		return transitionError(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotificationStateConflict
	}
	return nil
}

// transitionError keeps the database's own refusal distinguishable from a lost
// race.
//
// chat.enforce_notification_outbox_transition raises 23514 for a transition the
// Go machine should already have refused. Reaching it means the two definitions
// disagree, which is a defect and not contention, so it must not be reported as
// an ordinary conflict.
func transitionError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23514" {
		return fmt.Errorf("%w: refused by the database", ErrInvalidNotificationTransition)
	}
	return fmt.Errorf("transition notification: %w", err)
}

// scanNotificationEvents reads a result set of notification rows. Shared by both
// reads so their projections cannot drift apart.
func scanNotificationEvents(rows pgx.Rows, operation string) ([]NotificationEvent, error) {
	defer rows.Close()

	var events []NotificationEvent
	for rows.Next() {
		var event NotificationEvent
		// NULL whenever the recipient may not see the message now, which is why
		// this is a []byte and not a struct scan: the absence has to be
		// representable, and it has to read as the zero presentation.
		var presentation []byte
		if err := rows.Scan(&event.ID, &event.WorkspaceID, &event.RecipientID,
			&event.EventType, &event.Priority, &event.SourceType, &event.SourceID,
			&event.Origin, &event.DedupeKey, &event.Attempts, &event.OccurredAt,
			&event.Muted, &event.NotificationLevel, &presentation); err != nil {
			return nil, fmt.Errorf("%s: %w", operation, err)
		}
		if err := decodePresentation(presentation, &event.Presentation); err != nil {
			return nil, fmt.Errorf("%s: %w", operation, err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", operation, err)
	}
	return events, nil
}

// decodePresentation reads the jsonb presentationProjection produced.
//
// A NULL — the recipient may see nothing — leaves the zero value, which is the
// generic notification. A malformed value is an error and not a silent zero:
// the only thing that writes this column is the statement above, so anything
// unparseable is a defect in this file, and swallowing it would turn a
// permanently broken preview into an invisible one.
func decodePresentation(encoded []byte, into *MessagePresentation) error {
	if len(encoded) == 0 {
		return nil
	}
	if err := json.Unmarshal(encoded, into); err != nil {
		return fmt.Errorf("decode notification presentation: %w", err)
	}
	return nil
}
