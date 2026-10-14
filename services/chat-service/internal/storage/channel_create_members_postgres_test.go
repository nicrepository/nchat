package storage_test

import (
	"context"
	"errors"
	"maps"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Issue #1025: private-channel creation with its initial members, against a
// real PostgreSQL.
//
// Owner: `Tests / Go Integration` (scripts/ci/go-integration-test.sh), on a
// database of its own named by channelCreationDatabaseVariable. The family
// resets the chat schema like the add-members fixture it builds on, so it must
// never share a database with another suite, and it is not part of the Go
// Coverage profile: the store code it exercises is measured by the pgxmock
// tests in channel_create_members_test.go.
const channelCreationDatabaseVariable = "CHANNEL_CREATION_TEST_DATABASE_URL"

// Extras on top of the add-members fixture: workspace memberships that are not
// active, and a guest, which add-members treats as eligible.
const (
	ccGuest     = "f1000000-0000-4000-8000-0000000000a1"
	ccWMSuspend = "f1000000-0000-4000-8000-0000000000a2"
	ccWMLeft    = "f1000000-0000-4000-8000-0000000000a3"
	ccUnknown   = "f1000000-0000-4000-8000-0000000000af"
	ccHashA     = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	ccHashB     = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func channelCreatePostgres(t *testing.T) (*storage.PGXChannelStore, *pgxpool.Pool, context.Context) {
	t.Helper()
	// The add-members fixture reads CHAT_TEST_DATABASE_URL; point it, for this
	// test only, at the family's own database. Unset means skip, like every
	// opt-in suite, so Go Unit and Go Coverage never run this family.
	dsn := os.Getenv(channelCreationDatabaseVariable)
	if dsn == "" {
		t.Skip(channelCreationDatabaseVariable + " is not set")
	}
	t.Setenv("CHAT_TEST_DATABASE_URL", dsn)
	pool, ctx := addMembersPostgres(t)
	extra := []string{ccGuest, ccWMSuspend, ccWMLeft}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM auth.users WHERE id = ANY($1::uuid[])`, extra)
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO auth.users (id, email, display_name, status) VALUES
			($1, 'gu@example.test', 'Guest', 'active'),
			($2, 'ws@example.test', 'WM Suspended', 'active'),
			($3, 'wl@example.test', 'WM Left', 'active')
		ON CONFLICT (id) DO UPDATE SET status = 'active', deleted_at = NULL`, ccGuest, ccWMSuspend, ccWMLeft); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO chat.workspace_members (workspace_id, user_id, role, status) VALUES
			($1, $2, 'guest', 'active'), ($1, $3, 'member', 'suspended'), ($1, $4, 'member', 'left')`,
		amWS, ccGuest, ccWMSuspend, ccWMLeft); err != nil {
		t.Fatalf("seed workspace members: %v", err)
	}
	return storage.NewPGXChannelStore(pool), pool, ctx
}

func privateCreation(slug string, invitees ...string) storage.CreateChannelInput {
	return storage.CreateChannelInput{
		WorkspaceID: amWS, Slug: slug, DisplayName: slug, Type: domain.ChannelTypePrivate,
		CreatedBy: amActive1, EnsureCreatorMemberRole: domain.ChannelRoleMember,
		InitialMemberIDs: invitees,
	}
}

func withKey(input storage.CreateChannelInput, key, hash string) storage.CreateChannelInput {
	input.IdempotencyKey, input.RequestHash = key, hash
	return input
}

// requireCreation creates a channel that must succeed and must be a new one.
func requireCreation(
	t *testing.T, store *storage.PGXChannelStore, ctx context.Context, input storage.CreateChannelInput,
) storage.CreateChannelResult {
	t.Helper()
	result, err := store.CreateChannelForActiveMember(ctx, input)
	if err != nil {
		t.Fatalf("create %q: %v", input.Slug, err)
	}
	if result.Replayed {
		t.Fatalf("create %q replayed instead of creating", input.Slug)
	}
	return result
}

func channelMemberRoles(t *testing.T, pool *pgxpool.Pool, ctx context.Context, channelID string) map[string]string {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT user_id::text, role FROM chat.channel_members WHERE channel_id = $1`, channelID)
	if err != nil {
		t.Fatalf("read members: %v", err)
	}
	defer rows.Close()
	roles := map[string]string{}
	for rows.Next() {
		var userID, role string
		if err := rows.Scan(&userID, &role); err != nil {
			t.Fatalf("scan member: %v", err)
		}
		roles[userID] = role
	}
	return roles
}

