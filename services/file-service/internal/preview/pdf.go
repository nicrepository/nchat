package preview

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/klippa-app/go-pdfium"
	pdfiumerrors "github.com/klippa-app/go-pdfium/errors"
	"github.com/klippa-app/go-pdfium/references"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/webassembly"
	"github.com/tetratelabs/wazero"

	"github.com/nicrepository/nchat/services/file-service/internal/domain"
)

// Why PDFium compiled to WebAssembly, and not a system binary.
//
// A PDF is a program-shaped document format and its parsers have a long history
// of memory-safety bugs, so the question is not "which renderer" but "what
// contains it". The three options were:
//
//   - a system binary (pdftoppm, ImageMagick): needs a package in the image,
//     which this repository does not have — every service is built with
//     CGO_ENABLED=0 into gcr.io/distroless/static, which contains no shell and
//     no libraries. It would also mean exec'ing a process with a file path,
//     which is the injection surface this codebase deliberately does not have;
//   - PDFium through cgo: same problem from the other side. CGO_ENABLED=0 is a
//     property of every image here, and a segfault in a linked C++ library
//     takes the whole service down;
//   - PDFium compiled to WebAssembly, executed by wazero: pure Go, no cgo, no
//     package, no subprocess, no file path. The parser runs inside a sandbox
//     with its own linear memory, which cannot address the host's; a memory
//     limit it cannot exceed; and a context that closes the module when the
//     deadline passes. A crash inside it is an error value, not a signal.
//
// The sandbox is the security argument and it is the reason for the dependency:
// no amount of care in Go code would contain a native PDF parser as well.
//
// The cost is paid per render, not per process. wazero is asked for the
// interpreter rather than the optimising compiler: compiling the 5 MiB PDFium
// module costs about ten times the memory and, measured on this code, three
// seconds against a third of a second. For a background job that renders one
// page, interpretation is both cheaper and faster overall, and it keeps the
// steady-state footprint of file-service exactly where it was.
const (
	// pdfMemoryLimitPages caps the sandbox's linear memory, in 64 KiB
	// WebAssembly pages. 2048 pages is 128 MiB: comfortably more than a first
	// page render needs, and a hard ceiling a hostile document cannot talk its
	// way past. Exceeding it fails the render; it never grows the process.
	pdfMemoryLimitPages = 2048

	// pdfFirstPage is PDFium's zero-based index of the first page, used both
	// as the loop's starting index and as the "does this document have a
	// page at all" bound.
	pdfFirstPage = 0
)

// sandboxConfig is the only configuration the PDF sandbox is ever built with.
//
// Every field here is set explicitly, and that is the point rather than
// tidiness: go-pdfium fills in defaults for the ones left nil, and two of those
// defaults are exactly what a sandbox must not have.
//
//   - FSConfig nil makes it mount the host's root directory into the module,
//     read-write (webassembly.initWithConfig: WithDirMount("/", "/")). A PDF
//     parser has no business reading a single file, let alone all of them, so
//     an empty FSConfig is passed instead: the module gets no preopened
//     directory at all, and a WASI path call has nothing to resolve against.
//   - Stdout and Stderr nil make it write the module's output straight to the
//     process's streams, where a document that provokes a chatty parser becomes
//     unbounded log volume. They are discarded: nothing the module prints is
//     diagnostic to this service, and the outcome is already logged by the job.
//
// RandomSource is left to go-pdfium's default, which is crypto/rand — the
// module needs entropy and that is the right source for it. No environment
// variable, no argument and no socket is exposed: wazero grants none of them
// unless asked, and nothing here asks.
func sandboxConfig(ctx context.Context) webassembly.Config {
	return webassembly.Config{
		Context:  ctx,
		MinIdle:  0,
		MaxIdle:  1,
		MaxTotal: 1,
		// No mounts. Not "a narrow mount" — none.
		FSConfig: wazero.NewFSConfig(),
		Stdout:   io.Discard,
		Stderr:   io.Discard,
		RuntimeConfig: wazero.NewRuntimeConfigInterpreter().
			WithMemoryLimitPages(pdfMemoryLimitPages).
			WithCloseOnContextDone(true),
	}
}

