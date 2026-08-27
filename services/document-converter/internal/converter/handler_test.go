package converter

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func zipDocument(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for name, body := range files {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

type stubRunner struct {
	pdf []byte
	err error
}

func (r stubRunner) Convert(context.Context, Format, []byte) ([]byte, error) {
	return r.pdf, r.err
}

func TestHandlerReturnsPDFForValidatedDOCX(t *testing.T) {
	body := zipDocument(t, map[string]string{
		"[Content_Types].xml": `<Types/>`,
		"word/document.xml":   `<w:document/>`,
	})
	handler := NewHandler(stubRunner{pdf: []byte("%PDF-1.7\n%%EOF")})
	req := httptest.NewRequest(http.MethodPost, "/v1/convert", bytes.NewReader(body))
	req.Header.Set("X-Document-Format", "docx")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK || res.Header().Get("Content-Type") != "application/pdf" {
		t.Fatalf("status/content-type = %d/%q; body=%s", res.Code, res.Header().Get("Content-Type"), res.Body.String())
	}
}

func TestHandlerAcceptsLibreOfficeODTWithEmptyScriptsElement(t *testing.T) {
	body := zipDocument(t, map[string]string{
		"mimetype":    "application/vnd.oasis.opendocument.text",
		"content.xml": `<office:document xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0"><office:scripts/></office:document>`,
	})
	handler := NewHandler(stubRunner{pdf: []byte("%PDF-1.7\n%%EOF")})
	req := httptest.NewRequest(http.MethodPost, "/v1/convert", bytes.NewReader(body))
	req.Header.Set("X-Document-Format", "odt")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", res.Code, res.Body.String())
	}
}

func TestHandlerRejectsDisguisedOrActiveDocumentsBeforeRunner(t *testing.T) {
	tests := []struct {
		name   string
		format string
		body   []byte
	}{
		{"arbitrary zip", "docx", zipDocument(t, map[string]string{"payload": "x"})},
		{"macro docx", "docx", zipDocument(t, map[string]string{"word/document.xml": "x", "word/vbaProject.bin": "x"})},
		{"wrong subtype", "pptx", zipDocument(t, map[string]string{"word/document.xml": "x"})},
		{"external relationship", "docx", zipDocument(t, map[string]string{"word/document.xml": "x", "word/_rels/document.xml.rels": `<Relationship TargetMode="External" Target="https://example.test"/>`})},
		{"xml entity", "odt", zipDocument(t, map[string]string{"mimetype": "application/vnd.oasis.opendocument.text", "content.xml": `<!DOCTYPE x [<!ENTITY e SYSTEM "file:///etc/passwd">]>`})},
		{"odt script", "odt", zipDocument(t, map[string]string{"mimetype": "application/vnd.oasis.opendocument.text", "content.xml": `<office:scripts><office:script>macro</office:script></office:scripts>`})},
		{"unknown format", "exe", []byte("MZ")},
		{"forged CFB header", "ppt", append([]byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}, make([]byte, 504)...)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := NewHandler(stubRunner{pdf: []byte("%PDF")})
			req := httptest.NewRequest(http.MethodPost, "/v1/convert", bytes.NewReader(test.body))
			req.Header.Set("X-Document-Format", test.format)
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, req)
			if res.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, body=%s", res.Code, res.Body.String())
			}
			assertErrorCode(t, res, "blocked")
		})
	}
}

func TestHandlerClassifiesRunnerFailureAndOversizedInput(t *testing.T) {
	handler := NewHandler(stubRunner{err: ErrTimeout})
	body := zipDocument(t, map[string]string{"word/document.xml": "x"})
	req := httptest.NewRequest(http.MethodPost, "/v1/convert", bytes.NewReader(body))
	req.Header.Set("X-Document-Format", "docx")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusGatewayTimeout {
		t.Fatalf("timeout status = %d", res.Code)
	}
	assertErrorCode(t, res, "timeout")

	tooLarge := bytes.NewReader(make([]byte, MaxInputBytes+1))
	req = httptest.NewRequest(http.MethodPost, "/v1/convert", tooLarge)
	req.Header.Set("X-Document-Format", "docx")
	res = httptest.NewRecorder()
	NewHandler(stubRunner{}).ServeHTTP(res, req)
	if res.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized status = %d", res.Code)
	}
}

func TestHandlerServesHealthAndReadyEndpoints(t *testing.T) {
	handler := NewHandler(stubRunner{})
	for _, path := range []string{"/healthz", "/readyz"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != http.StatusNoContent {
			t.Fatalf("%s status = %d, want 204", path, res.Code)
		}
	}
}

