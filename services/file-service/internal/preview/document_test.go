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
		{"mimetype": "application/vnd.oasis.opendocument.spreadsheet", "content.xml": `<office:document xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0"><office:scripts><office:script>macro</office:script></office:scripts></office:document>`},
		{"mimetype": "application/vnd.oasis.opendocument.spreadsheet", "content.xml": `<draw:object xlink:href="Object 1"/>`},
		{"mimetype": "application/vnd.oasis.opendocument.spreadsheet", "content.xml": `<text:a xlink:href="https://example.test/secret"/>`},
		{"mimetype": "application/vnd.oasis.opendocument.spreadsheet", "content.xml": `<text:a xlink:href='file:///etc/passwd'/>`},
		{"mimetype": "application/vnd.oasis.opendocument.spreadsheet", "content.xml": `<office/>`, "Object 1/content.xml": `<office/>`},
	}
	for _, files := range tests {
		if _, err := InspectDocumentContainer(zipFixture(t, files)); err == nil {
			t.Fatalf("dangerous container accepted: %v", files)
		}
	}
}

func TestInspectDocumentContainerAcceptsEmptyLibreOfficeScriptsElement(t *testing.T) {
	data := zipFixture(t, map[string]string{
		"mimetype":    "application/vnd.oasis.opendocument.text",
		"content.xml": `<office:document xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0"><office:scripts/></office:document>`,
	})
	if got, err := InspectDocumentContainer(data); err != nil || got != "application/vnd.oasis.opendocument.text" {
		t.Fatalf("empty scripts element: mime=%q err=%v", got, err)
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

func TestSecurityDocumentReferences(t *testing.T) {
	for _, suffix := range []string{".rels", ".RELS", ".Rels"} {
		for _, attrs := range []string{
			`Type="http://schemas.test/attachedTemplate" Target="word/document.xml"`,
			`Type="http://schemas.test/oleObject" Target="word/document.xml"`,
			`Type="http://schemas.test/vbaProject" Target="word/document.xml"`,
			`Type="http://schemas.test/activeX" Target="word/document.xml"`,
			`Type="http://schemas.test/externalLink" Target="word/document.xml"`,
			`Type="http://schemas.test/document" Target="https://example.test" TargetMode = 'Ext&#101;rnal'`,
			`Type="http://schemas.test/document" Target="/etc/passwd"`,
		} {
			files := map[string]string{"word/document.xml": "<document/>", "_rels/" + suffix: `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="r1" ` + attrs + `/></Relationships>`}
			if _, err := InspectDocumentContainer(zipFixture(t, files)); err == nil {
				t.Fatalf("unsafe relation: %s %s", suffix, attrs)
			}
		}
	}
	for _, ref := range []string{"/etc/passwd", `C:\Windows\win.ini`, "../secret", "%2e%2e/secret", "file:///secret", "http://example.test", "https://example.test", "//example.test", "Pictures/a.png?q=x", "missing.png", "%252e%252e/secret", "%ZZ", "http&#58;//example.test", "#section", "Pictures/a.png"} {
		files := map[string]string{"mimetype": "application/vnd.oasis.opendocument.text", "Pictures/a.png": "image", "content.xml": `<document xmlns:r="http://www.w3.org/1999/xlink"><image r:href = '` + ref + `'/></document>`}
		_, err := InspectDocumentContainer(zipFixture(t, files))
		valid := ref == "#section" || ref == "Pictures/a.png"
		if (err == nil) != valid {
			t.Fatalf("ref %s: %v", ref, err)
		}
	}
}
