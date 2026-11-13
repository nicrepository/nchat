package storage_test

import (
	"github.com/nicrepository/nchat/services/file-service/internal/storage"
	"strings"
	"testing"
)

// Reuses the owned PG16/17 runner and real runtime grants. No schema changes.
func (e *scanPG) queueObservability(t *testing.T) {
	e.clear(t)
	a := e.seed(t)
	b := e.seed(t)
	c := e.seed(t)
	e.seed(t)
	e.exec(t, "UPDATE files.attachments SET uploaded_at=now()-interval '20 seconds' WHERE id=$1", a.AttachmentID)
	e.exec(t, "UPDATE files.attachments SET uploaded_at=now()-interval '10 seconds',scan_attempts=2,scan_next_attempt_at=now()+interval '60 seconds' WHERE id=$1", b.AttachmentID)
	e.exec(t, "UPDATE files.attachments SET uploaded_at=now()-interval '10 seconds',scan_lease_until=now()+interval '60 seconds',scan_claim_token = gen_random_uuid() WHERE id=$1", c.AttachmentID)
	snapshot, err := e.queue.ScanQueueSnapshot(e.ctx)
	requireScanPG(t, err)
	if snapshot.Depth != 4 || snapshot.Eligible != 2 || snapshot.Leased != 1 || snapshot.Backoff != 1 || snapshot.UnknownFinalization != 1 || snapshot.OverFiveSeconds != 3 || snapshot.OldestAge.Seconds() < 20 || !snapshot.PolicyEnabled {
		t.Fatalf("inconsistent global snapshot: %+v", snapshot)
	}
	// Collection does not acquire a row lock or mutate claims; another claim works.
	j := e.claim(t)
	e.assertState(t, j, "pending_scan")
	after, err := e.queue.ScanQueueSnapshot(e.ctx)
	requireScanPG(t, err)
	if after.Depth != 4 || after.Leased != 2 || after.Eligible != 1 {
		t.Fatalf("claim not reflected: %+v", after)
	}
	e.clear(t)
	empty, err := e.queue.ScanQueueSnapshot(e.ctx)
	requireScanPG(t, err)
	if empty.Depth != 0 || empty.OldestAge != 0 || empty.UnknownFinalization != 0 {
		t.Fatal("empty queue is not zero")
	}
	e.seed(t)
	e.exec(t, `INSERT INTO files.attachments(id,workspace_id,uploader_id,destination_kind,channel_id,
 original_filename,declared_mime,storage_provider,storage_object_key,envelope_version,dek_wrap_version,
 wrapped_dek,kek_key_id,status,scan_next_attempt_at)
 SELECT gen_random_uuid(),workspace_id,uploader_id,destination_kind,channel_id,original_filename,
 declared_mime,storage_provider,gen_random_uuid()::text,envelope_version,dek_wrap_version,
 wrapped_dek,kek_key_id,status,scan_next_attempt_at FROM files.attachments CROSS JOIN generate_series(1,20000)`)
	limited, err := e.queue.ScanQueueSnapshot(e.ctx)
	if err == nil || limited.Depth != 0 {
		t.Fatal("truncated snapshot reported authoritative state")
	}
	e.exec(t, "ANALYZE files.attachments")
	capture := &fakePool{}
	_, _ = storage.NewPGXScanStore(capture).ScanQueueSnapshot(e.ctx)
	rows, err := e.app.Query(e.ctx, "EXPLAIN (ANALYZE,BUFFERS) "+capture.lastSQL)
	requireScanPG(t, err)
	for rows.Next() {
		var line string
		requireScanPG(t, rows.Scan(&line))
		if strings.Contains(line, "Sort ") {
			t.Fatal("unbounded sort before LIMIT")
		}
		t.Log(line)
	}
	requireScanPG(t, rows.Err())
	rows.Close()
	e.clear(t)
}
