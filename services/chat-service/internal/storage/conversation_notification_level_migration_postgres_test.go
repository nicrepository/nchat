package storage_test

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/jackc/pgx/v5"
)

// The 000050/000051 transition, executed against a real PostgreSQL (issue #136).
//
// Everything else that checks these files reads their text. That proves the SQL
// says what it should; it cannot prove the SQL runs, and it cannot prove the one
// property the whole rollout rests on: that a row written by the build *before*
// the column existed still reads as silenced afterwards, and that rolling back
// does not invent silence for a row the old model cannot express.
//
// So this applies the real files — no schema copied into the test — in the order
// a runner applies them: the schema as it was, the legacy row, the upgrade, the
// granular row, then the down migrations.
//
// It never touches the shared test database. Like the #741 round trip beside it,
// it creates a database of its own, ending in _test, and drops it afterwards even
// when the test fails: a DOWN is destructive by definition.

const (
	// Fixed and ending in _test, so a stray drop can only reach a database this
	// test owns.
	levelRoundTripDatabase = "nchat_136_level_roundtrip_test"

	levelMigrationUp50   = "000050_conversation_notification_level.up.sql"
	levelMigrationDown50 = "000050_conversation_notification_level.down.sql"
	levelMigrationUp51   = "000051_validate_conversation_notification_level_check.up.sql"
	levelMigrationDown51 = "000051_validate_conversation_notification_level_check.down.sql"

	levelCheckConstraint = "conversation_notification_prefs_level_check"
	// The sparse-representation invariant 000050 adds alongside it: `all` and
	// unsilenced is the absence of a row, so a row saying it is forbidden.
	sparseDefaultConstraint = "conversation_notification_prefs_sparse_default_check"
)

// levelMigrationsUnderTest are the files this test applies by hand; every other
// up migration is baseline.
var levelMigrationsUnderTest = map[string]struct{}{
	levelMigrationUp50: {}, levelMigrationUp51: {},
}

// Fixture identifiers for the rows the transition is asserted on.
const (
	levelPrefWorkspace      = "d1360000-0000-4000-8000-000000000001"
	levelPrefGeneralChannel = "d1360000-0000-4000-8000-000000000020"
	levelPrefChannel        = "d1360000-0000-4000-8000-000000000040"
	levelPrefUser           = "d1360000-0000-4000-8000-00000000000a"
)

// newLevelRoundTripDatabase creates this test's own database, applies every
// migration except the two under test, and seeds a workspace, two channels and a
// member — so the preference rows below have something real to point at.
func newLevelRoundTripDatabase(t *testing.T) *pgx.Conn {
	t.Helper()
	dsn := os.Getenv("CHAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("CHAT_TEST_DATABASE_URL is not set")
	}
	admin := connectTo(t, databaseDSN(t, dsn, "postgres"))
	dropLevelRoundTripDatabase(t, admin)
	if _, err := admin.Exec(t.Context(), `CREATE DATABASE `+levelRoundTripDatabase); err != nil {
		t.Fatalf("create round-trip database: %v", err)
	}

	conn := connectTo(t, databaseDSN(t, dsn, levelRoundTripDatabase))
	t.Cleanup(func() {
		_ = conn.Close(context.Background())
		dropLevelRoundTripDatabase(t, admin)
		_ = admin.Close(context.Background())
	})
	assertLevelRoundTripDatabase(t, conn)
	applyLevelBaselineMigrations(t, conn)
	seedLevelRoundTripFixture(t, conn)
	return conn
}

// assertLevelRoundTripDatabase is the guard that makes every DOWN here safe: the
// connection about to run destructive SQL is proved to point at the throwaway
// database and nothing else.
func assertLevelRoundTripDatabase(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	var name string
	if err := conn.QueryRow(t.Context(), `SELECT current_database()`).Scan(&name); err != nil {
		t.Fatalf("read current database: %v", err)
	}
	if name != levelRoundTripDatabase {
		t.Fatalf("refusing to run migrations against %q, expected %q", name, levelRoundTripDatabase)
	}
}

func dropLevelRoundTripDatabase(t *testing.T, admin *pgx.Conn) {
	t.Helper()
	if _, err := admin.Exec(context.Background(),
		`DROP DATABASE IF EXISTS `+levelRoundTripDatabase+` WITH (FORCE)`); err != nil {
		t.Fatalf("drop round-trip database: %v", err)
	}
}

// applyLevelBaselineMigrations brings the database to exactly the state that
// precedes 000050, in the order the canonical runner applies them:
// scripts/db/migrate.sh collects them with `find ... -name "*.up.sql" | sort`,
// which is this glob and this sort.
//
// Everything but the two files under test is applied, so the baseline is the
// real chain and not a reconstruction of it: chat 000049, the acknowledgement
// migration, has already run when 000050 lands here, exactly as in production.
func applyLevelBaselineMigrations(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(migrationsRoot(t), "*", "*.up.sql"))
	if err != nil {
		t.Fatalf("list migrations: %v", err)
	}
	sort.Strings(paths)
	applied := 0
	for _, path := range paths {
		if _, underTest := levelMigrationsUnderTest[filepath.Base(path)]; underTest {
			continue
		}
		contents, err := os.ReadFile(path) //nolint:gosec // Glob is restricted to the repository migration directory.
		if err != nil {
			t.Fatalf("read %s: %v", filepath.Base(path), err)
		}
		if _, err := conn.Exec(t.Context(), string(contents)); err != nil {
			t.Fatalf("apply %s: %v", filepath.Base(path), err)
		}
		applied++
	}
	if applied != len(paths)-len(levelMigrationsUnderTest) {
		t.Fatalf("applied %d of %d migrations, expected the two under test to be excluded",
			applied, len(paths))
	}
}

