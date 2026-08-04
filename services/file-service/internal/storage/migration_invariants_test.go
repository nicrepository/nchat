package storage_test

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/services/file-service/internal/crypto"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
)

// readFilesMigration loads a migration from the repository so the schema
// invariants this service depends on are asserted without a live database. The
// runtime behaviour of the SQL is covered by make migrations-smoke.
func readFilesMigration(t *testing.T, name string) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve migration test path")
	}
	path := filepath.Join(filepath.Dir(currentFile), "..", "..", "..", "..", "migrations", "files", name)
	contents, err := os.ReadFile(path) //nolint:gosec // Test callers pass fixed migration filenames.
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(contents)
}

// executableSQL strips comments *and* single-quoted string literals, so a check
// for a forbidden statement reads only what the server would execute.
//
// It matters here because the migrations raise exceptions whose HINT text names
// the operations an operator must not perform ("Do not TRUNCATE, DELETE ...").
// Scanning the raw file would flag that advice as if it were a statement, which
// is exactly the fragile matching these tests are meant to avoid.
func executableSQL(migration string) string {
	stripped := sqlOnly(migration)
	var out strings.Builder
	inLiteral := false
	for _, r := range stripped {
		switch {
		case r == '\'':
			inLiteral = !inLiteral
			out.WriteRune(' ')
		case inLiteral:
			out.WriteRune(' ')
		default:
			out.WriteRune(r)
		}
	}
	return out.String()
}

// sqlOnly strips comments so an invariant check reads the statements rather
// than the prose that explains them.
func sqlOnly(migration string) string {
	var statements strings.Builder
	for _, line := range strings.Split(migration, "\n") {
		if index := strings.Index(line, "--"); index >= 0 {
			line = line[:index]
		}
		statements.WriteString(line)
		statements.WriteByte('\n')
	}
	return statements.String()
}

const attachmentsUpMigration = "000001_file_attachments.up.sql"
const attachmentsDownMigration = "000001_file_attachments.down.sql"

func TestAttachmentsMigrationIsTransactionalAndScopedToTheFilesSchema(t *testing.T) {
	up := readFilesMigration(t, attachmentsUpMigration)
	for _, expected := range []string{
		"BEGIN;", "COMMIT;",
		"CREATE SCHEMA IF NOT EXISTS files;",
		"CREATE TABLE files.attachments",
	} {
		if !strings.Contains(up, expected) {
			t.Fatalf("up migration must contain %q", expected)
		}
	}
}

// The public identifier must be a random UUID, never a sequence: attachment
// ids are handed to clients and must not be enumerable.
func TestAttachmentIDIsANonEnumerableUUID(t *testing.T) {
	up := readFilesMigration(t, attachmentsUpMigration)
	if !strings.Contains(up, "id                    UUID        PRIMARY KEY DEFAULT gen_random_uuid()") {
		t.Fatal("the primary key must be a random UUID")
	}
	statements := strings.ToUpper(sqlOnly(up))
	for _, forbidden := range []string{"SERIAL", "BIGSERIAL", "GENERATED ALWAYS AS IDENTITY"} {
		if strings.Contains(statements, forbidden) {
			t.Fatalf("the attachment id must not be sequential (%s)", forbidden)
		}
	}
}

func TestAttachmentsMigrationConstrainsTheStatusSet(t *testing.T) {
	up := readFilesMigration(t, attachmentsUpMigration)
	if !strings.Contains(up, "attachments_status_check") {
		t.Fatal("status must be constrained by a CHECK")
	}
	for _, status := range []domain.Status{
		domain.StatusPendingUpload, domain.StatusPendingScan, domain.StatusClean,
		domain.StatusRejected, domain.StatusFailed, domain.StatusDeleted,
	} {
		if !strings.Contains(up, "'"+string(status)+"'") {
			t.Fatalf("the status CHECK must allow %q", status)
		}
	}
	// A fresh row is never downloadable.
	if !strings.Contains(up, "DEFAULT 'pending_upload'") {
		t.Fatal("a new row must default to pending_upload")
	}
}

