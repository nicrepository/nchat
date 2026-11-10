package storage_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nicrepository/nchat/services/file-service/internal/filepolicy"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
	"github.com/nicrepository/nchat/services/file-service/internal/signatures"
	"github.com/nicrepository/nchat/services/file-service/internal/storage"
)

type scanPG struct {
	ctx        context.Context
	admin, app *pgxpool.Pool
	queue      *storage.PGXScanStore
	results    *storage.PGXAttachmentStore
}

// This opt-in is set only inside the owned network-none container runner. No DSN
// from the workstation is accepted; fixtures never contain uploaded user content.
func TestScanQueueFencingPostgreSQL(t *testing.T) {
	if os.Getenv("NCHAT_SCAN_QUEUE_TEST") != "owned-network-none" {
		t.Skip("requires scripts/testing/scan-queue-postgres.sh")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, "host=/var/run/postgresql dbname=nchat_scan_test user=nchat_migrator sslmode=disable")
	requireScanPG(t, err)
	defer admin.Close()
	app, err := pgxpool.New(ctx, "host=/var/run/postgresql dbname=nchat_scan_test user=nchat_app sslmode=disable")
	requireScanPG(t, err)
	defer app.Close()
	lockPool, _ := storage.LockConnPoolFrom(app)
	fence := storage.NewPGXAttachmentFence(lockPool, app, nil)
	e := &scanPG{ctx, admin, app, storage.NewPGXScanStore(app), storage.NewFencedAttachmentStore(app, fence)}
	t.Run("preparation and reversible expansion", e.preparation)
	_, err = admin.Exec(ctx, "UPDATE files.scan_queue_policy SET enabled=true,approval_reference='synthetic-local-cutover' WHERE id=1")
	requireScanPG(t, err)
	t.Run("pre remediation counterexamples", e.preRemediation)
	t.Run("real runtime grants and legacy barriers", e.grants)
	t.Run("restricted lifecycle and legitimate cancellation", e.lifecycleRemediation)
	t.Run("privileged identities cannot be forged", e.privilegesRemediation)
	t.Run("persisted retry categories", e.retryReasons)
	t.Run("lease is valid at authority acquisition", e.leaseLinearization)
	t.Run("one authority and skip locked", e.concurrentClaims)
	t.Run("expired authority cannot write even before reclaim", e.expired)
	t.Run("stale clean rejected evidence retry and restart", e.stale)
	t.Run("commit races serialize authority", e.commitRace)
	t.Run("rolled back claim cannot revive authority", e.rolledBackClaim)
	t.Run("deletion invalidates claim", e.deleted)
	t.Run("signature policy and idempotency", e.proof)
	t.Run("retry releases authority and bounds delay", e.retry)
	t.Run("format version advances before approval", e.formatConflicts)
	t.Run("cutover cannot lose protection", e.rollbackGuard)
}

func requireScanPG(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func (e *scanPG) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	_, err := e.admin.Exec(e.ctx, sql, args...)
	requireScanPG(t, err)
}
func (e *scanPG) seed(t *testing.T) service.ScanJob {
	t.Helper()
	id, workspace := uuid.NewString(), uuid.NewString()
	e.exec(t, `INSERT INTO files.attachments(id,workspace_id,uploader_id,destination_kind,channel_id,
 original_filename,declared_mime,storage_provider,storage_object_key,envelope_version,dek_wrap_version,
 wrapped_dek,kek_key_id,status,scan_next_attempt_at) VALUES($1,$2,$3,'channel',$4,'synthetic.txt',
 'text/plain','seaweedfs',$5,1,2,'\x01','test-only','pending_scan',now())`, id, workspace, uuid.NewString(), uuid.NewString(), "synthetic/"+id)
	return service.ScanJob{AttachmentID: id, WorkspaceID: workspace}
}
func (e *scanPG) clear(t *testing.T) { t.Helper(); e.exec(t, "DELETE FROM files.attachments") }
func (e *scanPG) claim(t *testing.T) service.ScanJob {
	t.Helper()
	jobs, err := e.queue.ClaimDueScans(e.ctx, 1, 330*time.Second)
	requireScanPG(t, err)
	if len(jobs) != 1 || jobs[0].ClaimToken == "" {
		t.Fatalf("claims=%+v", jobs)
	}
	return jobs[0]
}
func (e *scanPG) approve(job service.ScanJob, proof *signatures.Evidence) error {
	_, err := e.results.MarkScanClean(e.ctx, service.ScanApproval{AttachmentID: job.AttachmentID, WorkspaceID: job.WorkspaceID, ClaimToken: job.ClaimToken, Evidence: proof})
	return err
}
func (e *scanPG) reject(job service.ScanJob) error {
	_, err := e.results.MarkScanRejected(e.ctx, service.ScanRejection{AttachmentID: job.AttachmentID, WorkspaceID: job.WorkspaceID, ClaimToken: job.ClaimToken})
	return err
}
func (e *scanPG) assertState(t *testing.T, job service.ScanJob, want string) {
	t.Helper()
	var status string
	requireScanPG(t, e.app.QueryRow(e.ctx, "SELECT status FROM files.attachments WHERE id=$1", job.AttachmentID).Scan(&status))
	if status != want {
		t.Fatalf("status=%s want=%s", status, want)
	}
}
func scanCode(err error) string {
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) {
		return pgerr.Code
	}
	return ""
}

