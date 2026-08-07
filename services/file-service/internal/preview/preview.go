// Package preview turns an attachment's plaintext into the small raster the
// client shows inline (RF-31).
//
// Everything in this package is a pure transformation: bytes in, JPEG out. It
// has no database, no storage, no authorization and no knowledge of who is
// asking — those belong to the caller, which has already decided that this
// content may be rendered. That separation is what keeps the risky part of the
// feature, the decoders, small and testable.
//
// # Threat posture
//
// Both decoders are fed attacker-controlled bytes by definition, so the limits
// below are the feature, not decoration:
//
//   - the source is read through a bounded reader, so no attachment larger than
//     domain.MaxPreviewSourceBytes is ever held in memory;
//   - an image's declared dimensions are checked from its header, before a
//     single pixel is allocated, so a decompression bomb is refused rather than
//     decoded;
//   - a PDF is parsed by PDFium compiled to WebAssembly and run under wazero,
//     in a memory-isolated sandbox with an explicit page limit and a context
//     that closes the module when it expires. Nothing is executed by the host;
//   - there is no subprocess, no shell, no temporary file and no path built
//     from anything the client supplied. A hostile filename cannot reach this
//     package at all: only bytes and a detected type do.
//
// Failures are classified for the caller: ErrUnsupported is an expected absence
// (no renderer, or beyond the limits), ErrRender is an operational failure of a
// file that claimed a supported type. Anything else is transient and worth a
// retry.
package preview

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/nicrepository/nchat/services/file-service/internal/domain"
)

// Error classes. They are the whole contract with the job: one means "this will
// never work, and that is fine", the other means "this should have worked".
var (
	// ErrUnsupported marks content this service will not render: a type with no
	// decoder, or a file outside the render limits. It is permanent and
	// expected, and the client shows an icon.
	ErrUnsupported = errors.New("preview unsupported")
	// ErrRender marks content that claimed a supported type and could not be
	// rendered: malformed, truncated, encrypted, or empty of pages. It is
	// permanent and operational.
	ErrRender = errors.New("preview render failed")
)

// Renderer produces previews. It holds no state: a PDF render builds and tears
// down its own sandbox, so a renderer that is never asked for a PDF costs
// nothing and a process that has finished one holds nothing.
type Renderer struct{}

// New returns the renderer used by the preview job.
func New() *Renderer { return &Renderer{} }

// Render reads src and returns a JPEG no larger than domain.MaxPreviewDimension
// on its longest edge.
//
// detectedMIME is the type detected from the content at upload — never a
// declared type and never an extension. It selects the decoder; the decoder
// then validates the bytes itself, so a mismatch fails rather than being
// coerced.
func (r *Renderer) Render(ctx context.Context, detectedMIME string, src io.Reader) ([]byte, error) {
	if !domain.PreviewSupported(detectedMIME) {
		return nil, fmt.Errorf("%w: no renderer for this content type", ErrUnsupported)
	}
	data, err := readBounded(src, domain.MaxPreviewSourceBytes)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if domain.NormalizeDetectedMIME(detectedMIME) == "application/pdf" {
		return renderPDFFirstPage(ctx, data)
	}
	return renderImage(data)
}

// readBounded reads at most limit bytes and refuses anything longer.
//
// The check is "one byte past the limit", so an oversized source is detected
// without ever being buffered. An empty source is refused here rather than in a
// decoder, because zero bytes are not a malformed image — they are nothing.
func readBounded(src io.Reader, limit int64) ([]byte, error) {
	var buf bytes.Buffer
	n, err := io.Copy(&buf, io.LimitReader(src, limit+1))
	if err != nil {
		// A failed read of an already stored object is transient: storage or
		// the network, not the content.
		return nil, fmt.Errorf("read preview source: %w", err)
	}
	if n > limit {
		return nil, fmt.Errorf("%w: source exceeds the preview size limit", ErrUnsupported)
	}
	if n == 0 {
		return nil, fmt.Errorf("%w: empty source", ErrRender)
	}
	return buf.Bytes(), nil
}
