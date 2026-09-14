package storage_test

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nicrepository/nchat/libs/go/platform/notificationevent"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Issue #741: the two migrations are executed, reverted and executed again
// against a real PostgreSQL.
//
// Everything else that checks these files reads their text. That proves the SQL
// says what it should; it cannot prove the SQL runs, and a down migration is
// exactly the code nobody runs until the day it has to work. This one applies
// the real files — no schema copied into the test, no hand-written DDL — and
// asserts the contract by querying the catalogue and by exercising behaviour,
// not by matching strings.
//
// It never touches the shared test database. It creates a database of its own,
// applies the canonical migration set to it, and drops it afterwards even when
// the test fails: a DOWN is destructive by definition and must never be pointed
// at anything another test, or a person, is using.

const (
	// The name is fixed and ends in _test, so a stray drop can only ever reach
	// a database this test owns.
	roundTripDatabase = "nchat_741_migration_roundtrip_test"

	migrationUp42   = "000042_notification_outbox_event_contract.up.sql"
	migrationDown42 = "000042_notification_outbox_event_contract.down.sql"
	migrationUp43   = "000043_validate_notification_outbox_event_contract.up.sql"
	migrationDown43 = "000043_validate_notification_outbox_event_contract.down.sql"
	// Issue #742 adds the claim protocol's own state to the same table, so it
	// belongs to the same round trip: a rollback that stopped at 000043 would
	// leave columns behind that 000042's down cannot remove.
	migrationUp44   = "000044_notification_outbox_worker_claim.up.sql"
	migrationDown44 = "000044_notification_outbox_worker_claim.down.sql"

	// The constraints 000042 adds NOT VALID and 000043 validates. Their state is
	// the whole difference between the two migrations, so it is what the
	// intermediate assertions read.
	outboxKindConstraint         = "notification_outbox_kind_check"
	outboxStatusConstraint       = "notification_outbox_status_check"
	outboxSourceConstraint       = "notification_outbox_source_type_check"
	outboxPriorityConstraint     = "notification_outbox_priority_check"
	outboxOriginConstraint       = "notification_outbox_origin_check"
	outboxReasonConstraint       = "notification_outbox_suppressed_reason_check"
	outboxDedupeKeyConstraint    = "notification_outbox_dedupe_key_check"
	outboxTransitionTrigger      = "notification_outbox_enforce_transition"
	outboxTransitionFunction     = "enforce_notification_outbox_transition"
	outboxDedupeIndex            = "notification_outbox_dedupe_uq"
	outboxOpenIndex              = "idx_notification_outbox_open"
	outboxClaimableIndex         = "idx_notification_outbox_claimable"
	outboxLegacyPendingIndex     = "idx_notification_outbox_pending"
	outboxLegacyUniqueConstraint = "notification_outbox_message_recipient_unique"
)

var outboxValidatedConstraints = []string{
	outboxKindConstraint,
	outboxStatusConstraint,
	outboxSourceConstraint,
	outboxPriorityConstraint,
	outboxOriginConstraint,
	outboxReasonConstraint,
	outboxDedupeKeyConstraint,
}

// ── the disposable database ─────────────────────────────────────────────────

// newMigrationRoundTripDatabase creates a database of this test's own on the
// server the opt-in DSN points at, and returns a connection to it.
//
// Same environment variable as every other opt-in suite in this package, so the
// test runs wherever they run — including the CI job, whose coverage step
// exports it. Only the database name is this test's; the credentials and the
// host are the ones the suite already has.
func newMigrationRoundTripDatabase(t *testing.T) *pgx.Conn {
	t.Helper()
	dsn := os.Getenv("CHAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("CHAT_TEST_DATABASE_URL is not set")
	}
	admin := connectTo(t, databaseDSN(t, dsn, "postgres"))
	dropRoundTripDatabase(t, admin)
	if _, err := admin.Exec(t.Context(), `CREATE DATABASE `+roundTripDatabase); err != nil {
		t.Fatalf("create round-trip database: %v", err)
	}

	conn := connectTo(t, databaseDSN(t, dsn, roundTripDatabase))
	t.Cleanup(func() {
		_ = conn.Close(context.Background())
		dropRoundTripDatabase(t, admin)
		_ = admin.Close(context.Background())
	})
	assertOwnDatabase(t, conn)
	return conn
}

