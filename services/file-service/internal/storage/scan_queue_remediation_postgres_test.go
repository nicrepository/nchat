package storage_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
	"github.com/nicrepository/nchat/services/file-service/internal/storage"
)

func (e *scanPG) assertRuntimeDenied(t *testing.T, sql string, args ...any) {
	t.Helper()
	_, err := e.app.Exec(e.ctx, sql, args...)
	if scanCode(err) != "NC004" {
		t.Fatalf("runtime lifecycle guard code=%s err=%v", scanCode(err), err)
	}
}
func (e *scanPG) lifecycleRemediation(t *testing.T) {
	unclaimed := e.seed(t)
	e.assertRuntimeDenied(t, "UPDATE files.attachments SET status='failed' WHERE id=$1", unclaimed.AttachmentID)
	j := e.claim(t)
	for _, status := range []string{"failed", "clean", "rejected", "pending_upload", "deleted"} {
		e.assertRuntimeDenied(t, "UPDATE files.attachments SET status=$2 WHERE id=$1", j.AttachmentID, status)
	}
	for _, sql := range []string{
		"UPDATE files.attachments SET scan_claim_token=gen_random_uuid() WHERE id=$1",
		"UPDATE files.attachments SET scan_lease_until=now()+interval '1 hour' WHERE id=$1",
		"UPDATE files.attachments SET workspace_id=gen_random_uuid() WHERE id=$1",
	} {
		e.assertRuntimeDenied(t, sql, j.AttachmentID)
	}
	e.assertState(t, j, "pending_scan")
	foreign := j
	foreign.WorkspaceID = uuid.NewString()
	if err := e.approve(foreign, nil); !errors.Is(err, service.ErrScanSuperseded) {
		t.Fatalf("foreign workspace approval=%v", err)
	}
	if err := e.reject(foreign); !errors.Is(err, service.ErrScanSuperseded) {
		t.Fatalf("foreign workspace rejection=%v", err)
	}
	e.assertState(t, j, "pending_scan")
	e.cancelAndRemove(t, j)
	e.uploadTransitions(t)
	e.clear(t)
}
func (e *scanPG) cancelAndRemove(t *testing.T, j service.ScanJob) {
	t.Helper()
	uploader := uuid.NewString()
	e.exec(t, "UPDATE files.attachments SET uploader_id=$2,draft_expires_at=now()+interval '1 hour' WHERE id=$1", j.AttachmentID, uploader)
	s := storage.NewPGXAttachmentStore(e.app)
	if err := s.CancelDraft(e.ctx, j.AttachmentID, uuid.NewString()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("foreign uploader cancellation=%v", err)
	}
	requireScanPG(t, s.CancelDraft(e.ctx, j.AttachmentID, uploader))
	requireScanPG(t, s.CancelDraft(e.ctx, j.AttachmentID, uploader))
	var removed, unscheduled bool
	requireScanPG(t, e.app.QueryRow(e.ctx, "SELECT deleted_at IS NOT NULL,scan_next_attempt_at IS NULL FROM files.attachments WHERE id=$1", j.AttachmentID).Scan(&removed, &unscheduled))
	if !removed || !unscheduled {
		t.Fatal("draft cancellation did not hide/unschedule")
	}
	if err := e.approve(j, nil); !errors.Is(err, service.ErrScanSuperseded) {
		t.Fatalf("deleted claim approved: %v", err)
	}
	e.assertRuntimeDenied(t, "UPDATE files.attachments SET deleted_at=NULL WHERE id=$1", j.AttachmentID)
	e.clear(t)
	other := e.seed(t)
	if _, err := e.results.MarkAttachmentDeleted(e.ctx, service.AttachmentRemoval{AttachmentID: other.AttachmentID, WorkspaceID: uuid.NewString()}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-workspace removal=%v", err)
	}
	e.assertState(t, other, "pending_scan")
	_, err := e.results.MarkAttachmentDeleted(e.ctx, service.AttachmentRemoval{AttachmentID: other.AttachmentID, WorkspaceID: other.WorkspaceID})
	requireScanPG(t, err)
	e.assertRuntimeDenied(t, "UPDATE files.attachments SET deleted_at=NULL,status='pending_scan' WHERE id=$1", other.AttachmentID)
	e.clear(t)
	expiring := e.seed(t)
	e.exec(t, "UPDATE files.attachments SET draft_expires_at=now()-interval '1 second' WHERE id=$1", expiring.AttachmentID)
	count, err := s.ExpireDrafts(e.ctx, 1)
	requireScanPG(t, err)
	if count != 1 {
		t.Fatalf("draft expiry count=%d", count)
	}
	e.clear(t)
}
func (e *scanPG) uploadTransitions(t *testing.T) {
	t.Helper()
	j := e.seed(t)
	e.exec(t, "UPDATE files.attachments SET status='pending_upload',scan_next_attempt_at=NULL WHERE id=$1", j.AttachmentID)
	// The real upload compensation is restricted to pending_upload.
	requireScanPG(t, storage.NewPGXAttachmentStore(e.app).MarkFailed(e.ctx, j.AttachmentID, "storage_write"))
	e.assertState(t, j, "failed")
	e.assertRuntimeDenied(t, "UPDATE files.attachments SET status='pending_scan' WHERE id=$1", j.AttachmentID)
	e.clear(t)
	j = e.seed(t)
	e.exec(t, "UPDATE files.attachments SET status='pending_upload',scan_next_attempt_at=NULL WHERE id=$1", j.AttachmentID)
	e.assertRuntimeDenied(t, "UPDATE files.attachments SET scan_lease_until=now()+interval '1 hour' WHERE id=$1", j.AttachmentID)
	update := finalisedAttachment(domain.StatusPendingScan)
	update.ID = j.AttachmentID
	requireScanPG(t, storage.NewPGXAttachmentStore(e.app).MarkUploaded(e.ctx, update))
	requireScanPG(t, storage.NewPGXAttachmentStore(e.app).MarkFailed(e.ctx, j.AttachmentID, "late_compensation"))
	e.assertState(t, j, "pending_scan")
	current := e.claim(t)
	requireScanPG(t, e.approve(current, nil))
	e.assertRuntimeDenied(t, "UPDATE files.attachments SET status='pending_scan' WHERE id=$1", current.AttachmentID)
	e.clear(t)
	// INSERT must not seed a forged lease before the upload is finalized.
	e.assertRuntimeDenied(t, `INSERT INTO files.attachments(id,workspace_id,uploader_id,destination_kind,channel_id,
 original_filename,declared_mime,storage_provider,storage_object_key,envelope_version,dek_wrap_version,
 wrapped_dek,kek_key_id,scan_lease_until) VALUES($1,$2,$3,'channel',$4,'synthetic.txt','text/plain',
 'seaweedfs',$5,1,2,'\x01','test-only',now()+interval '1 hour')`, uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString())
}