func (e *scanPG) preparation(t *testing.T) {
	e.clear(t)
	j := e.seed(t)
	// Reproduce N's stale-writer hole while the compatibility contract is off.
	for attempt := 0; attempt < 2; attempt++ {
		_, err := e.app.Exec(e.ctx, "UPDATE files.attachments SET scan_attempts=scan_attempts+1,scan_next_attempt_at=now()+interval '330 seconds' WHERE id=$1", j.AttachmentID)
		requireScanPG(t, err)
	}
	requireScanPG(t, e.approve(j, nil))
	requireScanPG(t, e.reject(j)) // N permits late rejection even after another verdict.
	e.exec(t, "UPDATE files.signature_policy SET enabled=true,max_age_seconds=86400,approval_reference='preparation-B-fixture' WHERE id=1")
	proven := e.seed(t)
	requireScanPG(t, e.approve(proven, freshScanProof()))
	e.exec(t, "UPDATE files.signature_policy SET enabled=false WHERE id=1")
	_, err := e.queue.ClaimDueScans(e.ctx, 1, 330*time.Second)
	if scanCode(err) != "NC006" || !errors.Is(err, service.ErrScanQueueStandby) {
		t.Fatalf("new worker must wait: %v", err)
	}
	e.exec(t, readFilesMigration(t, "000021_scan_queue_fencing.down.sql"))
	e.exec(t, readFilesMigration(t, "000021_scan_queue_fencing.up.sql"))
	e.clear(t)
}
func (e *scanPG) grants(t *testing.T) {
	var super, member bool
	requireScanPG(t, e.app.QueryRow(e.ctx, "SELECT rolsuper,pg_has_role(current_user,'nchat_migrator','MEMBER') FROM pg_roles WHERE rolname=current_user").Scan(&super, &member))
	if super || member {
		t.Fatal("runtime role has owner powers")
	}
	grants, err := os.ReadFile("/repo/scripts/db/grant-runtime.sql")
	requireScanPG(t, err)
	e.exec(t, string(grants[bytes.IndexByte(grants, '\n')+1:]))

	j := e.seed(t)
	for _, sql := range []string{
		"UPDATE files.scan_queue_policy SET enabled=false",
		"SELECT * FROM files.persist_proven_scan_internal($1,$2,'x','x',1,now(),now(),now(),0)",
		"UPDATE files.attachments SET scan_signature_generation='forged' WHERE id=$1 AND workspace_id=$2",
		"UPDATE files.attachments SET status='clean' WHERE id=$1 AND workspace_id=$2",
		"UPDATE files.attachments SET status='rejected' WHERE id=$1 AND workspace_id=$2",
		"UPDATE files.attachments SET scan_attempts=scan_attempts+1 WHERE id=$1 AND workspace_id=$2",
	} {
		args := []any{j.AttachmentID, j.WorkspaceID}
		if sql == "UPDATE files.scan_queue_policy SET enabled=false" {
			args = nil
		}
		if _, err := e.app.Exec(e.ctx, sql, args...); err == nil {
			t.Fatalf("runtime bypass: %s", sql)
		}
	}
	e.temporaryShadow(t, j)
	if err := e.approve(j, nil); scanCode(err) != "NC004" {
		t.Fatalf("legacy approval=%v", err)
	}
	j.ClaimToken = ""
	proof := freshScanProof()
	if err := e.approve(j, proof); scanCode(err) != "NC004" {
		t.Fatalf("legacy proven approval=%v", err)
	}
	if err := e.reject(j); scanCode(err) != "NC004" {
		t.Fatalf("legacy rejection=%v", err)
	}
	e.clear(t)
}
func (e *scanPG) concurrentClaims(t *testing.T) {
	e.clear(t)
	j := e.seed(t)
	tx, err := e.app.Begin(e.ctx)
	requireScanPG(t, err)
	_, err = tx.Exec(e.ctx, "SELECT id FROM files.attachments WHERE id=$1 FOR UPDATE", j.AttachmentID)
	requireScanPG(t, err)
	rows, err := e.queue.ClaimDueScans(e.ctx, 1, 330*time.Second)
	requireScanPG(t, err)
	if len(rows) != 0 {
		t.Fatal("claim ignored locked row")
	}
	requireScanPG(t, tx.Rollback(e.ctx))
	start := make(chan struct{})
	jobs := make(chan []service.ScanJob, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for n := 0; n < 2; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			jobsClaimed, claimErr := e.queue.ClaimDueScans(e.ctx, 1, 330*time.Second)
			jobs <- jobsClaimed
			errs <- claimErr
		}()
	}
	close(start)
	wg.Wait()
	close(jobs)
	close(errs)
	total := 0
	for group := range jobs {
		total += len(group)
	}
	for claimErr := range errs {
		requireScanPG(t, claimErr)
	}
	if total != 1 {
		t.Fatalf("concurrent claims=%d", total)
	}
	e.clear(t)
}
func (e *scanPG) expire(t *testing.T, j service.ScanJob) {
	t.Helper()
	e.exec(t, "UPDATE files.attachments SET scan_lease_until=clock_timestamp()-interval '1 second' WHERE id=$1", j.AttachmentID)
}
func (e *scanPG) expired(t *testing.T) {
	e.seed(t)
	j := e.claim(t)
	e.expire(t, j)
	if err := e.approve(j, nil); !errors.Is(err, service.ErrScanSuperseded) {
		t.Fatalf("expired=%v", err)
	}
	e.assertState(t, j, "pending_scan")
	e.clear(t)
}
func (e *scanPG) stale(t *testing.T) {
	e.seed(t)
	a := e.claim(t)
	e.expire(t, a)
	b := e.claim(t)
	if a.ClaimToken == b.ClaimToken {
		t.Fatal("authority reused")
	}
	for _, proof := range []*signatures.Evidence{nil, freshScanProof()} {
		if err := e.approve(a, proof); !errors.Is(err, service.ErrScanSuperseded) {
			t.Fatalf("stale approve=%v", err)
		}
	}
	if err := e.reject(a); !errors.Is(err, service.ErrScanSuperseded) {
		t.Fatalf("stale reject=%v", err)
	}
	if err := e.queue.RetryScan(e.ctx, a, 10*time.Second, "timeout"); !errors.Is(err, service.ErrScanSuperseded) {
		t.Fatalf("stale retry=%v", err)
	}
	e.assertState(t, a, "pending_scan")
	// A fresh store instance (restart) still cannot use the old persisted token.
	restarted := storage.NewPGXAttachmentStore(e.app)
	_, err := restarted.MarkScanClean(e.ctx, service.ScanApproval{AttachmentID: a.AttachmentID, WorkspaceID: a.WorkspaceID, ClaimToken: a.ClaimToken})
	if !errors.Is(err, service.ErrScanSuperseded) {
		t.Fatalf("restart=%v", err)
	}
	requireScanPG(t, e.approve(b, nil))
	if err := e.reject(a); !errors.Is(err, service.ErrScanSuperseded) {
		t.Fatalf("late reject=%v", err)
	}
	e.assertState(t, b, "clean")
	var generation *string
	requireScanPG(t, e.app.QueryRow(e.ctx, "SELECT scan_signature_generation FROM files.attachments WHERE id=$1", b.AttachmentID).Scan(&generation))
	if generation != nil {
		t.Fatal("stale proof mutated evidence")
	}
	e.clear(t)
	e.seed(t)
	old := e.claim(t)
	e.expire(t, old)
	current := e.claim(t)
	requireScanPG(t, e.reject(current))
	if err := e.approve(old, freshScanProof()); !errors.Is(err, service.ErrScanSuperseded) {
		t.Fatalf("late approval after rejection=%v", err)
	}
	e.assertState(t, current, "rejected")
	e.clear(t)
}
func (e *scanPG) commitRace(t *testing.T) {
	e.seed(t)
	a := e.claim(t)
	e.expire(t, a)
	tx, err := e.app.Begin(e.ctx)
	requireScanPG(t, err)
	var token string
	requireScanPG(t, tx.QueryRow(e.ctx, "SELECT scan_claim_token::text FROM files.claim_scans(1,330)").Scan(&token))

	stale, err := e.app.Acquire(e.ctx)
	requireScanPG(t, err)
	defer stale.Release()
	done := make(chan error, 1)
	go func() {
		_, writeErr := stale.Exec(e.ctx, "SELECT * FROM files.persist_scan_v2($1,$2,$3,'clean',0,NULL,NULL,NULL,NULL,NULL,NULL)", a.AttachmentID, a.WorkspaceID, a.ClaimToken)
		done <- writeErr
	}()
	e.waitBlocked(t, stale.Conn().PgConn().PID())
	requireScanPG(t, tx.Commit(e.ctx))
	if err := <-done; scanCode(err) != "NC005" {
		t.Fatalf("stale commit race=%v", err)
	}

	b := a
	b.ClaimToken = token
	requireScanPG(t, e.reject(b))
	e.assertState(t, b, "rejected")
	e.clear(t)
}
func (e *scanPG) deleted(t *testing.T) {
	e.seed(t)
	j := e.claim(t)
	tx, err := e.admin.Begin(e.ctx)
	requireScanPG(t, err)
	_, err = tx.Exec(e.ctx, "UPDATE files.attachments SET deleted_at=now() WHERE id=$1", j.AttachmentID)
	requireScanPG(t, err)
	done := make(chan error, 1)
	go func() { done <- e.approve(j, nil) }()
	requireScanPG(t, tx.Commit(e.ctx))
	if err := <-done; !errors.Is(err, service.ErrScanSuperseded) {
		t.Fatalf("deleted result=%v", err)
	}
	e.clear(t)
}
func freshScanProof() *signatures.Evidence {
	now := time.Now().UTC().Add(-time.Second)
	return &signatures.Evidence{Generation: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Engine: "1.4.6", DailyVersion: 1, DatabaseTime: now.Add(-time.Hour), LoadedAt: now.Add(-time.Minute), CompletedAt: now}
}
func (e *scanPG) proof(t *testing.T) {
	e.exec(t, "UPDATE files.signature_policy SET enabled=true,max_age_seconds=86400,approval_reference='synthetic-only' WHERE id=1")
	e.seed(t)
	j := e.claim(t)
	if err := e.approve(j, nil); err == nil {
		t.Fatal("missing proof approved")
	}
	e.assertState(t, j, "pending_scan")
	proof := freshScanProof()
	requireScanPG(t, e.approve(j, proof))
	e.assertState(t, j, "clean")
	if err := e.approve(j, proof); !errors.Is(err, service.ErrScanAlreadyRecorded) {
		t.Fatalf("duplicate=%v", err)
	}
	// Rescan quarantines first; a new claim is mandatory and old evidence remains immutable.
	e.exec(t, "UPDATE files.attachments SET status='pending_scan',scan_next_attempt_at=now(),scan_lease_until=NULL WHERE id=$1", j.AttachmentID)
	newer := e.claim(t)
	if err := e.approve(newer, nil); err == nil {
		t.Fatal("rescan reused old evidence without a new receipt")
	}
	proof.CompletedAt = proof.CompletedAt.Add(time.Millisecond)
	requireScanPG(t, e.approve(newer, proof))
	if err := e.approve(j, proof); !errors.Is(err, service.ErrScanSuperseded) {
		t.Fatalf("old rescan token=%v", err)
	}
	e.exec(t, "UPDATE files.signature_policy SET enabled=false WHERE id=1")
	e.clear(t)
}
func (e *scanPG) retry(t *testing.T) {
	e.seed(t)
	a := e.claim(t)
	requireScanPG(t, e.queue.RetryScan(e.ctx, a, 10*time.Second, "timeout"))
	jobs, err := e.queue.ClaimDueScans(e.ctx, 1, 330*time.Second)
	requireScanPG(t, err)
	if len(jobs) != 0 {
		t.Fatal("retry ignored backoff")
	}
	if err := e.approve(a, nil); !errors.Is(err, service.ErrScanSuperseded) {
		t.Fatalf("released claim=%v", err)
	}
	e.exec(t, "UPDATE files.attachments SET scan_next_attempt_at=now() WHERE id=$1", a.AttachmentID)
	b := e.claim(t)
	if b.Attempts != 2 {
		t.Fatalf("attempts=%d", b.Attempts)
	}
	for _, delay := range []time.Duration{0, 2641 * time.Second} {
		if err := e.queue.RetryScan(e.ctx, b, delay, "timeout"); err == nil {
			t.Fatal("unbounded retry accepted")
		}
	}
	e.exec(t, "UPDATE files.attachments SET scan_attempts=32000 WHERE id=$1", b.AttachmentID)
	e.expire(t, b)
	c := e.claim(t)
	if c.Attempts != 32000 || c.ClaimToken == b.ClaimToken {
		t.Fatal("saturation reused authority")
	}
	e.clear(t)
}
func (e *scanPG) rollbackGuard(t *testing.T) {
	if _, err := e.admin.Exec(e.ctx, "UPDATE files.scan_queue_policy SET enabled=false WHERE id=1"); scanCode(err) != "NC004" {
		t.Fatalf("policy rollback=%v", err)
	}
	if _, err := e.admin.Exec(e.ctx, readFilesMigration(t, "000021_scan_queue_fencing.down.sql")); err == nil {
		t.Fatal("down removed active protection")
	}
	// The rejected multi-statement migration leaves its transaction aborted.
	_, err := e.admin.Exec(e.ctx, "ROLLBACK")
	requireScanPG(t, err)
}

