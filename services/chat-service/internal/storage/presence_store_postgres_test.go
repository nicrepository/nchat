package storage_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// chat.user_presence against a real PostgreSQL (issue #798): membership is part
// of every write, the server clock decides expiry, call participation follows
// the call domain's own tables, and the constraints refuse what the domain
// refuses. Opt-in through CHAT_TEST_DATABASE_URL like every other
// *_postgres_test.go here; the fixture is the call-leave one, which seeds one
// workspace and five active members.

const presencePGOutsider = "c5000000-0000-4000-8000-0000000000ff"

func TestPresenceStorePostgreSQL_ManualStateLifecycle(t *testing.T) {
	pool := newCallLeavePool(t)
	store := storage.NewPGXPresenceStore(pool)
	ctx := context.Background()
	expires := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)

	stored, err := store.SetManual(ctx, leavePGWorkspace, leavePGUserA, domain.PresenceManualDoNotDisturb, expires)
	if err != nil {
		t.Fatalf("SetManual: %v", err)
	}
	if stored.State != domain.PresenceManualDoNotDisturb || !stored.ExpiresAt.Equal(expires) || stored.UpdatedAt.IsZero() {
		t.Fatalf("stored = %+v", stored)
	}
	read, err := store.Manual(ctx, leavePGWorkspace, leavePGUserA)
	if err != nil || read.State != domain.PresenceManualDoNotDisturb {
		t.Fatalf("Manual = %+v, %v", read, err)
	}

	if _, err := store.SetManual(ctx, leavePGWorkspace, presencePGOutsider, domain.PresenceManualBusy, expires); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("non-member err = %v, want ErrForbidden", err)
	}

	if err := store.ClearManual(ctx, leavePGWorkspace, leavePGUserA); err != nil {
		t.Fatalf("ClearManual: %v", err)
	}
	if read, err := store.Manual(ctx, leavePGWorkspace, leavePGUserA); err != nil || read.State != "" {
		t.Fatalf("after clear = %+v, %v", read, err)
	}
	// Clearing what is already clear is not an error.
	if err := store.ClearManual(ctx, leavePGWorkspace, leavePGUserB); err != nil {
		t.Fatalf("ClearManual of nothing: %v", err)
	}
}

