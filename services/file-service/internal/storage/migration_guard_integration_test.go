package storage_test

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/services/file-service/internal/crypto"
)

// Opt-in integration coverage for migration 000002's emptiness guard.
//
// The structural tests in migration_invariants_test.go prove the guard is
// written and that it precedes every schema change. They cannot prove that
// PostgreSQL actually aborts on it, or that nothing survives the abort — only a
// real server can, and that is the claim the deployment safety rests on.
//
// Run with:
//
//	make dev-env-up
//	FILE_TEST_DATABASE_URL='postgresql://nchat:<password>@localhost:5432/nchat_test?sslmode=disable' \
//	  go test ./services/file-service/internal/storage/ -run MigrationGuardIntegration -v
//
// The suite skips without the variable, so the default run stays hermetic. Each
// case builds the whole files schema in a temporary schema of its own and drops
// it afterwards, so it never touches a real attachments table.
const migrationGuardDatabaseURLEnv = "FILE_TEST_DATABASE_URL"

// guardEnv is one throwaway schema plus the two migration files under test.
type guardEnv struct {
	pool   *pgxpool.Pool
	schema string
	up     string
	down   string
}

func newGuardEnv(t *testing.T) *guardEnv {
	t.Helper()

	dsn := os.Getenv(migrationGuardDatabaseURLEnv)
	if dsn == "" {
		t.Skipf("%s must be set", migrationGuardDatabaseURLEnv)
	}
	// The same guard the other integration suites use: never point a destructive
	// test at a database that is not obviously disposable.
	if !strings.Contains(dsn, "_test") {
		t.Fatalf("%s must point at a *_test database", migrationGuardDatabaseURLEnv)
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		t.Skipf("PostgreSQL at %s is not reachable: %v", migrationGuardDatabaseURLEnv, err)
	}
	t.Cleanup(pool.Close)

	// A private schema named after this run, so the migrations can be applied
	// verbatim — they say "files." — without colliding with anything real.
	schema := "files_guard_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	env := &guardEnv{
		pool:   pool,
		schema: schema,
		up:     rewriteSchema(readFilesMigration(t, dekBindingUpMigration), schema),
		down:   rewriteSchema(readFilesMigration(t, dekBindingDownMigration), schema),
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
	})

	base := rewriteSchema(readFilesMigration(t, attachmentsUpMigration), schema)
	// 000001 creates the schema itself; only its name has been substituted.
	if _, err := pool.Exec(ctx, base); err != nil {
		t.Fatalf("apply 000001 into %s: %v", schema, err)
	}
	return env
}

// rewriteSchema retargets a migration at a throwaway schema. Only the schema
// qualifier changes: every statement, constraint and guard is the file's own.
func rewriteSchema(migration, schema string) string {
	migration = strings.ReplaceAll(migration, "CREATE SCHEMA IF NOT EXISTS files;",
		"CREATE SCHEMA IF NOT EXISTS "+schema+";")
	return strings.ReplaceAll(migration, "files.attachments", schema+".attachments")
}

func (e *guardEnv) applyUp() error {
	_, err := e.pool.Exec(context.Background(), e.up)
	return err
}

// hasColumn reports whether the migration's schema change survived.
func (e *guardEnv) hasColumn(t *testing.T, column string) bool {
	t.Helper()
	var present bool
	err := e.pool.QueryRow(context.Background(), `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			 WHERE table_schema = $1 AND table_name = 'attachments' AND column_name = $2
		)`, e.schema, column).Scan(&present)
	if err != nil {
		t.Fatalf("inspect column %s: %v", column, err)
	}
	return present
}

// insertRow writes a row using exactly the column list the pre-000002 build
// emitted: no kek_key_id and no dek_wrap_version. It is what a legacy writer
// looks like to the database.
func (e *guardEnv) insertRow(t *testing.T, status string) {
	t.Helper()
	_, err := e.pool.Exec(context.Background(), `
		INSERT INTO `+e.schema+`.attachments (
			id, workspace_id, uploader_id, destination_kind, channel_id,
			original_filename, declared_mime, storage_provider, storage_object_key,
			envelope_version, wrapped_dek, status
		) VALUES ($1, $2, $3, 'channel', $4, 'legacy.pdf', 'application/pdf',
		          'seaweedfs', $5, 1, $6, $7)`,
		uuid.New(), uuid.New(), uuid.New(), uuid.New(),
		"nchat/attachments/"+uuid.NewString(), []byte{1, 2, 3}, status,
	)
	if err != nil {
		t.Fatalf("seed a %s row: %v", status, err)
	}
}

