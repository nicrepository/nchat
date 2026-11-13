package httpapi_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nicrepository/nchat/libs/go/platform/observability"
	"github.com/nicrepository/nchat/services/file-service/internal/crypto"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	httpapi "github.com/nicrepository/nchat/services/file-service/internal/http"
	"github.com/nicrepository/nchat/services/file-service/internal/scanner"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
	"github.com/nicrepository/nchat/services/file-service/internal/signatureguard"
	"github.com/nicrepository/nchat/services/file-service/internal/signatures"
	"github.com/nicrepository/nchat/services/file-service/internal/storage"
)

// Real crypto, format, Guardian/clamd, PostgreSQL authorization/runtime grants,
// B+C gates and HTTP delivery. Only the object store and JWT validator are
// local fixtures; browser rendering remains a separate contract.
type lostEfficacyEvent struct {
	pool    *pgxpool.Pool
	calls   int
	t       *testing.T
	failure error
}

func (p *lostEfficacyEvent) PublishAttachmentStatus(ctx context.Context, c service.AttachmentStatusChange) error {
	var status string
	efficacyOK(p.t, p.pool.QueryRow(ctx, "SELECT status FROM files.attachments WHERE id=$1", c.AttachmentID).Scan(&status))
	if status != string(c.Status) {
		p.t.Fatal("event before authorized commit")
	}
	p.calls++
	return p.failure
}

func TestIsolatedMalwareEfficacyHTTP(t *testing.T) {
	if os.Getenv("NCHAT_D_EFFICACY_TEST") != "owned-network-none" {
		t.Skip("requires scripts/testing/clamav-efficacy-isolated.sh")
	}
	t.Setenv("PROMETHEUS_METRICS_ENABLED", "true")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, "host=/pgsocket dbname=nchat_scan_test user=nchat_app sslmode=disable")
	efficacyOK(t, err)
	defer pool.Close()
	seedEfficacyDestinations(t, ctx, pool)
	malware := efficacyGuardian(t, ctx)
	for _, destination := range []struct {
		name   string
		target domain.Destination
	}{
		{"channel", domain.Destination{Kind: domain.DestinationKindChannel, ID: testChannelID}},
		{"direct", domain.Destination{Kind: domain.DestinationKindDM, ID: testDMID}},
		{"group", domain.Destination{Kind: domain.DestinationKindDM, ID: efficacyGroupID}},
	} {
		for _, tc := range []struct {
			name, body string
			clean      bool
		}{
			{"benign", "NChat synthetic benign original, preserved byte for byte.\n", true},
			{"eicar", signatureguard.SyntheticPayload, false},
		} {
			t.Run(destination.name+"/"+tc.name, func(t *testing.T) {
				f, worker, metrics, publisher := newEfficacyFixture(t, ctx, pool, malware, []byte(tc.body), destination.target)
				assertEfficacyBlocked(t, f)
				// Prove quarantine storage is ciphertext, not the original bytes.
				f.objects.mu.Lock()
				cipher := append([]byte(nil), f.objects.objects[f.store.pending.StorageObjectKey]...)
				f.objects.mu.Unlock()
				if bytes.Contains(cipher, []byte(tc.body)) {
					t.Fatal("plaintext stored")
				}
				n, err := worker.ProcessDue(ctx)
				efficacyOK(t, err)
				if n != 1 {
					t.Fatal("no real scan executed")
				}
				var status string
				efficacyOK(t, pool.QueryRow(ctx, "SELECT status FROM files.attachments WHERE id=$1", f.store.attachmentID()).Scan(&status))
				if tc.clean {
					if status != "clean" {
						t.Fatal("benign not approved")
					}
					downloaded := f.get(t, "/content", nil)
					if downloaded.Code != 200 || !bytes.Equal(downloaded.Body.Bytes(), []byte(tc.body)) || sha256.Sum256(downloaded.Body.Bytes()) != sha256.Sum256([]byte(tc.body)) {
						t.Fatal("original download changed")
					}
					requireScanSamples(t, scrapeScanMetrics(metrics), `nchat_file_malware_scans_total{result="clean"} 1`, `nchat_file_malware_scan_finalized_to_verdict_seconds_count{result="clean"} 1`)
				} else {
					if status != "rejected" {
						t.Fatal("EICAR not rejected by real engine")
					}
					assertEfficacyBlocked(t, f)
					body := scrapeScanMetrics(metrics)
					requireScanSamples(t, body, `nchat_file_malware_scans_total{result="infected"} 1`)
					requireScanSamples(t, body, `nchat_file_malware_scans_total{result="clean"} 0`)
				}
				// Lost event cannot undo commit; authorized refetch observes durable state.
				metadata := f.get(t, "", nil)
				if metadata.Code != http.StatusOK || !bytes.Contains(metadata.Body.Bytes(), []byte(`"status":"`+status+`"`)) {
					t.Fatal("refetch after lost event did not reconcile")
				}
				if publisher.calls != 1 {
					t.Fatal("missing post-commit publication attempt")
				}
				requireScanSamples(t, scrapeScanMetrics(metrics), `nchat_file_malware_scan_publications_total{result="error"} 1`)
			})
		}
	}
	if os.Getenv("NCHAT_D_BENCHMARK") == "true" {
		runEfficacyBenchmark(t, ctx, pool, malware)
	}

}

func newEfficacyFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, malware scanner.Scanner, body []byte, destination domain.Destination) (*gateFixture, *service.MalwareScanService, *observability.Metrics, *lostEfficacyEvent) {
	t.Helper()
	keys, err := crypto.NewKeyring(gateKeyID, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, crypto.KeySize)), "")
	efficacyOK(t, err)
	locks, ok := storage.LockConnPoolFrom(pool)
	if !ok {
		t.Fatal("no fence pool")
	}
	db := storage.NewFencedAttachmentStore(pool, storage.NewPGXAttachmentFence(locks, pool, nil))
	memory := &gateStore{}
	objects := newGateObjects()
	metrics := observability.NewMetrics(observability.LoadConfig("file-service"))
	a := httpapi.NewAttachmentMetrics(metrics)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	attachments := service.NewAttachmentService(storage.NewPGXDestinationAuthorizer(pool), db, objects, keys, domain.DefaultMaxUploadBytes, true, a, logger)
	target, err := attachments.AuthorizeUpload(ctx, service.AuthorizeUploadInput{Destination: destination, UserID: testUserID, SessionID: testSessionID})
	efficacyOK(t, err)
	view, err := attachments.Upload(ctx, service.UploadInput{Target: target, Filename: "synthetic.txt", DeclaredMIME: "text/plain", Content: bytes.NewReader(body)})
	efficacyOK(t, err)
	if view.Status != "pending_scan" {
		t.Fatal("upload skipped quarantine")
	}
	memory.pending.ID = view.ID
	efficacyOK(t, pool.QueryRow(ctx, "SELECT storage_object_key FROM files.attachments WHERE id=$1", view.ID).Scan(&memory.pending.StorageObjectKey))
	// A valid session in another workspace cannot discover this attachment.
	_, denied := attachments.Metadata(ctx, service.AttachmentAuthInput{AttachmentID: view.ID, UserID: efficacyOtherUser, SessionID: efficacyOtherSession})
	if !errors.Is(denied, domain.ErrNotFound) {
		t.Fatalf("cross-tenant metadata: %v", denied)
	}
	f := &gateFixture{router: newTestRouter(t, attachments, enabledConfig()), store: memory, objects: objects, keys: keys}
	publisher := &lostEfficacyEvent{pool: pool, t: t, failure: errors.New("synthetic notification loss")}
	worker := service.NewMalwareScanService(storage.NewPGXScanStore(pool), db, objects, keys, malware, publisher, a, 30*time.Second, logger)
	return f, worker, metrics, publisher
}
func assertEfficacyBlocked(t *testing.T, f *gateFixture) {
	t.Helper()
	for _, route := range []struct {
		suffix  string
		headers map[string]string
	}{
		{"/content", nil}, {"/content", map[string]string{"Range": "bytes=0-9"}},
		{"/preview", nil}, {"/audio.mp3", nil}, {"/document-preview", nil}, {"/document-preview/pages/1", nil},
	} {
		assertRefusedWithoutContent(t, f.get(t, route.suffix, route.headers), f.objects)
	}
}
func efficacyGuardian(t *testing.T, ctx context.Context) scanner.Scanner {
	t.Helper()
	age := 365 * 24 * time.Hour // Offline historic signed fixture only; no operational policy change.
	store := signatures.Store{Root: t.TempDir()}
	candidate := filepath.Join(store.Root, "candidate")
	efficacyOK(t, signatures.CopyDatabases("/var/lib/clamav", candidate))
	manifest, err := signatures.Verify(ctx, candidate, time.Now().UTC(), age)
	efficacyOK(t, err)
	efficacyOK(t, store.WithLock(func() error { return store.Promote(candidate, manifest, time.Now().UTC()) }))
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- signatureguard.Serve(runCtx, store, "/test/clamd.conf", "127.0.0.1:3310", age) }()
	t.Cleanup(sync.OnceFunc(func() {
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("Guardian shutdown: %v", err)
			}
		case <-time.After(15 * time.Second):
			t.Error("Guardian did not stop")
		}
	}))
	client := &http.Client{Timeout: time.Second}
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		response, err := client.Get("http://127.0.0.1:3310/readyz")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == 200 {
				break
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal("Guardian unavailable")
		case <-tick.C:
		}
	}
	malware, err := scanner.NewGuarded("127.0.0.1:3310", 30*time.Second, age)
	efficacyOK(t, err)
	return malware
}
func efficacyOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

const (
	efficacyGroupID        = "88888888-8888-4888-8888-888888888888"
	efficacyOtherUser      = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	efficacyOtherSession   = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	efficacyOtherWorkspace = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
)