func TestHandlerReturns404ForUnknownMethodOrPath(t *testing.T) {
	handler := NewHandler(stubRunner{})
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/v1/convert", nil),
		httptest.NewRequest(http.MethodPost, "/v1/does-not-exist", nil),
	} {
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != http.StatusNotFound {
			t.Fatalf("%s %s status = %d, want 404", req.Method, req.URL.Path, res.Code)
		}
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

func TestHandlerReturns400WhenTheRequestBodyFailsToRead(t *testing.T) {
	handler := NewHandler(stubRunner{})
	req := httptest.NewRequest(http.MethodPost, "/v1/convert", errorReader{})
	req.Header.Set("X-Document-Format", "docx")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", res.Code)
	}
	assertErrorCode(t, res, "invalid_document")
}

func TestHandlerClassifiesEveryRunnerError(t *testing.T) {
	tests := []struct {
		name       string
		runnerErr  error
		wantStatus int
		wantCode   string
	}{
		{"output too large", ErrOutputTooLarge, http.StatusUnprocessableEntity, "output_too_large"},
		{"blocked", ErrBlocked, http.StatusUnprocessableEntity, "blocked"},
		{"invalid document", ErrInvalidDocument, http.StatusUnprocessableEntity, "invalid_document"},
		{"unknown error", errors.New("unexpected"), http.StatusInternalServerError, "conversion_failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := NewHandler(stubRunner{err: test.runnerErr})
			body := zipDocument(t, map[string]string{"word/document.xml": "x"})
			req := httptest.NewRequest(http.MethodPost, "/v1/convert", bytes.NewReader(body))
			req.Header.Set("X-Document-Format", "docx")
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, req)
			if res.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", res.Code, test.wantStatus)
			}
			assertErrorCode(t, res, test.wantCode)
		})
	}
}

func TestHandlerRejectsAnOversizedConvertedPDF(t *testing.T) {
	oversized := append([]byte("%PDF-"), make([]byte, MaxOutputBytes+1)...)
	handler := NewHandler(stubRunner{pdf: oversized})
	body := zipDocument(t, map[string]string{"word/document.xml": "x"})
	req := httptest.NewRequest(http.MethodPost, "/v1/convert", bytes.NewReader(body))
	req.Header.Set("X-Document-Format", "docx")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", res.Code)
	}
	assertErrorCode(t, res, "output_too_large")
}

func TestHandlerRejectsNonPDFOutputFromTheRunner(t *testing.T) {
	handler := NewHandler(stubRunner{pdf: []byte("not a pdf")})
	body := zipDocument(t, map[string]string{"word/document.xml": "x"})
	req := httptest.NewRequest(http.MethodPost, "/v1/convert", bytes.NewReader(body))
	req.Header.Set("X-Document-Format", "docx")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", res.Code)
	}
	assertErrorCode(t, res, "conversion_failed")
}

// readTestdata is only ever called with one of the fixed literal filenames in
// this file (see call sites) — never a value derived from a request, a flag
// or any other external input.
func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name) // #nosec G304 -- name is always a fixed literal in this file, see comment above
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestValidatePPTRejectsTooShortInput(t *testing.T) {
	if err := validatePPT([]byte{0xd0, 0xcf, 0x11}); !errors.Is(err, ErrBlocked) {
		t.Fatalf("error = %v, want ErrBlocked", err)
	}
}

func TestValidatePPTRejectsWrongMagicBytes(t *testing.T) {
	if err := validatePPT(make([]byte, 512)); !errors.Is(err, ErrBlocked) {
		t.Fatalf("error = %v, want ErrBlocked", err)
	}
}

func TestValidatePPTAcceptsAGenuinePowerPointDocument(t *testing.T) {
	data := readTestdata(t, "valid.ppt")
	if err := validatePPT(data); err != nil {
		t.Fatalf("validatePPT = %v, want nil", err)
	}
}

func TestValidatePPTBlocksActiveContentStreams(t *testing.T) {
	data := readTestdata(t, "blocked-objectpool.doc")
	if err := validatePPT(data); !errors.Is(err, ErrBlocked) {
		t.Fatalf("error = %v, want ErrBlocked", err)
	}
}

func TestValidatePPTRejectsAValidContainerMissingThePowerPointStream(t *testing.T) {
	data := readTestdata(t, "no-powerpoint-stream.xls")
	if err := validatePPT(data); !errors.Is(err, ErrBlocked) {
		t.Fatalf("error = %v, want ErrBlocked", err)
	}
}

