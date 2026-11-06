package signatureguard

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/file-service/internal/scanner"
	"github.com/nicrepository/nchat/services/file-service/internal/signatures"
)

type scanFunc func(context.Context, io.Reader) (scanner.Verdict, error)

func (f scanFunc) Scan(ctx context.Context, r io.Reader) (scanner.Verdict, error) { return f(ctx, r) }
func testGuard() *Guard {
	now := time.Now().UTC()
	return &Guard{MaxAge: 2 * time.Hour, LastUpdate: now, Evidence: signatures.Evidence{
		Generation: strings.Repeat("a", 64), Engine: signatures.Engine, DailyVersion: 1, DatabaseTime: now.Add(-time.Hour), LoadedAt: now.Add(-time.Minute)},
		Scanner: scanFunc(func(_ context.Context, r io.Reader) (scanner.Verdict, error) {
			_, err := io.Copy(io.Discard, r)
			return scanner.VerdictClean, err
		})}
}

func TestGuardAdmissionAndFreshness(t *testing.T) {
	for name, change := range map[string]func(*Guard){
		"valid":             func(*Guard) {},
		"expired":           func(g *Guard) { g.MaxAge = time.Hour },
		"missing policy":    func(g *Guard) { g.MaxAge = 0 },
		"closed":            func(g *Guard) { g.Close() },
		"clock rollback":    func(g *Guard) { g.lastClock = time.Now().Add(time.Hour).UnixNano() },
		"reload incomplete": func(g *Guard) { g.Evidence.Generation = "" },
		"incompatible":      func(g *Guard) { g.Evidence.Engine = "1.3.0" },
		"offline": func(g *Guard) {
			g.Scanner = scanFunc(func(context.Context, io.Reader) (scanner.Verdict, error) {
				return scanner.VerdictInfected, errors.New("offline")
			})
		},
		"early OK": func(g *Guard) {
			g.Scanner = scanFunc(func(context.Context, io.Reader) (scanner.Verdict, error) { return scanner.VerdictClean, nil })
		},
		"unknown verdict": func(g *Guard) {
			g.Scanner = scanFunc(func(_ context.Context, r io.Reader) (scanner.Verdict, error) {
				_, _ = io.Copy(io.Discard, r)
				return scanner.Verdict(42), nil
			})
		},
		"expires in flight": func(g *Guard) {
			g.Scanner = scanFunc(func(_ context.Context, r io.Reader) (scanner.Verdict, error) {
				_, _ = io.Copy(io.Discard, r)
				g.Close()
				return scanner.VerdictClean, nil
			})
		},
	} {
		t.Run(name, func(t *testing.T) {
			g := testGuard()
			change(g)
			w := httptest.NewRecorder()
			g.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/scan", strings.NewReader("complete")))
			if (w.Code == 200) != (name == "valid") {
				t.Fatalf("status %d", w.Code)
			}
			if name == "valid" {
				assertCompletedEvidence(t, w.Body.Bytes())
			}
		})
	}
}

func TestGuardHealthAndClosedAdmission(t *testing.T) {
	g := testGuard()
	for _, path := range []string{"/readyz", "/healthz"} {
		w := httptest.NewRecorder()
		g.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != 200 {
			t.Fatal(path)
		}
	}
	g.Close()
	w := httptest.NewRecorder()
	g.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if w.Code != 503 {
		t.Fatal("closed guardian ready")
	}
	w = httptest.NewRecorder()
	g.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin", nil))
	if w.Code != 404 {
		t.Fatal("admin surface")
	}
}

func TestGuardMetricsDescribeLoadedDatabase(t *testing.T) {
	g := testGuard()
	w := httptest.NewRecorder()
	g.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if w.Code != 200 {
		t.Fatal("metrics unavailable")
	}
	for _, metric := range []string{"nchat_clamav_signature_version 1", "nchat_clamav_signatures_ready 1", "nchat_clamav_signature_age_seconds", "nchat_clamav_engine_info{version=\"1.4.6\"}"} {
		if !strings.Contains(w.Body.String(), metric) {
			t.Fatal("missing", metric)
		}
	}
}

func TestGuardReportsInfectedWithoutSignatureDetails(t *testing.T) {
	g := testGuard()
	g.Scanner = scanFunc(func(_ context.Context, r io.Reader) (scanner.Verdict, error) {
		_, err := io.Copy(io.Discard, r)
		return scanner.VerdictInfected, err
	})
	w := httptest.NewRecorder()
	g.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/scan", strings.NewReader("probe")))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"result":"infected"`) {
		t.Fatal("lost rejection")
	}
}

