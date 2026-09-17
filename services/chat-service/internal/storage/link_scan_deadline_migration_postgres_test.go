package storage_test

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Blue-green contract of the issue #807 deadline (CQ round 3).
//
// Production runs two release slots against one database. While the slot
// running the previous release is still live it keeps writing chat.link_scans
// the way it always did — an INSERT that does not know deadline_at, and an
// UPDATE that reopens an expired verdict as pending without touching it. Every
// one of those rows must still converge: a pending row without an end is the
// state the issue exists to abolish. This file applies the migration to the
// schema that precedes it and then writes exactly what the old slot writes.

const (
	migrationUp50LinkTargets   = "000050_link_targets_convergence_and_previews.up.sql"
	migrationDown50LinkTargets = "000050_link_targets_convergence_and_previews.down.sql"

	pendingDeadlineConstraint = "link_scans_pending_deadline_check"
	pendingDeadlineTrigger    = "link_scans_pending_deadline"
)

// The statements the previous release runs, verbatim in shape: no deadline_at.
const (
	oldWriterInsert = `INSERT INTO chat.link_scans (canonical_url) VALUES ($1) ON CONFLICT (canonical_url) DO NOTHING`
	oldWriterReopen = `UPDATE chat.link_scans
		   SET status = 'pending', scan_uuid = NULL, decided_at = NULL, next_attempt_at = now(), updated_at = now()
		 WHERE canonical_url = $1`
)

// applyMigrationsBefore brings the disposable database to the state that
// precedes name: every up migration the canonical runner applies, in its
// order, except that one.
func applyMigrationsBefore(t *testing.T, conn *pgx.Conn, name string) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(migrationsRoot(t), "*", "*.up.sql"))
	if err != nil {
		t.Fatalf("list migrations: %v", err)
	}
	for _, path := range paths {
		if filepath.Base(path) == name {
			continue
		}
		contents, err := os.ReadFile(path) //nolint:gosec // Glob is restricted to the repository migration directory.
		if err != nil {
			t.Fatalf("read %s: %v", filepath.Base(path), err)
		}
		if _, err := conn.Exec(t.Context(), string(contents)); err != nil {
			t.Fatalf("apply %s: %v", filepath.Base(path), err)
		}
	}
}

// deadlineOf reads one row's status and deadline; a NULL deadline is the zero time.
func deadlineOf(t *testing.T, conn *pgx.Conn, url string) (status string, deadline time.Time, reason string) {
	t.Helper()
	var at *time.Time
	var why *string
	err := conn.QueryRow(t.Context(),
		`SELECT status, deadline_at, terminal_reason FROM chat.link_scans WHERE canonical_url = $1`, url,
	).Scan(&status, &at, &why)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	if at != nil {
		deadline = *at
	}
	if why != nil {
		reason = *why
	}
	return status, deadline, reason
}

// assertFreshPendingDeadline checks a row is pending with a deadline the
// schema derived: LinkScanPendingDeadline from now, within a generous margin.
// This is the functional guard against the Go constant and the SQL interval
// drifting apart.
func assertFreshPendingDeadline(t *testing.T, conn *pgx.Conn, url, who string) {
	t.Helper()
	status, deadline, _ := deadlineOf(t, conn, url)
	if status != "pending" {
		t.Fatalf("%s: status = %s, want pending", who, status)
	}
	if deadline.IsZero() {
		t.Fatalf("%s: a pending row was written without a deadline", who)
	}
	if left := time.Until(deadline); left < storage.LinkScanPendingDeadline-time.Minute || left > storage.LinkScanPendingDeadline+time.Minute {
		t.Fatalf("%s: deadline %v from now, want about %v", who, left.Round(time.Second), storage.LinkScanPendingDeadline)
	}
}

func expire(t *testing.T, conn *pgx.Conn, urls ...string) {
	t.Helper()
	execOn(t, conn, `UPDATE chat.link_scans SET deadline_at = now() - interval '1 second' WHERE canonical_url = ANY($1::text[])`, urls)
}