// assertMembership compares the whole roster, roles included, so a missing,
// extra or promoted member all fail the same assertion.
func assertMembership(t *testing.T, pool *pgxpool.Pool, ctx context.Context, channelID string, want map[string]string) {
	t.Helper()
	if got := channelMemberRoles(t, pool, ctx, channelID); !maps.Equal(got, want) {
		t.Fatalf("members = %v, want %v", got, want)
	}
}

// assertVisibility runs the installed chat.channel_visible_to_user for each user.
func assertVisibility(t *testing.T, pool *pgxpool.Pool, ctx context.Context, channelID string, want map[string]bool) {
	t.Helper()
	for user, expected := range want {
		var visible bool
		if err := pool.QueryRow(ctx, `SELECT chat.channel_visible_to_user($1, $2)`, channelID, user).Scan(&visible); err != nil {
			t.Fatalf("visibility of %s: %v", user, err)
		}
		if visible != expected {
			t.Fatalf("visible(%s) = %v, want %v", user, visible, expected)
		}
	}
}

// assertNothingCreated proves a rollback was total: no channel, no membership
// and no claimed idempotency key survive a refused creation.
func assertNothingCreated(t *testing.T, pool *pgxpool.Pool, ctx context.Context, slug string) {
	t.Helper()
	var channels, members, keys int
	if err := pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM chat.channels WHERE workspace_id = $1 AND slug = $2),
		       (SELECT count(*) FROM chat.channel_members cm JOIN chat.channels c ON c.id = cm.channel_id
		         WHERE c.workspace_id = $1 AND c.slug = $2),
		       (SELECT count(*) FROM chat.channel_creation_requests)`, amWS, slug,
	).Scan(&channels, &members, &keys); err != nil {
		t.Fatalf("count leftovers: %v", err)
	}
	if channels != 0 || members != 0 || keys != 0 {
		t.Fatalf("partial creation survived: channels=%d members=%d keys=%d", channels, members, keys)
	}
}

func countChannelsWithSlug(t *testing.T, pool *pgxpool.Pool, ctx context.Context, slug string) int {
	t.Helper()
	var channels int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM chat.channels WHERE workspace_id = $1 AND slug = $2`, amWS, slug,
	).Scan(&channels); err != nil {
		t.Fatalf("count channels: %v", err)
	}
	return channels
}

// ── Creator and initial members ─────────────────────────────────────────────

func TestChannelCreationPostgreSQL_PrivateChannelStartsWithItsCreator(t *testing.T) {
	store, pool, ctx := channelCreatePostgres(t)
	created := requireCreation(t, store, ctx, privateCreation("so-eu"))
	assertMembership(t, pool, ctx, created.Channel.ID, map[string]string{amActive1: "member"})
}

func TestChannelCreationPostgreSQL_OneInviteeJoinsWithTheCreator(t *testing.T) {
	store, pool, ctx := channelCreatePostgres(t)
	created := requireCreation(t, store, ctx, privateCreation("um", amActive2))
	assertMembership(t, pool, ctx, created.Channel.ID, map[string]string{amActive1: "member", amActive2: "member"})
}

// Guests are eligible exactly as in add-members (EligibleTargetsCTE), and a
// workspace admin invited to a channel is an ordinary channel member: no role
// travels from the workspace or from the request.
func TestChannelCreationPostgreSQL_EveryInviteeGetsTheOrdinaryRole(t *testing.T) {
	store, pool, ctx := channelCreatePostgres(t)
	created := requireCreation(t, store, ctx, privateCreation("varios", amActive2, amActive3, ccGuest, amAdmin))
	assertMembership(t, pool, ctx, created.Channel.ID, map[string]string{
		amActive1: "member", amActive2: "member", amActive3: "member", ccGuest: "member", amAdmin: "member",
	})
}

func TestChannelCreationPostgreSQL_OnlyMembersCanSeeThePrivateChannel(t *testing.T) {
	store, pool, ctx := channelCreatePostgres(t)
	created := requireCreation(t, store, ctx, privateCreation("visivel", amActive2, ccGuest))
	assertVisibility(t, pool, ctx, created.Channel.ID, map[string]bool{
		amActive1: true, amActive2: true, ccGuest: true,
		amActive3: false, amAdmin: false, amForeignU: false,
	})
}

// ── All or nothing ───────────────────────────────────────────────────────────

