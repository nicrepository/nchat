package storage_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nicrepository/nchat/services/file-service/internal/service"
)

func (e *scanPG) expireCapacity(t *testing.T, j service.ScanJob) {
	t.Helper()
	e.exec(t, "UPDATE files.scan_capacity_reservations SET reserved_until=clock_timestamp()-interval '1 second' WHERE attachment_id=$1", j.AttachmentID)
}

func (e *scanPG) noCapacityClaim(t *testing.T) {
	t.Helper()
	jobs, err := e.queue.ClaimDueScans(e.ctx, 1, 330*time.Second)
	requireScanPG(t, err)
	if len(jobs) != 0 {
		t.Fatalf("claimed without capacity: %+v", jobs)
	}
}

func (e *scanPG) globalCapacity(t *testing.T) {
	t.Run("administrative bounds and regrant", e.capacityGrants)
	t.Run("concurrent replicas and terminal release", e.capacityReplicas)
	t.Run("retry crash deletion and late verdict", e.capacityRecovery)
	t.Run("atomic rollback and timeout derived reservation", e.capacityAtomic)
	t.Run("active C migration warmup and down guard", e.capacityUpgrade)
	t.Run("old transaction snapshot cannot exceed cap", e.capacitySnapshot)
	t.Run("missing policy fails closed", e.capacityMissingPolicy)
	e.clear(t)
	e.exec(t, "UPDATE files.scan_capacity_policy SET max_scans=1,admit_after=clock_timestamp() WHERE id=1")
}

func (e *scanPG) capacityGrants(t *testing.T) {
	e.grants(t) // Includes the official broad regrant before checking restrictions.
	for _, sql := range []string{
		"UPDATE files.scan_capacity_policy SET max_scans=4",
		"DELETE FROM files.scan_capacity_reservations",
		"INSERT INTO files.scan_capacity_reservations VALUES(gen_random_uuid(),gen_random_uuid(),now())",
		"SELECT files.release_terminal_scan_capacity()",
	} {
		if _, err := e.app.Exec(e.ctx, sql); scanCode(err) != "42501" {
			t.Fatalf("runtime capacity bypass %s: %v", sql, err)
		}
	}
	for _, value := range []int{0, 5} {
		if _, err := e.admin.Exec(e.ctx, "UPDATE files.scan_capacity_policy SET max_scans=$1", value); scanCode(err) != "23514" {
			t.Fatalf("capacity %d: %v", value, err)
		}
	}
	e.exec(t, "UPDATE files.scan_capacity_policy SET max_scans=4 WHERE id=1")
	e.exec(t, "UPDATE files.scan_capacity_policy SET max_scans=1 WHERE id=1")
}

func (e *scanPG) capacityReplicas(t *testing.T) {
	e.clear(t)
	e.exec(t, "UPDATE files.scan_capacity_policy SET max_scans=2 WHERE id=1")
	for i := 0; i < 8; i++ {
		e.seed(t)
	}
	start := make(chan struct{})
	results := make(chan []service.ScanJob, 8)
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			jobs, err := e.queue.ClaimDueScans(e.ctx, 1, 330*time.Second)
			results <- jobs
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	var claimed []service.ScanJob
	for jobs := range results {
		claimed = append(claimed, jobs...)
	}
	for err := range errs {
		requireScanPG(t, err)
	}
	if len(claimed) != 2 {
		t.Fatalf("global claims=%d want 2", len(claimed))
	}
	e.noCapacityClaim(t)
	// A lower cap drains current scans; it cannot revoke their authority.
	e.exec(t, "UPDATE files.scan_capacity_policy SET max_scans=1 WHERE id=1")
	requireScanPG(t, e.reject(claimed[0]))
	e.noCapacityClaim(t)
	requireScanPG(t, e.approve(claimed[1], nil))
	e.claim(t)
	e.clear(t)
}

func (e *scanPG) capacityRecovery(t *testing.T) {
	for _, reason := range []string{"timeout", "canceled", "clamd_unavailable", "storage_read"} {
		t.Run(reason, func(t *testing.T) { e.capacityRetryRecovery(t, reason) })
	}
	e.clear(t)
	e.seed(t)
	j := e.claim(t)
	// Crash: lease expiry alone cannot reclaim daemon capacity.
	e.exec(t, "UPDATE files.attachments SET scan_lease_until=now()-interval '1 second' WHERE id=$1", j.AttachmentID)
	e.noCapacityClaim(t)
	e.expireCapacity(t, j)
	recovered := e.claim(t)
	if recovered.ClaimToken == j.ClaimToken {
		t.Fatal("crash recovery reused token")
	}
	e.exec(t, "DELETE FROM files.attachments WHERE id=$1", recovered.AttachmentID)
	e.seed(t)
	e.noCapacityClaim(t)
	e.expireCapacity(t, recovered)
	e.claim(t)
	e.clear(t)
}

func (e *scanPG) capacityRetryRecovery(t *testing.T, reason string) {
	e.clear(t)
	e.seed(t)
	j := e.claim(t)
	requireScanPG(t, e.queue.RetryScan(e.ctx, j, 10*time.Second, reason))
	e.exec(t, "UPDATE files.attachments SET scan_next_attempt_at=now() WHERE id=$1", j.AttachmentID)
	e.seed(t)
	e.noCapacityClaim(t)
	// Spare global capacity must not permit the *same* orphaned job to restart.
	e.exec(t, "UPDATE files.scan_capacity_policy SET max_scans=2 WHERE id=1")
	next := e.claim(t)
	if next.AttachmentID == j.AttachmentID {
		t.Fatal("orphan job reclaimed early")
	}
	requireScanPG(t, e.reject(next))
	e.noCapacityClaim(t)
	e.expireCapacity(t, j)
	recovered := e.claim(t)
	if recovered.AttachmentID != j.AttachmentID || recovered.ClaimToken == j.ClaimToken {
		t.Fatal("recovery lost fencing")
	}
	if err := e.approve(j, nil); !errors.Is(err, service.ErrScanSuperseded) {
		t.Fatalf("late verdict: %v", err)
	}
	requireScanPG(t, e.reject(recovered))
	e.exec(t, "UPDATE files.scan_capacity_policy SET max_scans=1 WHERE id=1")
}