// Exactly one destination is a database invariant, not an application
// convention: neither both columns nor neither may be set.
func TestAttachmentsMigrationEnforcesDestinationExclusivity(t *testing.T) {
	up := readFilesMigration(t, attachmentsUpMigration)
	if !strings.Contains(up, "attachments_destination_exclusive_check") {
		t.Fatal("destination exclusivity must be a CHECK constraint")
	}
	for _, fragment := range []string{
		"destination_kind = 'channel' AND channel_id IS NOT NULL AND conversation_id IS NULL",
		"destination_kind = 'dm' AND conversation_id IS NOT NULL AND channel_id IS NULL",
	} {
		if !strings.Contains(up, fragment) {
			t.Fatalf("the exclusivity CHECK must contain %q", fragment)
		}
	}
	if !strings.Contains(up, "attachments_destination_kind_check") {
		t.Fatal("destination_kind must be constrained to the supported kinds")
	}
}

func TestAttachmentsMigrationBoundsTextColumnsAndSizes(t *testing.T) {
	up := readFilesMigration(t, attachmentsUpMigration)
	if !strings.Contains(up, "char_length(original_filename) BETWEEN 1 AND 255") {
		t.Fatal("the filename column must be bounded to match domain.MaxFilenameBytes")
	}
	if domain.MaxFilenameBytes != 255 {
		t.Fatalf("domain and schema disagree on the filename bound: %d", domain.MaxFilenameBytes)
	}
	if !strings.Contains(up, "size_bytes >= 0 AND ciphertext_size_bytes >= 0") {
		t.Fatal("sizes must be non-negative")
	}
	if !strings.Contains(up, "attachments_storage_object_key_unique") {
		t.Fatal("two attachments must not be able to claim the same storage object")
	}
}

// The wrapped data key is stored as bytes; the plaintext key never has a column.
func TestAttachmentsMigrationStoresOnlyTheWrappedDataKey(t *testing.T) {
	up := readFilesMigration(t, attachmentsUpMigration)
	if !strings.Contains(up, "wrapped_dek           BYTEA       NOT NULL") {
		t.Fatal("the wrapped data key must be a NOT NULL BYTEA column")
	}
	for _, forbidden := range []string{
		"plaintext_dek", "data_key ", "master_key", "kek ",
	} {
		if strings.Contains(up, forbidden) {
			t.Fatalf("the schema must not carry %q", forbidden)
		}
	}
	if !strings.Contains(up, "envelope_version") {
		t.Fatal("the envelope version must be persisted so the format can evolve")
	}
}

func TestAttachmentsMigrationIndexesTheQueriesThisServiceRuns(t *testing.T) {
	up := readFilesMigration(t, attachmentsUpMigration)
	for _, index := range []string{
		"idx_attachments_channel",
		"idx_attachments_conversation",
		"idx_attachments_pending",
	} {
		if !strings.Contains(up, index) {
			t.Fatalf("expected index %q", index)
		}
	}
	// The pending index is what a future scan worker and orphan sweep read.
	if !strings.Contains(up, "WHERE status IN ('pending_upload', 'pending_scan')") {
		t.Fatal("the pending index must be partial")
	}
}

func TestAttachmentsDownMigrationIsReversibleAndNotDestructiveBeyondItsScope(t *testing.T) {
	down := readFilesMigration(t, attachmentsDownMigration)
	if !strings.Contains(down, "DROP TABLE IF EXISTS files.attachments;") {
		t.Fatal("the down migration must drop the table it created")
	}
	statements := sqlOnly(down)
	for _, forbidden := range []string{
		"DROP SCHEMA", "DROP DATABASE", "TRUNCATE", "DROP EXTENSION", "chat.", "auth.",
	} {
		if strings.Contains(statements, forbidden) {
			t.Fatalf("the down migration must not contain %q", forbidden)
		}
	}
	if !strings.Contains(down, "BEGIN;") || !strings.Contains(down, "COMMIT;") {
		t.Fatal("the down migration must be transactional")
	}
}

