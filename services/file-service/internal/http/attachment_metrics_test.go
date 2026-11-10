package httpapi_test

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/observability"
	"github.com/nicrepository/nchat/services/file-service/internal/filepolicy"
	httpapi "github.com/nicrepository/nchat/services/file-service/internal/http"
)

func TestFormatMetricsExposeOnlyPolicyLabels(t *testing.T) {
	t.Setenv("PROMETHEUS_METRICS_ENABLED", "true")
	metrics := observability.NewMetrics(observability.LoadConfig("file-service"))
	attachments := httpapi.NewAttachmentMetrics(metrics)
	attachments.ObserveFileInspection(filepolicy.Result{Format: filepolicy.Format{ID: "private-name.json", Family: filepolicy.Data}, Decision: filepolicy.Reject, Code: filepolicy.Mismatch, MIMEMismatch: true}, time.Millisecond)
	attachments.ObserveFileInspection(filepolicy.Result{Decision: filepolicy.Indeterminate, Code: filepolicy.Unsupported}, time.Millisecond)
	var absent *httpapi.AttachmentMetrics
	absent.ObserveFileInspection(filepolicy.Result{}, 0)
	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	body := recorder.Body.String()
	if !strings.Contains(body, `nchat_file_format_inspections_total{code="format_mismatch",decision="reject",family="data",mime_mismatch="true"} 1`) {
		t.Fatalf("missing bounded policy metric: %s", body)
	}
	if strings.Contains(body, "private-name") {
		t.Fatal("user-controlled format ID leaked into metrics")
	}
}

// ObserveDocumentPreview and ObserveLinkPreview are thin, always-safe
// forwarders to the underlying Prometheus vectors — these just confirm they
// never panic, including on a nil receiver, which every other Observe* method
// on this type already guards against the same way.
func TestObserveDocumentPreviewAndObserveLinkPreviewDoNotPanic(t *testing.T) {
	metrics := observability.NewMetrics(observability.LoadConfig("file-service"))
	attachments := httpapi.NewAttachmentMetrics(metrics)

	attachments.ObserveDocumentPreview("pdf", "ready", "", 25*time.Millisecond)
	attachments.ObserveLinkPreview("ok")

	var nilMetrics *httpapi.AttachmentMetrics
	nilMetrics.ObserveDocumentPreview("pdf", "failed", "timeout", time.Second)
	nilMetrics.ObserveLinkPreview("blocked")
}

func TestScanQueueMetricsTrackSlotsAndDelays(t *testing.T) {
	t.Setenv("PROMETHEUS_METRICS_ENABLED", "true")
	metrics := observability.NewMetrics(observability.LoadConfig("file-service"))
	a := httpapi.NewAttachmentMetrics(metrics)
	a.SetScanCapacity(2)
	a.ScanStarted(time.Second)
	a.ScanFinished(2 * time.Second)
	a.ObserveScanBackoff(10*time.Second, "signature_policy")
	a.SetScanQueueState(3, 1)
	var absent *httpapi.AttachmentMetrics
	absent.SetScanCapacity(0)
	absent.ScanStarted(0)
	absent.ScanFinished(0)
	absent.ObserveScanBackoff(0, "database")
	absent.SetScanQueueState(0, 0)
	r := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(r, httptest.NewRequest("GET", "/metrics", nil))
	body := r.Body.String()
	for _, metric := range []string{"nchat_file_malware_scan_active 0", "nchat_file_malware_scan_capacity 2", "nchat_file_malware_scan_claims_total 1", "nchat_file_malware_scan_eligible 3", "nchat_file_malware_scan_leased 1", `nchat_file_malware_scan_backoff_seconds_count{reason="signature_policy"} 1`, "nchat_file_malware_scan_duration_seconds_count 1"} {
		if !strings.Contains(body, metric) {
			t.Fatalf("metric absent: %s", metric)
		}
	}
}