func TestChannelCreationPostgreSQL_IneligibleInviteeRollsBackEverything(t *testing.T) {
	store, pool, ctx := channelCreatePostgres(t)
	ineligible := map[string]string{
		"suspended-account": amSuspended,
		"deleted-account":   amDeleted,
		"cross-workspace":   amForeignU,
		"wm-suspended":      ccWMSuspend,
		"wm-left":           ccWMLeft,
		"nonexistent":       ccUnknown,
	}
	for name, userID := range ineligible {
		t.Run(name, func(t *testing.T) {
			slug := "x-" + name
			// One valid invitee next to the bad one: nothing partial may stay.
			_, err := store.CreateChannelForActiveMember(ctx,
				withKey(privateCreation(slug, amActive2, userID), "key-"+name, ccHashA))
			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("error = %v, want ErrForbidden", err)
			}
			assertNothingCreated(t, pool, ctx, slug)
		})
	}
}

// ── Idempotency ──────────────────────────────────────────────────────────────

func TestChannelCreationPostgreSQL_SameKeyAndPayloadReplaysTheChannel(t *testing.T) {
	store, pool, ctx := channelCreatePostgres(t)
	input := withKey(privateCreation("idem", amActive2, amActive3), "intent-1", ccHashA)
	first := requireCreation(t, store, ctx, input)

	retry, err := store.CreateChannelForActiveMember(ctx, input)
	if err != nil || !retry.Replayed || retry.Channel.ID != first.Channel.ID {
		t.Fatalf("retry = %+v, %v; want a replay of %s", retry, err, first.Channel.ID)
	}
	if n := countChannelMembers(t, pool, ctx, first.Channel.ID); n != 3 {
		t.Fatalf("members after replay = %d, want 3", n)
	}
	if n := countChannelsWithSlug(t, pool, ctx, "idem"); n != 1 {
		t.Fatalf("channels after replay = %d, want 1", n)
	}
}

// The same key with another payload is a deterministic conflict, never a
// second channel.
func TestChannelCreationPostgreSQL_SameKeyOtherPayloadConflicts(t *testing.T) {
	store, pool, ctx := channelCreatePostgres(t)
	requireCreation(t, store, ctx, withKey(privateCreation("idem", amActive2), "intent-1", ccHashA))

	_, err := store.CreateChannelForActiveMember(ctx, withKey(privateCreation("idem-outro", amActive3), "intent-1", ccHashB))
	if !errors.Is(err, domain.ErrIdempotencyKeyReused) {
		t.Fatalf("error = %v, want ErrIdempotencyKeyReused", err)
	}
	if n := countChannelsWithSlug(t, pool, ctx, "idem-outro"); n != 0 {
		t.Fatalf("the conflicting payload created %d channel(s)", n)
	}
}

// Another key with the same slug is a new intent, and slug uniqueness decides
// it — a 409 that is not a replay.
func TestChannelCreationPostgreSQL_OtherKeySameSlugIsANewIntent(t *testing.T) {
	store, _, ctx := channelCreatePostgres(t)
	requireCreation(t, store, ctx, withKey(privateCreation("idem", amActive2), "intent-1", ccHashA))

	_, err := store.CreateChannelForActiveMember(ctx, withKey(privateCreation("idem", amActive2), "intent-2", ccHashA))
	if !errors.Is(err, domain.ErrDuplicateSlug) {
		t.Fatalf("error = %v, want ErrDuplicateSlug", err)
	}
}

func TestChannelCreationPostgreSQL_KeyIsScopedToItsActor(t *testing.T) {
	store, _, ctx := channelCreatePostgres(t)
	requireCreation(t, store, ctx, withKey(privateCreation("idem-a"), "intent-1", ccHashA))

	otherActor := withKey(privateCreation("idem-b"), "intent-1", ccHashA)
	otherActor.CreatedBy = amActive2
	requireCreation(t, store, ctx, otherActor)
}

// A replay is no back door: an actor who lost access is refused.
func TestChannelCreationPostgreSQL_ReplayRequiresCurrentAccess(t *testing.T) {
	store, pool, ctx := channelCreatePostgres(t)
	input := withKey(privateCreation("idem"), "intent-1", ccHashA)
	requireCreation(t, store, ctx, input)
	if _, err := pool.Exec(ctx,
		`UPDATE chat.workspace_members SET status = 'suspended' WHERE workspace_id = $1 AND user_id = $2`,
		amWS, amActive1); err != nil {
		t.Fatalf("suspend actor: %v", err)
	}

	if _, err := store.CreateChannelForActiveMember(ctx, input); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("replay after suspension = %v, want ErrForbidden", err)
	}
}