func (e *scanPG) privilegesRemediation(t *testing.T) {
	roles := []string{"SET ROLE nchat_migrator", "SET ROLE postgres", "SET SESSION AUTHORIZATION nchat_migrator"}
	for _, sql := range roles {
		if _, err := e.app.Exec(e.ctx, sql); scanCode(err) != "42501" {
			t.Fatalf("privilege escalation %q: %v", sql, err)
		}
	}
	var canCreate, member, evidenceWritable bool
	requireScanPG(t, e.app.QueryRow(e.ctx, `SELECT has_schema_privilege(current_user,'files','CREATE')
 OR has_schema_privilege(current_user,'public','CREATE')
 OR has_schema_privilege(current_user,'auth','CREATE') OR has_schema_privilege(current_user,'chat','CREATE'),pg_has_role(current_user,'nchat_migrator','MEMBER'),
 has_column_privilege(current_user,'files.attachments','scan_signature_generation','UPDATE')`).Scan(&canCreate, &member, &evidenceWritable))
	if canCreate || member || evidenceWritable {
		t.Fatal("runtime regained privileged schema/role/evidence access")
	}
	e.assertFunctionACLs(t)
	j := e.seed(t)
	conn, err := e.app.Acquire(e.ctx)
	requireScanPG(t, err)
	defer conn.Release()
	_, err = conn.Exec(e.ctx, "SET nchat.scan_queue_write='on'")
	requireScanPG(t, err)
	_, err = conn.Exec(e.ctx, "UPDATE files.attachments SET status='failed' WHERE id=$1", j.AttachmentID)
	if scanCode(err) != "NC004" {
		t.Fatalf("client GUC forged trigger authority: %v", err)
	}
	_, err = conn.Exec(e.ctx, "RESET nchat.scan_queue_write")
	requireScanPG(t, err)
	e.clear(t)
}
func (e *scanPG) assertFunctionACLs(t *testing.T) {
	t.Helper()
	rows, err := e.app.Query(e.ctx, `SELECT p.proname,pg_get_userbyid(p.proowner),p.prosecdef,
 COALESCE(array_to_string(p.proconfig,','),''),has_function_privilege(current_user,p.oid,'EXECUTE'),
 EXISTS(SELECT 1 FROM aclexplode(COALESCE(p.proacl,acldefault('f',p.proowner))) a WHERE a.grantee=0 AND a.privilege_type='EXECUTE')
 FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid=p.pronamespace
 WHERE n.nspname='files' AND p.proname=ANY($1)`, []string{"scan_queue_policy_guard", "guard_scan_queue_write", "require_scan_fencing", "scan_fencing_enabled", "persist_proven_scan_internal", "persist_proven_scan", "claim_scans", "lock_scan_attempt", "persist_scan_v2", "retry_scan"})
	requireScanPG(t, err)
	defer rows.Close()
	count := 0
	for rows.Next() {
		var name, owner, config string
		var definer, allowed, public bool
		requireScanPG(t, rows.Scan(&name, &owner, &definer, &config, &allowed, &public))
		wantDefiner := name != "guard_scan_queue_write" && name != "scan_queue_policy_guard"
		wantAllowed := name == "scan_fencing_enabled" || name == "persist_proven_scan" || name == "claim_scans" || name == "persist_scan_v2" || name == "retry_scan"
		if owner != "nchat_migrator" || definer != wantDefiner || allowed != wantAllowed || public || strings.ReplaceAll(config, " ", "") != "search_path=pg_catalog,files,pg_temp" {
			t.Fatalf("unexpected function privileges: name=%s owner=%s definer=%v allowed=%v public=%v config=%s", name, owner, definer, allowed, public, config)
		}
		t.Logf("function=%s owner=%s definer=%v runtime_execute=%v public_execute=%v config=%s", name, owner, definer, allowed, public, config)
		count++
	}
	requireScanPG(t, rows.Err())
	if count != 10 {
		t.Fatalf("checked %d functions", count)
	}
}

