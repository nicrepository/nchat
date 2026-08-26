package preview

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

func zipFixture(t *testing.T, files map[string]string) []byte {
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

func TestInspectDocumentContainerIdentifiesSafeFormats(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"docx", map[string]string{"[Content_Types].xml": `<Types/>`, "word/document.xml": `<document/>`}, "application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
		{"xlsx", map[string]string{"[Content_Types].xml": `<Types/>`, "xl/workbook.xml": `<workbook/>`}, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"},
		{"pptx", map[string]string{"[Content_Types].xml": `<Types/>`, "ppt/presentation.xml": `<presentation/>`}, "application/vnd.openxmlformats-officedocument.presentationml.presentation"},
		{"odt", map[string]string{"mimetype": "application/vnd.oasis.opendocument.text", "content.xml": `<office/>`}, "application/vnd.oasis.opendocument.text"},
		{"ods", map[string]string{"mimetype": "application/vnd.oasis.opendocument.spreadsheet", "content.xml": `<office/>`}, "application/vnd.oasis.opendocument.spreadsheet"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := InspectDocumentContainer(zipFixture(t, test.files))
			if err != nil {
				t.Fatalf("InspectDocumentContainer: %v", err)
			}
			if got != test.want {
				t.Fatalf("mime = %q, want %q", got, test.want)
			}
		})
	}
}

func TestInspectDocumentContainerRejectsTraversalAndMacros(t *testing.T) {
	tests := []map[string]string{
		{"../escape": "x", "word/document.xml": "x"},
		{"word/document.xml": "x", "word/vbaProject.bin": "macro"},
		{"xl/workbook.xml": "x", "xl/externalLinks/externalLink1.xml": "external"},
	}
	for _, files := range tests {
		if _, err := InspectDocumentContainer(zipFixture(t, files)); err == nil {
			t.Fatalf("dangerous container accepted: %v", files)
		}
	}
}

func TestInspectDocumentContainerRejectsExcessiveExpansion(t *testing.T) {
	data := zipFixture(t, map[string]string{
		"word/document.xml": strings.Repeat("A", maxDocumentExpandedBytes+1),
	})
	if _, err := InspectDocumentContainer(data); err == nil {
		t.Fatal("expansion bomb accepted")
	}
}