// files.* references chat.* by convention only, exactly like chat.* references
// auth.users. A cross-schema foreign key would couple the two domains'
// migration order.
func TestAttachmentsMigrationAvoidsCrossSchemaForeignKeys(t *testing.T) {
	up := readFilesMigration(t, attachmentsUpMigration)
	for _, line := range strings.Split(sqlOnly(up), "\n") {
		if strings.Contains(line, "REFERENCES") {
			t.Fatalf("unexpected foreign key in the files schema: %s", strings.TrimSpace(line))
		}
	}
}

const dekBindingUpMigration = "000002_attachment_dek_binding.up.sql"
const dekBindingDownMigration = "000002_attachment_dek_binding.down.sql"

// The guard is the executable form of "there are no legacy attachments". The
// pre-000002 wrapping format is not implemented by this build, so a row that
// survived the migration would be permanently unopenable; refusing to migrate is
// the only honest outcome.
func TestDEKBindingMigrationRefusesToRunAgainstExistingRows(t *testing.T) {
	up := readFilesMigration(t, dekBindingUpMigration)
	statements := sqlOnly(up)

	if !strings.Contains(statements, "SELECT count(*) INTO existing_rows FROM files.attachments;") {
		t.Fatal("the migration must count the existing rows itself")
	}
	if !strings.Contains(statements, "RAISE EXCEPTION") {
		t.Fatal("a non-empty table must raise, not warn")
	}
	// Any row at all, whatever its state: a pending row is as unopenable as a
	// finished one once the old binding is gone.
	for _, forbidden := range []string{"WHERE status", "deleted_at IS NULL", "WHERE deleted_at"} {
		if strings.Contains(statements, forbidden) {
			t.Fatalf("the guard must not filter rows by %q", forbidden)
		}
	}
	if !strings.Contains(up, "BEGIN;") || !strings.Contains(up, "COMMIT;") {
		t.Fatal("the migration must be transactional so a raise leaves nothing behind")
	}
}

// Counting an empty table proves nothing on its own: a writer running the
// previous build can insert between the count and the ALTER. ACCESS EXCLUSIVE,
// taken first and held to COMMIT by the surrounding transaction, is what closes
// that window.
func TestDEKBindingMigrationLocksTheTableBeforeItLooksAtIt(t *testing.T) {
	statements := sqlOnly(readFilesMigration(t, dekBindingUpMigration))

	lock := strings.Index(statements, "LOCK TABLE files.attachments IN ACCESS EXCLUSIVE MODE;")
	if lock < 0 {
		t.Fatal("the migration must take an ACCESS EXCLUSIVE lock on files.attachments")
	}
	begin := strings.Index(statements, "BEGIN;")
	if begin < 0 || begin > lock {
		t.Fatal("the lock must be inside the migration's transaction, or it would not be held")
	}
	// Nothing may observe or change the table before the lock is held.
	for _, later := range []string{"SELECT count(*)", "ALTER TABLE", "ADD COLUMN", "ADD CONSTRAINT"} {
		at := strings.Index(statements, later)
		if at >= 0 && at < lock {
			t.Fatalf("%q appears before the ACCESS EXCLUSIVE lock", later)
		}
	}
	// And nothing may release it early.
	for _, forbidden := range []string{"COMMIT;\nLOCK", "ROLLBACK", "SET LOCAL lock_timeout", "NOWAIT"} {
		if strings.Contains(statements, forbidden) {
			t.Fatalf("the migration must not weaken the lock with %q", forbidden)
		}
	}
	// The lock must be the only one and must precede the single COMMIT.
	if commit := strings.Index(statements, "COMMIT;"); commit < lock {
		t.Fatal("the lock must be acquired before the transaction commits")
	}
}

