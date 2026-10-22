package domain_test

import (
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/filepolicy"
	"testing"
)

func TestPolicyPreviewPreservesLegacyAndRejectsUnknownMetadata(t *testing.T) {
	for _, tt := range []struct {
		name, mime string
		id         filepolicy.FormatID
		version    int
		want       domain.PreviewStatus
	}{
		{"legacy text", "text/plain; charset=utf-8", "", 0, domain.PreviewStatusPending},
		{"legacy container", "application/zip", "", 0, domain.PreviewStatusPending},
		{"unknown ID", "image/png", "other", 1, domain.PreviewStatusUnsupported},
		{"future version", "image/png", filepolicy.IDPNG, 2, domain.PreviewStatusUnsupported},
		{"mismatched MIME", "application/zip", filepolicy.IDDOCX, 1, domain.PreviewStatusUnsupported},
		{"normalized canonical CSV", "text/csv; charset=utf-8", filepolicy.IDCSV, 1, domain.PreviewStatusPending},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := domain.InitialPolicyPreviewStatus(tt.mime, 100, tt.id, tt.version); got != tt.want {
				t.Fatalf("got %s want %s", got, tt.want)
			}
		})
	}
}