func (e *scanPG) waitBlocked(t *testing.T, pid uint32) {
	t.Helper()
	for e.ctx.Err() == nil {
		var blocked bool
		requireScanPG(t, e.admin.QueryRow(e.ctx, "SELECT cardinality(pg_blocking_pids($1))>0", int64(pid)).Scan(&blocked))
		if blocked {
			return
		}
	}
	t.Fatal("transaction did not reach lock barrier")
}

func (e *scanPG) rolledBackClaim(t *testing.T) {
	e.seed(t)
	a := e.claim(t)
	e.expire(t, a)
	tx, err := e.app.Begin(e.ctx)
	requireScanPG(t, err)
	var abandoned string
	requireScanPG(t, tx.QueryRow(e.ctx, "SELECT scan_claim_token::text FROM files.claim_scans(1,330)").Scan(&abandoned))
	requireScanPG(t, tx.Rollback(e.ctx))
	if err := e.approve(a, nil); !errors.Is(err, service.ErrScanSuperseded) {
		t.Fatalf("rollback restored expired authority: %v", err)
	}
	c := e.claim(t)
	if c.ClaimToken == abandoned || c.ClaimToken == a.ClaimToken {
		t.Fatal("rollback reused token")
	}
	requireScanPG(t, e.approve(c, nil))
	if err := e.approve(c, nil); !errors.Is(err, service.ErrScanAlreadyRecorded) {
		t.Fatalf("lost acknowledgement replay=%v", err)
	}
	e.clear(t)
}