// Ordering is the whole point: the guard has to run before the first schema
// change, so an abort cannot leave a half-applied table even if some future
// edit made a statement non-transactional.
func TestDEKBindingGuardRunsBeforeAnySchemaChange(t *testing.T) {
	statements := sqlOnly(readFilesMigration(t, dekBindingUpMigration))

	guard := strings.Index(statements, "RAISE EXCEPTION")
	if guard < 0 {
		t.Fatal("the guard is missing")
	}
	for _, change := range []string{"ALTER TABLE", "ADD COLUMN", "ADD CONSTRAINT", "UPDATE ", "DROP NOT NULL"} {
		if at := strings.Index(statements, change); at >= 0 && at < guard {
			t.Fatalf("%q appears before the emptiness guard", change)
		}
	}
}

// The schema fence. A lock cannot stop an old writer that was queued behind it
// and is released the instant the migration commits, nor one that starts later:
// a mandatory column the previous build's INSERT does not name is what does.
func TestDEKBindingWrapVersionIsAMandatoryColumnWithoutADefault(t *testing.T) {
	up := readFilesMigration(t, dekBindingUpMigration)

	if !strings.Contains(up, "ADD COLUMN dek_wrap_version SMALLINT NOT NULL") {
		t.Fatal("dek_wrap_version must be NOT NULL, or an old INSERT would still succeed")
	}
	statements := executableSQL(up)
	// A DEFAULT — for any reason, including easing a rollout — would fill the
	// column for an INSERT that omits it and destroy the fence.
	if strings.Contains(strings.ToUpper(statements), "DEFAULT") {
		t.Fatal("no column added by this migration may have a DEFAULT")
	}
	// The fence only works if the version is present from creation. If it were
	// part of the completeness CHECK instead, an old writer could still create
	// the pending row.
	if strings.Contains(statements, "dek_wrap_version IS NOT NULL") {
		t.Fatal("dek_wrap_version must be NOT NULL by column definition, not by a status-conditional CHECK")
	}
	if strings.Contains(statements, "dek_wrap_version IS NULL") {
		t.Fatal("dek_wrap_version must never be allowed to be NULL")
	}
}

// The CHECK pins the one version this build implements, so the column cannot
// hold a value the service would refuse anyway. Reading the constant here keeps
// the SQL and the Go from drifting apart.
func TestDEKBindingWrapVersionCheckMatchesTheImplementedVersion(t *testing.T) {
	up := readFilesMigration(t, dekBindingUpMigration)
	expected := fmt.Sprintf("CHECK (dek_wrap_version = %d)", crypto.KeyWrapVersion)

	if !strings.Contains(up, expected) {
		t.Fatalf("the migration must pin the wrap version to %d: expected %q",
			crypto.KeyWrapVersion, expected)
	}
	if strings.Contains(sqlOnly(up), "dek_wrap_version = 0") {
		t.Fatal("zero must not be an accepted wrap version")
	}
}

func TestDEKBindingMigrationAddsTheBindingColumns(t *testing.T) {
	up := readFilesMigration(t, dekBindingUpMigration)
	for _, expected := range []string{
		"ADD COLUMN kek_key_id",
		"ADD COLUMN dek_wrap_version",
		"ALTER COLUMN wrapped_dek DROP NOT NULL",
		"attachments_kek_key_id_length_check",
		"attachments_dek_wrap_version_check",
		"attachments_dek_binding_complete_check",
	} {
		if !strings.Contains(up, expected) {
			t.Fatalf("the migration must contain %q", expected)
		}
	}
	// The wrapping format versions separately from the NCF1 content stream, so
	// the two columns must both exist and must not be conflated.
	if strings.Contains(sqlOnly(up), "envelope_version") {
		t.Fatal("the key wrap version must not be stored in envelope_version")
	}
	statements := strings.ToLower(sqlOnly(up))
	for _, forbidden := range []string{"kek_value", "kek_secret", "master_key", "plaintext_dek", "encryption_key"} {
		if strings.Contains(statements, forbidden) {
			t.Fatalf("the schema must not carry %q", forbidden)
		}
	}
}