func seedLevelRoundTripFixture(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	ctx := t.Context()
	// One transaction: a deferred constraint requires every workspace to hold
	// exactly one active public general channel by commit time.
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin seed: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `
		INSERT INTO chat.workspaces (id, slug, name, status)
		VALUES ($1, 'level-roundtrip', 'Level Round Trip', 'active')`,
		levelPrefWorkspace); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO chat.channels (id, workspace_id, slug, display_name, type, is_general, status) VALUES
			($1, $3, 'geral', 'geral', 'public', true,  'active'),
			($2, $3, 'infra', 'Infra', 'public', false, 'active')`,
		levelPrefGeneralChannel, levelPrefChannel, levelPrefWorkspace); err != nil {
		t.Fatalf("seed channels: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO chat.workspace_members (workspace_id, user_id, role, status)
		VALUES ($1, $2, 'member', 'active')`,
		levelPrefWorkspace, levelPrefUser); err != nil {
		t.Fatalf("seed member: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit seed: %v", err)
	}
}

// applyLevelMigration executes one migration file verbatim, behind this test's
// own guard.
//
// It does not reuse applyMigration: that one asserts the *other* round trip's
// database name, and widening its guard to accept a second name would weaken
// the thing that makes both of them safe.
func applyLevelMigration(t *testing.T, conn *pgx.Conn, name string) {
	t.Helper()
	assertLevelRoundTripDatabase(t, conn)
	if _, err := conn.Exec(t.Context(), readChatMigration(t, name)); err != nil {
		t.Fatalf("apply %s: %v", name, err)
	}
}

func levelColumnIsNullable(t *testing.T, conn *pgx.Conn, column string) bool {
	t.Helper()
	var nullable string
	if err := conn.QueryRow(t.Context(), `
		SELECT is_nullable FROM information_schema.columns
		WHERE table_schema = 'chat'
		  AND table_name = 'conversation_notification_prefs'
		  AND column_name = $1`, column).Scan(&nullable); err != nil {
		t.Fatalf("read nullability of %s: %v", column, err)
	}
	return nullable == "YES"
}

func levelPrefRowCount(t *testing.T, conn *pgx.Conn, channelID string) int {
	t.Helper()
	var rows int
	if err := conn.QueryRow(t.Context(), `
		SELECT count(*) FROM chat.conversation_notification_prefs
		WHERE user_id = $1::uuid AND channel_id = $2::uuid`,
		levelPrefUser, channelID).Scan(&rows); err != nil {
		t.Fatalf("count preference rows: %v", err)
	}
	return rows
}

