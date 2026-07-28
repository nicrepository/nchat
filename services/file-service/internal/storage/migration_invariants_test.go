package storage_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

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