func (e *scanPG) temporaryShadow(t *testing.T, j service.ScanJob) {
	conn, err := e.app.Acquire(e.ctx)
	requireScanPG(t, err)
	defer conn.Release()
	_, err = conn.Exec(e.ctx, "CREATE TEMP TABLE pg_proc(oid oid,proowner oid); INSERT INTO pg_temp.pg_proc SELECT 'files.require_scan_fencing()'::regprocedure::oid,current_user::regrole::oid")
	requireScanPG(t, err)
	_, err = conn.Exec(e.ctx, "UPDATE files.attachments SET status='clean' WHERE id=$1", j.AttachmentID)
	if scanCode(err) != "NC004" {
		t.Fatalf("temporary catalog shadow bypassed trigger: %v", err)
	}
	_, err = conn.Exec(e.ctx, "DROP TABLE pg_temp.pg_proc")
	requireScanPG(t, err)
}

// Frozen trigger from the handoff snapshot, confined to rolled-back transactions.
const preRemediationScanGuard = `CREATE OR REPLACE FUNCTION files.guard_scan_queue_write() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog,files,pg_temp AS $$
DECLARE active boolean;
DECLARE trusted boolean;
BEGIN
 active := files.scan_fencing_enabled();
 SELECT current_user::regrole = proowner INTO trusted FROM pg_catalog.pg_proc
  WHERE oid='files.require_scan_fencing()'::regprocedure;
 IF active IS DISTINCT FROM false AND NOT trusted THEN
  IF NEW.status IN ('clean','rejected') OR NEW.scan_claim_token IS NOT NULL THEN
   RAISE EXCEPTION 'fenced scan API required' USING ERRCODE='NC004';
  END IF;
  IF TG_OP='UPDATE' AND OLD.status='pending_scan' AND
    ROW(NEW.scan_attempts,NEW.scan_next_attempt_at,NEW.scan_lease_until,NEW.scan_claim_token,
        NEW.scan_last_error,NEW.scan_failed_at)
    IS DISTINCT FROM ROW(OLD.scan_attempts,OLD.scan_next_attempt_at,OLD.scan_lease_until,OLD.scan_claim_token,
        OLD.scan_last_error,OLD.scan_failed_at) THEN
   RAISE EXCEPTION 'fenced scheduling API required' USING ERRCODE='NC004';
  END IF;
 END IF;
 RETURN NEW;
END $$;
`

