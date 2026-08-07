package domain

import (
	"errors"
	"mime"
	"strings"

	"github.com/google/uuid"
)

// ErrPreviewUnavailable is returned for an attachment the caller may read but
// whose preview is not servable: still being generated, never supported, or
// failed. It is deliberately one error and not three — the client learns which
// of the three it is from the attachment's own metadata, so the content route
// never has to describe internal state to decide what the UI should draw.
var ErrPreviewUnavailable = errors.New("attachment preview not available")

// PreviewStatus is the lifecycle of an attachment's inline preview (RF-31).
// The client never supplies it, exactly like Status.
type PreviewStatus string

const (
	// PreviewStatusPending is the state a previewable attachment finishes its
	// upload in. It is the worker's queue: nothing else selects rows.
	PreviewStatusPending PreviewStatus = "pending"
	// PreviewStatusReady means a preview object exists and may be served, if
	// the attachment itself may be served.
	PreviewStatusReady PreviewStatus = "ready"
	// PreviewStatusUnsupported is an expected absence: this content has no
	// renderer, or is outside the limits this service is willing to spend on
	// one. The client draws an icon and a download button, not an error.
	PreviewStatusUnsupported PreviewStatus = "unsupported"
	// PreviewStatusFailed is an operational failure: the content claimed a
	// supported type and could not be rendered, or the pipeline gave up after
	// its retries. The attachment itself is unaffected.
	PreviewStatusFailed PreviewStatus = "failed"
)

// Valid reports whether the status is one of the closed set the CHECK allows.
func (s PreviewStatus) Valid() bool {
	switch s {
	case PreviewStatusPending, PreviewStatusReady, PreviewStatusUnsupported, PreviewStatusFailed:
		return true
	default:
		return false
	}
}

// Servable reports whether a preview object may be streamed. Readiness is
// necessary and never sufficient: the attachment's own Status.Downloadable
// decides first, so a preview can never outrun the malware scan.
func (s PreviewStatus) Servable() bool {
	return s == PreviewStatusReady
}

// Terminal reports whether the worker is done with this row. A terminal state
// is never re-queued by this build; regenerating is an explicit operation.
func (s PreviewStatus) Terminal() bool {
	return s == PreviewStatusReady || s == PreviewStatusUnsupported || s == PreviewStatusFailed
}

// PreviewContentType is the single type every preview is encoded in.
//
// One output format, decided by the server, is what makes the response safe to
// render inline: the bytes are always a re-encoded raster produced by this
// service, never the uploaded file, so an HTML, SVG or script upload cannot
// become a preview that a browser would execute.
const PreviewContentType = "image/jpeg"

// Preview generation limits. They are constants rather than configuration: they
// bound how much CPU and memory one hostile file may cost, and an operator who
// could raise them could turn a single upload into a denial of service.
const (
	// MaxPreviewSourceBytes is the largest plaintext this service will read
	// back to render a preview. Beyond it the attachment is left without one
	// rather than pulling hundreds of megabytes through a worker.
	MaxPreviewSourceBytes = 20 << 20

	// MaxPreviewSourcePixels bounds the decoded image before it is decoded.
	// It is checked against the header alone, so a 100000x100000 PNG — a few
	// hundred kilobytes compressed, 40 GB decoded — is refused without ever
	// allocating a pixel.
	MaxPreviewSourcePixels = 40_000_000

	// MaxPreviewDimension is the longest edge of the produced thumbnail. The
	// aspect ratio is preserved, so this bounds the output on both axes.
	MaxPreviewDimension = 512

	// PreviewJPEGQuality is the encoder quality. 80 is the usual thumbnail
	// trade-off: a 512px preview lands in tens of kilobytes.
	PreviewJPEGQuality = 80
)

// previewableMIMEs is the allowlist of *detected* content types that have a
// renderer. It is an allowlist on purpose: a type that is not named here is
// never handed to a decoder, so adding a format is a deliberate act.
//
// image/webp is absent: the standard library has no webp decoder and this
// change adds no dependency for one.
var previewableMIMEs = map[string]struct{}{
	"image/jpeg":      {},
	"image/png":       {},
	"image/gif":       {},
	"application/pdf": {},
}

// NormalizeDetectedMIME reduces a stored detected type to the bare type for
// comparison: lowercase, no parameters, no surrounding space. It never repairs
// a value — an unparseable one degrades to DefaultContentType, which is not on
// the allowlist.
func NormalizeDetectedMIME(detected string) string {
	parsed, _, err := mime.ParseMediaType(detected)
	if err != nil {
		// ParseMediaType rejects a bare type with a stray parameter; the type
		// itself is still usable, so it is taken and the rest discarded.
		parsed = strings.TrimSpace(strings.ToLower(strings.SplitN(detected, ";", 2)[0]))
	}
	if parsed == "" {
		return DefaultContentType
	}
	return parsed
}

// PreviewSupported reports whether a detected type has a renderer at all.
//
// The type is the one *detected from the content* at upload, never the one the
// client declared and never an extension: a .png that is really a zip is
// already recorded as application/zip and is refused here.
func PreviewSupported(detectedMIME string) bool {
	_, ok := previewableMIMEs[NormalizeDetectedMIME(detectedMIME)]
	return ok
}

// InitialPreviewStatus is the state a finished upload lands in. It is decided
// once, at finalisation, from facts the server already has, so an attachment
// that can never have a preview never enters the worker's queue.
func InitialPreviewStatus(detectedMIME string, size int64) PreviewStatus {
	if !PreviewSupported(detectedMIME) || size <= 0 || size > MaxPreviewSourceBytes {
		return PreviewStatusUnsupported
	}
	return PreviewStatusPending
}

// PreviewObjectKey derives the SeaweedFS key of a preview object.
//
// It is built from the preview's own random UUID, not from the attachment's:
// the preview is a separate object with its own data key, bound to this id, so
// neither object can be opened as, or substituted for, the other. Like the
// attachment key, it never reaches a client.
func PreviewObjectKey(previewObjectID uuid.UUID) string {
	return "nchat/previews/" + previewObjectID.String()
}
