package httpapi

import (
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/observability"
	"github.com/prometheus/client_golang/prometheus"
)

// AttachmentMetrics counts attachment outcomes.
//
// Every label value comes from a closed set of result strings decided in this
// package. Attachment ids, workspace ids, filenames, storage keys and paths are
// never labels: they are unbounded, and two of them are the identifiers this
// service exists to keep private.
type AttachmentMetrics struct {
	uploads         *prometheus.CounterVec
	downloads       *prometheus.CounterVec
	previews        *prometheus.CounterVec
	previewAttempts *prometheus.CounterVec
	previewDuration *prometheus.HistogramVec
	linkPreviews    *prometheus.CounterVec
	cleanups        *prometheus.CounterVec
	scans           *prometheus.CounterVec
	scanQueue       prometheus.Gauge
	orphans         prometheus.Counter
}

// NewAttachmentMetrics registers the attachment counters on the shared
// registry. The app builds it so the service and the handlers share one set.
func NewAttachmentMetrics(metrics *observability.Metrics) *AttachmentMetrics {
	m := &AttachmentMetrics{
		uploads: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "nchat_file_uploads_total",
			Help: "Attachment uploads by outcome.",
		}, []string{"result"}),
		downloads: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "nchat_file_downloads_total",
			Help: "Attachment downloads by outcome.",
		}, []string{"result"}),
		// Preview outcomes (RF-31), from both halves of the feature and with two
		// disjoint sets of label values:
		//
		//   - generation, from the worker: ready, unsupported, failed, retry,
		//     superseded. A rising "failed" is a problem; a rising "unsupported"
		//     is users uploading formats nobody renders;
		//   - delivery, from GetPreview: served, stream_failed, and the sanitised
		//     error codes attachmentErrorStatus produces.
		//
		// They share a counter because they are the same feature and the values
		// cannot collide, so either question is still answerable by selecting on
		// the label.
		previews: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "nchat_file_previews_total",
			Help: "Attachment preview generation and delivery by outcome.",
		}, []string{"result"}),
		previewAttempts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "nchat_file_preview_attempts_total",
			Help: "Document preview attempts by bounded format and outcome.",
		}, []string{"format", "result", "reason"}),
		previewDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "nchat_file_preview_duration_seconds",
			Help: "Document preview attempt duration by bounded format and outcome.",
		}, []string{"format", "result"}),
		// Open Graph link preview outcomes (RF-10): hit, success, invalid_url,
		// blocked, unsupported_content_type, timeout, upstream_error,
		// no_metadata.
		//
		// Its own series rather than a label on the attachment preview counter,
		// because the two answer different questions: that one is about
		// rendering files this service stores, this one is about reaching sites
		// it does not control. A rising "blocked" here is worth an alert — it
		// means callers are pointing the fetcher at destinations the SSRF
		// policy refuses — and it would be invisible mixed into the other.
		//
		// The URL is never a label. It is caller-supplied and unbounded, so one
		// hostile client could otherwise create a series per request and take
		// the metrics endpoint down with it.
		linkPreviews: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "nchat_file_link_previews_total",
			Help: "Open Graph link preview requests by outcome.",
		}, []string{"result"}),
		// Object cleanup outcomes (SR-002). A separate series from previews on
		// purpose: this counts storage being reclaimed, not previews being
		// produced, and its "retry" means a delete storage refused rather than a
		// render that will be attempted again. Sharing one counter made a storage
		// outage read as previews failing to render.
		cleanups: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "nchat_file_object_cleanups_total",
			Help: "Stored object cleanup jobs by outcome.",
		}, []string{"result"}),
		// Antimalware scan outcomes (RF-22): clean, infected, retry, too_large,
		// superseded. Its own series rather than a label on the preview counter,
		// because a rising "retry" here is a daemon that cannot be reached, which
		// has nothing to do with previews failing to render.
		scans: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "nchat_file_malware_scans_total",
			Help: "Attachment antimalware scans by outcome.",
		}, []string{"result"}),
		// The required RF-22 gauge: how many attachments are still awaiting a
		// verdict, cluster-wide.
		//
		// It has no labels at all, deliberately. A per-workspace or per-channel
		// breakdown would make the series count grow with the product, and the
		// question it answers — is the scan queue draining — does not need one.
		// It is a backlog, so it is a gauge and not a counter: it goes down.
		scanQueue: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "nchat_file_malware_scan_queue_depth",
			Help: "Attachments awaiting an antimalware verdict.",
		}),
		orphans: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "nchat_file_orphaned_objects_total",
			Help: "Failed uploads whose stored object could not be cleaned up.",
		}),
	}
	// Register reports false when metrics are disabled; the counters still work
	// as no-op accumulators, so callers never need a nil check.
	metrics.Register(m.uploads, m.downloads, m.previews, m.previewAttempts, m.previewDuration, m.linkPreviews, m.cleanups,
		m.scans, m.scanQueue, m.orphans)
	return m
}

func (m *AttachmentMetrics) ObserveDocumentPreview(format, result, reason string, duration time.Duration) {
	if m == nil {
		return
	}
	m.previewAttempts.WithLabelValues(format, result, reason).Inc()
	m.previewDuration.WithLabelValues(format, result).Observe(duration.Seconds())
}

// ObserveLinkPreview satisfies linkpreview.Observer.
func (m *AttachmentMetrics) ObserveLinkPreview(result string) {
	if m == nil {
		return
	}
	m.linkPreviews.WithLabelValues(result).Inc()
}

func (m *AttachmentMetrics) observeUpload(result string) {
	if m == nil {
		return
	}
	m.uploads.WithLabelValues(result).Inc()
}

func (m *AttachmentMetrics) observeDownload(result string) {
	if m == nil {
		return
	}
	m.downloads.WithLabelValues(result).Inc()
}

// ObservePreview satisfies service.PreviewObserver.
func (m *AttachmentMetrics) ObservePreview(result string) {
	if m == nil {
		return
	}
	m.previews.WithLabelValues(result).Inc()
}

// ObserveCleanup satisfies service.ObjectCleanupObserver.
func (m *AttachmentMetrics) ObserveCleanup(result string) {
	if m == nil {
		return
	}
	m.cleanups.WithLabelValues(result).Inc()
}

// ObserveScan satisfies service.ScanObserver.
func (m *AttachmentMetrics) ObserveScan(result string) {
	if m == nil {
		return
	}
	m.scans.WithLabelValues(result).Inc()
}

// SetScanQueueDepth satisfies service.ScanObserver.
//
// It is a Set rather than an Add because the worker reports the backlog it just
// counted, not a delta: two replicas both publishing the same authoritative
// number converge on it, whereas two of them adding would double it.
func (m *AttachmentMetrics) SetScanQueueDepth(depth int64) {
	if m == nil {
		return
	}
	m.scanQueue.Set(float64(depth))
}

// ObserveOrphanedObject satisfies service.OrphanObserver.
func (m *AttachmentMetrics) ObserveOrphanedObject() {
	if m == nil {
		return
	}
	m.orphans.Inc()
}
