package ws

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// The application clock decides no validity, against a real PostgreSQL and a
// real Valkey, through the hub's own composition (issue #798, sixth
// preparation): the same facts give the same public presence with the hub's
// clock a minute behind, right, or a minute ahead. Only version instants may
// differ.
//
// Runs when both CHAT_TEST_DATABASE_URL and CHAT_TEST_VALKEY_URL are set, in a
// database of its own beside the one named, so it never shares a schema with
// the storage suites that reset theirs.

const skewDatabase = "ws_presence_skew_test"

func skewPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("CHAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("CHAT_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = admin.Close(context.Background()) }()
	_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+skewDatabase+" WITH (FORCE)")
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+skewDatabase); err != nil {
		t.Fatalf("create %s: %v", skewDatabase, err)
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.Database = skewDatabase
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `CREATE SCHEMA auth; CREATE TABLE auth.users (
		id UUID PRIMARY KEY, email TEXT NOT NULL DEFAULT '', display_name TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT 'active', deleted_at TIMESTAMPTZ)`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, chatMigrations(t)); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return pool
}

// chatMigrations is every chat up migration, in order.
func chatMigrations(t *testing.T) string {
	t.Helper()
	_, here, _, _ := runtime.Caller(0)
	migrations := os.DirFS(filepath.Join(filepath.Dir(here), "..", "..", "..", "..", "migrations", "chat"))
	names, err := fs.Glob(migrations, "*.up.sql")
	if err != nil || len(names) == 0 {
		t.Fatalf("list migrations: %v", err)
	}
	var all strings.Builder
	for _, name := range names {
		contents, err := fs.ReadFile(migrations, name)
		if err != nil {
			t.Fatal(err)
		}
		all.Write(contents)
	}
	return all.String()
}

// skewFixture is one workspace, its general channel, and n members.
type skewFixture struct {
	pool      *pgxpool.Pool
	workspace string
	channel   string
	members   []string
}

func newSkewFixture(t *testing.T, pool *pgxpool.Pool, n int) skewFixture {
	t.Helper()
	ctx := t.Context()
	f := skewFixture{pool: pool, workspace: uuid.NewString(), channel: uuid.NewString()}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `INSERT INTO chat.workspaces (id, slug, name, status) VALUES ($1, $2, 'Skew', 'active')`,
		f.workspace, "skew-"+f.workspace[:8]); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO chat.channels (id, workspace_id, slug, display_name, type, is_general, status)
		VALUES ($1, $2, 'geral', 'geral', 'public', true, 'active')`, f.channel, f.workspace); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for range n {
		member := uuid.NewString()
		if _, err := pool.Exec(ctx, `INSERT INTO chat.workspace_members (workspace_id, user_id, role, status) VALUES ($1, $2, 'member', 'active')`,
			f.workspace, member); err != nil {
			t.Fatal(err)
		}
		f.members = append(f.members, member)
	}
	return f
}

// workspaceSource is the real presence store, read for the fixture's
// workspace whatever the hub calls it.
type workspaceSource struct {
	store     *storage.PGXPresenceStore
	workspace string
}

func (s workspaceSource) Contexts(ctx context.Context, _ string, userIDs []string) (map[string]domain.PresenceContext, error) {
	return s.store.Contexts(ctx, s.workspace, userIDs)
}

func (s workspaceSource) MarkLastSeen(ctx context.Context, _, userID string, at time.Time) error {
	return s.store.MarkLastSeen(ctx, s.workspace, userID, at)
}

// skewCase prepares one person's facts in the database and Valkey, and says
// what must be published for them.
type skewCase struct {
	name    string
	prepare func(t *testing.T, f skewFixture, w *raceWorld, userID string)
	want    domain.EffectivePresence
}

func setManual(t *testing.T, f skewFixture, userID string, state domain.PresenceManualState, ends string) {
	t.Helper()
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO chat.user_presence
		(workspace_id, user_id, manual_state, manual_expires_at, manual_updated_at)
		VALUES ($1, $2, $3, clock_timestamp() + $4::interval, clock_timestamp())`,
		f.workspace, userID, string(state), ends); err != nil {
		t.Fatal(err)
	}
}

