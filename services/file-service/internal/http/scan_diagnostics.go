package httpapi

import (
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/observability"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
	"github.com/prometheus/client_golang/prometheus"
)

type scanDiagnostics struct {
	required, snapshotOK, snapshotTime, policyEnabled prometheus.Gauge
	oldest, overdue, unknown, backoff                 prometheus.Gauge
	snapshotErrors                                    prometheus.Counter
	phases, committed                                 *prometheus.HistogramVec
	failures, publications                            *prometheus.CounterVec
}

func newScanDiagnostics(metrics *observability.Metrics) scanDiagnostics {
	gauge := func(name, help string) prometheus.Gauge {
		return prometheus.NewGauge(prometheus.GaugeOpts{Name: "nchat_file_malware_scan_" + name, Help: help})
	}
	buckets := []float64{.01, .05, .1, .25, .5, 1, 2, 5, 10, 30, 60, 300, 600, 3600, 86400}
	d := scanDiagnostics{
		required:       gauge("required", "Uploads enabled and malware approval required by this process."),
		snapshotOK:     gauge("queue_snapshot_success", "Last bounded queue snapshot succeeded; zero includes never sampled."),
		snapshotTime:   gauge("queue_snapshot_timestamp_seconds", "PostgreSQL timestamp of last successful global queue snapshot."),
		policyEnabled:  gauge("queue_policy_enabled", "Queue policy enabled in last successful PostgreSQL snapshot."),
		oldest:         gauge("oldest_pending_age_seconds", "Age since known upload finalization of oldest pending attachment; excludes unknown timestamps."),
		overdue:        gauge("pending_over_5_seconds", "Pending attachments over the proposed five-second objective; not a validated SLO."),
		unknown:        gauge("pending_unknown_finalization", "Pending attachments with missing or future upload finalization timestamp."),
		backoff:        gauge("backoff", "Pending retries scheduled in the future without a live lease."),
		snapshotErrors: prometheus.NewCounter(prometheus.CounterOpts{Name: "nchat_file_malware_scan_queue_snapshot_errors_total", Help: "Failed or row-budget-exceeded queue snapshots; old gauges retained."}),
		phases:         prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "nchat_file_malware_scan_phase_duration_seconds", Help: "Attempted operation duration, including failures; scanner_stream overlaps storage/decryption, not daemon CPU time.", Buckets: buckets}, []string{"phase"}),
		committed:      prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "nchat_file_malware_scan_finalized_to_verdict_seconds", Help: "Known finalization to acknowledged persistence of a new terminal verdict; excludes publication and unconfirmed commits.", Buckets: buckets}, []string{"result"}),
		failures:       prometheus.NewCounterVec(prometheus.CounterOpts{Name: "nchat_file_malware_scan_failures_total", Help: "Primary attempt failures by closed operational category."}, []string{"reason"}),
		publications:   prometheus.NewCounterVec(prometheus.CounterOpts{Name: "nchat_file_malware_scan_publications_total", Help: "Post-commit best-effort publication calls; success is not client receipt."}, []string{"result"}),
	}
	metrics.Register(d.required, d.snapshotOK, d.snapshotTime, d.policyEnabled, d.oldest, d.overdue, d.unknown, d.backoff, d.snapshotErrors, d.phases, d.committed, d.failures, d.publications)
	return d
}

func (m *AttachmentMetrics) SetScanRequired(required bool) {
	if m == nil {
		return
	}
	m.diagnostics.required.Set(boolFloat(required))
}
func boolFloat(v bool) float64 {
	if v {
		return 1
	}
	return 0
}

func (m *AttachmentMetrics) ObserveScanQueueSnapshot(s service.ScanQueueSnapshot, err error) {
	if m == nil {
		return
	}
	d := &m.diagnostics
	if err != nil {
		d.snapshotOK.Set(0)
		d.snapshotErrors.Inc()
		return
	}
	d.oldest.Set(s.OldestAge.Seconds())
	d.overdue.Set(float64(s.OverFiveSeconds))
	d.unknown.Set(float64(s.UnknownFinalization))
	d.backoff.Set(float64(s.Backoff))
	d.policyEnabled.Set(boolFloat(s.PolicyEnabled))
	d.snapshotTime.Set(float64(s.ObservedAt.UnixNano()) / 1e9)
	d.snapshotOK.Set(1)
}

func (m *AttachmentMetrics) ObserveScanPhase(phase string, elapsed time.Duration) {
	if m == nil || elapsed < 0 {
		return
	}
	switch phase {
	case "storage_open", "format_validation", "scanner_stream", "persistence":
		m.diagnostics.phases.WithLabelValues(phase).Observe(elapsed.Seconds())
	}
}
func (m *AttachmentMetrics) ObserveScanCommitted(result string, elapsed time.Duration) {
	if m == nil || elapsed < 0 {
		return
	}
	switch result {
	case "clean", "rejected":
		m.diagnostics.committed.WithLabelValues(result).Observe(elapsed.Seconds())
	}
}
func (m *AttachmentMetrics) ObserveScanPublication(result string) {
	if m == nil {
		return
	}
	switch result {
	case "success", "error":
		m.diagnostics.publications.WithLabelValues(result).Inc()
	}
}
func (m *AttachmentMetrics) ObserveScanFailure(reason string) {
	if m == nil {
		return
	}
	switch reason {
	case "storage_read", "decrypt", "clamd_unavailable", "clamd_protocol", "clamd_stream_too_large",
		"timeout", "canceled", "database", "database_conflict", "unsupported_policy",
		"signature_policy_inactive", "signature_policy_invalid":
		m.diagnostics.failures.WithLabelValues(reason).Inc()
	}
}
