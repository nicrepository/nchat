package service_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/scanner"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
	"github.com/nicrepository/nchat/services/file-service/internal/signatureguard"
	"github.com/nicrepository/nchat/services/file-service/internal/signatures"
	"github.com/nicrepository/nchat/services/file-service/internal/storage"
)

// The dedicated runner provides a disposable PostgreSQL over a private Unix
// socket volume. No host DSN, TCP port, shared namespace or app credential exists.
func TestIsolatedSignatureChainPersistsOnlyFreshEvidence(t *testing.T) {
	if os.Getenv("NCHAT_SIGNATURE_CHAIN_TEST") != "true" {
		t.Skip("requires owned network-none ClamAV/PostgreSQL pair")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, "host=/pgsocket dbname=signature_chain_test user=postgres sslmode=disable")
	chainOK(t, err)
	defer pool.Close()
	prepareSignaturePolicy(t, ctx, pool)
	malware, generation, stop := realSignatureGuardian(t, ctx)
	defer stop()
	valid := runSignatureChain(t, ctx, pool, malware)
	assertSignatureRow(t, ctx, pool, valid, "clean", generation)
	testProvenPolicyTransitions(t, ctx, pool, valid)
	testFreshProvenRescan(t, ctx, pool, valid)
	testConcurrentProvenTransition(t, ctx, pool, valid)
	_, err = pool.Exec(ctx, "UPDATE files.signature_policy SET max_age_seconds=1 WHERE id=1")
	chainOK(t, err)
	expired := runSignatureChain(t, ctx, pool, malware)
	assertSignatureRow(t, ctx, pool, expired, "pending_scan", "")
}

func prepareSignaturePolicy(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(ctx, `CREATE SCHEMA files; CREATE TABLE files.attachments (
 id uuid PRIMARY KEY,workspace_id uuid NOT NULL,status text NOT NULL,
 preview_status text NOT NULL DEFAULT 'pending',deleted_at timestamptz,
 format_policy_version integer NOT NULL DEFAULT 0,format_scan_version integer NOT NULL DEFAULT 0,
 scan_next_attempt_at timestamptz,updated_at timestamptz NOT NULL DEFAULT now());`)
	chainOK(t, err)
	migration, err := os.ReadFile("/test/signature-policy.up.sql")
	chainOK(t, err)
	_, err = pool.Exec(ctx, string(migration))
	chainOK(t, err)
	_, err = pool.Exec(ctx, `UPDATE files.signature_policy SET enabled=true,max_age_seconds=31536000,
 approval_reference='isolated-chain-fixture-only' WHERE id=1`)
	chainOK(t, err)
}

func realSignatureGuardian(t *testing.T, ctx context.Context) (scanner.Scanner, string, func()) {
	t.Helper()
	age := 365 * 24 * time.Hour
	store := signatures.Store{Root: t.TempDir()}
	candidate := filepath.Join(store.Root, "candidate")
	chainOK(t, signatures.CopyDatabases("/var/lib/clamav", candidate))
	m, err := signatures.Verify(ctx, candidate, time.Now().UTC(), age)
	chainOK(t, err)
	chainOK(t, store.WithLock(func() error { return store.Promote(candidate, m, time.Now().UTC()) }))
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- signatureguard.Serve(runCtx, store, "/test/clamd.conf", "127.0.0.1:3310", age) }()
	cleanup := sync.OnceFunc(func() { cancelSignatureGuardian(t, cancel, done) })
	t.Cleanup(cleanup)
	waitSignatureGuardian(t, ctx)
	client, err := scanner.NewGuarded("127.0.0.1:3310", 10*time.Second, age)
	chainOK(t, err)
	return client, m.ID(), cleanup
}

func cancelSignatureGuardian(t *testing.T, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		cancel()
		t.Errorf("guardian exited before cleanup: %v", err)
		return
	default:
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("unexpected guardian shutdown: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Error("guardian shutdown timed out")
	}
}

func runSignatureChain(t *testing.T, ctx context.Context, pool *pgxpool.Pool, malware scanner.Scanner) string {
	t.Helper()
	fixture := newScanFixture(t)
	job := fixture.storeAttachment(t, []byte("NChat complete authenticated original for signature chain\n"))
	_, err := pool.Exec(ctx, "INSERT INTO files.attachments (id,workspace_id,status) VALUES ($1,$2,'pending_scan')", job.AttachmentID, job.WorkspaceID)
	chainOK(t, err)
	worker := service.NewMalwareScanService(fixture.queue, storage.NewPGXAttachmentStore(pool), fixture.objects, fixture.keys, malware, nil, nil, 30*time.Second, discardLogger())
	_, err = worker.ProcessDue(ctx)
	chainOK(t, err)
	return job.AttachmentID
}

func assertSignatureRow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id, wantStatus, wantGeneration string) {
	t.Helper()
	var status, generation string
	err := pool.QueryRow(ctx, "SELECT status,coalesce(scan_signature_generation,'') FROM files.attachments WHERE id=$1", id).Scan(&status, &generation)
	chainOK(t, err)
	if status != wantStatus || generation != wantGeneration {
		t.Fatalf("untrusted persistence: status=%s generation_matches=%v", status, generation == wantGeneration)
	}
}

