package httpapi_test

import (
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/observability"
	httpapi "github.com/nicrepository/nchat/services/file-service/internal/http"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
)

func scrapeScanMetrics(m *observability.Metrics) string {
	r := httptest.NewRecorder()
	m.Handler().ServeHTTP(r, httptest.NewRequest("GET", "/metrics", nil))
	return r.Body.String()
}
func requireScanSamples(t *testing.T, body string, samples ...string) {
	t.Helper()
	for _, sample := range samples {
		if !strings.Contains(body, sample+"\n") {
			t.Errorf("missing sample %s", sample)
		}
	}
}
func TestScanSnapshotFailureRetainsValuesAndReportsStaleness(t *testing.T) {
	t.Setenv("PROMETHEUS_METRICS_ENABLED", "true")
	m := observability.NewMetrics(observability.LoadConfig("file-service"))
	a := httpapi.NewAttachmentMetrics(m)
	a.SetScanRequired(true)
	s := service.ScanQueueSnapshot{Depth: 9, OldestAge: 12 * time.Second, OverFiveSeconds: 4, UnknownFinalization: 1, Backoff: 2, PolicyEnabled: true, ObservedAt: time.Unix(100, 0)}
	a.ObserveScanQueueSnapshot(s, nil)
	a.SetScanQueueDepth(s.Depth)
	a.ObserveScanQueueSnapshot(service.ScanQueueSnapshot{}, errors.New("private database address"))
	body := scrapeScanMetrics(m)
	requireScanSamples(t, body, "nchat_file_malware_scan_required 1", "nchat_file_malware_scan_queue_depth 9",
		"nchat_file_malware_scan_oldest_pending_age_seconds 12", "nchat_file_malware_scan_pending_over_5_seconds 4",
		"nchat_file_malware_scan_pending_unknown_finalization 1", "nchat_file_malware_scan_backoff 2",
		"nchat_file_malware_scan_queue_snapshot_success 0", "nchat_file_malware_scan_queue_snapshot_timestamp_seconds 100",
		"nchat_file_malware_scan_queue_snapshot_errors_total 1", "nchat_file_malware_scan_queue_policy_enabled 1")
	for _, suffix := range []string{"required", "queue_snapshot_success", "queue_snapshot_timestamp_seconds", "queue_policy_enabled", "oldest_pending_age_seconds", "pending_over_5_seconds", "pending_unknown_finalization", "backoff"} {
		if strings.Count(body, "# TYPE nchat_file_malware_scan_"+suffix+" gauge") != 1 {
			t.Fatalf("wrong gauge type/registration: %s", suffix)
		}
	}
	if strings.Count(body, "# TYPE nchat_file_malware_scan_queue_snapshot_errors_total counter") != 1 {
		t.Fatal("wrong error counter type")
	}
	if strings.Contains(body, "private") {
		t.Fatal("error text leaked")
	}
	a.ObserveScanQueueSnapshot(s, nil)
	requireScanSamples(t, scrapeScanMetrics(m), "nchat_file_malware_scan_queue_snapshot_success 1")
}

func TestScanDiagnosticsBoundLabelsDurationsAndConcurrentAttempts(t *testing.T) {
	t.Setenv("PROMETHEUS_METRICS_ENABLED", "true")
	m := observability.NewMetrics(observability.LoadConfig("file-service"))
	a := httpapi.NewAttachmentMetrics(m)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			a.ScanStarted(time.Second)
			a.ObserveScanPhase("scanner_stream", 2*time.Second)
			a.ObserveScanCommitted("clean", 3*time.Second)
			a.ObserveScanFailure("timeout")
			a.ObserveScanPublication("error")
			a.ScanFinished(4 * time.Second)
		})
	}
	wg.Wait()
	a.ObserveScanPhase("scanner_stream", -time.Second)
	a.ObserveScanCommitted("clean", -time.Second)
	for _, secret := range []string{"user-123", "workspace-456", "private.txt"} {
		a.ObserveScanPhase(secret, time.Second)
		a.ObserveScanCommitted(secret, time.Second)
		a.ObserveScanFailure(secret)
		a.ObserveScanPublication(secret)
		a.ObserveScan(secret)
		a.ObserveScanBackoff(time.Second, secret)
	}
	body := scrapeScanMetrics(m)
	requireScanSamples(t, body, "nchat_file_malware_scan_active 0", "nchat_file_malware_scan_claims_total 8",
		`nchat_file_malware_scan_phase_duration_seconds_count{phase="scanner_stream"} 8`,
		`nchat_file_malware_scan_phase_duration_seconds_sum{phase="scanner_stream"} 16`,
		`nchat_file_malware_scan_phase_duration_seconds_bucket{phase="scanner_stream",le="2"} 8`,
		`nchat_file_malware_scan_finalized_to_verdict_seconds_count{result="clean"} 8`,
		`nchat_file_malware_scan_failures_total{reason="timeout"} 8`,
		`nchat_file_malware_scan_publications_total{result="error"} 8`)
	for _, marker := range []string{"user-123", "workspace-456", "private.txt"} {
		if strings.Contains(body, marker) {
			t.Fatal("unbounded label")
		}
	}
	if strings.Count(body, "# TYPE nchat_file_malware_scan_phase_duration_seconds histogram") != 1 {
		t.Fatal("wrong registration/type")
	}
	// Replicas use independent registries; authoritative queue values are not deltas.
	replica := observability.NewMetrics(observability.LoadConfig("file-service"))
	b := httpapi.NewAttachmentMetrics(replica)
	a.SetScanQueueDepth(7)
	b.SetScanQueueDepth(7)
	requireScanSamples(t, scrapeScanMetrics(replica), "nchat_file_malware_scan_queue_depth 7", "nchat_file_malware_scan_claims_total 0")
}
func TestScanDiagnosticsDisabledAndNil(t *testing.T) {
	t.Setenv("PROMETHEUS_METRICS_ENABLED", "false")
	m := observability.NewMetrics(observability.LoadConfig("file-service"))
	for _, a := range []*httpapi.AttachmentMetrics{nil, httpapi.NewAttachmentMetrics(m)} {
		a.SetScanRequired(true)
		a.ObserveScanQueueSnapshot(service.ScanQueueSnapshot{}, nil)
		a.ObserveScanPhase("persistence", time.Second)
		a.ObserveScanCommitted("clean", time.Second)
		a.ObserveScanFailure("database")
		a.ObserveScanPublication("success")
	}
	if strings.Contains(scrapeScanMetrics(m), "nchat_file_malware_") {
		t.Fatal("disabled metrics exposed")
	}
}