func joinCall(t *testing.T, f skewFixture, userID string, ends string) {
	t.Helper()
	call, _, _, _, err := storage.NewPGXCallStore(f.pool).CreateResourceCall(t.Context(), storage.CreateResourceCallInput{
		WorkspaceID: f.workspace, RequestID: uuid.NewString(), CallerID: userID,
		TargetType: domain.CallTargetChannel, TargetID: f.channel, Type: domain.CallTypeAudio,
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE chat.call_participant_leases SET expires_at = clock_timestamp() + $3::interval
		WHERE call_id = $1 AND user_id = $2`, call.ID, userID, ends); err != nil {
		t.Fatal(err)
	}
}

var skewCases = []skewCase{
	{"dnd in force", func(t *testing.T, f skewFixture, _ *raceWorld, u string) {
		setManual(t, f, u, domain.PresenceManualDoNotDisturb, "30 seconds")
	}, domain.EffectivePresence{Availability: domain.PresenceDoNotDisturb}},
	{"appear offline in force", func(t *testing.T, f skewFixture, _ *raceWorld, u string) {
		setManual(t, f, u, domain.PresenceManualAppearOffline, "30 seconds")
	}, domain.EffectivePresence{Availability: domain.PresenceOffline}},
	{"dnd ended", func(t *testing.T, f skewFixture, _ *raceWorld, u string) {
		setManual(t, f, u, domain.PresenceManualDoNotDisturb, "-1 second")
	}, domain.EffectivePresence{Availability: domain.PresenceAvailable}},
	{"change open", func(t *testing.T, _ skewFixture, w *raceWorld, u string) {
		_, closeFacts, err := w.b.hub.OpenPresenceFacts(t.Context(), "ws-1", []string{u})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(closeFacts)
	}, domain.EffectivePresence{Availability: domain.PresenceOffline}}, // nothing may be committed
	{"change lapsed without End", func(t *testing.T, _ skewFixture, w *raceWorld, u string) {
		if err := w.b.hub.userPresence().BeginFactsChange(t.Context(), "ws-1", []string{u}, FactsChange{Token: "lapsed", Lease: 0}); err != nil {
			t.Fatal(err)
		}
	}, domain.EffectivePresence{Availability: domain.PresenceAvailable}},
	{"call lease live", func(t *testing.T, f skewFixture, _ *raceWorld, u string) {
		joinCall(t, f, u, "30 seconds")
	}, domain.EffectivePresence{Availability: domain.PresenceBusy, Activity: domain.PresenceActivityInCall}},
	{"call lease lapsed", func(t *testing.T, f skewFixture, _ *raceWorld, u string) {
		joinCall(t, f, u, "-1 second")
	}, domain.EffectivePresence{Availability: domain.PresenceAvailable}},
}

func TestPresenceSkewPGValkey_TheAppClockChangesNoValidity(t *testing.T) {
	pool := skewPool(t)
	for _, appAhead := range []time.Duration{-time.Minute, 0, time.Minute} {
		t.Run(fmt.Sprintf("app %+v", appAhead), func(t *testing.T) {
			f := newSkewFixture(t, pool, len(skewCases))
			for i, tc := range skewCases {
				t.Run(tc.name, func(t *testing.T) {
					userID := f.members[i]
					w := newRaceWorldAt(t, time.Now().Add(appAhead), func(id string) PresenceDirectory {
						return realValkeyDirectory(t, id)
					}, userID, "chan-a-"+userID, "chan-b-"+userID)
					source := workspaceSource{store: storage.NewPGXPresenceStore(pool), workspace: f.workspace}
					w.a.hub.presenceContext, w.b.hub.presenceContext = source, source
					tc.prepare(t, f, w, userID)
					w.a.join(t, "c-a", userID, w.chanA)
					got := domain.EffectivePresence{Availability: domain.PresenceOffline}
					if p := storedProjection(t, w.a, userID); p != nil {
						got = p.Effective
					}
					if got != tc.want {
						t.Fatalf("published %+v, want %+v", got, tc.want)
					}
				})
			}
		})
	}
}

// HIGH-C against both real stores: Busy is in the database; a change opens;
// a composition reads inside it — Busy — and stops; the database commits DND;
// the End never comes and the lease is over. The old composition is refused;
// a fresh one commits DND.
func TestEndFailurePGValkey_TheReadInsideTheChangeIsRefused(t *testing.T) {
	pool := skewPool(t)
	f := newSkewFixture(t, pool, 1)
	userID := f.members[0]
	directory := realValkeyDirectory(t, "runtime-end-pg")
	store := storage.NewPGXPresenceStore(pool)
	ctx := t.Context()
	setManual(t, f, userID, domain.PresenceManualBusy, "1 hour")
	if err := directory.BeginFactsChange(ctx, f.workspace, []string{userID}, FactsChange{Token: "unended", Lease: 0}); err != nil {
		t.Fatal(err)
	}
	compose := func() ProjectionCommit {
		revision := revisionIn(t, directory, f.workspace, userID)
		contexts, err := store.Contexts(ctx, f.workspace, []string{userID})
		if err != nil {
			t.Fatal(err)
		}
		return ProjectionCommit{
			Effective: domain.ResolvePresence(domain.PresenceReachActive, contexts[userID]),
			Expected:  revision, Now: time.Now(), ValidUntil: factsValidUntil(contexts[userID]),
		}
	}
	inside := compose()
	if _, err := store.SetManual(ctx, f.workspace, userID, domain.PresenceManualDoNotDisturb, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, outcome, _ := directory.Project(ctx, f.workspace, userID, inside); outcome != projectionConflict {
		t.Fatalf("the Busy read inside the change = %v, want a conflict", outcome)
	}
	fresh := compose()
	if _, outcome, _ := directory.Project(ctx, f.workspace, userID, fresh); outcome != projectionApplied || fresh.Effective.Availability != domain.PresenceDoNotDisturb {
		t.Fatalf("the fresh read %+v = %v", fresh.Effective, outcome)
	}
}
