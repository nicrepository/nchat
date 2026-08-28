package httpapi_test

import (
	"testing"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/observability"
	httpapi "github.com/nicrepository/nchat/services/file-service/internal/http"
)

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
