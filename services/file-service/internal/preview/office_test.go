package preview_test

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
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

func TestRenderDocumentConvertsOfficeDocumentsThenRasterizesPDF(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		format   converterapi.Format
		files    map[string]string
	}{
		{name: "DOCX", filename: "report.docx", format: converterapi.FormatDOCX, files: map[string]string{"word/document.xml": `<document/>`}},
		{name: "ODT", filename: "report.odt", format: converterapi.FormatODT, files: map[string]string{"mimetype": "application/vnd.oasis.opendocument.text", "content.xml": `<document/>`}},
		{name: "PPTX", filename: "slides.pptx", format: converterapi.FormatPPTX, files: map[string]string{"ppt/presentation.xml": `<presentation/>`}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := zipBytes(t, test.files)
			client := &stubDocumentConverter{}
			renderer := preview.NewWithDocumentConverter(client)
			pages, contentType, err := renderer.RenderDocument(context.Background(), "application/zip", test.filename, bytes.NewReader(document))
			if err != nil {
				t.Fatalf("RenderDocument: %v", err)
			}
			if client.format != test.format || !bytes.Equal(client.body, document) {
				t.Fatalf("converter received format/body %q/%d bytes", client.format, len(client.body))
			}
			if contentType != domain.PreviewContentTypeJPEG || len(pages) != 2 {
				t.Fatalf("content type/pages = %q/%d", contentType, len(pages))
			}
		})
	}
}

type erroringDocumentConverter struct{ err error }

func (e erroringDocumentConverter) Convert(context.Context, converterapi.Format, io.Reader) ([]byte, error) {
	return nil, e.err
}

func TestRenderDocumentRefusesWithoutAConfiguredConverter(t *testing.T) {
	renderer := preview.New()
	document := zipBytes(t, map[string]string{"word/document.xml": `<document/>`})
	_, _, err := renderer.RenderDocument(context.Background(), "application/zip", "report.docx", bytes.NewReader(document))
	if !errors.Is(err, preview.ErrUnsupported) {
		t.Fatalf("error = %v, want ErrUnsupported", err)
	}
}

func TestRenderDocumentClassifiesConverterErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want error
	}{
		{"blocked", converterapi.ErrBlocked, preview.ErrUnsupported},
		{"permanent", converterapi.ErrPermanent, preview.ErrRender},
		{"transient", converterapi.ErrTransient, converterapi.ErrTransient},
	}
	document := zipBytes(t, map[string]string{"word/document.xml": `<document/>`})
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			renderer := preview.NewWithDocumentConverter(erroringDocumentConverter{err: test.err})
			_, _, err := renderer.RenderDocument(context.Background(), "application/zip", "report.docx", bytes.NewReader(document))
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want class %v", err, test.want)
			}
		})
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