func seedEfficacyDestinations(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(ctx, `WITH users AS (
 INSERT INTO auth.users(id,email,display_name) VALUES($1,'member@example.test','Synthetic member'),($2,'other@example.test','Other tenant')
 ), workspaces AS (
 INSERT INTO chat.workspaces(id,slug,name) VALUES($3,'d-corpus','Synthetic corpus'),($4,'d-other','Other tenant')
 ), members AS (
 INSERT INTO chat.workspace_members(workspace_id,user_id) VALUES($3,$1),($4,$2)
 ) INSERT INTO chat.channels(id,workspace_id,slug,display_name,type,is_general)
 VALUES($5,$3,'geral','Geral','public',true),(gen_random_uuid(),$4,'geral','Geral','public',true)`,
		testUserID, efficacyOtherUser, gateWorkspaceID, efficacyOtherWorkspace, testChannelID)
	efficacyOK(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO auth.user_sessions(id,user_id,refresh_token_hash,idle_expires_at)
 VALUES($1,$2,'synthetic-session-main',now()+interval '1 hour'),($3,$4,'synthetic-session-other',now()+interval '1 hour')`, testSessionID, testUserID, efficacyOtherSession, efficacyOtherUser)
	efficacyOK(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO chat.dm_conversations(id,workspace_id,type,direct_pair_key,created_by)
 VALUES($1,$3,'direct','synthetic-direct-pair',$4),($2,$3,'group',NULL,$4)`, testDMID, efficacyGroupID, gateWorkspaceID, testUserID)
	efficacyOK(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO chat.dm_members(conversation_id,user_id) VALUES($1,$3),($2,$3)`, testDMID, efficacyGroupID, testUserID)
	efficacyOK(t, err)
}

// Fixed corpus and concurrency=1; direct ProcessDue intentionally excludes the
// scheduler's idle poll. This component measurement cannot validate production SLOs.
func runEfficacyBenchmark(t *testing.T, ctx context.Context, pool *pgxpool.Pool, malware scanner.Scanner) {
	t.Helper()
	for _, size := range []int{1024, 1 << 20, 10 << 20} {
		body := bytes.Repeat([]byte("NChat isolated benign corpus.\n"), size/29+1)[:size]
		samples := map[string][]float64{"queue_age": {}, "scan_attempt": {}, "finalized_to_clean_ack": {}, "finalized_to_download_complete": {}}
		started := time.Now()
		for i := 0; i < 11; i++ {
			f, worker, metrics, publisher := newEfficacyFixture(t, ctx, pool, malware, body, domain.Destination{Kind: domain.DestinationKindChannel, ID: testChannelID})
			publisher.failure = nil
			var finalized time.Time
			efficacyOK(t, pool.QueryRow(ctx, "SELECT uploaded_at FROM files.attachments WHERE id=$1", f.store.attachmentID()).Scan(&finalized))
			n, err := worker.ProcessDue(ctx)
			efficacyOK(t, err)
			if n != 1 {
				t.Fatal("benchmark claim absent")
			}
			response := f.get(t, "/content", nil)
			if response.Code != 200 || sha256.Sum256(response.Body.Bytes()) != sha256.Sum256(body) {
				t.Fatal("benchmark correctness failed")
			}
			downloadElapsed := time.Since(finalized).Seconds()
			if i == 0 {
				continue
			} // One documented warm-up per size; never hide errors.
			exposed := scrapeScanMetrics(metrics)
			for key, name := range map[string]string{
				"queue_age":              "nchat_file_malware_scan_wait_seconds_sum",
				"scan_attempt":           "nchat_file_malware_scan_duration_seconds_sum",
				"finalized_to_clean_ack": `nchat_file_malware_scan_finalized_to_verdict_seconds_sum{result="clean"}`,
			} {
				samples[key] = append(samples[key], efficacySample(t, exposed, name))
			}
			samples["finalized_to_download_complete"] = append(samples["finalized_to_download_complete"], downloadElapsed)
		}
		quantiles := map[string][]float64{}
		for key, values := range samples {
			sort.Float64s(values)
			quantiles[key] = []float64{values[4], values[9], values[9]}
		}
		report := map[string]any{"bytes": size, "format": "text/plain", "sha256": fmt.Sprintf("%x", sha256.Sum256(body)), "samples": 10, "warmup": 1, "concurrency": 1, "seconds_including_warmup": time.Since(started).Seconds(), "p50_p95_p99_seconds_nearest_rank": quantiles, "errors": 0, "scheduler": "direct ProcessDue, no idle poll", "storage": "in-memory ciphertext fixture"}
		encoded, err := json.Marshal(report)
		efficacyOK(t, err)
		t.Logf("D_BENCHMARK %s", encoded)
	}
}
func efficacySample(t *testing.T, exposed, name string) float64 {
	t.Helper()
	for _, line := range strings.Split(exposed, "\n") {
		if strings.HasPrefix(line, name+" ") {
			value, err := strconv.ParseFloat(strings.TrimPrefix(line, name+" "), 64)
			efficacyOK(t, err)
			return value
		}
	}
	t.Fatalf("benchmark metric absent: %s", name)
	return 0
}