func (e *scanPG) preRemediation(t *testing.T) {
	cfg := e.admin.Config()
	cfg.ConnConfig.User = "postgres"
	cfg.MaxConns = 1
	root, err := pgxpool.NewWithConfig(e.ctx, cfg)
	requireScanPG(t, err)
	t.Cleanup(root.Close)

	for _, tc := range []struct {
		name, sql string
		wantCode  string
	}{
		{"pending silently leaves queue", "UPDATE files.attachments SET status='failed' WHERE id=$1", ""},
		{"draft deletion incorrectly blocked", "UPDATE files.attachments SET deleted_at=now(),scan_next_attempt_at=NULL WHERE id=$1", "NC004"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := e.seed(t)
			tx, err := root.Begin(e.ctx)
			requireScanPG(t, err)
			t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
			_, err = tx.Exec(e.ctx, preRemediationScanGuard)
			requireScanPG(t, err)
			_, err = tx.Exec(e.ctx, "SET LOCAL ROLE nchat_app")
			requireScanPG(t, err)
			_, err = tx.Exec(e.ctx, tc.sql, j.AttachmentID)
			if scanCode(err) != tc.wantCode {
				t.Fatalf("baseline code=%s err=%v", scanCode(err), err)
			}
			requireScanPG(t, tx.Rollback(e.ctx))
			e.clear(t)
		})
	}
}