// The happy path: an empty table migrates, and the columns appear.
func TestMigrationGuardIntegrationAppliesToAnEmptyTable(t *testing.T) {
	env := newGuardEnv(t)

	if err := env.applyUp(); err != nil {
		t.Fatalf("000002 must apply to an empty table: %v", err)
	}
	for _, column := range []string{"kek_key_id", "dek_wrap_version"} {
		if !env.hasColumn(t, column) {
			t.Fatalf("expected column %s after a successful migration", column)
		}
	}
	// wrapped_dek has to be nullable now, or a pending row could not exist
	// before its key is sealed.
	var nullable string
	if err := env.pool.QueryRow(context.Background(), `
		SELECT is_nullable FROM information_schema.columns
		 WHERE table_schema = $1 AND table_name = 'attachments' AND column_name = 'wrapped_dek'`,
		env.schema).Scan(&nullable); err != nil {
		t.Fatalf("inspect wrapped_dek: %v", err)
	}
	if nullable != "YES" {
		t.Fatal("wrapped_dek must be nullable so a pending row can precede its key")
	}
}

// The claim under test: any pre-existing attachment blocks the deploy. The rows
// were wrapped under a binding this build cannot open, and there is no rewrap.
func TestMigrationGuardIntegrationRefusesAnyExistingRow(t *testing.T) {
	// Every state a row can be in, not merely the downloadable ones.
	for _, status := range []string{"pending_upload", "pending_scan", "clean", "rejected", "failed", "deleted"} {
		t.Run(status, func(t *testing.T) {
			env := newGuardEnv(t)
			env.insertRow(t, status)

			err := env.applyUp()
			if err == nil {
				t.Fatalf("000002 must refuse to run with a %s row present", status)
			}
			if !strings.Contains(err.Error(), "to be empty") {
				t.Fatalf("expected the emptiness guard to fire, got %v", err)
			}
			// The whole transaction rolls back: no column, no half-applied table.
			for _, column := range []string{"kek_key_id", "dek_wrap_version"} {
				if env.hasColumn(t, column) {
					t.Fatalf("column %s survived an aborted migration", column)
				}
			}
			// And the row itself is untouched — the migration never deletes or
			// converts anything to make itself pass.
			var remaining int
			if err := env.pool.QueryRow(context.Background(),
				`SELECT count(*) FROM `+env.schema+`.attachments`).Scan(&remaining); err != nil {
				t.Fatalf("count rows: %v", err)
			}
			if remaining != 1 {
				t.Fatalf("expected the existing row to be left alone, found %d", remaining)
			}
		})
	}
}

// The completeness CHECK is what lets wrapped_dek be nullable safely: pending
// may lack the binding, finished states may not.
func TestMigrationGuardIntegrationEnforcesTheBindingOnFinishedRows(t *testing.T) {
	env := newGuardEnv(t)
	if err := env.applyUp(); err != nil {
		t.Fatalf("apply 000002: %v", err)
	}

	insert := func(status string, wrapped []byte, keyID any, version any) error {
		_, err := env.pool.Exec(context.Background(), `
			INSERT INTO `+env.schema+`.attachments (
				id, workspace_id, uploader_id, destination_kind, channel_id,
				original_filename, declared_mime, storage_provider, storage_object_key,
				envelope_version, wrapped_dek, kek_key_id, dek_wrap_version, status
			) VALUES ($1, $2, $3, 'channel', $4, 'a.pdf', 'application/pdf',
			          'seaweedfs', $5, 1, $6, $7, $8, $9)`,
			uuid.New(), uuid.New(), uuid.New(), uuid.New(),
			"nchat/attachments/"+uuid.NewString(), wrapped, keyID, version, status,
		)
		return err
	}

	// The wrap version is always present — the column is NOT NULL from creation
	// — while the key material is legitimately absent until the upload finishes.
	if err := insert("pending_upload", nil, nil, crypto.KeyWrapVersion); err != nil {
		t.Fatalf("a pending row without key material must be legal: %v", err)
	}
	if err := insert("failed", nil, nil, crypto.KeyWrapVersion); err != nil {
		t.Fatalf("a failed row without key material must be legal: %v", err)
	}
	for _, status := range []string{"pending_scan", "clean", "rejected", "deleted"} {
		if err := insert(status, nil, nil, crypto.KeyWrapVersion); err == nil {
			t.Fatalf("a %s row without key material must violate the CHECK", status)
		}
	}
	// A complete binding is accepted in a finished state.
	if err := insert("clean", []byte{9, 9, 9}, "kek-2026-08", crypto.KeyWrapVersion); err != nil {
		t.Fatalf("a complete binding must be accepted: %v", err)
	}
	// An unsupported wrap version is not, and zero is not a compatibility value.
	if err := insert("clean", []byte{9, 9, 9}, "kek-2026-08", 99); err == nil {
		t.Fatal("an unsupported dek_wrap_version must violate the CHECK")
	}
	if err := insert("pending_upload", nil, nil, 0); err == nil {
		t.Fatal("zero must not be an accepted dek_wrap_version")
	}
	// And NULL is refused by the column itself: that is the schema fence.
	if err := insert("pending_upload", nil, nil, nil); err == nil {
		t.Fatal("dek_wrap_version must be NOT NULL")
	}
}

