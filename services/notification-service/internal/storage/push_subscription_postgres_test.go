package storage_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/services/notification-service/internal/domain"
	"github.com/nicrepository/nchat/services/notification-service/internal/storage"
)

// Issue #745: the push subscription contract against a real PostgreSQL.
//
// None of this can be proved with a mock, and that is why the file exists. What
// is under test is behaviour the database owns and a fake would have to
// reimplement — and would then be testing itself:
//
//   - two unique indexes deciding what "the same subscription" and "the same
//     endpoint" mean, under concurrency, without a read-then-write anywhere;
//   - an ON CONFLICT that updates one identity and refuses the other, so an
//     endpoint can never change owner;
//   - the lifecycle CHECK that ties status, invalidated_at and the reason
//     together, so "not deliverable" can only be recorded with when and why;
//   - the migration itself: that it applies, constrains and rolls back.
//
// Opt-in like its neighbours: needs NOTIFICATION_TEST_DATABASE_URL against a
// _test database carrying the real migrations.

// The workspace migration 000001 seeds.
const pushWorkspace = "00000000-0000-0000-0000-000000000001"

// pushFixture is one workspace member and the rows they own.
type pushFixture struct {
	pool  *pgxpool.Pool
	users []string
}

func newPushFixture(t *testing.T) *pushFixture {
	t.Helper()
	pool := newNotificationTestPool(t)
	fixture := &pushFixture{pool: pool}
	t.Cleanup(func() {
		// Subscriptions cascade from the user, so removing the users is enough.
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM auth.users WHERE id = ANY($1::uuid[])`, fixture.users)
	})
	return fixture
}

// member creates an active user with an active membership of the seeded
// workspace, and returns the principal a request from them resolves to.
func (f *pushFixture) member(t *testing.T) domain.Principal {
	t.Helper()
	var userID string
	if err := f.pool.QueryRow(t.Context(), `
		INSERT INTO auth.users (email, display_name)
		VALUES ('push-745-' || gen_random_uuid()::text || '@e.test', 'Push member')
		RETURNING id::text`).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	f.users = append(f.users, userID)
	if _, err := f.pool.Exec(t.Context(), `
		INSERT INTO chat.workspace_members (workspace_id, user_id, status)
		VALUES ($1::uuid, $2::uuid, 'active')
		ON CONFLICT DO NOTHING`, pushWorkspace, userID); err != nil {
		t.Fatalf("seed membership: %v", err)
	}
	return domain.Principal{UserID: userID, WorkspaceID: pushWorkspace}
}

func (f *pushFixture) store() *storage.PGXPushSubscriptionStore {
	return storage.NewPGXPushSubscriptionStore(f.pool)
}

// registration builds a distinct browser subscription. The keys are structurally
// sized but obviously synthetic: no fixture in this repository should read as a
// real Web Push credential.
func registrationFor(device, endpointSuffix string) domain.Registration {
	return domain.Registration{
		DeviceID: device,
		Endpoint: "https://push.example.test/s/" + endpointSuffix,
		P256dh:   strings.Repeat("Pp", 43),
		Auth:     strings.Repeat("Aa", 11),
	}
}

// The store never inspects the keys — the domain validated them before it — so
// these only have to be distinguishable and of a plausible length.
func rotatedP256dh() string { return strings.Repeat("Qq", 43) }
func rotatedAuth() string   { return strings.Repeat("Bb", 11) }

// operational reads the columns the reconcile projection deliberately does not
// return, straight from the table.
type operationalState struct {
	status       string
	generation   int64
	failureCount int
	lastSuccess  *time.Time
	invalidated  *time.Time
	reason       *string
	endpoint     string
}

func (f *pushFixture) operational(t *testing.T, id string) operationalState {
	t.Helper()
	var state operationalState
	if err := f.pool.QueryRow(t.Context(), `
		SELECT status, generation, failure_count, last_success_at, invalidated_at,
		       invalidation_reason, endpoint
		FROM chat.push_subscriptions WHERE id = $1::uuid`, id).
		Scan(&state.status, &state.generation, &state.failureCount, &state.lastSuccess,
			&state.invalidated, &state.reason, &state.endpoint); err != nil {
		t.Fatalf("read operational state: %v", err)
	}
	return state
}

func (f *pushFixture) countFor(t *testing.T, principal domain.Principal) int {
	t.Helper()
	var count int
	if err := f.pool.QueryRow(t.Context(), `
		SELECT count(*) FROM chat.push_subscriptions
		WHERE workspace_id = $1::uuid AND user_id = $2::uuid`,
		principal.WorkspaceID, principal.UserID).Scan(&count); err != nil {
		t.Fatalf("count subscriptions: %v", err)
	}
	return count
}

// ── registration and identity ────────────────────────────────────────────────

func TestPushSubscriptionRegistersPostgreSQL(t *testing.T) {
	fixture := newPushFixture(t)
	owner := fixture.member(t)

	subscription, err := fixture.store().
		Upsert(t.Context(), owner, registrationFor("device-a", "aaa"))
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if subscription.Status != domain.StatusActive || subscription.DeviceID != "device-a" {
		t.Fatalf("subscription = %+v", subscription)
	}
	if state := fixture.operational(t, subscription.ID); state.failureCount != 0 ||
		state.invalidated != nil || state.reason != nil {
		t.Fatalf("a fresh subscription is not clean: %+v", state)
	}
}

// The same request twice is the same row. A client retrying a registration whose
// response it never saw must not end up with two.
func TestPushSubscriptionRetryIsIdempotentPostgreSQL(t *testing.T) {
	fixture := newPushFixture(t)
	owner := fixture.member(t)
	registration := registrationFor("device-a", "aaa")

	first, err := fixture.store().Upsert(t.Context(), owner, registration)
	if err != nil {
		t.Fatalf("first Upsert: %v", err)
	}
	second, err := fixture.store().Upsert(t.Context(), owner, registration)
	if err != nil {
		t.Fatalf("second Upsert: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("retry created a second subscription: %q then %q", first.ID, second.ID)
	}
	if count := fixture.countFor(t, owner); count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
}

// A browser that rotates its subscription presents the same device with a new
// endpoint. The row has to follow it, or the server would keep pushing to an
// endpoint the browser has abandoned.
func TestPushSubscriptionReRegistersTheSameDevicePostgreSQL(t *testing.T) {
	fixture := newPushFixture(t)
	owner := fixture.member(t)

	first, err := fixture.store().Upsert(t.Context(), owner, registrationFor("device-a", "old"))
	if err != nil {
		t.Fatalf("first Upsert: %v", err)
	}
	second, err := fixture.store().Upsert(t.Context(), owner, registrationFor("device-a", "new"))
	if err != nil {
		t.Fatalf("second Upsert: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("re-registration created a new row: %q then %q", first.ID, second.ID)
	}
	if state := fixture.operational(t, second.ID); !strings.HasSuffix(state.endpoint, "/new") {
		t.Fatalf("endpoint was not updated: %q", state.endpoint)
	}
	if count := fixture.countFor(t, owner); count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
}

// One person, several browsers and several devices, all at once. This is the
// requirement that rules out "one subscription per user": modelling it that way
// would silently unsubscribe a laptop the moment the phone registered.
func TestPushSubscriptionKeepsSeveralDevicesPostgreSQL(t *testing.T) {
	fixture := newPushFixture(t)
	owner := fixture.member(t)

	for _, device := range []string{"laptop-firefox", "laptop-chrome", "phone-chrome"} {
		if _, err := fixture.store().
			Upsert(t.Context(), owner, registrationFor(device, device)); err != nil {
			t.Fatalf("Upsert %s: %v", device, err)
		}
	}
	subscriptions, err := fixture.store().List(t.Context(), owner)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(subscriptions) != 3 {
		t.Fatalf("List returned %d subscriptions, want 3", len(subscriptions))
	}
}

// ── ownership ────────────────────────────────────────────────────────────────

// The property the whole design turns on. A Web Push endpoint is a capability
// URL: whoever holds the row holds the right to push to that browser. A second
// user presenting it must be refused, and the first user's row must be exactly
// as it was.
func TestPushSubscriptionEndpointCannotChangeOwnerPostgreSQL(t *testing.T) {
	fixture := newPushFixture(t)
	owner := fixture.member(t)
	attacker := fixture.member(t)
	registration := registrationFor("device-a", "shared")

	original, err := fixture.store().Upsert(t.Context(), owner, registration)
	if err != nil {
		t.Fatalf("owner Upsert: %v", err)
	}

	_, err = fixture.store().Upsert(t.Context(), attacker, registrationFor("device-b", "shared"))
	if !errors.Is(err, domain.ErrEndpointConflict) {
		t.Fatalf("attacker Upsert = %v, want ErrEndpointConflict", err)
	}
	if count := fixture.countFor(t, attacker); count != 0 {
		t.Fatalf("the attacker owns %d subscriptions, want 0", count)
	}

	var ownerID string
	if err := fixture.pool.QueryRow(t.Context(),
		`SELECT user_id::text FROM chat.push_subscriptions WHERE id = $1::uuid`,
		original.ID).Scan(&ownerID); err != nil {
		t.Fatalf("read owner: %v", err)
	}
	if ownerID != owner.UserID {
		t.Fatalf("the endpoint changed hands: owner is now %q", ownerID)
	}
}

// The same refusal across a workspace boundary. Isolation has to hold for the
// same person acting in a different tenant, not only for a different person.
func TestPushSubscriptionEndpointIsIsolatedAcrossWorkspacesPostgreSQL(t *testing.T) {
	fixture := newPushFixture(t)
	owner := fixture.member(t)
	elsewhere := domain.Principal{UserID: owner.UserID, WorkspaceID: otherWorkspaceID(t, fixture)}
	registration := registrationFor("device-a", "shared")

	if _, err := fixture.store().Upsert(t.Context(), owner, registration); err != nil {
		t.Fatalf("owner Upsert: %v", err)
	}
	_, err := fixture.store().Upsert(t.Context(), elsewhere, registration)
	if !errors.Is(err, domain.ErrEndpointConflict) {
		t.Fatalf("cross-workspace Upsert = %v, want ErrEndpointConflict", err)
	}
	if count := fixture.countFor(t, elsewhere); count != 0 {
		t.Fatalf("the other workspace owns %d subscriptions, want 0", count)
	}
}

// otherWorkspaceID creates a second workspace so isolation can be tested against
// a real tenant boundary rather than an invented identifier.
func otherWorkspaceID(t *testing.T, fixture *pushFixture) string {
	t.Helper()
	var id string
	slug := "push-745-" + strings.ReplaceAll(time.Now().Format("150405.000000000"), ".", "")
	// The workspace and its #geral channel go in one transaction: migration
	// 000002 installs a deferred constraint trigger requiring every workspace to
	// hold exactly one active public general channel, so a bare INSERT would be
	// refused at COMMIT.
	transaction, err := fixture.pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin workspace fixture: %v", err)
	}
	if err := transaction.QueryRow(t.Context(), `
		INSERT INTO chat.workspaces (slug, name, status)
		VALUES ($1, 'Push 745 workspace', 'active')
		RETURNING id::text`, slug).Scan(&id); err != nil {
		_ = transaction.Rollback(t.Context())
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := transaction.Exec(t.Context(), `
		INSERT INTO chat.channels (workspace_id, slug, display_name, type, status, is_general)
		VALUES ($1::uuid, 'geral', 'Geral', 'public', 'active', true)`, id); err != nil {
		_ = transaction.Rollback(t.Context())
		t.Fatalf("seed general channel: %v", err)
	}
	if err := transaction.Commit(t.Context()); err != nil {
		t.Fatalf("commit workspace fixture: %v", err)
	}
	t.Cleanup(func() {
		_, _ = fixture.pool.Exec(context.Background(),
			`DELETE FROM chat.workspaces WHERE id = $1::uuid`, id)
	})
	return id
}

// Reading and disabling are scoped by the principal, so another user's
// identifier reaches nothing. Same identifier, different caller, no effect.
func TestPushSubscriptionIsUnreachableByAnotherUserPostgreSQL(t *testing.T) {
	fixture := newPushFixture(t)
	owner := fixture.member(t)
	other := fixture.member(t)

	subscription, err := fixture.store().Upsert(t.Context(), owner, registrationFor("device-a", "aaa"))
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	listed, err := fixture.store().List(t.Context(), other)
	if err != nil || len(listed) != 0 {
		t.Fatalf("List for another user = %v, %v", listed, err)
	}
	if err := fixture.store().Disable(t.Context(), other, subscription.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Disable by another user = %v, want ErrNotFound", err)
	}
	if state := fixture.operational(t, subscription.ID); state.status != string(domain.StatusActive) {
		t.Fatalf("the owner's subscription was changed: %+v", state)
	}
}

// ── concurrency ──────────────────────────────────────────────────────────────

// Two registrations of one logical identity, racing. The unique index serialises
// them, so exactly one row exists afterwards and both callers see the same one.
// There is no read-then-write in the statement, so there is no window to lose.
func TestPushSubscriptionConcurrentRegistrationStaysSinglePostgreSQL(t *testing.T) {
	fixture := newPushFixture(t)
	owner := fixture.member(t)
	registration := registrationFor("device-a", "aaa")

	const racers = 8
	results := make([]domain.PushSubscription, racers)
	failures := make([]error, racers)
	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	for i := 0; i < racers; i++ {
		done.Add(1)
		go func(index int) {
			defer done.Done()
			start.Wait()
			results[index], failures[index] = fixture.store().
				Upsert(context.Background(), owner, registration)
		}(i)
	}
	start.Done()
	done.Wait()

	for index, err := range failures {
		if err != nil {
			t.Fatalf("racer %d: %v", index, err)
		}
		if results[index].ID != results[0].ID {
			t.Fatalf("racer %d got a different subscription: %q vs %q",
				index, results[index].ID, results[0].ID)
		}
	}
	if count := fixture.countFor(t, owner); count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
}

// Two different users racing for one endpoint. Exactly one wins, the other is
// refused, and no ownership moves — which is the same guarantee as the
// sequential case, held under contention.
func TestPushSubscriptionConcurrentEndpointClaimHasOneWinnerPostgreSQL(t *testing.T) {
	fixture := newPushFixture(t)
	first := fixture.member(t)
	second := fixture.member(t)
	registration := registrationFor("device-a", "contested")

	var start sync.WaitGroup
	var done sync.WaitGroup
	errs := make([]error, 2)
	start.Add(1)
	for index, principal := range []domain.Principal{first, second} {
		done.Add(1)
		go func(index int, principal domain.Principal) {
			defer done.Done()
			start.Wait()
			_, errs[index] = fixture.store().Upsert(context.Background(), principal, registration)
		}(index, principal)
	}
	start.Done()
	done.Wait()

	winners := 0
	for _, err := range errs {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, domain.ErrEndpointConflict):
		default:
			t.Fatalf("unexpected failure: %v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("%d racers won the endpoint, want exactly 1", winners)
	}
	if fixture.countFor(t, first)+fixture.countFor(t, second) != 1 {
		t.Fatal("the contested endpoint produced more than one subscription")
	}
}

// Concurrent disable and re-registration. Whichever order the database picks,
// the row has to end up in one of the two coherent states and never in a mixture
// the lifecycle CHECK would refuse.
func TestPushSubscriptionConcurrentDisableAndRegisterStaysCoherentPostgreSQL(t *testing.T) {
	fixture := newPushFixture(t)
	owner := fixture.member(t)
	registration := registrationFor("device-a", "aaa")

	subscription, err := fixture.store().Upsert(t.Context(), owner, registration)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	var start, done sync.WaitGroup
	start.Add(1)
	done.Add(2)
	var disableErr, upsertErr error
	go func() {
		defer done.Done()
		start.Wait()
		disableErr = fixture.store().Disable(context.Background(), owner, subscription.ID)
	}()
	go func() {
		defer done.Done()
		start.Wait()
		_, upsertErr = fixture.store().Upsert(context.Background(), owner, registration)
	}()
	start.Done()
	done.Wait()

	if disableErr != nil || upsertErr != nil {
		t.Fatalf("disable = %v, upsert = %v", disableErr, upsertErr)
	}
	assertCoherentLifecycle(t, fixture.operational(t, subscription.ID))
	if count := fixture.countFor(t, owner); count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
}

// assertCoherentLifecycle holds the lifecycle CHECK to the row: either active
// with no invalidation recorded, or not active with both halves of one. A
// mixture is the state the constraint exists to make unreachable.
func assertCoherentLifecycle(t *testing.T, state operationalState) {
	t.Helper()
	active := state.status == string(domain.StatusActive) &&
		state.invalidated == nil && state.reason == nil
	disabled := state.status == string(domain.StatusDisabled) &&
		state.invalidated != nil && state.reason != nil
	if !active && !disabled {
		t.Fatalf("the row is in an incoherent state: %+v", state)
	}
}

// ── delivery outcomes ────────────────────────────────────────────────────────

// A success clears the failure history and stamps the moment. That timestamp is
// what a future cleanup and a future diagnosis both read.
func TestPushSubscriptionSuccessResetsFailuresPostgreSQL(t *testing.T) {
	fixture := newPushFixture(t)
	owner := fixture.member(t)
	subscription := mustRegister(t, fixture, owner, "device-a", "aaa")

	recordStatus(t, fixture, subscription.ID, subscription.Generation, 429)
	recordStatus(t, fixture, subscription.ID, subscription.Generation, 500)
	if state := fixture.operational(t, subscription.ID); state.failureCount != 2 {
		t.Fatalf("failure_count = %d, want 2", state.failureCount)
	}

	recordStatus(t, fixture, subscription.ID, subscription.Generation, 201)
	state := fixture.operational(t, subscription.ID)
	if state.failureCount != 0 || state.lastSuccess == nil ||
		state.status != string(domain.StatusActive) {
		t.Fatalf("after a success: %+v", state)
	}
}

// The two verdicts that retire a subscription, and the reason each records. This
// is the whole of what may make a subscription permanently undeliverable.
func TestPushSubscriptionGoneStatusesInvalidatePostgreSQL(t *testing.T) {
	cases := map[int]domain.InvalidationReason{
		404: domain.ReasonNotFound,
		410: domain.ReasonGone,
	}
	for status, reason := range cases {
		fixture := newPushFixture(t)
		owner := fixture.member(t)
		subscription := mustRegister(t, fixture, owner, "device-a", strconv.Itoa(status))

		recordStatus(t, fixture, subscription.ID, subscription.Generation, status)
		state := fixture.operational(t, subscription.ID)
		if state.status != string(domain.StatusInvalid) || state.invalidated == nil ||
			state.reason == nil || *state.reason != string(reason) {
			t.Fatalf("status %d left %+v", status, state)
		}
	}
}

// Everything else leaves the subscription deliverable. A subscription discarded
// on a rate limit, a provider outage or a timeout is a real person who silently
// stops being notified, and no response distinguishes those from "your endpoint
// is dead" except the two codes above.
func TestPushSubscriptionTransientFailuresDoNotInvalidatePostgreSQL(t *testing.T) {
	for _, status := range []int{429, 500, 502, 503, 504, 408, 0} {
		fixture := newPushFixture(t)
		owner := fixture.member(t)
		subscription := mustRegister(t, fixture, owner, "device-a", strconv.Itoa(status))

		recordStatus(t, fixture, subscription.ID, subscription.Generation, status)
		state := fixture.operational(t, subscription.ID)
		if state.status != string(domain.StatusActive) {
			t.Fatalf("status %d retired the subscription: %+v", status, state)
		}
		if state.failureCount != 1 || state.invalidated != nil || state.reason != nil {
			t.Fatalf("status %d left %+v", status, state)
		}
	}
}

// Invalidating one device leaves the person's other devices alone. A single
// dead browser must never cost somebody every notification they get.
func TestPushSubscriptionInvalidationIsPerSubscriptionPostgreSQL(t *testing.T) {
	fixture := newPushFixture(t)
	owner := fixture.member(t)
	dead := mustRegister(t, fixture, owner, "device-dead", "dead")
	alive := mustRegister(t, fixture, owner, "device-alive", "alive")

	recordStatus(t, fixture, dead.ID, dead.Generation, 410)

	if state := fixture.operational(t, dead.ID); state.status != string(domain.StatusInvalid) {
		t.Fatalf("the dead subscription is %+v", state)
	}
	if state := fixture.operational(t, alive.ID); state.status != string(domain.StatusActive) ||
		state.invalidated != nil {
		t.Fatalf("the other subscription was affected: %+v", state)
	}
}

// A result arriving after the subscription moved on changes nothing. A stale
// success must not revive a subscription its owner disabled, and a second 410
// must not overwrite the instant and reason the first recorded.
func TestPushSubscriptionLateResultsCannotReviveOrRewritePostgreSQL(t *testing.T) {
	fixture := newPushFixture(t)
	owner := fixture.member(t)
	subscription := mustRegister(t, fixture, owner, "device-a", "aaa")

	recordStatus(t, fixture, subscription.ID, subscription.Generation, 410)
	retired := fixture.operational(t, subscription.ID)

	for _, late := range []int{200, 410, 429} {
		if recordStatusApplied(t, fixture, subscription.ID, subscription.Generation, late) {
			t.Fatalf("late %d applied to a retired subscription", late)
		}
	}
	state := fixture.operational(t, subscription.ID)
	if state.status != retired.status || *state.reason != *retired.reason ||
		!state.invalidated.Equal(*retired.invalidated) || state.lastSuccess != nil {
		t.Fatalf("a late result changed the row: %+v, was %+v", state, retired)
	}
}

// ── generation ───────────────────────────────────────────────────────────────
//
// The defect this whole column exists for: a delivery attempt and its answer are
// not simultaneous. An attempt starts against the endpoint the row holds; while
// it is in flight the browser re-registers and replaces that endpoint; the
// answer then describes an endpoint the row no longer has. Applying it would
// retire a subscription that works, or write success and failure history
// belonging to something that is gone.

func TestPushSubscriptionStartsAtAValidGenerationPostgreSQL(t *testing.T) {
	fixture := newPushFixture(t)
	owner := fixture.member(t)

	subscription := mustRegister(t, fixture, owner, "device-a", "aaa")
	if subscription.Generation < 1 {
		t.Fatalf("Generation = %d, want at least 1", subscription.Generation)
	}
	if state := fixture.operational(t, subscription.ID); state.generation != subscription.Generation {
		t.Fatalf("stored generation %d, returned %d", state.generation, subscription.Generation)
	}
}

// An identical retry of an active subscription is the same generation. It has to
// be: a client that retries a request whose response it never saw would
// otherwise invalidate every attempt already in flight for the endpoint it just
// re-confirmed.
func TestPushSubscriptionIdenticalRetryKeepsTheGenerationPostgreSQL(t *testing.T) {
	fixture := newPushFixture(t)
	owner := fixture.member(t)
	registration := registrationFor("device-a", "aaa")

	first, err := fixture.store().Upsert(t.Context(), owner, registration)
	if err != nil {
		t.Fatalf("first Upsert: %v", err)
	}
	for attempt := 0; attempt < 3; attempt++ {
		again, err := fixture.store().Upsert(t.Context(), owner, registration)
		if err != nil {
			t.Fatalf("retry %d: %v", attempt, err)
		}
		if again.ID != first.ID || again.Generation != first.Generation {
			t.Fatalf("retry %d moved the subscription: %+v, was %+v", attempt, again, first)
		}
	}
}

// Each of the three things that define an endpoint lifetime advances the
// generation on its own. Any one of them left out of the rotation predicate
// would let a late answer reach a subscription that is no longer what the
// attempt was made against.
func TestPushSubscriptionRotationAdvancesTheGenerationPostgreSQL(t *testing.T) {
	rotations := map[string]func(*domain.Registration){
		"endpoint": func(r *domain.Registration) { r.Endpoint += "-rotated" },
		"p256dh":   func(r *domain.Registration) { r.P256dh = rotatedP256dh() },
		"auth":     func(r *domain.Registration) { r.Auth = rotatedAuth() },
	}
	for name, rotate := range rotations {
		t.Run(name, func(t *testing.T) { assertRotationAdvancesTheGeneration(t, name, rotate) })
	}
}

func assertRotationAdvancesTheGeneration(
	t *testing.T, name string, rotate func(*domain.Registration),
) {
	t.Helper()
	fixture := newPushFixture(t)
	owner := fixture.member(t)
	registration := registrationFor("device-a", name)

	before, err := fixture.store().Upsert(t.Context(), owner, registration)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	rotate(&registration)
	after, err := fixture.store().Upsert(t.Context(), owner, registration)
	if err != nil {
		t.Fatalf("rotate %s: %v", name, err)
	}
	if after.ID != before.ID {
		t.Fatalf("rotation created a new row: %q, was %q", after.ID, before.ID)
	}
	if after.Generation <= before.Generation {
		t.Fatalf("generation = %d, want greater than %d", after.Generation, before.Generation)
	}
}

// Reactivation is a new lifetime too, even when the bytes are identical: results
// outstanding against the previous one must not land on the revived row. Two
// tests rather than a table, because the two ways a subscription stops being
// active are two different paths through the lifecycle.

func TestPushSubscriptionReactivationAfterDisableAdvancesTheGenerationPostgreSQL(t *testing.T) {
	assertReactivationAdvancesTheGeneration(t, "reactivate-disabled", disableSubscription)
}

func TestPushSubscriptionReactivationAfterRetirementAdvancesTheGenerationPostgreSQL(t *testing.T) {
	assertReactivationAdvancesTheGeneration(t, "reactivate-retired", retireByProvider)
}

// retireSubscription puts a subscription into a state that is not active.
type retireSubscription func(
	*testing.T, *pushFixture, domain.Principal, domain.PushSubscription,
)

func disableSubscription(
	t *testing.T, fixture *pushFixture,
	owner domain.Principal, subscription domain.PushSubscription,
) {
	t.Helper()
	if err := fixture.store().Disable(t.Context(), owner, subscription.ID); err != nil {
		t.Fatalf("Disable: %v", err)
	}
}

func retireByProvider(
	t *testing.T, fixture *pushFixture,
	_ domain.Principal, subscription domain.PushSubscription,
) {
	t.Helper()
	recordStatus(t, fixture, subscription.ID, subscription.Generation, 410)
}

func assertReactivationAdvancesTheGeneration(
	t *testing.T, suffix string, retire retireSubscription,
) {
	t.Helper()
	fixture := newPushFixture(t)
	owner := fixture.member(t)
	registration := registrationFor("device-a", suffix)

	before, err := fixture.store().Upsert(t.Context(), owner, registration)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	retire(t, fixture, owner, before)

	// The very same bytes, re-presented. It is still a new lifetime.
	after, err := fixture.store().Upsert(t.Context(), owner, registration)
	if err != nil {
		t.Fatalf("reactivate: %v", err)
	}
	if after.Generation <= before.Generation {
		t.Fatalf("generation = %d, want greater than %d", after.Generation, before.Generation)
	}
	if after.Status != domain.StatusActive {
		t.Fatalf("status = %q, want active", after.Status)
	}
}

// The case the review named, end to end and for every outcome. An attempt made
// against generation one answers after the browser rotated to generation two;
// none of the answers may touch the row.
func TestPushSubscriptionStaleResultCannotTouchANewGenerationPostgreSQL(t *testing.T) {
	// 410 first: it is the one that would silently unsubscribe a working
	// browser, and the reason the column exists.
	for _, late := range []int{410, 404, 200, 429, 500, 0} {
		assertStaleResultIsInert(t, late)
	}
}

func assertStaleResultIsInert(t *testing.T, late int) {
	t.Helper()
	fixture := newPushFixture(t)
	owner := fixture.member(t)
	registration := registrationFor("device-a", "stale-"+strconv.Itoa(late))

	first, err := fixture.store().Upsert(t.Context(), owner, registration)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	// Give the first generation a history, so a stale write that landed would be
	// visible as more than an unchanged zero.
	recordStatus(t, fixture, first.ID, first.Generation, 200)
	recordStatus(t, fixture, first.ID, first.Generation, 503)

	registration.Endpoint += "-rotated"
	second, err := fixture.store().Upsert(t.Context(), owner, registration)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if second.Generation == first.Generation {
		t.Fatal("the fixture did not actually rotate")
	}
	before := fixture.operational(t, second.ID)

	if recordStatusApplied(t, fixture, first.ID, first.Generation, late) {
		t.Fatalf("a stale %d applied to the generation that replaced it", late)
	}
	assertUntouchedByStaleResult(t, late, fixture.operational(t, second.ID), before)
}

// assertUntouchedByStaleResult checks the whole row and then each field the
// review named, so a failure says which guarantee broke rather than only that
// something moved.
func assertUntouchedByStaleResult(t *testing.T, late int, after, before operationalState) {
	t.Helper()
	if after != before {
		t.Fatalf("stale %d changed the current generation: %+v, was %+v", late, after, before)
	}
	if after.status != string(domain.StatusActive) || after.invalidated != nil ||
		after.reason != nil || after.lastSuccess != nil || after.failureCount != 0 {
		t.Fatalf("stale %d left %+v", late, after)
	}
}

// The other half: a result carrying the current generation still applies. A
// predicate that refused everything would pass every test above and deliver
// nothing.
func TestPushSubscriptionCurrentGenerationStillAppliesPostgreSQL(t *testing.T) {
	fixture := newPushFixture(t)
	owner := fixture.member(t)
	registration := registrationFor("device-a", "current")

	first, err := fixture.store().Upsert(t.Context(), owner, registration)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	registration.Endpoint += "-rotated"
	second, err := fixture.store().Upsert(t.Context(), owner, registration)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}

	recordStatus(t, fixture, second.ID, second.Generation, 200)
	if state := fixture.operational(t, second.ID); state.lastSuccess == nil {
		t.Fatalf("the current generation did not record its success: %+v", state)
	}
	if recordStatusApplied(t, fixture, first.ID, first.Generation, 410) {
		t.Fatal("the superseded generation was still writable")
	}
}

// ── success metadata across generations ──────────────────────────────────────

// A new endpoint starts clean. Inheriting "last delivered successfully at 09:14"
// from the endpoint it replaced would record a success that never happened to
// it, and a failure count that belongs to something else.
func TestPushSubscriptionRotationClearsSuccessHistoryPostgreSQL(t *testing.T) {
	fixture := newPushFixture(t)
	owner := fixture.member(t)
	registration := registrationFor("device-a", "history")

	first, err := fixture.store().Upsert(t.Context(), owner, registration)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	recordStatus(t, fixture, first.ID, first.Generation, 200)
	recordStatus(t, fixture, first.ID, first.Generation, 503)
	established := fixture.operational(t, first.ID)
	if established.lastSuccess == nil || established.failureCount != 1 {
		t.Fatalf("the fixture did not establish a history: %+v", established)
	}

	registration.Endpoint += "-rotated"
	second, err := fixture.store().Upsert(t.Context(), owner, registration)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	state := fixture.operational(t, second.ID)
	if state.lastSuccess != nil {
		t.Fatalf("the new generation inherited a success: %+v", state)
	}
	if state.failureCount != 0 || state.status != string(domain.StatusActive) ||
		state.generation <= established.generation {
		t.Fatalf("after rotation: %+v", state)
	}
}

// An identical retry is not a new endpoint, so the history still describes what
// is on file and is kept. Clearing it here would let a client erase its own
// delivery record by re-registering.
func TestPushSubscriptionIdenticalRetryKeepsSuccessHistoryPostgreSQL(t *testing.T) {
	fixture := newPushFixture(t)
	owner := fixture.member(t)
	registration := registrationFor("device-a", "kept")

	subscription, err := fixture.store().Upsert(t.Context(), owner, registration)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	recordStatus(t, fixture, subscription.ID, subscription.Generation, 200)
	recordStatus(t, fixture, subscription.ID, subscription.Generation, 503)
	established := fixture.operational(t, subscription.ID)

	if _, err := fixture.store().Upsert(t.Context(), owner, registration); err != nil {
		t.Fatalf("retry: %v", err)
	}
	state := fixture.operational(t, subscription.ID)
	if state.lastSuccess == nil || !state.lastSuccess.Equal(*established.lastSuccess) {
		t.Fatalf("the retry moved last_success_at: %+v, was %+v", state, established)
	}
	if state.failureCount != established.failureCount ||
		state.generation != established.generation {
		t.Fatalf("the retry changed the generation's history: %+v, was %+v",
			state, established)
	}
}

// ── disable and retention ────────────────────────────────────────────────────

// Disabling keeps the row. It is what a later diagnosis reads and what a
// controlled cleanup would act on; deleting it the moment somebody switches push
// off would throw both away.
func TestPushSubscriptionDisableRetainsTheRowPostgreSQL(t *testing.T) {
	fixture := newPushFixture(t)
	owner := fixture.member(t)
	subscription := mustRegister(t, fixture, owner, "device-a", "aaa")

	if err := fixture.store().Disable(t.Context(), owner, subscription.ID); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	state := fixture.operational(t, subscription.ID)
	if state.status != string(domain.StatusDisabled) || state.invalidated == nil ||
		state.reason == nil || *state.reason != string(domain.ReasonUserDisabled) {
		t.Fatalf("after Disable: %+v", state)
	}
	// Still visible to its owner, which is what lets a client reconcile: a
	// device it cannot see is one it would never know to register again.
	subscriptions, err := fixture.store().List(t.Context(), owner)
	if err != nil || len(subscriptions) != 1 ||
		subscriptions[0].Status != domain.StatusDisabled {
		t.Fatalf("List = %+v, %v", subscriptions, err)
	}
}

// Disabling a subscription the provider already retired records the owner's
// decision without erasing the diagnosis of why it stopped working.
func TestPushSubscriptionDisablePreservesAProviderVerdictPostgreSQL(t *testing.T) {
	fixture := newPushFixture(t)
	owner := fixture.member(t)
	subscription := mustRegister(t, fixture, owner, "device-a", "aaa")

	recordStatus(t, fixture, subscription.ID, subscription.Generation, 410)
	retired := fixture.operational(t, subscription.ID)

	if err := fixture.store().Disable(t.Context(), owner, subscription.ID); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	state := fixture.operational(t, subscription.ID)
	if state.status != string(domain.StatusDisabled) {
		t.Fatalf("status = %q, want disabled", state.status)
	}
	if *state.reason != string(domain.ReasonGone) ||
		!state.invalidated.Equal(*retired.invalidated) {
		t.Fatalf("the provider verdict was erased: %+v", state)
	}
}

// Re-registering revives a retired subscription. The browser presenting it is
// live evidence the endpoint works — exactly what an invalid row lacks — so
// without this a subscription retired during an outage could never come back.
func TestPushSubscriptionReRegistrationRevivesARetiredRowPostgreSQL(t *testing.T) {
	fixture := newPushFixture(t)
	owner := fixture.member(t)
	registration := registrationFor("device-a", "aaa")
	subscription := mustRegister(t, fixture, owner, "device-a", "aaa")

	recordStatus(t, fixture, subscription.ID, subscription.Generation, 410)
	revived, err := fixture.store().Upsert(t.Context(), owner, registration)
	if err != nil {
		t.Fatalf("re-register: %v", err)
	}
	if revived.ID != subscription.ID {
		t.Fatalf("revival created a new row: %q", revived.ID)
	}
	state := fixture.operational(t, subscription.ID)
	if state.status != string(domain.StatusActive) || state.failureCount != 0 ||
		state.invalidated != nil || state.reason != nil {
		t.Fatalf("after revival: %+v", state)
	}
}

// ── cardinality ──────────────────────────────────────────────────────────────

// There is no ceiling on how many devices one person may register, and this is
// the test that says so. A count-then-insert cap was removed rather than made
// atomic: it was not a requirement, it could not hold under two concurrent
// registrations anyway, and making it hold would have meant serialising every
// registration of one user behind a lock.
func TestPushSubscriptionHasNoDeviceCeilingPostgreSQL(t *testing.T) {
	fixture := newPushFixture(t)
	owner := fixture.member(t)

	const devices = 25
	for i := 0; i < devices; i++ {
		name := "device-" + strconv.Itoa(i)
		if _, err := fixture.store().
			Upsert(t.Context(), owner, registrationFor(name, name)); err != nil {
			t.Fatalf("Upsert %s: %v", name, err)
		}
	}
	if count := fixture.countFor(t, owner); count != devices {
		t.Fatalf("count = %d, want %d", count, devices)
	}
	subscriptions, err := fixture.store().List(t.Context(), owner)
	if err != nil || len(subscriptions) != devices {
		t.Fatalf("List returned %d subscriptions (%v), want %d",
			len(subscriptions), err, devices)
	}
}

// ── schema invariants ────────────────────────────────────────────────────────

// The constraints have to be the database's and not the application's, because
// only the database sees every writer. Each of these is a state no code should
// be able to persist even by mistake.
func TestPushSubscriptionSchemaRefusesIncoherentRowsPostgreSQL(t *testing.T) {
	fixture := newPushFixture(t)
	owner := fixture.member(t)
	subscription := mustRegister(t, fixture, owner, "device-a", "aaa")

	cases := map[string]string{
		"negative failure count": `UPDATE chat.push_subscriptions
			SET failure_count = -1 WHERE id = $1::uuid`,
		// A generation below one is a token no attempt could ever carry, which
		// would make every answer for that row stale for ever.
		"zero generation": `UPDATE chat.push_subscriptions
			SET generation = 0 WHERE id = $1::uuid`,
		"negative generation": `UPDATE chat.push_subscriptions
			SET generation = -1 WHERE id = $1::uuid`,
		"unknown status": `UPDATE chat.push_subscriptions
			SET status = 'zombie' WHERE id = $1::uuid`,
		"retired without an instant": `UPDATE chat.push_subscriptions
			SET status = 'invalid', invalidation_reason = 'gone' WHERE id = $1::uuid`,
		"retired without a reason": `UPDATE chat.push_subscriptions
			SET status = 'invalid', invalidated_at = now() WHERE id = $1::uuid`,
		"active with an invalidation": `UPDATE chat.push_subscriptions
			SET invalidated_at = now(), invalidation_reason = 'gone' WHERE id = $1::uuid`,
		"free-text reason": `UPDATE chat.push_subscriptions
			SET status = 'invalid', invalidated_at = now(),
			    invalidation_reason = 'provider said: token abc' WHERE id = $1::uuid`,
		"empty device id": `UPDATE chat.push_subscriptions
			SET device_id = '' WHERE id = $1::uuid`,
		"empty endpoint": `UPDATE chat.push_subscriptions
			SET endpoint = '' WHERE id = $1::uuid`,
		"empty p256dh": `UPDATE chat.push_subscriptions
			SET p256dh = '' WHERE id = $1::uuid`,
		"empty auth": `UPDATE chat.push_subscriptions
			SET auth = '' WHERE id = $1::uuid`,
		"oversized endpoint": `UPDATE chat.push_subscriptions
			SET endpoint = repeat('x', 2049) WHERE id = $1::uuid`,
	}
	for name, statement := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := fixture.pool.Exec(t.Context(), statement, subscription.ID); err == nil {
				t.Fatalf("the database accepted %q", name)
			}
		})
	}
}

// The indexes the queries depend on, asserted against the live catalogue rather
// than against the migration text: an index that was dropped, renamed or created
// over different columns would leave every statement above still compiling.
func TestPushSubscriptionSchemaCarriesItsIndexesPostgreSQL(t *testing.T) {
	fixture := newPushFixture(t)
	expected := map[string]string{
		"push_subscriptions_device_unique":   "UNIQUE INDEX push_subscriptions_device_unique ON chat.push_subscriptions USING btree (workspace_id, user_id, device_id)",
		"push_subscriptions_endpoint_unique": "UNIQUE INDEX push_subscriptions_endpoint_unique ON chat.push_subscriptions USING btree (endpoint)",
	}
	for name, want := range expected {
		var definition string
		if err := fixture.pool.QueryRow(t.Context(), `
			SELECT indexdef FROM pg_indexes
			WHERE schemaname = 'chat' AND tablename = 'push_subscriptions' AND indexname = $1`,
			name).Scan(&definition); err != nil {
			t.Fatalf("index %s is missing: %v", name, err)
		}
		if !strings.Contains(definition, want) {
			t.Fatalf("index %s = %q, want %q", name, definition, want)
		}
	}
}

// The migration's own round trip. down has to remove exactly what up added, so a
// rollback leaves the schema as it was rather than half of a feature behind.
func TestPushSubscriptionMigrationRoundTripPostgreSQL(t *testing.T) {
	fixture := newPushFixture(t)
	if !tableExists(t, fixture, "push_subscriptions") {
		t.Fatal("the migration did not create chat.push_subscriptions")
	}

	// A savepoint, so the rollback is proved without leaving the shared test
	// database without the table the rest of the suite needs.
	transaction, err := fixture.pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = transaction.Rollback(context.Background()) }()

	// In reverse order, which is the only order a rollback ever runs in.
	// 000046's delivery ledger references this table (issue #746), so its own
	// down migration goes first — exactly as scripts/db/migrate.sh would do it.
	// Dropping this table with CASCADE instead would pass here and quietly take
	// a later migration's table with it in production.
	for _, table := range []string{
		"chat.notification_push_deliveries",
		"chat.push_subscriptions",
	} {
		if _, err := transaction.Exec(t.Context(),
			`DROP TABLE IF EXISTS `+table); err != nil {
			t.Fatalf("down migration failed on %s: %v", table, err)
		}
	}
	var remaining int
	if err := transaction.QueryRow(t.Context(), `
		SELECT count(*) FROM pg_indexes
		WHERE schemaname = 'chat' AND tablename = 'push_subscriptions'`).Scan(&remaining); err != nil {
		t.Fatalf("count indexes: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("%d indexes survived the rollback", remaining)
	}
}

func tableExists(t *testing.T, fixture *pushFixture, name string) bool {
	t.Helper()
	var exists bool
	if err := fixture.pool.QueryRow(t.Context(), `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'chat' AND table_name = $1)`, name).Scan(&exists); err != nil {
		t.Fatalf("check table: %v", err)
	}
	return exists
}

// ── helpers ──────────────────────────────────────────────────────────────────

func mustRegister(
	t *testing.T, fixture *pushFixture, principal domain.Principal, device, endpoint string,
) domain.PushSubscription {
	t.Helper()
	subscription, err := fixture.store().
		Upsert(t.Context(), principal, registrationFor(device, endpoint))
	if err != nil {
		t.Fatalf("register %s: %v", device, err)
	}
	return subscription
}

// recordStatus applies a provider status to one generation and asserts that it
// landed. A test that meant to establish metadata and silently recorded nothing
// would go on to assert against a row it never changed.
func recordStatus(
	t *testing.T, fixture *pushFixture, subscriptionID string, generation int64, status int,
) {
	t.Helper()
	if !recordStatusApplied(t, fixture, subscriptionID, generation, status) {
		t.Fatalf("status %d for generation %d did not apply", status, generation)
	}
}

// recordStatusApplied is the same call without the assertion, for the tests
// whose subject is precisely that a result did not apply.
func recordStatusApplied(
	t *testing.T, fixture *pushFixture, subscriptionID string, generation int64, status int,
) bool {
	t.Helper()
	return recordStatusApplication(t, fixture, subscriptionID, generation, status) ==
		domain.ApplicationRecorded
}

// recordStatusApplication is the same call returning the full classification,
// for the tests whose subject is *why* a result did not apply (issue #746).
func recordStatusApplication(
	t *testing.T, fixture *pushFixture, subscriptionID string, generation int64, status int,
) domain.DeliveryApplication {
	t.Helper()
	application, err := fixture.store().RecordDelivery(t.Context(), subscriptionID, generation,
		domain.ClassifyDeliveryStatus(status))
	if err != nil {
		t.Fatalf("record status %d: %v", status, err)
	}
	return application
}

// ── principal resolution ─────────────────────────────────────────────────────

// The authorisation query, against the real auth and chat schemas. A token is an
// input to it and never its conclusion: the session row has to exist and be
// live, and the caller has to be an active member of an active workspace.

// session creates a live session for a user and returns its identifier.
func (f *pushFixture) session(t *testing.T, userID string) string {
	t.Helper()
	var sessionID string
	if err := f.pool.QueryRow(t.Context(), `
		INSERT INTO auth.user_sessions
			(user_id, refresh_token_hash, idle_expires_at, absolute_expires_at)
		VALUES ($1::uuid, 'push-745-' || gen_random_uuid()::text,
		        now() + interval '1 hour', now() + interval '8 hours')
		RETURNING id::text`, userID).Scan(&sessionID); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	return sessionID
}

func TestPushPrincipalResolvesFromTheSessionPostgreSQL(t *testing.T) {
	fixture := newPushFixture(t)
	member := fixture.member(t)
	sessionID := fixture.session(t, member.UserID)

	principal, err := storage.NewPGXPrincipalResolver(fixture.pool).
		Resolve(t.Context(), member.UserID, sessionID)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if principal.UserID != member.UserID || principal.WorkspaceID != pushWorkspace {
		t.Fatalf("principal = %+v", principal)
	}
}

// A session identifier presented with somebody else's user identifier resolves
// to nothing: the query requires the pair, not either half.
func TestPushPrincipalRefusesAMismatchedSessionPostgreSQL(t *testing.T) {
	fixture := newPushFixture(t)
	member := fixture.member(t)
	other := fixture.member(t)
	sessionID := fixture.session(t, member.UserID)

	if _, err := storage.NewPGXPrincipalResolver(fixture.pool).
		Resolve(t.Context(), other.UserID, sessionID); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("Resolve = %v, want ErrUnauthenticated", err)
	}
}

// Revocation and expiry are facts only the database holds. A perfectly signed
// token whose session carries either is not a caller.
func TestPushPrincipalRefusesARetiredSessionPostgreSQL(t *testing.T) {
	cases := map[string]string{
		"revoked": `UPDATE auth.user_sessions
			SET revoked_at = now(), revoked_reason = 'logout' WHERE id = $1::uuid`,
		"idle expired": `UPDATE auth.user_sessions
			SET idle_expires_at = now() - interval '1 minute' WHERE id = $1::uuid`,
		"absolutely expired": `UPDATE auth.user_sessions
			SET absolute_expires_at = now() - interval '1 minute' WHERE id = $1::uuid`,
	}
	for name, statement := range cases {
		t.Run(name, func(t *testing.T) {
			fixture := newPushFixture(t)
			member := fixture.member(t)
			sessionID := fixture.session(t, member.UserID)
			if _, err := fixture.pool.Exec(t.Context(), statement, sessionID); err != nil {
				t.Fatalf("retire session: %v", err)
			}
			if _, err := storage.NewPGXPrincipalResolver(fixture.pool).
				Resolve(t.Context(), member.UserID, sessionID); !errors.Is(
				err, domain.ErrUnauthenticated) {
				t.Fatalf("Resolve = %v, want ErrUnauthenticated", err)
			}
		})
	}
}

// A live session whose owner is no longer an active member is authenticated and
// unauthorised, and the two answers stay distinguishable.
func TestPushPrincipalRefusesALostMembershipPostgreSQL(t *testing.T) {
	fixture := newPushFixture(t)
	member := fixture.member(t)
	sessionID := fixture.session(t, member.UserID)

	if _, err := fixture.pool.Exec(t.Context(), `
		UPDATE chat.workspace_members SET status = 'left'
		WHERE workspace_id = $1::uuid AND user_id = $2::uuid`,
		pushWorkspace, member.UserID); err != nil {
		t.Fatalf("remove membership: %v", err)
	}
	if _, err := storage.NewPGXPrincipalResolver(fixture.pool).
		Resolve(t.Context(), member.UserID, sessionID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("Resolve = %v, want ErrForbidden", err)
	}
}
