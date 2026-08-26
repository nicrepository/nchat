package domain_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
)

func TestPreviewStatusSetIsClosed(t *testing.T) {
	for _, status := range []domain.PreviewStatus{
		domain.PreviewStatusPending, domain.PreviewStatusReady,
		domain.PreviewStatusUnsupported, domain.PreviewStatusFailed,
	} {
		if !status.Valid() {
			t.Fatalf("%q must be a valid preview status", status)
		}
	}
	for _, status := range []domain.PreviewStatus{"", "READY", "processing", "clean"} {
		if status.Valid() {
			t.Fatalf("%q must not be a valid preview status", status)
		}
	}
}

// Only one state may be served, and the check is positive: an unrecognised
// value must never be treated as a preview that exists.
func TestOnlyAReadyPreviewIsServable(t *testing.T) {
	if !domain.PreviewStatusReady.Servable() {
		t.Fatal("ready must be servable")
	}
	for _, status := range []domain.PreviewStatus{
		domain.PreviewStatusPending, domain.PreviewStatusUnsupported,
		domain.PreviewStatusFailed, "anything else",
	} {
		if status.Servable() {
			t.Fatalf("%q must not be servable", status)
		}
	}
}

func TestOnlyPendingIsNonTerminal(t *testing.T) {
	if domain.PreviewStatusPending.Terminal() {
		t.Fatal("pending is the worker's queue, not a conclusion")
	}
	for _, status := range []domain.PreviewStatus{
		domain.PreviewStatusReady, domain.PreviewStatusUnsupported, domain.PreviewStatusFailed,
	} {
		if !status.Terminal() {
			t.Fatalf("%q must be terminal", status)
		}
	}
}

// The allowlist is decided by the *detected* type, and it is an allowlist: a
// type nobody named is refused rather than tried.
func TestPreviewSupportedNamesExactlyTheRenderableTypes(t *testing.T) {
	for _, supported := range []string{
		"image/jpeg", "image/png", "image/gif", "application/pdf",
		// text/plain and application/zip are the coarse sniffs
		// net/http.DetectContentType actually produces for CSV and XLSX —
		// never the specific strings, which that sniffer cannot name. See
		// previewableMIMEs' own comment.
		"text/plain", "application/zip",
		"IMAGE/PNG", " image/jpeg ", "image/jpeg; charset=binary",
	} {
		if !domain.PreviewSupported(supported) {
			t.Fatalf("%q must be previewable", supported)
		}
	}
	for _, unsupported := range []string{
		"", "image/webp", "image/svg+xml", "text/html",
		"video/mp4", "application/octet-stream", "application/pdf-something",
	} {
		if domain.PreviewSupported(unsupported) {
			t.Fatalf("%q must not be previewable", unsupported)
		}
	}
}

// An SVG is an image to a user and a script host to a browser. It must never
// reach a decoder, and it must never be treated as renderable.
func TestPreviewIsRefusedForActiveImageFormats(t *testing.T) {
	for _, active := range []string{"image/svg+xml", "text/html", "application/xml"} {
		if domain.InitialPreviewStatus(active, 1024) != domain.PreviewStatusUnsupported {
			t.Fatalf("%q must not be queued for rendering", active)
		}
	}
}

func TestInitialPreviewStatusQueuesOnlyWhatCanBeRendered(t *testing.T) {
	tests := map[string]struct {
		detected string
		size     int64
		want     domain.PreviewStatus
	}{
		"image":            {detected: "image/png", size: 4096, want: domain.PreviewStatusPending},
		"pdf":              {detected: "application/pdf", size: 4096, want: domain.PreviewStatusPending},
		"unknown type":     {detected: "video/mp4", size: 4096, want: domain.PreviewStatusUnsupported},
		"empty":            {detected: "image/png", size: 0, want: domain.PreviewStatusUnsupported},
		"at the limit":     {detected: "image/png", size: domain.MaxPreviewSourceBytes, want: domain.PreviewStatusPending},
		"beyond the limit": {detected: "image/png", size: domain.MaxPreviewSourceBytes + 1, want: domain.PreviewStatusUnsupported},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := domain.InitialPreviewStatus(tt.detected, tt.size); got != tt.want {
				t.Fatalf("status = %q, want %q", got, tt.want)
			}
		})
	}
}

// The key is built from the preview's own random UUID and nothing else: no
// filename, no client value, no attachment id, and nothing that could traverse.
func TestPreviewObjectKeyIsDerivedOnlyFromTheGeneratedID(t *testing.T) {
	id := uuid.New()
	key := domain.PreviewObjectKey(id)

	if key != "nchat/previews/"+id.String() {
		t.Fatalf("unexpected key %q", key)
	}
	if strings.Contains(key, "..") || strings.Contains(key, "\\") {
		t.Fatalf("key must not contain traversal syntax: %q", key)
	}
	// Previews and attachments never share a prefix, so one can never be read
	// through the other's key.
	if strings.HasPrefix(key, domain.StorageObjectKey(id)) {
		t.Fatal("preview and attachment keys must not overlap")
	}
}

func TestPreviewContentTypeIsFixed(t *testing.T) {
	if domain.PreviewContentTypeJPEG != "image/jpeg" {
		t.Fatalf("preview content type = %q", domain.PreviewContentTypeJPEG)
	}
}
