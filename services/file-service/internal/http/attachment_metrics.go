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
		orphans: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "nchat_file_orphaned_objects_total",
			Help: "Failed uploads whose stored object could not be cleaned up.",
		}),
	}
	// Register reports false when metrics are disabled; the counters still work
	// as no-op accumulators, so callers never need a nil check.
	metrics.Register(m.uploads, m.downloads, m.orphans)
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

// ObserveOrphanedObject satisfies service.OrphanObserver.
func (m *AttachmentMetrics) ObserveOrphanedObject() {
	if m == nil {
		return
	}
	m.orphans.Inc()
}
