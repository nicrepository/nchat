package converter

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
)

const (
	MaxInputBytes  = 20 << 20
	MaxOutputBytes = 50 << 20
)

type Format string

const (
	FormatDOCX Format = "docx"
	FormatODT  Format = "odt"
	FormatPPT  Format = "ppt"
	FormatPPTX Format = "pptx"
)

var (
	ErrBlocked          = errors.New("blocked")
	ErrInvalidDocument  = errors.New("invalid document")
	ErrTimeout          = errors.New("conversion timeout")
	ErrConversionFailed = errors.New("conversion failed")
	ErrOutputTooLarge   = errors.New("output too large")
)

type Runner interface {
	Convert(context.Context, Format, []byte) ([]byte, error)
}

type Handler struct{ runner Runner }

func NewHandler(runner Runner) http.Handler { return &Handler{runner: runner} }

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && (r.URL.Path == "/healthz" || r.URL.Path == "/readyz") {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost || r.URL.Path != "/v1/convert" {
		http.NotFound(w, r)
		return
	}
	format := Format(strings.ToLower(strings.TrimSpace(r.Header.Get("X-Document-Format"))))
	if !format.valid() {
		writeError(w, http.StatusUnprocessableEntity, "blocked")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxInputBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_document")
		return
	}
	if len(body) > MaxInputBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "output_too_large")
		return
	}
	if err := validateDocument(format, body); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "blocked")
		return
	}
	pdf, err := h.runner.Convert(r.Context(), format, body)
	if err != nil {
		status, code := http.StatusInternalServerError, "conversion_failed"
		switch {
		case errors.Is(err, ErrTimeout), errors.Is(err, context.DeadlineExceeded):
			status, code = http.StatusGatewayTimeout, "timeout"
		case errors.Is(err, ErrOutputTooLarge):
			status, code = http.StatusUnprocessableEntity, "output_too_large"
		case errors.Is(err, ErrBlocked):
			status, code = http.StatusUnprocessableEntity, "blocked"
		case errors.Is(err, ErrInvalidDocument):
			status, code = http.StatusUnprocessableEntity, "invalid_document"
		}
		writeError(w, status, code)
		return
	}
	if len(pdf) > MaxOutputBytes {
		writeError(w, http.StatusUnprocessableEntity, "output_too_large")
		return
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		writeError(w, http.StatusInternalServerError, "conversion_failed")
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Length", fmt.Sprint(len(pdf)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pdf)
}

func (f Format) valid() bool {
	return f == FormatDOCX || f == FormatODT || f == FormatPPT || f == FormatPPTX
}

func validateDocument(format Format, data []byte) error {
	if format == FormatPPT {
		return validatePPT(data)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(zr.File) == 0 || len(zr.File) > 2048 {
		return ErrBlocked
	}
	names := make(map[string]bool, len(zr.File))
	var odfMIME string
	var expanded uint64
	for _, file := range zr.File {
		name := strings.ReplaceAll(file.Name, "\\", "/")
		clean, lower := path.Clean(name), strings.ToLower(path.Clean(name))
		if clean == "." || strings.HasPrefix(clean, "../") || strings.HasPrefix(name, "/") {
			return ErrBlocked
		}
		expanded += file.UncompressedSize64
		if expanded > 32<<20 || file.UncompressedSize64 > 8<<20 || (file.CompressedSize64 > 0 && file.UncompressedSize64/file.CompressedSize64 > 100) {
			return ErrBlocked
		}
		if strings.Contains(lower, "vbaproject") || strings.Contains(lower, "/activex/") || strings.Contains(lower, "/embeddings/") || strings.Contains(lower, "/externallinks/") || strings.HasPrefix(lower, "object ") || strings.HasPrefix(lower, "scripts/") || strings.HasPrefix(lower, "basic/") {
			return ErrBlocked
		}
		names[lower] = true
		if strings.HasSuffix(lower, ".xml") || strings.HasSuffix(lower, ".rels") {
			reader, openErr := file.Open()
			if openErr != nil {
				return ErrBlocked
			}
			body, readErr := io.ReadAll(io.LimitReader(reader, (8<<20)+1))
			_ = reader.Close()
			if readErr != nil || len(body) > 8<<20 {
				return ErrBlocked
			}
			folded := strings.ToLower(string(body))
			if strings.Contains(folded, "<!doctype") || strings.Contains(folded, "<!entity") || strings.Contains(folded, `targetmode="external"`) || strings.Contains(folded, "targetmode='external'") || strings.Contains(folded, "<office:scripts") || strings.Contains(folded, "<script:") || strings.Contains(folded, "<draw:object") || strings.Contains(folded, "<draw:plugin") || externalReference(folded) {
				return ErrBlocked
			}
		}
		if lower == "mimetype" {
			reader, openErr := file.Open()
			if openErr != nil {
				return ErrBlocked
			}
			value, readErr := io.ReadAll(io.LimitReader(reader, 257))
			_ = reader.Close()
			if readErr != nil || len(value) > 256 {
				return ErrBlocked
			}
			odfMIME = strings.TrimSpace(string(value))
		}
	}
	switch format {
	case FormatDOCX:
		if names["word/document.xml"] {
			return nil
		}
	case FormatPPTX:
		if names["ppt/presentation.xml"] {
			return nil
		}
	case FormatODT:
		if names["content.xml"] && odfMIME == "application/vnd.oasis.opendocument.text" {
			return nil
		}
	}
	return ErrBlocked
}

func externalReference(xml string) bool {
	for _, quote := range []string{`xlink:href="`, "xlink:href='"} {
		for _, scheme := range []string{"http:", "https:", "file:", "ftp:"} {
			if strings.Contains(xml, quote+scheme) {
				return true
			}
		}
	}
	return false
}

func validatePPT(data []byte) error {
	// CFB signature. Stream-directory validation is performed by the runner
	// immediately before invoking LibreOffice.
	if len(data) < 8 || !bytes.Equal(data[:8], []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}) {
		return ErrBlocked
	}
	return nil
}

func writeError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code})
}