func (e *scanPG) retryReasons(t *testing.T) {
	for _, reason := range []string{"signature_policy", "database"} {
		e.seed(t)
		j := e.claim(t)
		requireScanPG(t, e.queue.RetryScan(e.ctx, j, 10*time.Second, reason))
		var stored, status string
		requireScanPG(t, e.app.QueryRow(e.ctx, "SELECT scan_last_error,status FROM files.attachments WHERE id=$1", j.AttachmentID).Scan(&stored, &status))
		if stored != reason || status != "pending_scan" {
			t.Fatalf("persisted retry=%s/%s", stored, status)
		}
		e.assertRuntimeDenied(t, "UPDATE files.attachments SET status='failed' WHERE id=$1", j.AttachmentID)
		e.clear(t)
	}
}

// SEC-C-001: the deadline is checked while obtaining the row lock. This test
// crosses the actual database deadline, not an arbitrary fixed sleep.
func (e *scanPG) leaseLinearization(t *testing.T) {
	e.seed(t)
	j := e.claim(t)
	e.exec(t, "UPDATE files.attachments SET scan_lease_until=clock_timestamp()+interval '1 second' WHERE id=$1", j.AttachmentID)
	var deadline time.Time
	requireScanPG(t, e.app.QueryRow(e.ctx, "SELECT scan_lease_until FROM files.attachments WHERE id=$1", j.AttachmentID).Scan(&deadline))
	tx, err := e.app.Begin(e.ctx)
	requireScanPG(t, err)
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	var outcome string
	requireScanPG(t, tx.QueryRow(e.ctx, "SELECT result_outcome FROM files.persist_scan_v2($1,$2,$3,'clean',0,NULL,NULL,NULL,NULL,NULL,NULL)", j.AttachmentID, j.WorkspaceID, j.ClaimToken).Scan(&outcome))
	if outcome != "applied" {
		t.Fatalf("authority acquisition=%s", outcome)
	}
	e.exec(t, "SELECT pg_sleep(GREATEST(0,EXTRACT(EPOCH FROM ($1::timestamptz-clock_timestamp()))))", deadline)
	var expired bool
	requireScanPG(t, e.app.QueryRow(e.ctx, "SELECT clock_timestamp()>=$1", deadline).Scan(&expired))
	if !expired {
		t.Fatal("deadline barrier not reached")
	}
	jobs, err := e.queue.ClaimDueScans(e.ctx, 1, 330*time.Second)
	requireScanPG(t, err)
	if len(jobs) != 0 {
		t.Fatal("row lock allowed a second authority")
	}
	requireScanPG(t, tx.Commit(e.ctx))
	e.assertState(t, j, "clean")
	t.Log("commit after absolute deadline allowed; competing claim refused while row lock held (SEC-C-001 LOW remains open)")
	e.clear(t)
}