// assertOwnDatabase is the guard that makes every DOWN in this file safe: the
// connection about to run destructive SQL is proved to be pointing at the
// throwaway database and nothing else.
func assertOwnDatabase(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	var name string
	if err := conn.QueryRow(t.Context(), `SELECT current_database()`).Scan(&name); err != nil {
		t.Fatalf("read current database: %v", err)
	}
	if name != roundTripDatabase {
		t.Fatalf("refusing to run migrations against %q, expected %q", name, roundTripDatabase)
	}
}

func dropRoundTripDatabase(t *testing.T, admin *pgx.Conn) {
	t.Helper()
	if _, err := admin.Exec(context.Background(),
		`DROP DATABASE IF EXISTS `+roundTripDatabase+` WITH (FORCE)`); err != nil {
		t.Fatalf("drop round-trip database: %v", err)
	}
}

// databaseDSN rewrites which database the opt-in DSN points at, keeping its
// host, credentials and parameters.
func databaseDSN(t *testing.T, dsn, database string) string {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse test DSN: %v", err)
	}
	parsed.Path = "/" + database
	return parsed.String()
}

func connectTo(t *testing.T, dsn string) *pgx.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	return conn
}

// ── applying the real files ─────────────────────────────────────────────────

func migrationsRoot(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve migration path")
	}
	return filepath.Join(filepath.Dir(currentFile), "..", "..", "..", "..", "migrations")
}

// migrationsUnderTest are the outbox migrations this file applies by hand. Every
// other up migration is baseline.
var migrationsUnderTest = map[string]struct{}{
	migrationUp42: {}, migrationUp43: {}, migrationUp44: {},
}

// baselineMigrations lists every up migration the canonical runner applies
// before the ones under test, in the order it applies them: scripts/db/migrate.sh
// collects them with `find "$MIGRATIONS_DIR" -name "*.up.sql" | sort`, which is
// this glob and this sort. The files under test are excluded so the database
// arrives at exactly the state that precedes them.
func baselineMigrations(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(migrationsRoot(t), "*", "*.up.sql"))
	if err != nil {
		t.Fatalf("list migrations: %v", err)
	}
	sort.Strings(paths)
	baseline := make([]string, 0, len(paths))
	for _, path := range paths {
		if _, underTest := migrationsUnderTest[filepath.Base(path)]; underTest {
			continue
		}
		baseline = append(baseline, path)
	}
	if len(baseline) != len(paths)-len(migrationsUnderTest) {
		t.Fatalf("expected every migration under test to be excluded, kept %d of %d",
			len(baseline), len(paths))
	}
	return baseline
}

func applyBaselineMigrations(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	for _, path := range baselineMigrations(t) {
		contents, err := os.ReadFile(path) //nolint:gosec // Glob is restricted to the repository migration directory.
		if err != nil {
			t.Fatalf("read %s: %v", filepath.Base(path), err)
		}
		if _, err := conn.Exec(t.Context(), string(contents)); err != nil {
			t.Fatalf("apply %s: %v", filepath.Base(path), err)
		}
	}
}

// applyMigration executes one migration file verbatim.
func applyMigration(t *testing.T, conn *pgx.Conn, name string) {
	t.Helper()
	assertOwnDatabase(t, conn)
	if _, err := conn.Exec(t.Context(), readChatMigration(t, name)); err != nil {
		t.Fatalf("apply %s: %v", name, err)
	}
}

// ── catalogue predicates ────────────────────────────────────────────────────

func queryBool(t *testing.T, conn *pgx.Conn, sql string, args ...any) bool {
	t.Helper()
	var result bool
	if err := conn.QueryRow(t.Context(), sql, args...).Scan(&result); err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return result
}

func hasTable(t *testing.T, conn *pgx.Conn, qualified string) bool {
	t.Helper()
	return queryBool(t, conn, `SELECT to_regclass($1) IS NOT NULL`, qualified)
}