func waitSignatureGuardian(t *testing.T, ctx context.Context) {
	t.Helper()
	client := &http.Client{Timeout: time.Second}
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		response, err := client.Get("http://127.0.0.1:3310/readyz")
		if err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.StatusCode == 200 {
				return
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal("guardian failed startup")
		case <-tick.C:
		}
	}
}

func chainOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func readPersistedApproval(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id string) service.ScanApproval {
	t.Helper()
	a := service.ScanApproval{AttachmentID: id, Evidence: &signatures.Evidence{}}
	e := a.Evidence
	err := pool.QueryRow(ctx, `SELECT workspace_id::text,scan_signature_generation,scan_engine_version,
 scan_signature_version,scan_database_time,scan_loaded_at,scan_completed_at FROM files.attachments WHERE id=$1`, id).
		Scan(&a.WorkspaceID, &e.Generation, &e.Engine, &e.DailyVersion, &e.DatabaseTime, &e.LoadedAt, &e.CompletedAt)
	chainOK(t, err)
	return a
}

func testProvenPolicyTransitions(t *testing.T, ctx context.Context, pool *pgxpool.Pool, valid string) {
	t.Helper()
	store := storage.NewPGXAttachmentStore(pool)
	a := readPersistedApproval(t, ctx, pool, valid)
	_, err := store.MarkScanClean(ctx, a)
	assertSignatureError(t, err, service.ErrScanAlreadyRecorded)
	changed := *a.Evidence
	changed.DailyVersion++
	a.Evidence = &changed
	_, err = store.MarkScanClean(ctx, a)
	assertSignatureError(t, err, service.ErrScanConflict)
	a = readPersistedApproval(t, ctx, pool, valid)
	chainOK(t, pool.QueryRow(ctx, "SELECT gen_random_uuid()::text").Scan(&a.AttachmentID))
	_, err = store.MarkScanClean(ctx, a)
	assertSignatureError(t, err, domain.ErrNotFound)
	_, err = pool.Exec(ctx, "INSERT INTO files.attachments(id,workspace_id,status) VALUES($1,$2,'pending_scan')", a.AttachmentID, a.WorkspaceID)
	chainOK(t, err)
	_, err = pool.Exec(ctx, "UPDATE files.signature_policy SET enabled=false")
	chainOK(t, err)
	_, err = store.MarkScanClean(ctx, a)
	assertSignatureError(t, err, service.ErrSignaturePolicyInactive)
	_, err = pool.Exec(ctx, "UPDATE files.signature_policy SET enabled=true")
	chainOK(t, err)
	_, err = store.MarkScanClean(ctx, a)
	chainOK(t, err)
	_, err = pool.Exec(ctx, "DELETE FROM files.signature_policy")
	chainOK(t, err)
	_, err = store.MarkScanClean(ctx, a)
	assertSignatureError(t, err, service.ErrSignaturePolicyInvalid)
	_, err = pool.Exec(ctx, "INSERT INTO files.signature_policy VALUES(1,true,31536000,'isolated-chain-only')")
	chainOK(t, err)
}

func assertSignatureError(t *testing.T, got, want error) {
	t.Helper()
	if !errors.Is(got, want) {
		t.Fatalf("error=%v want=%v", got, want)
	}
}

func testConcurrentProvenTransition(t *testing.T, ctx context.Context, pool *pgxpool.Pool, valid string) {
	t.Helper()
	a := readPersistedApproval(t, ctx, pool, valid)
	chainOK(t, pool.QueryRow(ctx, "SELECT gen_random_uuid()::text").Scan(&a.AttachmentID))
	_, err := pool.Exec(ctx, "INSERT INTO files.attachments(id,workspace_id,status) VALUES($1,$2,'pending_scan')", a.AttachmentID, a.WorkspaceID)
	chainOK(t, err)
	tx, err := pool.Begin(ctx)
	chainOK(t, err)
	defer func() { _ = tx.Rollback(context.Background()) }()
	_, err = tx.Exec(ctx, "UPDATE files.attachments SET status='rejected' WHERE id=$1", a.AttachmentID)
	chainOK(t, err)
	done := make(chan error, 1)
	go func() { _, writeErr := storage.NewPGXAttachmentStore(pool).MarkScanClean(ctx, a); done <- writeErr }()
	chainOK(t, tx.Commit(ctx))
	assertSignatureError(t, <-done, service.ErrScanSuperseded)
	assertSignatureRow(t, ctx, pool, a.AttachmentID, "rejected", "")
}

func testFreshProvenRescan(t *testing.T, ctx context.Context, pool *pgxpool.Pool, valid string) {
	t.Helper()
	a := readPersistedApproval(t, ctx, pool, valid)
	a.Evidence.CompletedAt = time.Now().UTC()
	state, err := storage.NewPGXAttachmentStore(pool).MarkScanClean(ctx, a)
	chainOK(t, err)
	if !state.ScanStatusUnchanged {
		t.Fatal("rescan advertised a new status transition")
	}
}
