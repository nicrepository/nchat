package preview_test

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"testing"

	converterapi "github.com/nicrepository/nchat/services/file-service/internal/converter"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/preview"
)

func zipBytes(t *testing.T, files map[string]string) []byte {
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

type stubDocumentConverter struct {
	format converterapi.Format
	body   []byte
}

func (s *stubDocumentConverter) Convert(_ context.Context, format converterapi.Format, source io.Reader) ([]byte, error) {
	s.format = format
	s.body, _ = io.ReadAll(source)
	return multiPagePDF(2), nil
}

func TestRenderDocumentConvertsDOCXThenRasterizesPDF(t *testing.T) {
	document := zipBytes(t, map[string]string{"word/document.xml": `<document/>`})
	client := &stubDocumentConverter{}
	renderer := preview.NewWithDocumentConverter(client)
	pages, contentType, err := renderer.RenderDocument(context.Background(), "application/zip", "report.docx", bytes.NewReader(document))
	if err != nil {
		t.Fatalf("RenderDocument: %v", err)
	}
	if client.format != converterapi.FormatDOCX || !bytes.Equal(client.body, document) {
		t.Fatalf("converter received format/body %q/%d bytes", client.format, len(client.body))
	}
	if contentType != domain.PreviewContentTypeJPEG || len(pages) != 2 {
		t.Fatalf("content type/pages = %q/%d", contentType, len(pages))
	}
}

func TestRenderDocumentUsesExtensionOnlyAsLegacyPPTCandidate(t *testing.T) {
	client := &stubDocumentConverter{}
	renderer := preview.NewWithDocumentConverter(client)
	_, _, err := renderer.RenderDocument(context.Background(), "application/octet-stream", "slides.ppt", bytes.NewReader([]byte("not CFB")))
	if err == nil || client.format != "" {
		t.Fatalf("disguised PPT reached converter: format=%q err=%v", client.format, err)
	}
}