func (e *scanPG) capacityAtomic(t *testing.T) {
	e.clear(t)
	e.seed(t)
	tx, err := e.app.Begin(e.ctx)
	requireScanPG(t, err)
	var token string
	requireScanPG(t, tx.QueryRow(e.ctx, "SELECT scan_claim_token::text FROM files.claim_scans(1,3630)").Scan(&token))
	requireScanPG(t, tx.Rollback(e.ctx))
	var count int
	requireScanPG(t, e.app.QueryRow(e.ctx, "SELECT count(*) FROM files.scan_capacity_reservations").Scan(&count))
	if count != 0 {
		t.Fatal("rollback leaked capacity")
	}
	jobs, err := e.queue.ClaimDueScans(e.ctx, 1, 3630*time.Second)
	requireScanPG(t, err)
	if len(jobs) != 1 {
		t.Fatal("rolled back claim lost work")
	}
	var seconds float64
	requireScanPG(t, e.app.QueryRow(e.ctx, `SELECT extract(epoch FROM (r.reserved_until-a.scan_lease_until))
 FROM files.scan_capacity_reservations r JOIN files.attachments a ON a.id=r.attachment_id`).Scan(&seconds))
	if seconds != 450 {
		t.Fatalf("daemon margin=%v", seconds)
	}
	e.clear(t)
}

func (e *scanPG) capacityUpgrade(t *testing.T) {
	e.clear(t)
	e.seed(t)
	// Reapply E over an active C policy in a rollback-only transaction.
	tx, err := e.admin.Begin(e.ctx)
	requireScanPG(t, err)
	defer func() { _ = tx.Rollback(e.ctx) }()
	_, err = tx.Exec(e.ctx, `DROP TRIGGER attachments_terminal_scan_capacity ON files.attachments;
 DROP FUNCTION files.release_terminal_scan_capacity();
 DROP TABLE files.scan_capacity_reservations; DROP TABLE files.scan_capacity_policy;`)
	requireScanPG(t, err)
	// Strip only transaction boundaries; the test owns the outer rollback.
	migration := readFilesMigration(t, "000022_scan_global_capacity.up.sql")
	migration = migration[len("BEGIN;") : len(migration)-len("COMMIT;\n")]
	_, err = tx.Exec(e.ctx, migration)
	requireScanPG(t, err)
	var seconds float64
	requireScanPG(t, tx.QueryRow(e.ctx, "SELECT extract(epoch FROM(admit_after-clock_timestamp())) FROM files.scan_capacity_policy").Scan(&seconds))
	if seconds < 4070 || seconds > 4080 {
		t.Fatalf("upgrade quarantine=%v", seconds)
	}
	var count int
	requireScanPG(t, tx.QueryRow(e.ctx, "SELECT count(*) FROM files.claim_scans(1,330)").Scan(&count))
	if count != 0 {
		t.Fatal("migration admitted during orphan warmup")
	}
	_, err = tx.Exec(e.ctx, "UPDATE files.scan_capacity_policy SET admit_after=clock_timestamp()-interval '1 second' WHERE id=1")
	requireScanPG(t, err)
	requireScanPG(t, tx.QueryRow(e.ctx, "SELECT count(*) FROM files.claim_scans(1,330)").Scan(&count))
	if count != 1 {
		t.Fatal("migration warmup lost eligible work")
	}
	requireScanPG(t, tx.Rollback(e.ctx))
	_, err = e.admin.Exec(e.ctx, readFilesMigration(t, "000022_scan_global_capacity.down.sql"))
	if scanCode(err) != "NC004" {
		t.Fatalf("active capacity down=%v", err)
	}
	e.exec(t, "ROLLBACK")
}

func (e *scanPG) capacitySnapshot(t *testing.T) {
	e.clear(t)
	e.seed(t)
	e.seed(t)
	tx, err := e.app.BeginTx(e.ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	requireScanPG(t, err)
	defer func() { _ = tx.Rollback(e.ctx) }()
	var count int
	requireScanPG(t, tx.QueryRow(e.ctx, "SELECT count(*) FROM files.scan_capacity_reservations").Scan(&count))
	e.claim(t)
	_, err = tx.Exec(e.ctx, "SELECT * FROM files.claim_scans(1,330)")
	if scanCode(err) != "40001" {
		t.Fatalf("snapshot oversubscription: %v", err)
	}
	requireScanPG(t, tx.Rollback(e.ctx))
	e.noCapacityClaim(t)
	e.clear(t)
}

func (e *scanPG) capacityMissingPolicy(t *testing.T) {
	e.clear(t)
	e.seed(t)
	tx, err := e.admin.Begin(e.ctx)
	requireScanPG(t, err)
	defer func() { _ = tx.Rollback(e.ctx) }()
	_, err = tx.Exec(e.ctx, "DELETE FROM files.scan_capacity_policy")
	requireScanPG(t, err)
	_, err = tx.Exec(e.ctx, "SELECT * FROM files.claim_scans(1,330)")
	if scanCode(err) != "NC004" {
		t.Fatalf("missing capacity policy fallback: %v", err)
	}
	requireScanPG(t, tx.Rollback(e.ctx))
	e.clear(t)
}