func (e *guardEnv) applyDown() error {
	_, err := e.pool.Exec(context.Background(), e.down)
	return err
}

// insertCurrent writes a row the way the current build does: the wrap version on
// the pending insert, the key material only once the upload has finished.
func (e *guardEnv) insertCurrent(t *testing.T, status string, wrapped []byte, keyID any) {
	t.Helper()
	_, err := e.pool.Exec(context.Background(), `
		INSERT INTO `+e.schema+`.attachments (
			id, workspace_id, uploader_id, destination_kind, channel_id,
			original_filename, declared_mime, detected_mime,
			size_bytes, ciphertext_size_bytes,
			storage_provider, storage_object_key,
			envelope_version, dek_wrap_version, wrapped_dek, kek_key_id, status
		) VALUES ($1, $2, $3, 'channel', $4, 'report.pdf', 'application/pdf', $5,
		          $6, $7, 'seaweedfs', $8, 1, $9, $10, $11, $12)`,
		uuid.New(), uuid.New(), uuid.New(), uuid.New(), detectedMIMEFor(status),
		sizeFor(status), ciphertextSizeFor(status),
		"nchat/attachments/"+uuid.NewString(),
		crypto.KeyWrapVersion, wrapped, keyID, status,
	)
	if err != nil {
		t.Fatalf("seed a %s row in the current format: %v", status, err)
	}
}

func detectedMIMEFor(status string) any {
	if status == "pending_upload" {
		return nil
	}
	return "application/pdf"
}

func sizeFor(status string) int64 {
	if status == "pending_upload" {
		return 0
	}
	return 4096
}

func ciphertextSizeFor(status string) int64 {
	if status == "pending_upload" {
		return 0
	}
	return 4124
}

// constraintNames lists the CHECKs currently on the table, so a test can assert
// that an aborted rollback removed none of them.
func (e *guardEnv) constraintNames(t *testing.T) []string {
	t.Helper()
	rows, err := e.pool.Query(context.Background(), `
		SELECT conname FROM pg_constraint
		 WHERE conrelid = ($1 || '.attachments')::regclass
		 ORDER BY conname`, e.schema)
	if err != nil {
		t.Fatalf("list constraints: %v", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan constraint: %v", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate constraints: %v", err)
	}
	return names
}

