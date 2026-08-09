package storage_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Opt-in integration coverage for migration 000005's backfill (RF-22).
//
// The structural tests in migration_invariants_test.go prove the statement is
// written the way it should be. They cannot prove what it does to a row — that
// a legacy 'clean' actually comes out as 'pending_scan', that a 'rejected' is
// actually left alone, that the CHECK constraints actually accept the result.
// Only a real server can, and that is the claim the deployment safety rests on:
// after this migration, no attachment is approved that a scan has not approved.
//
// Run with:
//
//	make dev-env-up
//	FILE_TEST_DATABASE_URL='postgresql://nchat:<password>@localhost:5432/nchat_test?sslmode=disable' \
//	  go test ./services/file-service/internal/storage/ -run ScanBackfillIntegration -v
//
// It skips without the variable, so the default run stays hermetic. Each case
// builds the files schema in a throwaway schema of its own and drops it after.
type scanBackfillEnv struct {
	pool   *pgxpool.Pool
	schema string
}

// newScanBackfillEnv applies 000001 through 000003 into a private schema and
// leaves the table empty, which is the state a legacy deployment's writer would
// have been filling.
//
// 000002's emptiness guard is why the ordering matters: it refuses to run with
// any row present, so the schema has to be brought fully up to date *before*
// anything legacy is seeded. 000005 is then applied by the test itself, which
// is the statement under examination.
func newScanBackfillEnv(t *testing.T) *scanBackfillEnv {
	t.Helper()

	dsn := os.Getenv(migrationGuardDatabaseURLEnv)
	if dsn == "" {
		t.Skipf("%s must be set", migrationGuardDatabaseURLEnv)
	}
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

	schema := "files_scan_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
	})

	for _, migration := range []string{
		attachmentsUpMigration, dekBindingUpMigration, previewsUpMigration,
	} {
		sql := rewriteSchema(readFilesMigration(t, migration), schema)
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatalf("apply %s into %s: %v", migration, schema, err)
		}
	}
	return &scanBackfillEnv{pool: pool, schema: schema}
}

// seed writes one attachment in the given status, with the key material a
// finished upload has. Every state except pending_upload and failed requires it
// (attachments_dek_binding_complete_check), so the row is what the previous
// build would actually have left behind.
func (e *scanBackfillEnv) seed(t *testing.T, status string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	sealed := status != "pending_upload" && status != "failed"

	var wrappedDEK []byte
	var kekKeyID any
	if sealed {
		wrappedDEK = []byte{1, 2, 3}
		kekKeyID = "kek-legacy"
	}
	_, err := e.pool.Exec(context.Background(), `
		INSERT INTO `+e.schema+`.attachments (
			id, workspace_id, uploader_id, destination_kind, channel_id,
			original_filename, declared_mime, storage_provider, storage_object_key,
			envelope_version, dek_wrap_version, wrapped_dek, kek_key_id, status
		) VALUES ($1, $2, $3, 'channel', $4, 'legacy.pdf', 'application/pdf',
		          'seaweedfs', $5, 1, 2, $6, $7, $8)`,
		id, uuid.New(), uuid.New(), uuid.New(),
		"nchat/attachments/"+id.String(), wrappedDEK, kekKeyID, status,
	)
	if err != nil {
		t.Fatalf("seed a %s row: %v", status, err)
	}
	return id
}

// softDelete marks a row removed, which every read path in the service filters
// on.
func (e *scanBackfillEnv) softDelete(t *testing.T, id uuid.UUID) {
	t.Helper()
	if _, err := e.pool.Exec(context.Background(),
		`UPDATE `+e.schema+`.attachments SET deleted_at = now() WHERE id = $1`, id); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
}

// applyScanMigration runs 000005 verbatim against the throwaway schema.
func (e *scanBackfillEnv) applyScanMigration(t *testing.T) {
	t.Helper()
	sql := rewriteSchema(readFilesMigration(t, scanUpMigration), e.schema)
	if _, err := e.pool.Exec(context.Background(), sql); err != nil {
		t.Fatalf("apply 000005: %v", err)
	}
}

type scanRowState struct {
	status        string
	attempts      int16
	nextAttemptAt *time.Time
	previewStatus string
}

func (e *scanBackfillEnv) read(t *testing.T, id uuid.UUID) scanRowState {
	t.Helper()
	var state scanRowState
	err := e.pool.QueryRow(context.Background(), `
		SELECT status, scan_attempts, scan_next_attempt_at, preview_status
		  FROM `+e.schema+`.attachments WHERE id = $1`, id,
	).Scan(&state.status, &state.attempts, &state.nextAttemptAt, &state.previewStatus)
	if err != nil {
		t.Fatalf("read row: %v", err)
	}
	return state
}