// Dropping the key material during pending_upload is only safe because the
// finished states demand it back. Without this CHECK a row could reach clean
// with no key, and the size it records would be authenticated by nothing.
func TestDEKBindingIsMandatoryOnceAnUploadHasFinished(t *testing.T) {
	up := readFilesMigration(t, dekBindingUpMigration)
	for _, fragment := range []string{
		"status IN ('pending_upload', 'failed')",
		"wrapped_dek IS NOT NULL",
		"kek_key_id IS NOT NULL",
	} {
		if !strings.Contains(up, fragment) {
			t.Fatalf("the completeness CHECK must contain %q", fragment)
		}
	}
}

// A NOT NULL DEFAULT would stamp rows with a key id and a format version that
// never protected them. Since the table is provably empty, nothing needs one.
func TestDEKBindingMigrationDoesNotBackfillOrDefault(t *testing.T) {
	statements := strings.ToUpper(executableSQL(readFilesMigration(t, dekBindingUpMigration)))
	for _, forbidden := range []string{"DEFAULT", "UPDATE FILES.ATTACHMENTS", "INSERT INTO"} {
		if strings.Contains(statements, forbidden) {
			t.Fatalf("the binding columns must not be backfilled or defaulted (%s)", forbidden)
		}
	}
	if strings.Contains(statements, "ADD COLUMN KEK_KEY_ID TEXT NOT NULL") {
		t.Fatal("kek_key_id must be nullable so a pending row is representable")
	}
}

// Dropping kek_key_id and dek_wrap_version does not reverse anything: the
// wrapped data keys stay sealed under a binding the previous build cannot
// reconstruct. A rollback with rows present would succeed at the schema level
// and break every download, so it is refused before the first DROP.
func TestDEKBindingDownMigrationRefusesToRunAgainstExistingRows(t *testing.T) {
	statements := sqlOnly(readFilesMigration(t, dekBindingDownMigration))

	if !strings.Contains(statements, "SELECT count(*) INTO existing_rows FROM files.attachments;") {
		t.Fatal("the down migration must count the existing rows itself")
	}
	if !strings.Contains(statements, "RAISE EXCEPTION") {
		t.Fatal("a non-empty table must abort the rollback, not warn")
	}
	// Every row blocks it, whatever its status: a pending upload would be
	// stranded exactly like a downloadable one.
	for _, forbidden := range []string{
		"WHERE status", "status IN", "status =", "deleted_at IS NULL", "WHERE deleted_at",
		"wrapped_dek IS", "kek_key_id IS",
	} {
		if strings.Contains(statements, forbidden) {
			t.Fatalf("the rollback guard must not filter rows by %q", forbidden)
		}
	}
}

