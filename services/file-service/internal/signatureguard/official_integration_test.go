package signatureguard

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/file-service/internal/scanner"
	"github.com/nicrepository/nchat/services/file-service/internal/signatures"
)

func TestIsolatedGuardianRealEngine(t *testing.T) {
	if os.Getenv("NCHAT_CLAMAV_ISOLATED_TEST") != "true" {
		t.Skip("requires isolated official ClamAV image")
	}
	age := 365 * 24 * time.Hour // Fixture age only; deliberately not an approved default.
	store := signatures.Store{Root: t.TempDir()}
	candidate := filepath.Join(store.Root, "candidate")
	requireSuccess(t, signatures.CopyDatabases("/var/lib/clamav", candidate))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	manifest, err := signatures.Verify(ctx, candidate, time.Now().UTC(), age)
	requireSuccess(t, err)
	requireSuccess(t, store.WithLock(func() error { return store.Promote(candidate, manifest, time.Now().UTC()) }))
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- Serve(runCtx, store, "/test/clamd.conf", "127.0.0.1:3310", age) }()
	finished := false
	t.Cleanup(sync.OnceFunc(func() { stopRealGuardian(t, stop, done, &finished) }))
	waitReady(t, ctx)
	client, err := scanner.NewGuarded("127.0.0.1:3310", 10*time.Second, age)
	requireSuccess(t, err)
	for _, test := range []struct {
		body    string
		verdict scanner.Verdict
	}{{"benign isolated original", scanner.VerdictClean}, {SyntheticPayload, scanner.VerdictInfected}} {
		assertRealScan(t, ctx, client, test.body, test.verdict, manifest.ID())
	}
	// Removing the publisher state closes admission at the next poll and drains.
	select {
	case err := <-done:
		finished = true
		t.Fatalf("guardian exited before publication loss: %v", err)
	default:
	}
	requireSuccess(t, os.Remove(filepath.Join(store.Root, "state.json")))
	select {
	case err := <-done:
		finished = true
		if err == nil || err.Error() != "signature publication unavailable" {
			t.Fatalf("unexpected publication-loss shutdown: %v", err)
		}
	case <-time.After(35 * time.Second):
		t.Fatal("publication loss left guardian available")
	}
	if _, err = client.Scan(ctx, strings.NewReader("benign")); err == nil {
		t.Fatal("offline guardian approved")
	}
}

func stopRealGuardian(t *testing.T, stop context.CancelFunc, done <-chan error, finished *bool) {
	t.Helper()
	stop()
	if *finished {
		return
	}
	select {
	case err := <-done:
		*finished = true
		if !errors.Is(err, context.Canceled) {
			t.Errorf("unexpected guardian shutdown: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Error("guardian failed to stop")
	}
}

func waitReady(t *testing.T, ctx context.Context) {
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
			t.Fatal("real engine failed startup")
		case <-tick.C:
		}
	}
}

func assertRealScan(t *testing.T, ctx context.Context, client *scanner.Guarded, body string, want scanner.Verdict, generation string) {
	t.Helper()
	verdict, proof, err := client.ScanWithEvidence(ctx, strings.NewReader(body))
	requireSuccess(t, err)
	if verdict != want || proof.Generation != generation {
		t.Fatal("real scan lost verdict or generation")
	}
}