func (e *guardEnv) rowCount(t *testing.T) int {
	t.Helper()
	var count int
	if err := e.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM `+e.schema+`.attachments`).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return count
}

// 6.1 — an empty table rolls back cleanly and lands back on the 000001 schema.
func TestMigrationGuardIntegrationDownReversesAnEmptyTable(t *testing.T) {
	env := newGuardEnv(t)
	if err := env.applyUp(); err != nil {
		t.Fatalf("apply 000002: %v", err)
	}
	for _, column := range []string{"kek_key_id", "dek_wrap_version"} {
		if !env.hasColumn(t, column) {
			t.Fatalf("column %s must exist before the rollback", column)
		}
	}

	if err := env.applyDown(); err != nil {
		t.Fatalf("the rollback must succeed against an empty table: %v", err)
	}

	for _, column := range []string{"kek_key_id", "dek_wrap_version"} {
		if env.hasColumn(t, column) {
			t.Fatalf("column %s survived the down migration", column)
		}
	}
	// Everything 000001 created is untouched, including the NOT NULL that 000002
	// relaxed.
	for _, column := range []string{
		"wrapped_dek", "envelope_version", "size_bytes", "ciphertext_size_bytes",
		"storage_object_key", "destination_kind", "status",
	} {
		if !env.hasColumn(t, column) {
			t.Fatalf("the down migration dropped %q, which it did not create", column)
		}
	}
	var nullable string
	if err := env.pool.QueryRow(context.Background(), `
		SELECT is_nullable FROM information_schema.columns
		 WHERE table_schema = $1 AND table_name = 'attachments' AND column_name = 'wrapped_dek'`,
		env.schema).Scan(&nullable); err != nil {
		t.Fatalf("inspect wrapped_dek: %v", err)
	}
	if nullable != "NO" {
		t.Fatal("the rollback must restore the NOT NULL that 000001 declared")
	}
	// And the table is usable by the previous build again: its INSERT shape works.
	env.insertRow(t, "pending_upload")
	if got := env.rowCount(t); got != 1 {
		t.Fatalf("expected the 000001-shaped insert to land, found %d row(s)", got)
	}
}

// 6.2 / 6.3 / 6.4 — any row blocks the rollback, and the abort leaves the schema
// and the data exactly as they were.
//
// Dropping kek_key_id and dek_wrap_version does not convert anything: the
// wrapped keys stay sealed under a binding the previous build cannot
// reconstruct, so a "successful" rollback would break every download instead.
func TestMigrationGuardIntegrationDownRefusesAnyExistingRow(t *testing.T) {
	tests := map[string]func(*guardEnv, *testing.T){
		"pending upload": func(e *guardEnv, t *testing.T) {
			// No key material yet — legal, and still fatal to a rollback: the
			// upload in flight would finalise against a schema that has lost the
			// columns it needs.
			e.insertCurrent(t, "pending_upload", nil, nil)
		},
		"finished and downloadable": func(e *guardEnv, t *testing.T) {
			e.insertCurrent(t, "clean", []byte{9, 9, 9, 9}, "kek-2026-08")
		},
		"awaiting scan": func(e *guardEnv, t *testing.T) {
			e.insertCurrent(t, "pending_scan", []byte{1, 2, 3, 4}, "kek-2026-08")
		},
		"failed upload": func(e *guardEnv, t *testing.T) {
			e.insertCurrent(t, "failed", nil, nil)
		},
		"soft deleted": func(e *guardEnv, t *testing.T) {
			e.insertCurrent(t, "deleted", []byte{7, 7}, "kek-2026-08")
		},
	}
	for name, seed := range tests {
		t.Run(name, func(t *testing.T) {
			env := newGuardEnv(t)
			if err := env.applyUp(); err != nil {
				t.Fatalf("apply 000002: %v", err)
			}
			seed(env, t)
			constraintsBefore := env.constraintNames(t)

			err := env.applyDown()
			if err == nil {
				t.Fatal("the rollback must refuse to run while the table has rows")
			}
			if !strings.Contains(err.Error(), "cannot roll back migration 000002") {
				t.Fatalf("expected the rollback guard to fire, got %v", err)
			}

			// Atomicity: nothing was dropped.
			for _, column := range []string{"kek_key_id", "dek_wrap_version"} {
				if !env.hasColumn(t, column) {
					t.Fatalf("column %s was dropped by an aborted rollback", column)
				}
			}
			if got := env.constraintNames(t); !slices.Equal(got, constraintsBefore) {
				t.Fatalf("constraints changed during an aborted rollback: %v -> %v",
					constraintsBefore, got)
			}
			// And the row is untouched: the guard never deletes or converts
			// anything to make itself pass.
			if got := env.rowCount(t); got != 1 {
				t.Fatalf("expected the row to survive, found %d", got)
			}
			var wrapVersion int
			if err := env.pool.QueryRow(context.Background(),
				`SELECT dek_wrap_version FROM `+env.schema+`.attachments`).Scan(&wrapVersion); err != nil {
				t.Fatalf("read the surviving row: %v", err)
			}
			if wrapVersion != crypto.KeyWrapVersion {
				t.Fatalf("the row's binding was altered: wrap version %d", wrapVersion)
			}
		})
	}
}

// 6.5 — a writer running the *current* build races the rollback.
//
// It must not be able to insert between the emptiness check and the DROPs, and
// once the rollback commits its statement must fail: the columns it names are
// gone. Either way no row is created, so the rollback never strands one.
func TestMigrationGuardIntegrationDownRejectsConcurrentCurrentWriter(t *testing.T) {
	env := newGuardEnv(t)
	if err := env.applyUp(); err != nil {
		t.Fatalf("apply 000002: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	writer, writerPID := dedicatedWriter(ctx, t)

	rollback, err := env.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the rollback transaction: %v", err)
	}
	committed := false
	t.Cleanup(func() {
		if !committed {
			_ = rollback.Rollback(context.Background())
		}
	})

	if _, err := rollback.Exec(ctx,
		`LOCK TABLE `+env.schema+`.attachments IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatalf("acquire ACCESS EXCLUSIVE: %v", err)
	}
	var before int
	if err := rollback.QueryRow(ctx,
		`SELECT count(*) FROM `+env.schema+`.attachments`).Scan(&before); err != nil {
		t.Fatalf("count under the lock: %v", err)
	}
	if before != 0 {
		t.Fatalf("the table must be empty under the lock, found %d", before)
	}

	// The current build's INSERT, on its own connection.
	currentInsert := make(chan error, 1)
	go func() {
		_, insertErr := writer.Exec(ctx, `
			INSERT INTO `+env.schema+`.attachments (
				id, workspace_id, uploader_id, destination_kind, channel_id,
				original_filename, declared_mime, storage_provider, storage_object_key,
				envelope_version, dek_wrap_version, status
			) VALUES ($1, $2, $3, 'channel', $4, 'current.pdf', 'application/pdf',
			          'seaweedfs', $5, 1, $6, 'pending_upload')`,
			uuid.New(), uuid.New(), uuid.New(), uuid.New(),
			"nchat/attachments/"+uuid.NewString(), crypto.KeyWrapVersion,
		)
		currentInsert <- insertErr
	}()

	waitForBlockedInsert(ctx, t, env, writerPID)

	select {
	case err := <-currentInsert:
		t.Fatalf("the INSERT completed while the rollback held ACCESS EXCLUSIVE: %v", err)
	default:
	}

	// Apply what the rollback applies, then commit and release the writer.
	if _, err := rollback.Exec(ctx, `
		ALTER TABLE `+env.schema+`.attachments
			DROP CONSTRAINT IF EXISTS attachments_dek_binding_complete_check,
			DROP CONSTRAINT IF EXISTS attachments_dek_wrap_version_check,
			DROP CONSTRAINT IF EXISTS attachments_kek_key_id_length_check`); err != nil {
		t.Fatalf("drop the constraints: %v", err)
	}
	if _, err := rollback.Exec(ctx, `
		ALTER TABLE `+env.schema+`.attachments
			DROP COLUMN IF EXISTS dek_wrap_version,
			DROP COLUMN IF EXISTS kek_key_id`); err != nil {
		t.Fatalf("drop the columns: %v", err)
	}
	if _, err := rollback.Exec(ctx, `
		ALTER TABLE `+env.schema+`.attachments ALTER COLUMN wrapped_dek SET NOT NULL`); err != nil {
		t.Fatalf("restore the NOT NULL: %v", err)
	}
	if err := rollback.Commit(ctx); err != nil {
		t.Fatalf("commit the rollback: %v", err)
	}
	committed = true

	select {
	case err := <-currentInsert:
		if err == nil {
			t.Fatal("the INSERT succeeded after the rollback: it must fail on the removed columns")
		}
		if !strings.Contains(strings.ToLower(err.Error()), "dek_wrap_version") {
			t.Fatalf("expected a missing-column error for dek_wrap_version, got %v", err)
		}
	case <-ctx.Done():
		t.Fatal("the INSERT never completed after the rollback committed")
	}

	if got := env.rowCount(t); got != 0 {
		t.Fatalf("the rollback must leave no row behind, found %d", got)
	}
	// The final schema is 000001's.
	for _, column := range []string{"kek_key_id", "dek_wrap_version"} {
		if env.hasColumn(t, column) {
			t.Fatalf("column %s survived the rollback", column)
		}
	}
}