func TestHandlerConvertsAGenuinePowerPointDocumentEndToEnd(t *testing.T) {
	data := readTestdata(t, "valid.ppt")
	handler := NewHandler(stubRunner{pdf: []byte("%PDF-1.7\n%%EOF")})
	req := httptest.NewRequest(http.MethodPost, "/v1/convert", bytes.NewReader(data))
	req.Header.Set("X-Document-Format", "ppt")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", res.Code, res.Body.String())
	}
}

func TestHasNonEmptyScriptsElementBlocksDirectTextContent(t *testing.T) {
	body := zipDocument(t, map[string]string{
		"mimetype":    "application/vnd.oasis.opendocument.text",
		"content.xml": `<office:document xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0"><office:scripts>alert(1)</office:scripts></office:document>`,
	})
	if err := validateDocument(FormatODT, body); !errors.Is(err, ErrBlocked) {
		t.Fatalf("error = %v, want ErrBlocked", err)
	}
}

func TestHasNonEmptyScriptsElementBlocksMalformedXMLContainingScripts(t *testing.T) {
	body := zipDocument(t, map[string]string{
		"mimetype":    "application/vnd.oasis.opendocument.text",
		"content.xml": `<office:document><office:scripts><unterminated`,
	})
	if err := validateDocument(FormatODT, body); !errors.Is(err, ErrBlocked) {
		t.Fatalf("error = %v, want ErrBlocked", err)
	}
}

func TestExternalReferenceBlocksEverySchemeAndQuoteStyle(t *testing.T) {
	tests := []struct {
		name string
		href string
	}{
		{"http double-quote", `xlink:href="http://example.test"`},
		{"https double-quote", `xlink:href="https://example.test"`},
		{"file double-quote", `xlink:href="file:///etc/passwd"`},
		{"ftp double-quote", `xlink:href="ftp://example.test"`},
		{"https single-quote", `xlink:href='https://example.test'`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// No TargetMode="External" here on purpose: that condition is
			// checked earlier in validateDocument's OR chain and would
			// short-circuit before externalReference ever runs.
			body := zipDocument(t, map[string]string{
				"mimetype":    "application/vnd.oasis.opendocument.text",
				"content.xml": `<office:document ` + test.href + `></office:document>`,
			})
			if err := validateDocument(FormatODT, body); !errors.Is(err, ErrBlocked) {
				t.Fatalf("error = %v, want ErrBlocked", err)
			}
		})
	}
}

func TestValidateDocumentRejectsAnEmptyZip(t *testing.T) {
	var output bytes.Buffer
	if err := zip.NewWriter(&output).Close(); err != nil {
		t.Fatal(err)
	}
	if err := validateDocument(FormatDOCX, output.Bytes()); !errors.Is(err, ErrBlocked) {
		t.Fatalf("error = %v, want ErrBlocked", err)
	}
}

func TestValidateDocumentRejectsPathTraversalAndAbsoluteEntryNames(t *testing.T) {
	tests := []struct {
		name  string
		entry string
	}{
		{"path traversal", "../../etc/passwd"},
		{"absolute path", "/etc/passwd"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := zipDocument(t, map[string]string{test.entry: "x", "word/document.xml": "x"})
			if err := validateDocument(FormatDOCX, body); !errors.Is(err, ErrBlocked) {
				t.Fatalf("error = %v, want ErrBlocked", err)
			}
		})
	}
}

func TestValidateDocumentRejectsAFileOverThePerFileSizeCap(t *testing.T) {
	huge := map[string]string{
		"word/document.xml": "x",
		// Highly compressible so the test zip itself stays small; the cap
		// checks the declared uncompressed size, which archive/zip computes
		// accurately regardless of how well the content compresses.
		"word/media/huge.bin": strings.Repeat("A", (8<<20)+1),
	}
	body := zipDocument(t, huge)
	if err := validateDocument(FormatDOCX, body); !errors.Is(err, ErrBlocked) {
		t.Fatalf("error = %v, want ErrBlocked", err)
	}
}

func TestValidateDocumentAcceptsAWellFormedPPTX(t *testing.T) {
	body := zipDocument(t, map[string]string{"ppt/presentation.xml": `<p:presentation/>`})
	if err := validateDocument(FormatPPTX, body); err != nil {
		t.Fatalf("validateDocument = %v, want nil", err)
	}
}

func assertErrorCode(t *testing.T, res *httptest.ResponseRecorder, want string) {
	t.Helper()
	var payload struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Code != want {
		t.Fatalf("code = %q, want %q", payload.Code, want)
	}
}