func hasColumn(t *testing.T, conn *pgx.Conn, schema, table, column string) bool {
	t.Helper()
	return queryBool(t, conn, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = $1 AND table_name = $2 AND column_name = $3
		)`, schema, table, column)
}

// hasConstraint reports whether the constraint exists, and constraintValidated
// whether the catalogue has read the existing rows against it — the difference
// 000043 exists to make.
func hasConstraint(t *testing.T, conn *pgx.Conn, qualified, name string) bool {
	t.Helper()
	return queryBool(t, conn, `
		SELECT EXISTS (
			SELECT 1 FROM pg_constraint
			WHERE conname = $2 AND conrelid = to_regclass($1)
		)`, qualified, name)
}

func constraintValidated(t *testing.T, conn *pgx.Conn, qualified, name string) bool {
	t.Helper()
	return queryBool(t, conn, `
		SELECT COALESCE(bool_and(convalidated), false) FROM pg_constraint
		WHERE conname = $2 AND conrelid = to_regclass($1)`, qualified, name)
}

func hasIndex(t *testing.T, conn *pgx.Conn, name string) bool {
	t.Helper()
	return queryBool(t, conn, `
		SELECT EXISTS (
			SELECT 1 FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE c.relname = $1 AND c.relkind = 'i' AND n.nspname = 'chat'
		)`, name)
}

func hasTrigger(t *testing.T, conn *pgx.Conn, qualified, name string) bool {
	t.Helper()
	return queryBool(t, conn, `
		SELECT EXISTS (
			SELECT 1 FROM pg_trigger
			WHERE tgname = $2 AND tgrelid = to_regclass($1) AND NOT tgisinternal
		)`, qualified, name)
}

func hasFunction(t *testing.T, conn *pgx.Conn, schema, name string) bool {
	t.Helper()
	return queryBool(t, conn, `
		SELECT EXISTS (
			SELECT 1 FROM pg_proc p
			JOIN pg_namespace n ON n.oid = p.pronamespace
			WHERE n.nspname = $1 AND p.proname = $2
		)`, schema, name)
}

func assertBool(t *testing.T, got, want bool, why string) {
	t.Helper()
	if got != want {
		t.Errorf("%s: got %t, want %t", why, got, want)
	}
}

// ── the contracts ───────────────────────────────────────────────────────────

const outboxTable = "chat.notification_outbox"

// assertLegacyContract describes chat.notification_outbox as 000006 and 000024
// left it: mention-only, four states, no event contract, no enforcement.
func assertLegacyContract(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	assertBool(t, hasTable(t, conn, outboxTable), true, "the outbox predates #741 and must exist")
	for _, column := range []string{"dedupe_key", "occurred_at", "origin", "priority", "source_type",
		"suppressed_reason", "processed_at", "updated_at"} {
		assertBool(t, hasColumn(t, conn, "chat", "notification_outbox", column), false,
			"column "+column+" belongs to 000042")
	}
	assertBool(t, hasColumn(t, conn, "chat", "message_pending_mentions", "kind"), false,
		"the parking table's classification belongs to 000042")
	assertBool(t, hasTrigger(t, conn, outboxTable, outboxTransitionTrigger), false,
		"the transition trigger belongs to 000042")
	assertBool(t, hasFunction(t, conn, "chat", outboxTransitionFunction), false,
		"the transition function belongs to 000042")
	assertBool(t, hasIndex(t, conn, outboxDedupeIndex), false, "the dedupe index belongs to 000042")
	assertBool(t, hasIndex(t, conn, outboxLegacyPendingIndex), true,
		"000042's down must restore the index it replaced")
	assertLegacyVocabulary(t, conn)
}

// assertLegacyVocabulary proves the narrow contract by executing against it: the
// only kind the legacy table accepts is a mention, and the only states are the
// four 000006 declared.
func assertLegacyVocabulary(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	assertBool(t, hasConstraint(t, conn, outboxTable, outboxKindConstraint), true,
		"the legacy kind constraint must be back")
	assertBool(t, hasConstraint(t, conn, outboxTable, outboxLegacyUniqueConstraint), true,
		"the legacy unique constraint predates #741 and must survive")
	assertBool(t, queryBool(t, conn, `
		SELECT pg_get_constraintdef(oid) = 'CHECK ((kind = ''mention''::text))'
		FROM pg_constraint WHERE conname = $1 AND conrelid = to_regclass($2)`,
		outboxKindConstraint, outboxTable), true, "the legacy kind vocabulary must be restored")
	assertBool(t, queryBool(t, conn, `
		SELECT pg_get_constraintdef(oid) NOT LIKE '%suppressed%'
		FROM pg_constraint WHERE conname = $1 AND conrelid = to_regclass($2)`,
		outboxStatusConstraint, outboxTable), true, "the legacy state vocabulary must be restored")
}

// assertCurrentContract describes the table after 000042. validated says whether
// 000043 has run: it is the only thing that distinguishes the two states, so it
// is what the intermediate step reads.
func assertCurrentContract(t *testing.T, conn *pgx.Conn, validated bool) {
	t.Helper()
	for _, column := range []string{"dedupe_key", "occurred_at", "origin", "priority", "source_type",
		"suppressed_reason", "processed_at", "updated_at"} {
		assertBool(t, hasColumn(t, conn, "chat", "notification_outbox", column), true,
			"column "+column+" must exist after 000042")
	}
	for _, column := range []string{"kind", "priority"} {
		assertBool(t, hasColumn(t, conn, "chat", "message_pending_mentions", column), true,
			"the parking table must carry its classification")
	}
	assertBool(t, hasTrigger(t, conn, outboxTable, outboxTransitionTrigger), true,
		"the transition trigger must exist after 000042")
	assertBool(t, hasFunction(t, conn, "chat", outboxTransitionFunction), true,
		"the transition function must exist after 000042")
	assertBool(t, hasIndex(t, conn, outboxDedupeIndex), true, "the dedupe index must exist after 000042")
	assertBool(t, hasIndex(t, conn, outboxOpenIndex), true, "the worker index must exist after 000042")
	assertBool(t, hasIndex(t, conn, outboxLegacyPendingIndex), false,
		"000042 replaces the narrow pending index")
	assertBool(t, hasConstraint(t, conn, outboxTable, outboxLegacyUniqueConstraint), true,
		"the legacy unique constraint is retained through the expand window")
	assertConstraintsValidated(t, conn, validated)
}

func assertConstraintsValidated(t *testing.T, conn *pgx.Conn, validated bool) {
	t.Helper()
	for _, name := range outboxValidatedConstraints {
		assertBool(t, hasConstraint(t, conn, outboxTable, name), true, "constraint "+name+" must exist")
		assertBool(t, constraintValidated(t, conn, outboxTable, name), validated,
			"constraint "+name+" validated")
	}
}

// ── data that has to survive the round trip ─────────────────────────────────

const (
	roundTripUser    = "74100000-0000-4000-8000-0000000000b1"
	roundTripMessage = "74100000-0000-4000-8000-0000000000b2"
	// A second recipient, so the previous release's row shape can be seeded
	// alongside the current one without colliding on the legacy unique
	// constraint over (message_id, recipient_user_id, kind).
	roundTripLegacyUser = "74100000-0000-4000-8000-0000000000b3"
	// The workspace and #geral channel migration 000001 seeds.
	roundTripWorkspace = "00000000-0000-0000-0000-000000000001"
	roundTripChannel   = "00000000-0000-0000-0000-000000000002"
)

func execOn(t *testing.T, conn *pgx.Conn, sql string, args ...any) {
	t.Helper()
	if _, err := conn.Exec(t.Context(), sql, args...); err != nil {
		t.Fatalf("exec: %v", err)
	}
}

// seedCompatibleRows writes the minimum real data the rollback has to carry:
// one notification written the way the current producer writes it, and one
// written the way the previous release does — no new columns at all. Both are
// expressible in the legacy contract, which is what makes them the right proof
// that a supported rollback works.
func seedCompatibleRows(t *testing.T, conn *pgx.Conn) string {
	t.Helper()
	execOn(t, conn, `INSERT INTO auth.users (id, email, display_name) VALUES
		($1, 'round-trip-741@e.test', 'Round trip'),
		($2, 'round-trip-741-legacy@e.test', 'Legacy shape')`, roundTripUser, roundTripLegacyUser)
	execOn(t, conn, `INSERT INTO chat.workspace_members (workspace_id, user_id, status) VALUES
		($1, $2, 'active'), ($1, $3, 'active')`, roundTripWorkspace, roundTripUser, roundTripLegacyUser)
	execOn(t, conn, `INSERT INTO chat.messages
			(id, workspace_id, channel_id, sender_id, kind, body_text, body_format, status)
		VALUES ($1, $2, $3, $4, 'user', 'round trip', 'v2', 'active')`,
		roundTripMessage, roundTripWorkspace, roundTripChannel, roundTripUser)

	var current string
	if err := conn.QueryRow(t.Context(), `
		INSERT INTO chat.notification_outbox
			(workspace_id, message_id, recipient_user_id, kind, status,
			 source_type, occurred_at, priority, origin, dedupe_key)
		SELECT $1::uuid, $2::uuid, $3::uuid, 'mention', 'pending',
		       'message', m.created_at, 'high', 'live',
		       'message:' || m.id::text || ':mention'
		FROM chat.messages m WHERE m.id = $2::uuid
		RETURNING id::text`,
		roundTripWorkspace, roundTripMessage, roundTripUser).Scan(&current); err != nil {
		t.Fatalf("seed current-shaped notification: %v", err)
	}
	// The previous release's INSERT names none of the new columns, and its
	// parking row names no classification. Every one of those has to be
	// defaultable, or the expand migration would break a slot still running that
	// code — and both rows have to survive the rollback unchanged.
	execOn(t, conn, `INSERT INTO chat.notification_outbox
			(workspace_id, message_id, recipient_user_id, kind, status)
		VALUES ($1, $2, $3, 'mention', 'pending')`,
		roundTripWorkspace, roundTripMessage, roundTripLegacyUser)
	execOn(t, conn, `INSERT INTO chat.message_pending_mentions (message_id, user_id)
		VALUES ($1, $2)`, roundTripMessage, roundTripUser)
	return current
}

// assertEnforcementWorks drives the real storage layer against the freshly
// migrated schema. The contract is not only present, it decides.
func assertEnforcementWorks(t *testing.T, conn *pgx.Conn, notificationID string) {
	t.Helper()
	store := storage.NewPGXNotificationOutboxStore(conn)
	for _, step := range [][2]notificationevent.State{
		{notificationevent.StatePending, notificationevent.StateEligible},
		{notificationevent.StateEligible, notificationevent.StateProcessing},
		{notificationevent.StateProcessing, notificationevent.StateSent},
	} {
		if err := store.TransitionState(t.Context(), storage.NotificationTransitionInput{
			NotificationID: notificationID, From: step[0], To: step[1],
		}); err != nil {
			t.Fatalf("TransitionState %q -> %q: %v", step[0], step[1], err)
		}
	}
	// sent is terminal, and the trigger is what makes that true of the database
	// rather than only of the Go constants.
	_, err := conn.Exec(t.Context(),
		`UPDATE chat.notification_outbox SET status = 'retrying' WHERE id = $1::uuid`, notificationID)
	if !isCheckViolation(err) {
		t.Errorf("escaping a terminal state returned %v, want a refusal from the trigger", err)
	}
}

// assertWritesStillChecked is the behaviour that separates the intermediate
// state from a missing constraint: 000043's down leaves every constraint NOT
// VALID, and a NOT VALID constraint still refuses every new row.
func assertWritesStillChecked(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	_, err := conn.Exec(t.Context(), `
		INSERT INTO chat.notification_outbox (workspace_id, message_id, recipient_user_id, kind)
		VALUES ($1, $2, $3, 'gossip')`, roundTripWorkspace, roundTripMessage, roundTripUser)
	if !isCheckViolation(err) {
		t.Errorf("an undeclared kind returned %v, want a refusal even from an unvalidated constraint", err)
	}
}

// assertPreexistingSchemaIntact proves the rollback took away only what #741
// added. A down migration that reached past its own change would be the most
// expensive kind of mistake to discover in production.
func assertPreexistingSchemaIntact(t *testing.T, conn *pgx.Conn, notificationID string) {
	t.Helper()
	for _, table := range []string{
		"chat.messages", "chat.channels", "chat.workspaces", "chat.dm_conversations",
		"chat.message_pending_mentions", "chat.message_reactions", "auth.users", "files.attachments",
	} {
		assertBool(t, hasTable(t, conn, table), true, table+" predates #741 and must survive its rollback")
	}
	assertBool(t, queryBool(t, conn,
		`SELECT EXISTS (SELECT 1 FROM chat.notification_outbox WHERE id = $1::uuid AND kind = 'mention')`,
		notificationID), true, "a notification written before the rollback must still be readable")
	assertBool(t, queryBool(t, conn,
		`SELECT count(*) = 2 FROM chat.notification_outbox`), true,
		"the current-shaped and previous-release-shaped notifications must both survive")
	assertBool(t, queryBool(t, conn,
		`SELECT EXISTS (SELECT 1 FROM chat.messages WHERE id = $1::uuid)`, roundTripMessage), true,
		"the message the notifications name must survive")
}

// assertClaimContract describes what 000044 adds: the three columns the claim
// protocol writes, and the partial index its claim query is ordered to match.
//
// present is what makes the same function serve both halves of the round trip.
func assertClaimContract(t *testing.T, conn *pgx.Conn, present bool) {
	t.Helper()
	for _, column := range []string{"attempts", "next_attempt_at", "last_error"} {
		assertBool(t, hasColumn(t, conn, "chat", "notification_outbox", column), present,
			"column "+column+" belongs to 000044")
	}
	assertBool(t, hasIndex(t, conn, outboxClaimableIndex), present,
		"the claim index belongs to 000044")
	// The bound on last_error is the security control, not a formatting choice:
	// unbounded, it is where a provider's error body would end up.
	if present {
		assertBool(t, queryBool(t, conn, `
			SELECT character_maximum_length = 64
			FROM information_schema.columns
			WHERE table_schema = 'chat' AND table_name = 'notification_outbox'
			  AND column_name = 'last_error'`), true,
			"last_error must stay too small to hold a provider payload")
	}
}

// ── the round trip ──────────────────────────────────────────────────────────

// The migrations are applied, reverted and applied again, in the order the
// runner would use, with real data in the table throughout.
func TestNotificationOutboxMigrationRoundTripPostgreSQL(t *testing.T) {
	conn := newMigrationRoundTripDatabase(t)

	applyBaselineMigrations(t, conn)
	assertLegacyContract(t, conn)

	applyMigration(t, conn, migrationUp42)
	applyMigration(t, conn, migrationUp43)
	assertCurrentContract(t, conn, true)
	assertClaimContract(t, conn, false)

	applyMigration(t, conn, migrationUp44)
	assertCurrentContract(t, conn, true)
	assertClaimContract(t, conn, true)

	notificationID := seedCompatibleRows(t, conn)
	assertEnforcementWorks(t, conn, notificationID)

	// 000044's down takes away only the claim state; the event contract beneath
	// it must be untouched, which is what makes the two migrations independently
	// revertible.
	applyMigration(t, conn, migrationDown44)
	assertClaimContract(t, conn, false)
	assertCurrentContract(t, conn, true)

	// 000043 owns nothing but the validation of 000042's constraints, so its down
	// must leave every object 000042 created standing.
	applyMigration(t, conn, migrationDown43)
	assertCurrentContract(t, conn, false)
	assertWritesStillChecked(t, conn)

	applyMigration(t, conn, migrationDown42)
	assertLegacyContract(t, conn)
	assertPreexistingSchemaIntact(t, conn, notificationID)

	// Re-up. Anything the down forgot to remove surfaces here as an "already
	// exists", which is the failure mode a text-matching test cannot see.
	applyMigration(t, conn, migrationUp42)
	applyMigration(t, conn, migrationUp43)
	applyMigration(t, conn, migrationUp44)
	assertCurrentContract(t, conn, true)
	assertClaimContract(t, conn, true)
	assertReUpAcceptsWork(t, conn)
}

// assertReUpAcceptsWork proves the re-applied contract is not merely present but
// working: a row is written through the restored dedupe identity and moved
// through the restored state machine.
func assertReUpAcceptsWork(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	var id string
	if err := conn.QueryRow(t.Context(), `
		INSERT INTO chat.notification_outbox
			(workspace_id, message_id, recipient_user_id, kind, status,
			 source_type, occurred_at, priority, origin, dedupe_key)
		SELECT $1::uuid, $2::uuid, $3::uuid, 'reply', 'pending',
		       'message', m.created_at, 'high', 'live',
		       'message:' || m.id::text || ':reply'
		FROM chat.messages m WHERE m.id = $2::uuid
		RETURNING id::text`,
		roundTripWorkspace, roundTripMessage, roundTripUser).Scan(&id); err != nil {
		t.Fatalf("write through the re-applied contract: %v", err)
	}
	store := storage.NewPGXNotificationOutboxStore(conn)
	if err := store.TransitionState(t.Context(), storage.NotificationTransitionInput{
		NotificationID:   id,
		From:             notificationevent.StatePending,
		To:               notificationevent.StateSuppressed,
		SuppressedReason: "quiet_hours",
	}); err != nil {
		t.Fatalf("transition through the re-applied machine: %v", err)
	}
	assertBool(t, queryBool(t, conn, `
		SELECT status = 'suppressed' AND suppressed_reason = 'quiet_hours' AND processed_at IS NOT NULL
		FROM chat.notification_outbox WHERE id = $1::uuid`, id), true,
		"the re-applied contract must record a suppression in full")
}

// The down is honest about what it cannot carry. A state the legacy vocabulary
// has no word for is refused rather than silently rewritten into a different
// outcome — turning a deliberate suppression into a delivery or a failure would
// be worse than refusing the rollback.
func TestNotificationOutboxMigrationDownRefusesUnrepresentableStatePostgreSQL(t *testing.T) {
	conn := newMigrationRoundTripDatabase(t)
	applyBaselineMigrations(t, conn)
	applyMigration(t, conn, migrationUp42)
	applyMigration(t, conn, migrationUp43)
	notificationID := seedCompatibleRows(t, conn)

	store := storage.NewPGXNotificationOutboxStore(conn)
	if err := store.TransitionState(t.Context(), storage.NotificationTransitionInput{
		NotificationID:   notificationID,
		From:             notificationevent.StatePending,
		To:               notificationevent.StateSuppressed,
		SuppressedReason: "conversation_muted",
	}); err != nil {
		t.Fatalf("suppress: %v", err)
	}

	assertOwnDatabase(t, conn)
	if _, err := conn.Exec(t.Context(), readChatMigration(t, migrationDown43)); err != nil {
		t.Fatalf("apply %s: %v", migrationDown43, err)
	}
	_, err := conn.Exec(t.Context(), readChatMigration(t, migrationDown42))
	if !isCheckViolation(err) {
		t.Fatalf("rolling back over a suppressed notification returned %v, want a refusal", err)
	}
	// A migration file is one BEGIN ... COMMIT batch. PostgreSQL skips the rest
	// of the batch when a statement fails, COMMIT included, so the transaction is
	// left open and aborted — the canonical runner reaches the same point by
	// exiting under ON_ERROR_STOP and dropping the connection. Here the
	// connection is reused, so it is unwound explicitly before anything is read.
	execOn(t, conn, `ROLLBACK`)

	// The refusal is atomic: the migration's own transaction took nothing with it.
	assertCurrentContract(t, conn, false)

	// With the row the legacy contract can express, the same rollback succeeds.
	execOn(t, conn, `DELETE FROM chat.notification_outbox WHERE id = $1::uuid`, notificationID)
	applyMigration(t, conn, migrationDown42)
	assertLegacyContract(t, conn)
}
