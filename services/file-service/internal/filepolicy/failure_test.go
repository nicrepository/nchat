package filepolicy

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// An authenticated storage reader can fail at any seek/read. No such failure
// may be converted into a format approval, even after valid framing was read.
type failingSource struct {
	*bytes.Reader
	remaining int
}

var errStorage = errors.New("storage interrupted")

func (r *failingSource) step() error {
	r.remaining--
	if r.remaining <= 0 {
		return errStorage
	}
	return nil
}

func (r *failingSource) Read(p []byte) (int, error) {
	if err := r.step(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

func (r *failingSource) Seek(offset int64, whence int) (int64, error) {
	if err := r.step(); err != nil {
		return 0, err
	}
	return r.Reader.Seek(offset, whence)
}

func TestStorageFailuresNeverApproveContent(t *testing.T) {
	for _, name := range []string{"notes.txt", "data.json", "data.xml", "message.eml", "document.pdf", "document.docx", "bundle.zip"} {
		t.Run(name, func(t *testing.T) {
			b := mustReadCorpus(t, name)
			for operation := 1; operation <= 20; operation++ {
				source := &failingSource{bytes.NewReader(b), operation}
				r, _ := Inspect(context.Background(), source, int64(len(b)), name, "", allProfiles)
				if source.remaining <= 0 && r.Decision == Allow {
					t.Fatalf("operation %d: failed storage approved", operation)
				}
			}
		})
	}
}

func TestAdvertisedSizeMustMatchInspectedText(t *testing.T) {
	for _, name := range []string{"notes.txt", "data.json", "message.eml"} {
		t.Run(name, func(t *testing.T) {
			b := mustReadCorpus(t, name)
			r, err := Inspect(context.Background(), bytes.NewReader(b), int64(len(b)+1), name, "", allProfiles)
			if err == nil && r.Decision == Allow {
				t.Fatal("incorrect authenticated size approved")
			}
		})
	}
}

func TestOfficeRequiresConsistentRelationshipsAndBodies(t *testing.T) {
	cases := []struct{ format, part, old, replacement string }{
		{"docx", "[Content_Types].xml", "Types", "Unknown"},
		{"docx", "[Content_Types].xml", `Extension="xml"`, `Extension="a/b"`},
		{"docx", "[Content_Types].xml", `/word/document.xml`, `/Word/document.xml`},
		{"docx", "[Content_Types].xml", `/word/document.xml`, `word/document.xml`},
		{"docx", "[Content_Types].xml", `Extension="rels"`, `Extension="other"`},
		{"docx", "_rels/.rels", "Relationships", "Unknown"},
		{"docx", "_rels/.rels", `Id="rMain"`, `Id=""`},
		{"docx", "_rels/.rels", "officeDocument", "metadata"},
		{"docx", "_rels/.rels", `word/document.xml`, `%zz`},
		{"xlsx", "xl/workbook.xml", `r:id="rSheet"`, `r:id="missing"`},
		{"xlsx", "xl/workbook.xml", `r:id="rSheet"`, `r:unknown="rSheet"`},
		{"xlsx", "xl/workbook.xml", `sheets`, `other`},
		{"xlsx", "xl/_rels/workbook.xml.rels", `/worksheet`, `/metadata`},
		{"odt", "mimetype", `opendocument.text`, `opendocument.spreadsheet`},
		{"odt", "content.xml", `office:text`, `office:spreadsheet`},
		{"odt", "content.xml", `office:document-content`, `office:unknown`},
		{"odt", "META-INF/manifest.xml", `manifest:manifest`, `manifest:unknown`},
		{"odt", "META-INF/manifest.xml", `opendocument.text`, `opendocument.spreadsheet`},
		{"odt", "META-INF/manifest.xml", `content.xml`, `missing.xml`},
		{"odt", "META-INF/manifest.xml", `content.xml`, `../content.xml`},
	}
	for _, tt := range cases {
		t.Run(tt.format+"/"+tt.part+"/"+tt.replacement, func(t *testing.T) {
			b := replaceOfficePart(t, officeFixture(t, tt.format), tt.part, tt.old, tt.replacement)
			if r := inspectFixture(t, "document."+tt.format, "", b, allProfiles); r.Decision == Allow {
				t.Fatal("inconsistent package approved")
			}
		})
	}
}

func TestXMLRequiresOnePassiveBoundedRoot(t *testing.T) {
	for _, input := range []string{"<a/><b/>", "text<a/>", "<a>text", "<!--empty-->", "<?untrusted x?><a/>", "<a><scripts/></a>", "<a><b onload=\"x\"/></a>", "<a>\x00</a>", strings.Repeat("<a>", MaxXMLDepth+1) + strings.Repeat("</a>", MaxXMLDepth+1)} {
		if r := inspectFixture(t, "document.xml", "", []byte(input), allProfiles); r.Decision == Allow {
			t.Fatalf("unsafe XML approved: %q", input)
		}
	}
}

func TestZIPMetadataCannotChangePathOrEncryptionInterpretation(t *testing.T) {
	cases := []struct {
		name  string
		extra []byte
		want  Decision
	}{
		{"timestamp", []byte{0x55, 0x54, 1, 0, 0}, Allow},
		{"unix ownership", []byte{0x75, 0x78, 3, 0, 1, 0, 0}, Allow},
		{"NTFS timestamp", []byte{0x0a, 0, 4, 0, 0, 0, 0, 0}, Allow},
		{"alternate unicode path", []byte{0x75, 0x70, 0, 0}, Restricted},
		{"AES encryption", []byte{0x01, 0x99, 0, 0}, Restricted},
		{"truncated extra header", []byte{0x55}, Reject},
		{"truncated extra body", []byte{0x55, 0x54, 2, 0, 0}, Reject},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			b := zipWithExtra(t, tt.extra)
			if r := inspectFixture(t, "bundle.zip", "", b, allProfiles); r.Decision != tt.want {
				t.Fatalf("metadata: %+v, want %s", r, tt.want)
			}
		})
	}
}

