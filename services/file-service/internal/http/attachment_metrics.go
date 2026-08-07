package httpapi

import (
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
	uploads   *prometheus.CounterVec
	downloads *prometheus.CounterVec
	previews  *prometheus.CounterVec
	cleanups  *prometheus.CounterVec
	orphans   prometheus.Counter
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
		// Object cleanup outcomes (SR-002). A separate series from previews on
		// purpose: this counts storage being reclaimed, not previews being
		// produced, and its "retry" means a delete storage refused rather than a
		// render that will be attempted again. Sharing one counter made a storage
		// outage read as previews failing to render.
		cleanups: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "nchat_file_object_cleanups_total",
			Help: "Stored object cleanup jobs by outcome.",
		}, []string{"result"}),
		orphans: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "nchat_file_orphaned_objects_total",
			Help: "Failed uploads whose stored object could not be cleaned up.",
		}),
	}
	// Register reports false when metrics are disabled; the counters still work
	// as no-op accumulators, so callers never need a nil check.
	metrics.Register(m.uploads, m.downloads, m.previews, m.cleanups, m.orphans)
	return m
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

// ObserveOrphanedObject satisfies service.OrphanObserver.
func (m *AttachmentMetrics) ObserveOrphanedObject() {
	if m == nil {
		return
	}
	m.orphans.Inc()
}