// renderPDFPages rasterises up to domain.MaxPreviewPDFPages pages, starting
// from the first, and returns each as its own JPEG.
//
// The whole sandbox — runtime, module, instance, document — is built and torn
// down around this one call, exactly as it was when this rendered only page
// one. That is still deliberate: a long-lived PDFium pool would hold its
// module for the life of the process whether or not another PDF ever arrives,
// and tearing down is also what makes the timeout real. ctx is the module's
// context, so an expired deadline closes the module out from under a render
// that will not finish, instead of leaving a goroutine spinning inside it.
//
// # Why a cap and not the document's real page count
//
// A preview is bounded work, not a copy of the document: rendering is the
// dominant per-page cost once the sandbox and the document are already open,
// so a thousand-page PDF would otherwise turn one worker slot into a
// thousand-page job. domain.MaxPreviewPDFPages is that ceiling, applied to
// whichever is smaller — the document's own page count or the cap.
//
// # Why a partial result is success, not failure
//
// The loop checks ctx.Err() before every page, not just at the start. If the
// job's deadline (previewJobTimeout, enforced by the caller) arrives after
// page 6 of a 20-page cap, this returns those 6 pages and no error — the
// caller publishes a preview with 6 pages rather than none. Only zero
// rendered pages is a failure: a preview cut short by its own time budget is
// still a preview, and the alternative — discarding partial work because the
// clock ran out — would make a slow-but-legitimate document indistinguishable
// from one this service could never open at all.
func renderPDFPages(ctx context.Context, data []byte) ([][]byte, error) {
	pool, err := webassembly.Init(sandboxConfig(ctx))
	if err != nil {
		// Building the sandbox failed, which says nothing about the document:
		// transient, and worth another attempt.
		return nil, fmt.Errorf("start pdf sandbox: %w", err)
	}
	defer func() { _ = pool.Close() }()

	instance, err := pool.GetInstance(pdfInstanceTimeout(ctx))
	if err != nil {
		return nil, fmt.Errorf("acquire pdf sandbox: %w", err)
	}
	defer func() { _ = instance.Close() }()

	document, err := instance.OpenDocument(&requests.OpenDocument{File: &data})
	if err != nil {
		return nil, openDocumentError(err)
	}
	defer func() {
		_, _ = instance.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: document.Document})
	}()

	pageCount, err := renderablePageCount(instance, document.Document)
	if err != nil {
		return nil, err
	}
	renderCount := pageCount
	if renderCount > domain.MaxPreviewPDFPages {
		renderCount = domain.MaxPreviewPDFPages
	}

	pages := make([][]byte, 0, renderCount)
	for index := pdfFirstPage; index < renderCount; index++ {
		if ctx.Err() != nil {
			// The deadline is spent. Whatever rendered so far is kept; nothing
			// past this point is attempted.
			break
		}
		page, err := renderPDFPage(ctx, instance, document.Document, index)
		if err != nil {
			if len(pages) > 0 {
				// A later page failing to rasterise does not invalidate the
				// ones that already did — the job still publishes a shorter,
				// genuine preview instead of discarding good work over one
				// bad page.
				break
			}
			return nil, err
		}
		pages = append(pages, page)
	}
	if len(pages) == 0 {
		return nil, fmt.Errorf("%w: no page could be rasterised", ErrRender)
	}
	return pages, nil
}

// renderPDFPage rasterises one page and returns it as a JPEG.
//
// PDFium fits the page inside the box and keeps its aspect ratio, so the
// dimensions below bound the output on both axes. RenderForm is left off:
// form fields, and anything else interactive, are not part of a preview.
func renderPDFPage(
	ctx context.Context, instance pdfium.Pdfium, document references.FPDF_DOCUMENT, index int,
) ([]byte, error) {
	rendered, err := instance.RenderPageInPixels(&requests.RenderPageInPixels{
		Page: requests.Page{ByIndex: &requests.PageByIndex{
			Document: document,
			Index:    index,
		}},
		Width:  domain.MaxPreviewDimension,
		Height: domain.MaxPreviewDimension,
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			// The deadline closed the module mid-render. That is the timeout
			// working, and it is transient rather than a broken document.
			return nil, fmt.Errorf("render pdf page: %w", ctxErr)
		}
		return nil, fmt.Errorf("%w: page could not be rasterised", ErrRender)
	}
	if rendered == nil || rendered.Result.Image == nil {
		return nil, fmt.Errorf("%w: renderer produced no image", ErrRender)
	}
	// Already inside the box, so this only flattens the alpha PDFium leaves on
	// the bitmap onto white; it does not rescale.
	return encodeJPEG(thumbnail(rendered.Result.Image, domain.MaxPreviewDimension))
}

// pdfInstanceTimeout bounds the wait for a sandbox worker by whatever is left
// of the job's deadline, so this call can never outlive the job that made it.
func pdfInstanceTimeout(ctx context.Context) time.Duration {
	const fallback = 10 * time.Second
	deadline, ok := ctx.Deadline()
	if !ok {
		return fallback
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		// Already expired: ask for zero and let the acquisition fail now rather
		// than starting work that has no time to finish.
		return 0
	}
	if remaining > fallback {
		return fallback
	}
	return remaining
}

// renderablePageCount refuses a document with no first page before any raster
// is attempted, and otherwise reports how many pages it has. A structurally
// valid PDF with zero pages is a real thing to receive, and it is an expected
// absence rather than a failure.
func renderablePageCount(instance pdfium.Pdfium, document references.FPDF_DOCUMENT) (int, error) {
	count, err := instance.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: document})
	if err != nil {
		return 0, fmt.Errorf("%w: page count is unreadable", ErrRender)
	}
	if count == nil || count.PageCount <= pdfFirstPage {
		return 0, fmt.Errorf("%w: document has no renderable page", ErrUnsupported)
	}
	return count.PageCount, nil
}

// openDocumentError classifies a document PDFium refused to open.
//
// A password-protected or otherwise encrypted PDF is a legitimate file this
// service simply cannot look inside: unsupported, so the client draws an icon
// and nobody is paged. Malformed or unreadable bytes claimed to be a PDF and
// were not: a render failure. Neither message repeats anything PDFium said.
func openDocumentError(err error) error {
	switch {
	case errors.Is(err, pdfiumerrors.ErrPassword), errors.Is(err, pdfiumerrors.ErrSecurity):
		return fmt.Errorf("%w: document is protected", ErrUnsupported)
	case errors.Is(err, pdfiumerrors.ErrFormat), errors.Is(err, pdfiumerrors.ErrFile):
		return fmt.Errorf("%w: document is not a readable pdf", ErrRender)
	default:
		return fmt.Errorf("%w: document could not be opened", ErrRender)
	}
}
