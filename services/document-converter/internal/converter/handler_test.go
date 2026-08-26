package converter

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
