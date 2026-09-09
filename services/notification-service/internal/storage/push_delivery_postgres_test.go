package storage_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/services/notification-service/internal/domain"
	"github.com/nicrepository/nchat/services/notification-service/internal/storage"
)

// Issue #746: the delivery ledger against a real PostgreSQL.
//
// A mock cannot prove any of this, because all of it is behaviour the database
// owns:
//
//   - a primary key over (notification_id, subscription_id) deduplicating under
//     genuine concurrency, with no read-then-write anywhere in the path;
//   - a fan-out whose exclusions are a join rather than a filter in Go, so
//     "already delivered" and "retired" are decided by the same statement that
//     selects;
//   - two foreign keys that cascade, which is the whole of this table's
//     retention policy;
//   - the migration: that it applies, constrains, and rolls back.
//
// Opt-in like its neighbours: needs NOTIFICATION_TEST_DATABASE_URL against a
// _test database carrying the real migrations.

// deliveryFixture is one notification and one recipient's browsers.
type deliveryFixture struct {
	pool           *pgxpool.Pool
	notificationID string
	principal      domain.Principal
	messageID      string
	users          []string
}

// seedDelivery creates a workspace member, a message, and one eligible
// notification addressed to that member.
func seedDelivery(t *testing.T) *deliveryFixture {
	t.Helper()
	pool := newNotificationTestPool(t)
	fixture := &deliveryFixture{pool: pool}

	sender := newFixtureUser(t, pool, "sender")
	recipient := newFixtureUser(t, pool, "recipient")
	fixture.users = []string{sender, recipient}
	fixture.principal = domain.Principal{UserID: recipient, WorkspaceID: notifyWorkerWorkspace}

	execFixture(t, pool, `
		INSERT INTO chat.workspace_members (workspace_id, user_id, status)
		VALUES ($1::uuid, $2::uuid, 'active')
		ON CONFLICT DO NOTHING`, notifyWorkerWorkspace, recipient)

	if err := pool.QueryRow(t.Context(), `
		INSERT INTO chat.messages
			(workspace_id, channel_id, sender_id, kind, body_text, body_format, status)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'user', 'delivery fixture', 'v2', 'active')
		RETURNING id::text`,
		notifyWorkerWorkspace, notifyWorkerChannel, sender).Scan(&fixture.messageID); err != nil {
		t.Fatalf("seed message: %v", err)
	}

	if err := pool.QueryRow(t.Context(), `
		INSERT INTO chat.notification_outbox
			(workspace_id, message_id, recipient_user_id, kind, status, source_type,
			 occurred_at, priority, origin, dedupe_key, next_attempt_at)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'mention', 'eligible', 'message',
		        now(), 'high', 'live', 'message:' || $2::text || ':mention', now())
		RETURNING id::text`,
		notifyWorkerWorkspace, fixture.messageID, recipient).Scan(&fixture.notificationID); err != nil {
		t.Fatalf("seed notification: %v", err)
	}

	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM chat.messages WHERE id = $1::uuid`, fixture.messageID)
		_, _ = pool.Exec(ctx, `DELETE FROM auth.users WHERE id = ANY($1::uuid[])`, fixture.users)
	})
	return fixture
}

func (f *deliveryFixture) store() *storage.PGXPushDeliveryStore {
	return storage.NewPGXPushDeliveryStore(f.pool)
}

// browser registers one subscription for the recipient and returns it.
func (f *deliveryFixture) browser(t *testing.T, device string) domain.PushSubscription {
	t.Helper()
	subscription, err := storage.NewPGXPushSubscriptionStore(f.pool).Upsert(
		t.Context(), f.principal, registrationFor(device, device+"-"+f.notificationID))
	if err != nil {
		t.Fatalf("register %s: %v", device, err)
	}
	return subscription
}

func (f *deliveryFixture) deliverable(t *testing.T) []storage.PushTarget {
	t.Helper()
	targets, err := f.store().ListDeliverable(t.Context(),
		f.notificationID, f.principal.WorkspaceID, f.principal.UserID)
	if err != nil {
		t.Fatalf("ListDeliverable: %v", err)
	}
	return targets
}

func targetIDs(targets []storage.PushTarget) []string {
	ids := make([]string, 0, len(targets))
	for _, target := range targets {
		ids = append(ids, target.SubscriptionID)
	}
	return ids
}

func (f *deliveryFixture) ledgerCount(t *testing.T) int {
	t.Helper()
	var count int
	if err := f.pool.QueryRow(t.Context(), `
		SELECT count(*) FROM chat.notification_push_deliveries
		WHERE notification_id = $1::uuid`, f.notificationID).Scan(&count); err != nil {
		t.Fatalf("count ledger: %v", err)
	}
	return count
}

// ---------------------------------------------------------------------------
// The fan-out
// ---------------------------------------------------------------------------

// Every active browser of the addressed recipient, and the keys a send needs.
func TestPushDeliveryFanOutReturnsEveryActiveBrowserPostgreSQL(t *testing.T) {
	fixture := seedDelivery(t)
	first := fixture.browser(t, "laptop")
	second := fixture.browser(t, "phone")

	targets := fixture.deliverable(t)

	if got := targetIDs(targets); len(got) != 2 {
		t.Fatalf("got %v, want both browsers", got)
	}
	byID := map[string]storage.PushTarget{}
	for _, target := range targets {
		byID[target.SubscriptionID] = target
	}
	for _, subscription := range []domain.PushSubscription{first, second} {
		target, ok := byID[subscription.ID]
		if !ok {
			t.Fatalf("subscription %s was not in the fan-out", subscription.ID)
		}
		if target.Generation != subscription.Generation {
			t.Fatalf("generation = %d, want the registered %d",
				target.Generation, subscription.Generation)
		}
		if target.Endpoint == "" || target.P256dh == "" || target.Auth == "" {
			t.Fatalf("the fan-out returned a target with no credentials: %+v", target)
		}
	}
}

// The scoping is the authorisation. Another workspace member's browsers are not
// reachable through this query, whatever identifiers are handed to it.
func TestPushDeliveryFanOutIsScopedToTheRecipientPostgreSQL(t *testing.T) {
	fixture := seedDelivery(t)
	fixture.browser(t, "laptop")

	// A second member of the same workspace, with their own browser.
	other := newFixtureUser(t, fixture.pool, "other")
	fixture.users = append(fixture.users, other)
	execFixture(t, fixture.pool, `
		INSERT INTO chat.workspace_members (workspace_id, user_id, status)
		VALUES ($1::uuid, $2::uuid, 'active') ON CONFLICT DO NOTHING`,
		notifyWorkerWorkspace, other)
	if _, err := storage.NewPGXPushSubscriptionStore(fixture.pool).Upsert(t.Context(),
		domain.Principal{UserID: other, WorkspaceID: notifyWorkerWorkspace},
		registrationFor("other-laptop", "other-"+fixture.notificationID)); err != nil {
		t.Fatalf("register the other member: %v", err)
	}

	if got := len(fixture.deliverable(t)); got != 1 {
		t.Fatalf("the fan-out returned %d browsers, want only the recipient's", got)
	}
}

// A retired subscription leaves the fan-out, and it leaves for good rather than
// only for the notification that retired it.
func TestPushDeliveryFanOutExcludesRetiredBrowsersPostgreSQL(t *testing.T) {
	fixture := seedDelivery(t)
	retired := fixture.browser(t, "laptop")
	live := fixture.browser(t, "phone")

	application, err := fixture.store().RecordDelivery(t.Context(),
		retired.ID, retired.Generation,
		domain.DeliveryResult{Outcome: domain.OutcomeInvalidated, Reason: domain.ReasonGone})
	if err != nil {
		t.Fatalf("RecordDelivery: %v", err)
	}
	if application != domain.ApplicationRecorded {
		t.Fatalf("the retirement reported %v, want it applied", application)
	}

	ids := targetIDs(fixture.deliverable(t))
	if len(ids) != 1 || ids[0] != live.ID {
		t.Fatalf("got %v, want only the live browser", ids)
	}
}

// A disabled subscription is excluded too. It is the owner's own decision, and
// the fan-out reads status rather than deciding what each status means.
func TestPushDeliveryFanOutExcludesDisabledBrowsersPostgreSQL(t *testing.T) {
	fixture := seedDelivery(t)
	disabled := fixture.browser(t, "laptop")
	fixture.browser(t, "phone")

	if err := storage.NewPGXPushSubscriptionStore(fixture.pool).
		Disable(t.Context(), fixture.principal, disabled.ID); err != nil {
		t.Fatalf("Disable: %v", err)
	}

	if got := len(fixture.deliverable(t)); got != 1 {
		t.Fatalf("the fan-out returned %d browsers, want the one still enabled", got)
	}
}

// The exclusion that makes partial success work: an endpoint the notification
// has already reached is not in the next fan-out, and the ones still owed it
// are.
func TestPushDeliveryFanOutExcludesAlreadyDeliveredBrowsersPostgreSQL(t *testing.T) {
	fixture := seedDelivery(t)
	done := fixture.browser(t, "laptop")
	owed := fixture.browser(t, "phone")

	if err := fixture.store().MarkDelivered(t.Context(),
		fixture.notificationID, done.ID, done.Generation); err != nil {
		t.Fatalf("MarkDelivered: %v", err)
	}

	ids := targetIDs(fixture.deliverable(t))
	if len(ids) != 1 || ids[0] != owed.ID {
		t.Fatalf("got %v, want only the browser still owed a delivery", ids)
	}
}

// The ledger is keyed by the pair, so a browser that received one notification
// is still owed the next.
func TestPushDeliveryLedgerIsPerNotificationPostgreSQL(t *testing.T) {
	first := seedDelivery(t)
	browser := first.browser(t, "laptop")

	if err := first.store().MarkDelivered(t.Context(),
		first.notificationID, browser.ID, browser.Generation); err != nil {
		t.Fatalf("MarkDelivered: %v", err)
	}

	// A second notification to the same recipient, on the same message.
	var second string
	if err := first.pool.QueryRow(t.Context(), `
		INSERT INTO chat.notification_outbox
			(workspace_id, message_id, recipient_user_id, kind, status, source_type,
			 occurred_at, priority, origin, dedupe_key, next_attempt_at)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'reply', 'eligible', 'message',
		        now(), 'normal', 'live', 'message:' || $2::text || ':reply', now())
		RETURNING id::text`,
		notifyWorkerWorkspace, first.messageID, first.principal.UserID).Scan(&second); err != nil {
		t.Fatalf("seed the second notification: %v", err)
	}

	targets, err := first.store().ListDeliverable(t.Context(),
		second, first.principal.WorkspaceID, first.principal.UserID)
	if err != nil {
		t.Fatalf("ListDeliverable: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("the second notification found %d browsers, want the one", len(targets))
	}
}

// ---------------------------------------------------------------------------
// The ledger under concurrency
// ---------------------------------------------------------------------------

// The primary key is the deduplication, and it holds without a read preceding
// any write. Ten writers racing on one pair leave one row.
func TestPushDeliveryConcurrentRecordsStaySinglePostgreSQL(t *testing.T) {
	fixture := seedDelivery(t)
	browser := fixture.browser(t, "laptop")

	const writers = 10
	var group sync.WaitGroup
	errs := make(chan error, writers)
	for range writers {
		group.Add(1)
		go func() {
			defer group.Done()
			errs <- fixture.store().MarkDelivered(context.Background(),
				fixture.notificationID, browser.ID, browser.Generation)
		}()
	}
	group.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("a concurrent ledger write failed: %v", err)
		}
	}
	if got := fixture.ledgerCount(t); got != 1 {
		t.Fatalf("%d ledger rows after %d concurrent writes, want one", got, writers)
	}
}

// A replay writes nothing and keeps the first instant. When somebody was told
// is a fact, and the second attempt did not change it.
func TestPushDeliveryReplayKeepsTheFirstRecordPostgreSQL(t *testing.T) {
	fixture := seedDelivery(t)
	browser := fixture.browser(t, "laptop")

	if err := fixture.store().MarkDelivered(t.Context(),
		fixture.notificationID, browser.ID, browser.Generation); err != nil {
		t.Fatalf("MarkDelivered: %v", err)
	}
	var first string
	if err := fixture.pool.QueryRow(t.Context(), `
		SELECT delivered_at::text FROM chat.notification_push_deliveries
		WHERE notification_id = $1::uuid AND subscription_id = $2::uuid`,
		fixture.notificationID, browser.ID).Scan(&first); err != nil {
		t.Fatalf("read delivered_at: %v", err)
	}

	// A later generation, as a browser that re-subscribed would present.
	if err := fixture.store().MarkDelivered(t.Context(),
		fixture.notificationID, browser.ID, browser.Generation+5); err != nil {
		t.Fatalf("replay: %v", err)
	}

	var second, generation string
	if err := fixture.pool.QueryRow(t.Context(), `
		SELECT delivered_at::text, generation::text
		FROM chat.notification_push_deliveries
		WHERE notification_id = $1::uuid AND subscription_id = $2::uuid`,
		fixture.notificationID, browser.ID).Scan(&second, &generation); err != nil {
		t.Fatalf("re-read delivered_at: %v", err)
	}
	if second != first {
		t.Fatalf("a replay rewrote delivered_at from %s to %s", first, second)
	}
	if got := fixture.ledgerCount(t); got != 1 {
		t.Fatalf("a replay produced %d rows", got)
	}
}

// ---------------------------------------------------------------------------
// The schema
// ---------------------------------------------------------------------------

// Both cascades are the whole retention policy: the outbox's own retention
// takes these rows with it, and a deleted subscription takes its own.
func TestPushDeliveryLedgerCascadesFromBothParentsPostgreSQL(t *testing.T) {
	t.Run("from the notification", func(t *testing.T) {
		fixture := seedDelivery(t)
		browser := fixture.browser(t, "laptop")
		if err := fixture.store().MarkDelivered(t.Context(),
			fixture.notificationID, browser.ID, browser.Generation); err != nil {
			t.Fatalf("MarkDelivered: %v", err)
		}

		execFixture(t, fixture.pool,
			`DELETE FROM chat.notification_outbox WHERE id = $1::uuid`, fixture.notificationID)

		if got := fixture.ledgerCount(t); got != 0 {
			t.Fatalf("%d ledger rows survived the notification", got)
		}
	})

	t.Run("from the subscription", func(t *testing.T) {
		fixture := seedDelivery(t)
		browser := fixture.browser(t, "laptop")
		if err := fixture.store().MarkDelivered(t.Context(),
			fixture.notificationID, browser.ID, browser.Generation); err != nil {
			t.Fatalf("MarkDelivered: %v", err)
		}

		execFixture(t, fixture.pool,
			`DELETE FROM chat.push_subscriptions WHERE id = $1::uuid`, browser.ID)

		if got := fixture.ledgerCount(t); got != 0 {
			t.Fatalf("%d ledger rows survived the subscription", got)
		}
	})
}

// The table refuses a row that names nothing real, and refuses a generation
// that cannot have existed.
func TestPushDeliveryLedgerRefusesIncoherentRowsPostgreSQL(t *testing.T) {
	fixture := seedDelivery(t)
	browser := fixture.browser(t, "laptop")

	cases := map[string]struct {
		notification string
		subscription string
		generation   int64
	}{
		"a notification that does not exist": {
			notification: "00000000-0000-0000-0000-0000000000ff",
			subscription: browser.ID, generation: 1,
		},
		"a subscription that does not exist": {
			notification: fixture.notificationID,
			subscription: "00000000-0000-0000-0000-0000000000ff", generation: 1,
		},
		"a generation below the first": {
			notification: fixture.notificationID,
			subscription: browser.ID, generation: 0,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := fixture.pool.Exec(t.Context(), `
				INSERT INTO chat.notification_push_deliveries
					(notification_id, subscription_id, generation)
				VALUES ($1::uuid, $2::uuid, $3::bigint)`,
				tc.notification, tc.subscription, tc.generation)
			if err == nil {
				t.Fatal("the database accepted an incoherent ledger row")
			}
		})
	}
}

// The migration applies with the index the cascade needs, and rolls back
// cleanly.
func TestPushDeliveryLedgerMigrationRoundTripPostgreSQL(t *testing.T) {
	pool := newNotificationTestPool(t)

	var indexes []string
	rows, err := pool.Query(t.Context(), `
		SELECT indexname FROM pg_indexes
		WHERE schemaname = 'chat' AND tablename = 'notification_push_deliveries'
		ORDER BY indexname`)
	if err != nil {
		t.Fatalf("list indexes: %v", err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan index: %v", err)
		}
		indexes = append(indexes, name)
	}
	rows.Close()

	joined := strings.Join(indexes, " ")
	if !strings.Contains(joined, "notification_push_deliveries_pkey") {
		t.Fatalf("the primary key is missing: %v", indexes)
	}
	if !strings.Contains(joined, "idx_notification_push_deliveries_subscription") {
		t.Fatalf("the subscription index is missing: %v", indexes)
	}

	// A savepoint, so the rollback is proved without leaving the shared test
	// database without the table the rest of the suite needs.
	transaction, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = transaction.Rollback(context.Background()) }()

	if _, err := transaction.Exec(t.Context(),
		`DROP TABLE IF EXISTS chat.notification_push_deliveries`); err != nil {
		t.Fatalf("down migration failed: %v", err)
	}
	var remaining int
	if err := transaction.QueryRow(t.Context(), `
		SELECT count(*) FROM pg_indexes
		WHERE schemaname = 'chat' AND tablename = 'notification_push_deliveries'`).
		Scan(&remaining); err != nil {
		t.Fatalf("count indexes: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("%d indexes survived the rollback", remaining)
	}
}

// ---------------------------------------------------------------------------
// Why a compare-and-set matched nothing (issue #746)
// ---------------------------------------------------------------------------

// The race the finding describes, against a real PostgreSQL.
//
// The delivery layer captures generation 1, the browser re-registers, and the
// push service answers 410 about the endpoint generation 1 held. The classified
// compare-and-set has to say "superseded" and not "retired": the subscription is
// active on generation 2 with an endpoint that has had nothing, and treating the
// stale 410 as a retirement is what silently lost that notification.
func TestPushDeliveryStale410DoesNotRetireARotatedSubscriptionPostgreSQL(t *testing.T) {
	fixture := seedDelivery(t)
	original := fixture.browser(t, "laptop")

	// The browser re-registers with a different endpoint: same subscription id,
	// next generation, still active.
	rotated, err := storage.NewPGXPushSubscriptionStore(fixture.pool).Upsert(
		t.Context(), fixture.principal,
		registrationFor("laptop", "rotated-"+fixture.notificationID))
	if err != nil {
		t.Fatalf("re-register: %v", err)
	}
	if rotated.ID != original.ID {
		t.Fatalf("rotation changed the subscription id: %s then %s", original.ID, rotated.ID)
	}
	if rotated.Generation <= original.Generation {
		t.Fatalf("generation = %d, want it past %d", rotated.Generation, original.Generation)
	}

	// The 410 arrives for the generation the attempt was made against.
	application, err := fixture.store().RecordDelivery(t.Context(),
		original.ID, original.Generation,
		domain.DeliveryResult{Outcome: domain.OutcomeInvalidated, Reason: domain.ReasonGone})
	if err != nil {
		t.Fatalf("RecordDelivery: %v", err)
	}
	if application != domain.ApplicationSuperseded {
		t.Fatalf("application = %v, want superseded", application)
	}
	if !application.Deliverable() {
		t.Fatal("a rotated subscription was not reported as still deliverable")
	}

	// The row is untouched, and the fan-out still offers the new endpoint.
	var status string
	if err := fixture.pool.QueryRow(t.Context(),
		`SELECT status FROM chat.push_subscriptions WHERE id = $1::uuid`,
		original.ID).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != string(domain.StatusActive) {
		t.Fatalf("status = %q, want the rotated subscription left active", status)
	}
	targets := fixture.deliverable(t)
	if len(targets) != 1 || targets[0].Generation != rotated.Generation {
		t.Fatalf("fan-out returned %+v, want the new generation", targets)
	}
}

// The other three answers a compare-and-set can give, each proved against the
// database rather than against a fake that reimplements the predicate.
func TestPushDeliveryClassifiesEveryCompareAndSetOutcomePostgreSQL(t *testing.T) {
	t.Run("expected generation and active applies", func(t *testing.T) {
		fixture := seedDelivery(t)
		browser := fixture.browser(t, "laptop")

		if got := recordGone(t, fixture, browser.ID, browser.Generation); got != domain.ApplicationRecorded {
			t.Fatalf("application = %v, want recorded", got)
		}
	})

	t.Run("already retired is inactive", func(t *testing.T) {
		fixture := seedDelivery(t)
		browser := fixture.browser(t, "laptop")

		if got := recordGone(t, fixture, browser.ID, browser.Generation); got != domain.ApplicationRecorded {
			t.Fatalf("the first retirement reported %v", got)
		}
		// A second 410 for the same generation finds a row that is no longer
		// active: nothing to retire, and nothing live behind it either.
		if got := recordGone(t, fixture, browser.ID, browser.Generation); got != domain.ApplicationInactive {
			t.Fatalf("application = %v, want inactive", got)
		}
	})

	t.Run("disabled by its owner is inactive", func(t *testing.T) {
		fixture := seedDelivery(t)
		browser := fixture.browser(t, "laptop")

		if err := storage.NewPGXPushSubscriptionStore(fixture.pool).
			Disable(t.Context(), fixture.principal, browser.ID); err != nil {
			t.Fatalf("Disable: %v", err)
		}
		if got := recordGone(t, fixture, browser.ID, browser.Generation); got != domain.ApplicationInactive {
			t.Fatalf("application = %v, want inactive", got)
		}
	})

	t.Run("deleted is missing", func(t *testing.T) {
		fixture := seedDelivery(t)
		browser := fixture.browser(t, "laptop")

		execFixture(t, fixture.pool,
			`DELETE FROM chat.push_subscriptions WHERE id = $1::uuid`, browser.ID)

		if got := recordGone(t, fixture, browser.ID, browser.Generation); got != domain.ApplicationMissing {
			t.Fatalf("application = %v, want missing", got)
		}
	})
}

// A success or a transient failure is classified by the same statement shape,
// so the generation predicate cannot be lost from one of them without being
// lost from all.
func TestPushDeliveryEveryOutcomeIsClassifiedPostgreSQL(t *testing.T) {
	fixture := seedDelivery(t)
	browser := fixture.browser(t, "laptop")

	for name, result := range map[string]domain.DeliveryResult{
		"success":   {Outcome: domain.OutcomeSucceeded},
		"transient": {Outcome: domain.OutcomeTransient},
	} {
		t.Run(name, func(t *testing.T) {
			applied, err := fixture.store().RecordDelivery(t.Context(),
				browser.ID, browser.Generation, result)
			if err != nil {
				t.Fatalf("RecordDelivery: %v", err)
			}
			if applied != domain.ApplicationRecorded {
				t.Fatalf("application = %v, want recorded", applied)
			}

			// The same write against a generation the row never had.
			stale, err := fixture.store().RecordDelivery(t.Context(),
				browser.ID, browser.Generation+99, result)
			if err != nil {
				t.Fatalf("RecordDelivery: %v", err)
			}
			if stale != domain.ApplicationSuperseded {
				t.Fatalf("application = %v, want superseded", stale)
			}
		})
	}
}

// recordGone applies a 410 to one generation and returns the classification.
func recordGone(
	t *testing.T, fixture *deliveryFixture, subscriptionID string, generation int64,
) domain.DeliveryApplication {
	t.Helper()
	application, err := fixture.store().RecordDelivery(t.Context(), subscriptionID, generation,
		domain.DeliveryResult{Outcome: domain.OutcomeInvalidated, Reason: domain.ReasonGone})
	if err != nil {
		t.Fatalf("RecordDelivery: %v", err)
	}
	return application
}