// The headline claim, against a real server: an approval nothing can vouch for
// does not survive the migration.
func TestScanBackfillIntegrationDemotesLegacyClean(t *testing.T) {
	env := newScanBackfillEnv(t)
	legacyClean := env.seed(t, "clean")

	env.applyScanMigration(t)

	state := env.read(t, legacyClean)
	if state.status != "pending_scan" {
		t.Fatalf("status = %q, want pending_scan: an unscanned approval survived", state.status)
	}
	if state.nextAttemptAt == nil {
		t.Fatal("a demoted row must be scheduled, or it is demoted and stranded")
	}
	if state.nextAttemptAt.After(time.Now().Add(time.Minute)) {
		t.Fatalf("next attempt is %v, want it due now", state.nextAttemptAt)
	}
	if state.attempts != 0 {
		t.Fatalf("scan_attempts = %d, want a fresh budget", state.attempts)
	}
}

// A row that was already awaiting a scan is queued, not disturbed otherwise.
func TestScanBackfillIntegrationSchedulesLegacyPendingScan(t *testing.T) {
	env := newScanBackfillEnv(t)
	pending := env.seed(t, "pending_scan")

	env.applyScanMigration(t)

	state := env.read(t, pending)
	if state.status != "pending_scan" {
		t.Fatalf("status = %q, want pending_scan", state.status)
	}
	if state.nextAttemptAt == nil {
		t.Fatal("a legacy pending_scan row was left with no schedule and would never be claimed")
	}
}

// Every other state is left exactly as it was. Two of these are security
// properties: a condemned file must not be reopened for "a fresh look", and an
// upload that never finished must not be promoted into one that did.
func TestScanBackfillIntegrationLeavesEveryOtherStateAlone(t *testing.T) {
	for _, status := range []string{"rejected", "pending_upload", "failed", "deleted"} {
		t.Run(status, func(t *testing.T) {
			env := newScanBackfillEnv(t)
			id := env.seed(t, status)

			env.applyScanMigration(t)

			state := env.read(t, id)
			if state.status != status {
				t.Fatalf("status = %q, want it unchanged at %q", state.status, status)
			}
			if state.nextAttemptAt != nil {
				t.Fatalf("a %s row was queued for a scan it has no object for", status)
			}
		})
	}
}

// A removed attachment is unreachable through every read path, so queueing it
// would only spend the worker on a file nobody can obtain.
func TestScanBackfillIntegrationSkipsRemovedAttachments(t *testing.T) {
	env := newScanBackfillEnv(t)
	removedClean := env.seed(t, "clean")
	env.softDelete(t, removedClean)

	env.applyScanMigration(t)

	state := env.read(t, removedClean)
	if state.nextAttemptAt != nil {
		t.Fatal("a removed attachment was queued for a scan")
	}
}

// The migration must not become the thing that grants access. Nothing it does
// may leave a row downloadable that was not scanned — and after it has run, no
// row at all is in the downloadable state.
func TestScanBackfillIntegrationLeavesNothingApproved(t *testing.T) {
	env := newScanBackfillEnv(t)
	for _, status := range []string{"pending_upload", "pending_scan", "clean", "rejected", "failed", "deleted"} {
		env.seed(t, status)
	}

	env.applyScanMigration(t)

	var approved int
	if err := env.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM `+env.schema+`.attachments WHERE status = 'clean'`,
	).Scan(&approved); err != nil {
		t.Fatalf("count approved: %v", err)
	}
	if approved != 0 {
		t.Fatalf("%d attachment(s) are still approved with no scan behind them", approved)
	}
}

// The preview is left where it was. A demoted row's thumbnail stops being
// servable on its own — delivery reads the attachment's status — so a second
// write would only throw away a render that a re-approval can reuse.
func TestScanBackfillIntegrationDoesNotTouchThePreview(t *testing.T) {
	env := newScanBackfillEnv(t)
	legacyClean := env.seed(t, "clean")
	if _, err := env.pool.Exec(context.Background(),
		`UPDATE `+env.schema+`.attachments SET preview_status = 'unsupported' WHERE id = $1`,
		legacyClean); err != nil {
		t.Fatalf("set preview state: %v", err)
	}

	env.applyScanMigration(t)

	if got := env.read(t, legacyClean).previewStatus; got != "unsupported" {
		t.Fatalf("preview_status = %q, want it untouched", got)
	}
}

// Applying the backfill twice converges: the migration runner applies it once,
// but a determinstic statement is what makes a re-run during recovery safe.
func TestScanBackfillIntegrationIsIdempotent(t *testing.T) {
	env := newScanBackfillEnv(t)
	legacyClean := env.seed(t, "clean")
	rejected := env.seed(t, "rejected")

	env.applyScanMigration(t)
	// Only the backfill re-runs: the ALTERs would fail on a duplicate column,
	// which is the migration runner's job to prevent, not this statement's.
	if _, err := env.pool.Exec(context.Background(), `
		UPDATE `+env.schema+`.attachments
		   SET status = 'pending_scan', scan_attempts = 0, scan_next_attempt_at = now()
		 WHERE status IN ('pending_scan', 'clean') AND deleted_at IS NULL`); err != nil {
		t.Fatalf("re-apply the backfill: %v", err)
	}

	if got := env.read(t, legacyClean).status; got != "pending_scan" {
		t.Fatalf("status = %q after a second application, want pending_scan", got)
	}
	if got := env.read(t, rejected).status; got != "rejected" {
		t.Fatalf("a rejected row moved on the second application: %q", got)
	}
}