// Ordering: the lock, then the check, then the first schema change. Anything
// else leaves a window in which a row can appear between the two.
func TestDEKBindingDownMigrationLocksAndChecksBeforeAnyDrop(t *testing.T) {
	statements := sqlOnly(readFilesMigration(t, dekBindingDownMigration))

	begin := strings.Index(statements, "BEGIN;")
	lock := strings.Index(statements, "LOCK TABLE files.attachments IN ACCESS EXCLUSIVE MODE;")
	count := strings.Index(statements, "SELECT count(*)")
	raise := strings.Index(statements, "RAISE EXCEPTION")

	if begin < 0 || lock < 0 || count < 0 || raise < 0 {
		t.Fatalf("missing statement: begin=%d lock=%d count=%d raise=%d", begin, lock, count, raise)
	}
	if begin >= lock || lock >= count || count >= raise {
		t.Fatal("the order must be BEGIN, LOCK, count, RAISE")
	}
	// No schema change, of any shape, may precede the guard.
	for _, change := range []string{
		"ALTER TABLE", "DROP CONSTRAINT", "DROP COLUMN", "SET NOT NULL", "ADD COLUMN",
	} {
		if at := strings.Index(statements, change); at >= 0 && at < raise {
			t.Fatalf("%q appears before the emptiness guard", change)
		}
	}
	// And the lock is not released before the work is done.
	if commit := strings.Index(statements, "COMMIT;"); commit < lock {
		t.Fatal("the lock must be held until the transaction commits")
	}
	for _, weakening := range []string{"ROLLBACK", "NOWAIT", "SET LOCAL lock_timeout"} {
		if strings.Contains(statements, weakening) {
			t.Fatalf("the down migration must not weaken the lock with %q", weakening)
		}
	}
}

// The guard must not be able to make itself pass by touching data, and the
// message has to say why the rollback is impossible without naming a row.
func TestDEKBindingDownMigrationNeverMutatesDataOrInventsCompatibility(t *testing.T) {
	down := readFilesMigration(t, dekBindingDownMigration)
	statements := strings.ToUpper(executableSQL(down))

	for _, forbidden := range []string{
		"DELETE FROM", "TRUNCATE", "UPDATE FILES.ATTACHMENTS", "INSERT INTO",
		"SET DEFAULT", "ADD COLUMN", "COALESCE",
	} {
		if strings.Contains(statements, forbidden) {
			t.Fatalf("the down migration must not execute %q", forbidden)
		}
	}
	// The operator has to learn that reverse rewrap is the missing piece.
	lowered := strings.ToLower(down)
	for _, expected := range []string{"rewrap is not implemented", "do not truncate"} {
		if !strings.Contains(lowered, expected) {
			t.Fatalf("the rollback message must mention %q", expected)
		}
	}
	// And nothing that identifies an attachment or its key material.
	for _, leak := range []string{
		"attachment_id", "SELECT id", "storage_object_key", "wrapped_dek FROM", "kek_key_id FROM",
	} {
		if strings.Contains(executableSQL(down), leak) {
			t.Fatalf("the rollback must not surface %q", leak)
		}
	}
}

func TestDEKBindingDownMigrationIsScopedAndTransactional(t *testing.T) {
	down := readFilesMigration(t, dekBindingDownMigration)
	for _, expected := range []string{
		"DROP COLUMN IF EXISTS dek_wrap_version",
		"DROP COLUMN IF EXISTS kek_key_id",
		"ALTER COLUMN wrapped_dek SET NOT NULL",
		// The rollback removes the fence, so it takes the same lock.
		"LOCK TABLE files.attachments IN ACCESS EXCLUSIVE MODE;",
		"BEGIN;", "COMMIT;",
	} {
		if !strings.Contains(down, expected) {
			t.Fatalf("the down migration must contain %q", expected)
		}
	}
	statements := executableSQL(down)
	for _, forbidden := range []string{
		"DROP SCHEMA", "DROP DATABASE", "DROP TABLE", "TRUNCATE", "DELETE FROM", "chat.", "auth.",
	} {
		if strings.Contains(statements, forbidden) {
			t.Fatalf("the down migration must not execute %q", forbidden)
		}
	}
	// Only the columns 000002 introduced. Nothing from 000001 may be touched
	// beyond restoring the constraint 000002 relaxed.
	for _, untouched := range []string{
		"size_bytes", "ciphertext_size_bytes", "storage_object_key", "envelope_version",
		"destination_kind", "channel_id", "conversation_id", "idx_attachments_",
	} {
		if strings.Contains(statements, untouched) {
			t.Fatalf("the down migration must not touch %q from migration 000001", untouched)
		}
	}
}