func TestLinkScanDeadlineBlueGreenPostgreSQL(t *testing.T) {
	conn := newMigrationRoundTripDatabase(t)
	applyMigrationsBefore(t, conn, migrationUp50LinkTargets)

	// Before the migration: a pending row the old slot left waiting, and a
	// terminal row with a verdict.
	const (
		waitingBefore = "https://blue-green.example/waiting-before"
		terminalRow   = "https://blue-green.example/terminal"
		oldInsert     = "https://blue-green.example/old-insert"
		newInsert     = "https://blue-green.example/new-insert"
	)
	execOn(t, conn, oldWriterInsert, waitingBefore)
	execOn(t, conn, `INSERT INTO chat.link_scans (canonical_url, status, scan_uuid, decided_at)
		VALUES ($1, 'safe', 'scan-bg', now() - interval '2 days')`, terminalRow)

	applyMigration(t, conn, migrationUp50LinkTargets)

	// A: the backfill gave the row that was already waiting an end.
	assertFreshPendingDeadline(t, conn, waitingBefore, "row pending before the migration")
	// D: a terminal row is not touched by the backfill.
	if status, deadline, _ := deadlineOf(t, conn, terminalRow); status != "safe" || !deadline.IsZero() {
		t.Fatalf("terminal row after the migration = %s %v, want safe without a deadline", status, deadline)
	}

	// B: the old slot inserts without knowing the column.
	execOn(t, conn, oldWriterInsert, oldInsert)
	assertFreshPendingDeadline(t, conn, oldInsert, "old writer insert")
	// B': the old slot reopens an expired verdict without touching the column.
	execOn(t, conn, oldWriterReopen, terminalRow)
	assertFreshPendingDeadline(t, conn, terminalRow, "old writer reopen")

	// C: the new slot sets the deadline itself and the schema leaves it alone.
	explicit := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	execOn(t, conn, `INSERT INTO chat.link_scans (canonical_url, deadline_at) VALUES ($1, $2)`, newInsert, explicit)
	if _, deadline, _ := deadlineOf(t, conn, newInsert); !deadline.Equal(explicit) {
		t.Fatalf("new writer deadline = %v, want the explicit %v", deadline, explicit)
	}

	// The invariant is a fact of the schema, not only of the writers: a
	// statement that would leave a pending row without an end is repaired by
	// the trigger before the CHECK ever sees it, and the CHECK stands behind it.
	execOn(t, conn, `UPDATE chat.link_scans SET deadline_at = NULL WHERE canonical_url = $1`, oldInsert)
	assertFreshPendingDeadline(t, conn, oldInsert, "pending row whose deadline a statement tried to clear")
	assertBool(t, hasConstraint(t, conn, "chat.link_scans", pendingDeadlineConstraint), true, "the pending-deadline check exists")
	var pending int
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM chat.link_scans WHERE status = 'pending' AND deadline_at IS NULL`).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("pending rows without a deadline = %d %v", pending, err)
	}

	// Every pending row past its deadline converges through the service's own
	// sweep — the old slot's rows exactly like the new slot's.
	expire(t, conn, waitingBefore, oldInsert, terminalRow)
	pool, err := pgxpool.New(t.Context(), databaseDSN(t, os.Getenv("CHAT_TEST_DATABASE_URL"), roundTripDatabase))
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	converged, err := storage.NewPGXMessageStore(pool).TerminalizeExpiredLinkScans(context.Background())
	if err != nil || len(converged) != 3 {
		t.Fatalf("TerminalizeExpiredLinkScans = %v %v, want the three expired rows", converged, err)
	}
	for _, url := range []string{waitingBefore, oldInsert, terminalRow} {
		if status, _, reason := deadlineOf(t, conn, url); status != "unknown" || reason != storage.TerminalReasonDeadline {
			t.Fatalf("%s after the sweep = %s/%s, want unknown/deadline", url, status, reason)
		}
	}
	if status, _, _ := deadlineOf(t, conn, newInsert); status != "pending" {
		t.Fatalf("a row inside its deadline was terminalised: %s", status)
	}

	// Down refuses while unknown rows exist (the previous CHECK cannot hold
	// them); once they are gone it removes everything this migration added, and
	// up again restores the contract for the old writer.
	if _, err := conn.Exec(t.Context(), readChatMigration(t, migrationDown50LinkTargets)); err == nil {
		t.Fatal("down must refuse while unknown targets exist")
	}
	execOn(t, conn, `ROLLBACK`)
	execOn(t, conn, `DELETE FROM chat.link_scans`)
	applyMigration(t, conn, migrationDown50LinkTargets)
	assertBool(t, hasColumn(t, conn, "chat", "link_scans", "deadline_at"), false, "down removes deadline_at")
	assertBool(t, hasConstraint(t, conn, "chat.link_scans", pendingDeadlineConstraint), false, "down removes the pending-deadline check")
	assertBool(t, hasTrigger(t, conn, "chat.link_scans", pendingDeadlineTrigger), false, "down removes the trigger")
	assertBool(t, hasFunction(t, conn, "chat", pendingDeadlineTrigger), false, "down removes the trigger function")
	execOn(t, conn, oldWriterInsert, waitingBefore)

	applyMigration(t, conn, migrationUp50LinkTargets)
	assertBool(t, hasTrigger(t, conn, "chat.link_scans", pendingDeadlineTrigger), true, "up restores the trigger")
	assertFreshPendingDeadline(t, conn, waitingBefore, "row pending across down/up")
	execOn(t, conn, oldWriterInsert, oldInsert)
	assertFreshPendingDeadline(t, conn, oldInsert, "old writer insert after re-up")
}

// The five minutes are written twice — storage.LinkScanPendingDeadline in Go,
// `interval '5 minutes'` in SQL, because a migration cannot import a constant —
// so this holds every SQL occurrence to the Go value.
func TestLinkScanPendingDeadlineMatchesTheSchema(t *testing.T) {
	sql := readChatMigration(t, migrationUp50LinkTargets)
	intervals := regexp.MustCompile(`now\(\) \+ interval '(\d+) minutes'`).FindAllStringSubmatch(sql, -1)
	if len(intervals) < 3 {
		t.Fatalf("expected the default, the backfill and the trigger to spell the deadline; found %d", len(intervals))
	}
	for _, match := range intervals {
		minutes, _ := strconv.Atoi(match[1])
		if time.Duration(minutes)*time.Minute != storage.LinkScanPendingDeadline {
			t.Fatalf("migration deadline %s minutes != storage.LinkScanPendingDeadline %v", match[1], storage.LinkScanPendingDeadline)
		}
	}
}