func (e *scanPG) formatConflicts(t *testing.T) {
	for _, withProof := range []bool{false, true} {
		t.Run(fmt.Sprint(withProof), func(t *testing.T) {
			e.seed(t)
			j := e.claim(t)
			var proof *signatures.Evidence
			want := service.ErrScanConflict
			if withProof {
				e.exec(t, "UPDATE files.signature_policy SET enabled=true,max_age_seconds=86400,approval_reference='format-fixture' WHERE id=1")
				proof = freshScanProof()
				want = service.ErrScanConflict
			}
			pool := e.singleAppPool(t)
			var pid uint32
			requireScanPG(t, pool.QueryRow(e.ctx, "SELECT pg_backend_pid()").Scan(&pid))
			tx, err := e.admin.Begin(e.ctx)
			requireScanPG(t, err)
			t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
			_, err = tx.Exec(e.ctx, "UPDATE files.attachments SET format_policy_version=$2 WHERE id=$1", j.AttachmentID, filepolicy.Version+1)
			requireScanPG(t, err)
			done := make(chan error, 1)
			go func() {
				_, writeErr := storage.NewPGXAttachmentStore(pool).MarkScanClean(e.ctx, service.ScanApproval{AttachmentID: j.AttachmentID, WorkspaceID: j.WorkspaceID, ClaimToken: j.ClaimToken, Evidence: proof})
				done <- writeErr
			}()
			e.waitBlocked(t, pid)
			requireScanPG(t, tx.Commit(e.ctx))
			if err := <-done; !errors.Is(err, want) {
				t.Fatalf("format conflict=%v want=%v", err, want)
			} else {
				t.Logf("proof=%v domain_error=%v SQLSTATE=%q", withProof, want, scanCode(err))
			}
			e.assertState(t, j, "pending_scan")
			if !withProof {
				var outcome string
				requireScanPG(t, e.app.QueryRow(e.ctx, "SELECT result_outcome FROM files.persist_scan_v2($1,$2,$3,'clean',$4,NULL,NULL,NULL,NULL,NULL,NULL)", j.AttachmentID, j.WorkspaceID, j.ClaimToken, filepolicy.Version).Scan(&outcome))
				if outcome != "conflict" {
					t.Fatalf("SQL outcome=%q", outcome)
				}
			}
			e.exec(t, "UPDATE files.signature_policy SET enabled=false WHERE id=1")
			e.clear(t)
		})
	}
}
func (e *scanPG) singleAppPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	cfg := e.app.Config()
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(e.ctx, cfg)
	requireScanPG(t, err)
	t.Cleanup(pool.Close)
	return pool
}
