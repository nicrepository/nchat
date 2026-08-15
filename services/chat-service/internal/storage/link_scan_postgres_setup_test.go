package storage_test

import (
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Shared setup for the RF-21 PostgreSQL tests.
//
// It connects and refuses anything that is not a _test database, and that is
// deliberately all it does: these tests run against the schema the real
// migrations produce, applied by scripts/db/migrate.sh before the suite.
//
// They do not rebuild the schema themselves, and that is a considered choice
// rather than an omission. Several older tests in this package drop and recreate
// `chat` alone; the RF-21 read paths join `files.attachments`, so a chat-only
// schema cannot serve them. The two styles therefore cannot share one process,
// which is a pre-existing property of this opt-in suite — the RF-21 tests are
// run against a freshly migrated database, which is how the surrounding
// documentation describes running them.
func newLinkScanTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("CHAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("CHAT_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	var databaseName string
	if err := pool.QueryRow(t.Context(), `SELECT current_database()`).Scan(&databaseName); err != nil {
		t.Fatalf("read current database: %v", err)
	}
	if !strings.HasSuffix(databaseName, "_test") {
		t.Fatalf("refusing destructive test against non-test database %q", databaseName)
	}
	return pool
}