func TestCanonicalPreviewTypesSelectExistingIsolatedRenderers(t *testing.T) {
	for _, tt := range []struct{ mime, renderer string }{
		{"application/vnd.openxmlformats-officedocument.wordprocessingml.document", "application/zip"},
		{"application/vnd.oasis.opendocument.spreadsheet", "application/zip"},
		{"text/csv", "text/plain"},
		{"application/pdf", "application/pdf"},
		{"application/json", "application/json"},
	} {
		if got := PreviewMIME(tt.mime); got != tt.renderer {
			t.Fatalf("%s: %s", tt.mime, got)
		}
	}
}

func BenchmarkInspectStreamingText(b *testing.B) {
	payload := bytes.Repeat([]byte("corporate data\n"), 1<<19)
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	for b.Loop() {
		r, err := Inspect(context.Background(), bytes.NewReader(payload), int64(len(payload)), "notes.txt", "", Profiles{})
		if err != nil || r.Decision != Allow {
			b.Fatalf("inspection: %+v, %v", r, err)
		}
	}
}

func BenchmarkInspectOffice(b *testing.B) {
	payload := mustReadCorpus(b, "document.xlsx")
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		r, err := Inspect(context.Background(), bytes.NewReader(payload), int64(len(payload)), "costs.xlsx", "", allProfiles)
		if err != nil || r.Decision != Allow {
			b.Fatalf("inspection: %+v, %v", r, err)
		}
	}
}

var _ io.ReadSeeker = (*failingSource)(nil)

func replaceOfficePart(t testing.TB, b []byte, part, old, replacement string) []byte {
	t.Helper()
	return rewriteFixture(t, b, func(name, body string) string {
		if name != part {
			return body
		}
		if !strings.Contains(body, old) {
			t.Fatalf("fixture lacks mutation target %q", old)
		}
		return strings.ReplaceAll(body, old, replacement)
	})
}

func zipWithExtra(t testing.TB, extra []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	entry, err := w.CreateHeader(&zip.FileHeader{Name: "notes.txt", Method: zip.Store, Extra: extra})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.WriteString(entry, "notes"); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