func TestPresenceStorePostgreSQL_ConcurrentWritesSettleOnOneAnswer(t *testing.T) {
	pool := newCallLeavePool(t)
	store := storage.NewPGXPresenceStore(pool)
	ctx := context.Background()
	expires := time.Now().Add(time.Hour).UTC()

	states := []domain.PresenceManualState{
		domain.PresenceManualBusy, domain.PresenceManualDoNotDisturb, domain.PresenceManualBeRightBack,
		domain.PresenceManualAway, domain.PresenceManualAvailable, domain.PresenceManualAppearOffline,
	}
	var wg sync.WaitGroup
	errs := make([]error, len(states))
	for i, state := range states {
		wg.Add(1)
		go func(i int, state domain.PresenceManualState) {
			defer wg.Done()
			_, errs[i] = store.SetManual(ctx, leavePGWorkspace, leavePGUserA, state, expires)
		}(i, state)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM chat.user_presence WHERE user_id = $1`, leavePGUserA).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	read, err := store.Manual(ctx, leavePGWorkspace, leavePGUserA)
	if rows != 1 || err != nil || read.State == "" {
		t.Fatalf("rows = %d, state = %+v, err = %v; want exactly one settled row", rows, read, err)
	}
}

func TestPresenceStorePostgreSQL_ContextsFollowTheServerClockAndTheCallDomain(t *testing.T) {
	pool := newCallLeavePool(t)
	store := storage.NewPGXPresenceStore(pool)
	ctx := context.Background()

	if _, err := store.SetManual(ctx, leavePGWorkspace, leavePGUserA, domain.PresenceManualDoNotDisturb, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("SetManual: %v", err)
	}
	// An override whose expiry has passed on the database clock, written
	// directly: the API would refuse to create one.
	if _, err := pool.Exec(ctx, `
		INSERT INTO chat.user_presence (workspace_id, user_id, manual_state, manual_expires_at, manual_updated_at)
		VALUES ($1, $2, 'busy', clock_timestamp() - interval '1 second', clock_timestamp() - interval '1 hour')`,
		leavePGWorkspace, leavePGUserB); err != nil {
		t.Fatalf("seed expired override: %v", err)
	}
	// An accepted 1:1 call between D and E, and a ringing one from C to A.
	if _, err := pool.Exec(ctx, `
		INSERT INTO chat.calls (workspace_id, request_id, caller_id, callee_id, target_type, target_id, call_type, status, expires_at, accepted_at)
		VALUES ($1, gen_random_uuid(), $2, $3, 'user', $3, 'audio', 'active', clock_timestamp() + interval '1 hour', clock_timestamp()),
		       ($1, gen_random_uuid(), $4, $5, 'user', $5, 'audio', 'ringing', clock_timestamp() + interval '1 hour', NULL)`,
		leavePGWorkspace, leavePGUserD, leavePGUserE, leavePGUserC, leavePGUserA); err != nil {
		t.Fatalf("seed calls: %v", err)
	}

	users := []string{leavePGUserA, leavePGUserB, leavePGUserC, leavePGUserD, leavePGUserE}
	contexts, err := store.Contexts(ctx, leavePGWorkspace, users)
	if err != nil {
		t.Fatalf("Contexts: %v", err)
	}
	if got := contexts[leavePGUserA]; got.Override.State != domain.PresenceManualDoNotDisturb || got.Activity != "" {
		t.Fatalf("A (dnd, only ringing) = %+v", got)
	}
	if _, present := contexts[leavePGUserB]; present {
		t.Fatalf("B's expired override is still live: %+v", contexts[leavePGUserB])
	}
	if _, present := contexts[leavePGUserC]; present {
		t.Fatalf("C is only ringing somebody, not in a call: %+v", contexts[leavePGUserC])
	}
	for _, inCall := range []string{leavePGUserD, leavePGUserE} {
		// A direct call lasts until it is ended: no lease bounds it.
		if contexts[inCall].Activity != domain.PresenceActivityInCall || !contexts[inCall].ActivityUntil.IsZero() {
			t.Fatalf("%s is in an accepted call: %+v", inCall, contexts[inCall])
		}
	}
	// Another workspace's id reads nothing for these users.
	other, err := store.Contexts(ctx, "c5000000-0000-4000-8000-0000000000ee", users)
	if err != nil || len(other) != 0 {
		t.Fatalf("cross-workspace = %+v, %v", other, err)
	}

	dnd, err := store.DoNotDisturbUsers(ctx, leavePGWorkspace, users)
	if err != nil || len(dnd) != 1 || !dnd[leavePGUserA] {
		t.Fatalf("DoNotDisturbUsers = %+v, %v", dnd, err)
	}
}

func TestPresenceStorePostgreSQL_LastSeenIsMonotonicAndMemberScoped(t *testing.T) {
	pool := newCallLeavePool(t)
	store := storage.NewPGXPresenceStore(pool)
	ctx := context.Background()
	later := time.Now().UTC().Truncate(time.Microsecond)
	earlier := later.Add(-time.Minute)

	if _, found, err := store.LastSeen(ctx, leavePGWorkspace, leavePGUserA); err != nil || found {
		t.Fatalf("never seen = %v, %v", found, err)
	}
	for _, at := range []time.Time{later, earlier} {
		if err := store.MarkLastSeen(ctx, leavePGWorkspace, leavePGUserA, at); err != nil {
			t.Fatalf("MarkLastSeen: %v", err)
		}
	}
	got, found, err := store.LastSeen(ctx, leavePGWorkspace, leavePGUserA)
	if err != nil || !found || !got.Equal(later) {
		t.Fatalf("LastSeen = %v %v %v, want %v", got, found, err, later)
	}
	// A non-member gets no row at all.
	if err := store.MarkLastSeen(ctx, leavePGWorkspace, presencePGOutsider, later); err != nil {
		t.Fatalf("MarkLastSeen outsider: %v", err)
	}
	if _, found, _ := store.LastSeen(ctx, leavePGWorkspace, presencePGOutsider); found {
		t.Fatal("a non-member was given a last seen")
	}
}

func TestPresenceStorePostgreSQL_ConstraintsRefuseWhatTheDomainRefuses(t *testing.T) {
	pool := newCallLeavePool(t)
	ctx := context.Background()
	cases := map[string]string{
		"unknown state": `INSERT INTO chat.user_presence (workspace_id, user_id, manual_state, manual_expires_at, manual_updated_at)
			VALUES ($1, $2, 'online', now() + interval '1 hour', now())`,
		"state without expiry": `INSERT INTO chat.user_presence (workspace_id, user_id, manual_state, manual_updated_at)
			VALUES ($1, $2, 'busy', now())`,
		"expiry without state": `INSERT INTO chat.user_presence (workspace_id, user_id, manual_expires_at)
			VALUES ($1, $2, now())`,
	}
	for name, statement := range cases {
		if _, err := pool.Exec(ctx, statement, leavePGWorkspace, leavePGUserA); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO chat.user_presence (workspace_id, user_id, last_seen_at) VALUES ($1, $2, now())`,
		leavePGWorkspace, presencePGOutsider); err == nil {
		t.Fatal("a row for a non-member was accepted")
	}
	if _, err := pool.Exec(ctx, readChatMigration(t, "000065_user_presence.down.sql")); err != nil {
		t.Fatalf("down migration: %v", err)
	}
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('chat.user_presence') IS NOT NULL`).Scan(&exists); err != nil || exists {
		t.Fatalf("table after down = %v, %v", exists, err)
	}
}