// Guards that the suite really is opt-in.
func TestMigrationGuardIntegrationSuiteIsOptIn(t *testing.T) {
	if os.Getenv(migrationGuardDatabaseURLEnv) == "" {
		t.Logf("%s is unset: the real-database migration cases are skipped", migrationGuardDatabaseURLEnv)
	}
}

// TestAttachmentDEKBindingMigrationRejectsConcurrentLegacyInsert is the race the
// review named, reproduced against a real server with two independent
// connections.
//
// The counting guard alone cannot close it: an instance running the previous
// build can begin an INSERT at any moment, and "the table was empty when I
// looked" says nothing about the instant afterwards. Two mechanisms are under
// test here, and both are needed:
//
//   - ACCESS EXCLUSIVE, taken before the count and held to COMMIT, makes the
//     concurrent INSERT block instead of landing between the count and the ALTER;
//   - dek_wrap_version NOT NULL with no DEFAULT makes that same INSERT fail once
//     it is released, because the previous build's column list does not name it.
//
// Without the second, the blocked statement would simply succeed the moment the
// migration committed and put a legacy row into the migrated table.
//
// Run with:
//
//	FILE_TEST_DATABASE_URL='postgresql://nchat:<password>@localhost:5432/nchat_test?sslmode=disable' \
//	  go test ./internal/storage -run TestAttachmentDEKBindingMigrationRejectsConcurrentLegacyInsert -count=1 -v
func TestAttachmentDEKBindingMigrationRejectsConcurrentLegacyInsert(t *testing.T) {
	env := newGuardEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// A dedicated connection for the writer, so it can never share a session
	// with the migration and be let through by holding the same lock, and so the
	// pid below identifies the backend that actually runs the INSERT.
	writer, writerPID := dedicatedWriter(ctx, t)

	// The migration runs inside an explicit transaction on its own connection so
	// the test can hold it open at the exact point the real migration would.
	migration, err := env.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the migration transaction: %v", err)
	}
	// Defensive: if any assertion below fails before the commit, the transaction
	// is rolled back rather than left holding an ACCESS EXCLUSIVE lock.
	committed := false
	t.Cleanup(func() {
		if !committed {
			_ = migration.Rollback(context.Background())
		}
	})

	if _, err := migration.Exec(ctx,
		`LOCK TABLE `+env.schema+`.attachments IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatalf("acquire ACCESS EXCLUSIVE: %v", err)
	}
	var before int
	if err := migration.QueryRow(ctx,
		`SELECT count(*) FROM `+env.schema+`.attachments`).Scan(&before); err != nil {
		t.Fatalf("count under the lock: %v", err)
	}
	if before != 0 {
		t.Fatalf("the table must be empty under the lock, found %d", before)
	}

	// The legacy writer, on its own connection, using exactly the column list the
	// previous build emitted.
	legacyInsert := make(chan error, 1)
	go func() {
		_, insertErr := writer.Exec(ctx, `
			INSERT INTO `+env.schema+`.attachments (
				id, workspace_id, uploader_id, destination_kind, channel_id,
				original_filename, declared_mime, storage_provider, storage_object_key,
				envelope_version, wrapped_dek, status
			) VALUES ($1, $2, $3, 'channel', $4, 'legacy.pdf', 'application/pdf',
			          'seaweedfs', $5, 1, $6, 'pending_upload')`,
			uuid.New(), uuid.New(), uuid.New(), uuid.New(),
			"nchat/attachments/"+uuid.NewString(), []byte{1, 2, 3},
		)
		legacyInsert <- insertErr
	}()

	// Wait for the writer to be genuinely blocked on the lock rather than merely
	// slow: pg_stat_activity is the server's own answer, so there is no sleep
	// standing in for synchronisation.
	waitForBlockedInsert(ctx, t, env, writerPID)

	// It must still be blocked — nothing may have slipped past the lock.
	select {
	case err := <-legacyInsert:
		t.Fatalf("the legacy INSERT completed while the migration held ACCESS EXCLUSIVE: %v", err)
	default:
	}

	// Apply what the migration applies, then commit and release the writer.
	if _, err := migration.Exec(ctx, `
		ALTER TABLE `+env.schema+`.attachments
			ALTER COLUMN wrapped_dek DROP NOT NULL,
			ADD COLUMN kek_key_id TEXT,
			ADD COLUMN dek_wrap_version SMALLINT NOT NULL`); err != nil {
		t.Fatalf("apply the schema change: %v", err)
	}
	if err := migration.Commit(ctx); err != nil {
		t.Fatalf("commit the migration: %v", err)
	}
	committed = true

	// The queued statement now runs against the new schema, and the fence — not
	// the lock — is what stops it.
	select {
	case err := <-legacyInsert:
		if err == nil {
			t.Fatal("the legacy INSERT succeeded after the migration committed: the schema fence is missing")
		}
		if !strings.Contains(strings.ToLower(err.Error()), "dek_wrap_version") {
			t.Fatalf("expected a not-null violation on dek_wrap_version, got %v", err)
		}
	case <-ctx.Done():
		t.Fatal("the legacy INSERT never completed after the migration committed")
	}

	// Nothing legacy landed.
	var remaining int
	if err := env.pool.QueryRow(ctx,
		`SELECT count(*) FROM `+env.schema+`.attachments`).Scan(&remaining); err != nil {
		t.Fatalf("count after the migration: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("the table must still be empty, found %d row(s)", remaining)
	}

	// And the new build's INSERT — the same columns plus dek_wrap_version — works.
	if _, err := env.pool.Exec(ctx, `
		INSERT INTO `+env.schema+`.attachments (
			id, workspace_id, uploader_id, destination_kind, channel_id,
			original_filename, declared_mime, storage_provider, storage_object_key,
			envelope_version, dek_wrap_version, status
		) VALUES ($1, $2, $3, 'channel', $4, 'current.pdf', 'application/pdf',
		          'seaweedfs', $5, 1, $6, 'pending_upload')`,
		uuid.New(), uuid.New(), uuid.New(), uuid.New(),
		"nchat/attachments/"+uuid.NewString(), crypto.KeyWrapVersion,
	); err != nil {
		t.Fatalf("the current build's INSERT must succeed after the migration: %v", err)
	}
}

// waitForBlockedInsert polls pg_stat_activity until the given backend is waiting
// on a lock. Polling the server's own view is what makes the test deterministic:
// it observes the state it needs instead of guessing at a delay.
//
// The wait is keyed on the writer's own backend pid, not on a LIKE over the
// query text: a shared test database can easily have another session blocked on
// something unrelated, and matching that would let the test proceed while the
// statement under test had not started.
func waitForBlockedInsert(ctx context.Context, t *testing.T, env *guardEnv, writerPID int) {
	t.Helper()

	deadline, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for {
		var blocked bool
		err := env.pool.QueryRow(deadline, `
			SELECT EXISTS (
				SELECT 1 FROM pg_stat_activity
				 WHERE pid = $1
				   AND state = 'active'
				   AND wait_event_type = 'Lock'
			)`, writerPID).Scan(&blocked)
		if err != nil {
			t.Fatalf("inspect pg_stat_activity: %v", err)
		}
		if blocked {
			return
		}
		select {
		case <-deadline.Done():
			t.Fatalf("backend %d never blocked on the ACCESS EXCLUSIVE lock", writerPID)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// dedicatedWriter opens a pool the test controls, pinned to a single connection
// so the statement it issues and the pid it reports are the same backend.
func dedicatedWriter(ctx context.Context, t *testing.T) (*pgxpool.Pool, int) {
	t.Helper()
	config, err := pgxpool.ParseConfig(os.Getenv(migrationGuardDatabaseURLEnv))
	if err != nil {
		t.Fatalf("parse the writer DSN: %v", err)
	}
	// One connection: pg_backend_pid() must describe the backend that will run
	// the INSERT, which a multi-connection pool cannot guarantee.
	config.MinConns, config.MaxConns = 1, 1
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect the writer: %v", err)
	}
	t.Cleanup(pool.Close)

	var pid int
	if err := pool.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatalf("read the writer backend pid: %v", err)
	}
	return pool, pid
}