// The whole transition, in one test, because the steps only mean anything in
// sequence.
func TestConversationNotificationLevelMigrationRoundTripPostgreSQL(t *testing.T) {
	conn := newLevelRoundTripDatabase(t)
	ctx := t.Context()

	// ── The schema as it was, before 000050 ─────────────────────────────────
	if hasColumn(t, conn, "chat", "conversation_notification_prefs", "notification_level") {
		t.Fatal("the level column already exists; this is not the pre-000050 schema")
	}
	if levelColumnIsNullable(t, conn, "muted_at") {
		t.Fatal("muted_at is already nullable; this is not the pre-000050 schema")
	}
	// And the half of that schema the baseline owes to develop: without 000049
	// this would be a database #136 never has to migrate. It is also what keeps
	// the renumbering honest.
	if !hasColumn(t, conn, "chat", "messages", "acknowledgement_required") {
		t.Fatal("000049_message_acknowledgement did not run; this is not the real pre-000050 chain")
	}
	// Exactly what the previous build wrote: a row, and nothing else. It names
	// no level because there is no level to name.
	if _, err := conn.Exec(ctx, `
		INSERT INTO chat.conversation_notification_prefs (user_id, workspace_id, channel_id)
		VALUES ($1::uuid, $2::uuid, $3::uuid)`,
		levelPrefUser, levelPrefWorkspace, levelPrefChannel); err != nil {
		t.Fatalf("write legacy mute row: %v", err)
	}

	// ── Upgrade ─────────────────────────────────────────────────────────────
	applyLevelMigration(t, conn, levelMigrationUp50)
	applyLevelMigration(t, conn, levelMigrationUp51)

	var level string
	var mutedAt *string
	if err := conn.QueryRow(ctx, `
		SELECT notification_level, muted_at::text
		FROM chat.conversation_notification_prefs
		WHERE user_id = $1::uuid AND channel_id = $2::uuid`,
		levelPrefUser, levelPrefChannel).Scan(&level, &mutedAt); err != nil {
		t.Fatalf("read the migrated legacy row: %v", err)
	}
	// The compatibility requirement the whole migration turns on.
	if mutedAt == nil {
		t.Fatal("the legacy row stopped being silenced")
	}
	if level != "all" {
		t.Fatalf("notification_level = %q, want the default", level)
	}
	if !levelColumnIsNullable(t, conn, "muted_at") {
		t.Fatal("muted_at did not become nullable, so an unsilenced level cannot be expressed")
	}
	if levelColumnIsNullable(t, conn, "notification_level") {
		t.Fatal("notification_level is nullable, so a row could say nothing about the level")
	}

	// The CHECK is present and validated, so the planner may rely on it and no
	// path can store a level outside the set.
	for _, constraint := range []string{levelCheckConstraint, sparseDefaultConstraint} {
		if !queryBool(t, conn,
			`SELECT convalidated FROM pg_constraint WHERE conname = $1`, constraint) {
			t.Fatalf("%s was left NOT VALID after 000051", constraint)
		}
	}
	if _, err := conn.Exec(ctx, `
		UPDATE chat.conversation_notification_prefs SET notification_level = 'everything_always'
		WHERE user_id = $1::uuid`, levelPrefUser); err == nil {
		t.Fatal("the database accepted a level outside the CHECK")
	}
	// The forbidden sparse state is refused on the legacy row too: clearing its
	// mute without narrowing it would be exactly `all` + NULL.
	if _, err := conn.Exec(ctx, `
		UPDATE chat.conversation_notification_prefs SET muted_at = NULL
		WHERE user_id = $1::uuid AND channel_id = $2::uuid`,
		levelPrefUser, levelPrefChannel); err == nil {
		t.Fatal("the database accepted 'all' with a NULL muted_at")
	}

	// ── The state only the new schema can hold ──────────────────────────────
	if _, err := conn.Exec(ctx, `
		INSERT INTO chat.conversation_notification_prefs
			(user_id, workspace_id, channel_id, notification_level, muted_at)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'mentions_replies', NULL)`,
		levelPrefUser, levelPrefWorkspace, levelPrefGeneralChannel); err != nil {
		t.Fatalf("write a granular unsilenced row: %v", err)
	}
	var granularLevel string
	var granularMuted bool
	if err := conn.QueryRow(ctx, `
		SELECT notification_level, (muted_at IS NOT NULL)
		FROM chat.conversation_notification_prefs
		WHERE user_id = $1::uuid AND channel_id = $2::uuid`,
		levelPrefUser, levelPrefGeneralChannel).Scan(&granularLevel, &granularMuted); err != nil {
		t.Fatalf("read the granular row: %v", err)
	}
	if granularLevel != "mentions_replies" || granularMuted {
		t.Fatalf("granular row = (%q, muted=%v), want mentions_replies and unsilenced",
			granularLevel, granularMuted)
	}

	// ── Rollback, in the order a runner applies it ──────────────────────────
	applyLevelMigration(t, conn, levelMigrationDown51)
	applyLevelMigration(t, conn, levelMigrationDown50)

	if hasColumn(t, conn, "chat", "conversation_notification_prefs", "notification_level") {
		t.Fatal("the level column survived the rollback")
	}
	// Both constraints go with it: one left behind would block the previous
	// build's own inserts, which name no level at all.
	for _, constraint := range []string{levelCheckConstraint, sparseDefaultConstraint} {
		if queryBool(t, conn,
			`SELECT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = $1)`, constraint) {
			t.Fatalf("%s survived the rollback", constraint)
		}
	}
	if levelColumnIsNullable(t, conn, "muted_at") {
		t.Fatal("muted_at stayed nullable, so the old model's invariant was not restored")
	}
	// The granular unsilenced row had no representation in the old model, and
	// the safe conversion is "not silenced" — which the old model expresses by
	// the absence of a row. Keeping it would have silenced a conversation the
	// user had deliberately left active.
	if rows := levelPrefRowCount(t, conn, levelPrefGeneralChannel); rows != 0 {
		t.Fatalf("the rollback kept %d granular row(s), turning an active conversation into a silenced one", rows)
	}
	// ...and every row the old model *can* represent is untouched, so no
	// existing mute is lost either.
	if rows := levelPrefRowCount(t, conn, levelPrefChannel); rows != 1 {
		t.Fatalf("the rollback left %d legacy mute row(s), want the one it started with", rows)
	}

	// Re-applying afterwards is the other half of a usable rollback: the state
	// the down migration leaves behind has to be upgradable again.
	applyLevelMigration(t, conn, levelMigrationUp50)
	applyLevelMigration(t, conn, levelMigrationUp51)
	if !hasColumn(t, conn, "chat", "conversation_notification_prefs", "notification_level") {
		t.Fatal("the level column did not come back on re-apply")
	}
	if rows := levelPrefRowCount(t, conn, levelPrefChannel); rows != 1 {
		t.Fatal("re-applying the migration lost the legacy mute")
	}
}