func assertCompletedEvidence(t *testing.T, data []byte) {
	t.Helper()
	var result scanner.GuardedReply
	requireSuccess(t, json.Unmarshal(data, &result))
	if result.Result != "clean" || result.Evidence.CompletedAt.IsZero() {
		t.Fatal("missing completed proof")
	}
}

func TestPrivateConfigPreservesScanLimits(t *testing.T) {
	base := "../../../../infra/k8s/base/services/clamav/clamd.conf"
	if os.Getenv("NCHAT_CLAMAV_ISOLATED_TEST") == "true" {
		base = "/test/clamd.conf"
	}
	path, err := privateConfig(base, t.TempDir())
	requireSuccess(t, err)
	f, err := os.OpenInRoot(filepath.Dir(path), filepath.Base(path))
	requireSuccess(t, err)
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(f)
	requireSuccess(t, err)
	for _, required := range []string{"AlertExceedsMax yes", "MaxScanTime 420000", "StreamMaxLength 512M", "MaxThreads 4", "SelfCheck 0", "OfficialDatabaseOnly yes"} {
		if !strings.Contains(string(data), required) {
			t.Fatal("lost", required)
		}
	}
	if strings.Contains(string(data), "\nTCPSocket ") || strings.Contains(string(data), "\nTCPAddr ") {
		t.Fatal("raw clamd TCP exposed")
	}
}

func requireSuccess(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestSyntheticMustDetectAndAcceptBenign(t *testing.T) {
	for _, verdict := range []scanner.Verdict{scanner.VerdictClean, scanner.VerdictInfected} {
		local := scanFunc(func(context.Context, io.Reader) (scanner.Verdict, error) { return verdict, nil })
		if synthetic(context.Background(), local) == nil {
			t.Fatal("one-sided synthetic scanner accepted")
		}
	}
}

func TestUnavailablePublicationAndInvalidRuntimeConfiguration(t *testing.T) {
	store := signatures.Store{Root: t.TempDir()}
	if Serve(context.Background(), store, "missing", "127.0.0.1:0", time.Hour) == nil {
		t.Fatal("missing generation served")
	}
	if _, err := privateConfig("missing", t.TempDir()); err == nil {
		t.Fatal("missing scan limits accepted")
	}
	if serveGeneration(context.Background(), store, testGuard(), "bad listen address", make(chan struct{})) == nil {
		t.Fatal("invalid listener accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := serveGeneration(ctx, store, testGuard(), "127.0.0.1:0", make(chan struct{})); err != context.Canceled {
		t.Fatal("shutdown did not propagate")
	}
	died := make(chan struct{})
	close(died)
	if serveGeneration(context.Background(), store, testGuard(), "127.0.0.1:0", died) == nil {
		t.Fatal("dead engine served")
	}
}

func TestConcurrentReadinessDoesNotInventClockRollback(t *testing.T) {
	g := testGuard()
	var group sync.WaitGroup
	for range 100 {
		group.Go(func() {
			if !g.ready() {
				t.Error("concurrent checks closed healthy generation")
			}
		})
	}
	group.Wait()
	if !g.ready() {
		t.Fatal("healthy generation was permanently closed")
	}
}

func TestUnsafeOrIncompleteScanLimitsCannotStartDaemon(t *testing.T) {
	safe := "AlertExceedsMax yes\nMaxScanTime 420000\nStreamMaxLength 512M\nMaxFileSize 512M\nMaxScanSize 1024M\nMaxThreads 4\nMaxRecursion 12\nMaxFiles 10000\n"
	requireSuccess(t, validateScanLimits(safe))
	for _, unsafe := range []string{"", strings.Replace(safe, "AlertExceedsMax yes", "AlertExceedsMax no", 1), strings.Replace(safe, "420000", "120000", 1), safe + "MaxScanTime 420000\n"} {
		if validateScanLimits(unsafe) == nil {
			t.Fatal("unsafe scan limits accepted")
		}
	}
	source := filepath.Join(t.TempDir(), "clamd.conf")
	requireSuccess(t, os.WriteFile(source, []byte(strings.Repeat("#", 65537)), 0600))
	if _, err := privateConfig(source, t.TempDir()); err == nil {
		t.Fatal("truncated configuration accepted")
	}
	requireSuccess(t, os.WriteFile(source, []byte("Foreground yes\n"), 0600))
	if _, err := privateConfig(source, t.TempDir()); err == nil {
		t.Fatal("defaults could authorize incomplete scans")
	}
}
