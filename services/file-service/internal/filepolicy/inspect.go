package filepolicy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"path"
	"slices"
	"strings"
)

// Inspection budgets fit inside the existing 20 MiB preview input budget and
// 512 MiB process limit. Text parsers retain at most 2 MiB per upload; ZIP
// expansion is streamed, never retained or extracted. See supported-file-types.
const (
	MaxTextBytes      int64  = 2 << 20
	MaxContainerBytes int64  = 20 << 20
	MaxExpandedBytes  uint64 = 20 << 20
	MaxEntries               = 256
	MaxExpansionRatio uint64 = 100
	MaxNameBytes             = 255
	MaxXMLDepth              = 64
	MaxXMLAttributes         = 64
)

var (
	errStructure = errors.New("invalid structure")
	errActive    = errors.New("active content")
	errBudget    = errors.New("inspection budget exceeded")
	errOpaque    = errors.New("opaque content")
)

// Inspect does not modify source. Callers must supply the authenticated exact
// plaintext size, and must not share its seek cursor with another operation.
func Inspect(ctx context.Context, source io.ReadSeeker, size int64, filename, declared string, profiles Profiles) (Result, error) {
	f, known := byName(filename)
	if !known {
		return unknown(filename, f, declared), nil
	}
	if f.Decision != Allow {
		return outcome(f, f.Decision, Opaque, declared), nil
	}
	if !enabled(f, profiles) {
		return outcome(f, Restricted, Disabled, declared), nil
	}
	if size <= 0 {
		return outcome(f, Reject, Invalid, declared), nil
	}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return Result{}, err
	}
	head := make([]byte, min(size, 512))
	if _, err := io.ReadFull(source, head); err != nil {
		return Result{}, err
	}
	if dangerousHead(head) {
		return outcome(f, Reject, Active, declared), nil
	}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return Result{}, err
	}
	err := inspectFormat(ctx, source, size, head, f, profiles)
	return inspectionResult(f, declared, err)
}

func unknown(filename string, f Format, declared string) Result {
	ext := strings.ToLower(path.Ext(filename))
	if slices.Contains(blocked, ext) {
		return outcome(f, Reject, Active, declared)
	}
	if family, ok := restricted[ext]; ok {
		f.Family = family
		return outcome(f, Restricted, Opaque, declared)
	}
	return outcome(f, Indeterminate, Unsupported, declared)
}

func inspectionResult(f Format, declared string, err error) (Result, error) {
	switch {
	case err == nil:
		result := outcome(f, Allow, Recognized, declared)
		if result.MIMEMismatch {
			result.Decision = Reject
			result.Code = Mismatch
		}
		return result, nil
	case errors.Is(err, errActive):
		return outcome(f, Reject, Active, declared), nil
	case errors.Is(err, errBudget):
		return outcome(f, Indeterminate, BudgetExceeded, declared), nil
	case errors.Is(err, errOpaque):
		return outcome(f, Restricted, Opaque, declared), nil
	case errors.Is(err, errStructure):
		return outcome(f, Reject, Invalid, declared), nil
	default:
		return Result{}, err
	}
}

func inspectFormat(ctx context.Context, r io.ReadSeeker, size int64, head []byte, f Format, p Profiles) error {
	switch f.Strategy {
	case RFC822:
		return inspectEmail(ctx, r, size)
	case ZIP, OOXML, ODF:
		return inspectZIP(ctx, r, size, f, p)
	case Text, JSON, XML, Delimited:
		if !textSignature(head, f.Strategy) {
			return errStructure
		}
		return inspectText(ctx, r, size, f)
	case Signature:
		return inspectSignature(ctx, r, size, head, f)
	default:
		return errOpaque
	}
}

func textSignature(head []byte, strategy Strategy) bool {
	m, _, _ := strings.Cut(http.DetectContentType(head), ";")
	return m == "text/plain" || strategy == XML && m == "text/xml"
}

func dangerousHead(head []byte) bool {
	for _, prefix := range [][]byte{[]byte("MZ"), {0x7f, 'E', 'L', 'F'}, []byte("#!"), {0xca, 0xfe, 0xba, 0xbe}, {0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}} {
		if bytes.HasPrefix(head, prefix) {
			return true
		}
	}
	return false
}

func inspectSignature(ctx context.Context, r io.ReadSeeker, size int64, head []byte, f Format) error {
	if f.ID == IDMOV {
		return inspectMOV(ctx, head)
	}
	if f.ID == IDAVI {
		return inspectAVI(ctx, head)
	}
	detected := http.DetectContentType(head)
	detected, _, _ = strings.Cut(detected, ";")
	if detected != f.MIME && !slices.Contains(f.Aliases, detected) {
		return errStructure
	}
	if f.ID == IDPDF {
		return inspectPDF(ctx, r, size)
	}
	return ctx.Err()
}

func inspectMOV(ctx context.Context, head []byte) error {
	if len(head) < 12 || string(head[4:8]) != "ftyp" || string(head[8:12]) != "qt  " {
		return errStructure
	}
	return ctx.Err()
}

func inspectAVI(ctx context.Context, head []byte) error {
	if len(head) < 12 || string(head[:4]) != "RIFF" || string(head[8:12]) != "AVI " {
		return errStructure
	}
	return ctx.Err()
}

// PDF remains a download/isolated-rendering format. This checks framing and
// rejects active/encrypted features conservatively; it is not PDF validation or
// a claim that arbitrary polyglots can be identified by lexical inspection.
func inspectPDF(ctx context.Context, r io.ReadSeeker, size int64) error {
	if size < 12 {
		return errStructure
	}
	if _, err := r.Seek(max(0, size-1024), io.SeekStart); err != nil {
		return err
	}
	tail, err := io.ReadAll(io.LimitReader(r, 1024))
	if err != nil {
		return err
	}
	if !bytes.HasSuffix(bytes.TrimSpace(tail), []byte("%%EOF")) {
		return errStructure
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return rejectTokens(ctx, r, [][]byte{[]byte("/Encrypt"), []byte("/JavaScript"), []byte("/JS"), []byte("/Launch"), []byte("/EmbeddedFile"), []byte("/OpenAction"), []byte("/AA"), []byte("/RichMedia"), []byte("/XFA"), []byte("#")})
}

type contextReader struct {
	ctx    context.Context
	source io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.source.Read(p)
}

func rejectTokens(ctx context.Context, source io.Reader, tokens [][]byte) error {
	r := contextReader{ctx, source}
	buffer := make([]byte, 64<<10)
	var tail []byte
	for {
		n, err := r.Read(buffer)
		tail = append(tail, buffer[:n]...)
		window := tail
		if containsToken(window, tokens) {
			return errActive
		}
		tail = append(tail[:0], window[max(0, len(window)-32):]...)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func containsToken(b []byte, tokens [][]byte) bool {
	for _, token := range tokens {
		if bytes.Contains(b, token) {
			return true
		}
	}
	return false
}