// ── Concurrency ──────────────────────────────────────────────────────────────

// createConcurrently releases every attempt at once, so they race on the key.
func createConcurrently(
	ctx context.Context, store *storage.PGXChannelStore, input storage.CreateChannelInput, attempts int,
) ([]storage.CreateChannelResult, []error) {
	results := make([]storage.CreateChannelResult, attempts)
	errs := make([]error, attempts)
	var start, done sync.WaitGroup
	start.Add(1)
	for i := range attempts {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()
			results[i], errs[i] = store.CreateChannelForActiveMember(ctx, input)
		}()
	}
	start.Done()
	done.Wait()
	return results, errs
}

// assertOneCreationRestReplayed: every attempt succeeded with the same channel,
// and exactly one of them is the creation.
func assertOneCreationRestReplayed(t *testing.T, results []storage.CreateChannelResult, errs []error) {
	t.Helper()
	created := 0
	for i := range results {
		if errs[i] != nil {
			t.Fatalf("attempt %d: %v", i, errs[i])
		}
		if results[i].Channel.ID != results[0].Channel.ID {
			t.Fatalf("attempt %d returned %s, want %s", i, results[i].Channel.ID, results[0].Channel.ID)
		}
		if !results[i].Replayed {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("non-replayed results = %d, want exactly 1", created)
	}
}

func TestChannelCreationPostgreSQL_ConcurrentSameKeyCreatesOnceAndTheRestReplay(t *testing.T) {
	store, pool, ctx := channelCreatePostgres(t)
	input := withKey(privateCreation("corrida", amActive2, amActive3), "race-key", ccHashA)

	results, errs := createConcurrently(ctx, store, input, 8)

	assertOneCreationRestReplayed(t, results, errs)
	if n := countChannelsWithSlug(t, pool, ctx, "corrida"); n != 1 {
		t.Fatalf("channels = %d, want 1", n)
	}
	if n := countChannelMembers(t, pool, ctx, results[0].Channel.ID); n != 3 {
		t.Fatalf("members = %d, want 3", n)
	}
}

// createAgainstPendingInvalidation holds an uncommitted invalidation, starts a
// creation, waits until PostgreSQL reports it blocked on that row, then commits
// the invalidation and returns the creation's outcome.
func createAgainstPendingInvalidation(
	t *testing.T, store *storage.PGXChannelStore, pool *pgxpool.Pool, ctx context.Context,
	invalidation string, target string, input storage.CreateChannelInput,
) error {
	t.Helper()
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin blocker: %v", err)
	}
	defer func() { _ = blocker.Rollback(context.Background()) }()
	if _, err := blocker.Exec(ctx, invalidation, amWS, target); err != nil {
		t.Fatalf("invalidate: %v", err)
	}

	result := make(chan error, 1)
	go func() {
		_, err := store.CreateChannelForActiveMember(ctx, input)
		result <- err
	}()
	waitForPostgresLockWaiter(t, pool)
	if err := blocker.Commit(ctx); err != nil {
		t.Fatalf("commit invalidation: %v", err)
	}
	return <-result
}

// A state change that commits while the creation waits on its locks must win:
// the creation re-evaluates the row and refuses, leaving nothing behind.
func TestChannelCreationPostgreSQL_ConcurrentInvalidationIsSerialized(t *testing.T) {
	cases := map[string]struct{ statement, target string }{
		"invitee-leaves-workspace": {
			`UPDATE chat.workspace_members SET status = 'left' WHERE workspace_id = $1 AND user_id = $2`, amActive2,
		},
		"actor-demoted-to-guest": {
			`UPDATE chat.workspace_members SET role = 'guest' WHERE workspace_id = $1 AND user_id = $2`, amActive1,
		},
		"invitee-account-disabled": {
			`UPDATE auth.users SET status = 'suspended' WHERE id = $2 AND $1::uuid IS NOT NULL`, amActive2,
		},
	}
	for name, invalidation := range cases {
		t.Run(name, func(t *testing.T) {
			store, pool, ctx := channelCreatePostgres(t)
			slug := "toctou-" + strings.ReplaceAll(name, "-", "")
			err := createAgainstPendingInvalidation(t, store, pool, ctx,
				invalidation.statement, invalidation.target, privateCreation(slug, amActive2))
			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("error = %v, want ErrForbidden", err)
			}
			assertNothingCreated(t, pool, ctx, slug)
		})
	}
}
