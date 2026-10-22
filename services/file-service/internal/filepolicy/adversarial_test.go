package filepolicy

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
)

func rewriteFixture(t testing.TB, b []byte, change func(string, string) string) []byte {
	t.Helper()
	r, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	entries := make([]zipEntry, 0, len(r.File))
	for _, f := range r.File {
		body, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(body)
		_ = body.Close()
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, zipEntry{f.Name, change(f.Name, string(data)), 0})
	}
	return archiveFixture(t, entries)
}

func TestOOXMLCannotHideActiveOrAmbiguousParts(t *testing.T) {
	cases := []struct{ label, part, old, new string }{
		{"wrong content namespace", "[Content_Types].xml", contentTypesNS, "urn:untrusted"},
		{"macro content type", "[Content_Types].xml", "wordprocessingml.document.main+xml", "wordprocessingml.document.macroEnabled.main+xml"},
		{"missing override", "[Content_Types].xml", "/word/document.xml", "/missing.xml"},
		{"bad default", "[Content_Types].xml", "application/xml", "text/html"},
		{"unknown content marker", "[Content_Types].xml", "<Default ", "<Unknown "},
		{"duplicate default", "[Content_Types].xml", "</Types>", `<Default Extension="xml" ContentType="application/xml"/></Types>`},
		{"external relationship", "_rels/.rels", `Target="word/document.xml"`, `Target="https://example.test/data" TargetMode="External"`},
		{"internal mode external target", "_rels/.rels", `Target="word/document.xml"`, `Target="file:///etc/passwd" TargetMode="Internal"`},
		{"macro relation", "_rels/.rels", "/officeDocument", "/vbaProject"},
		{"broken relation", "_rels/.rels", "word/document.xml", "../document.xml"},
		{"wrong relation namespace", "_rels/.rels", packageRelsNS, "urn:untrusted"},
		{"missing main body", "word/document.xml", "w:body", "w:unknown"},
		{"DDE fields", "word/document.xml", "w:p", "w:instrText"},
		{"duplicate attribute", "word/document.xml", "<w:document ", `<w:document a="x" a="y" `},
	}
	for _, tt := range cases {
		t.Run(tt.label, func(t *testing.T) {
			b := rewriteFixture(t, officeFixture(t, "docx"), func(name, body string) string {
				if name == tt.part {
					return strings.ReplaceAll(body, tt.old, tt.new)
				}
				return body
			})
			if r := inspectFixture(t, "document.docx", "", b, allProfiles); r.Decision == Allow {
				t.Fatal("unsafe Office package admitted")
			}
		})
	}
}

func TestODFRejectsActiveContents(t *testing.T) {
	original := mustReadCorpus(t, "document.odt")
	for _, payload := range []string{`<office:scripts><script>macro</script></office:scripts>`, `<office:scripts>macro</office:scripts>`, `<object/>`, `<x xmlns:xlink="http://www.w3.org/1999/xlink" xlink:href="file:///etc/passwd"/>`} {
		t.Run(payload, func(t *testing.T) {
			b := rewriteFixture(t, original, func(name, body string) string {
				if name == "content.xml" {
					return strings.Replace(body, "<office:body>", payload+"<office:body>", 1)
				}
				return body
			})
			if r := inspectFixture(t, "document.odt", "", b, allProfiles); r.Decision == Allow {
				t.Fatal("active ODF accepted")
			}
		})
	}
}

func TestODFEmptyScriptsMarkerIsPassive(t *testing.T) {
	original := mustReadCorpus(t, "document.odt")
	b := rewriteFixture(t, original, func(name, body string) string {
		if name == "content.xml" {
			return strings.Replace(body, "<office:body>", "<office:scripts/><office:body>", 1)
		}
		return body
	})
	if r := inspectFixture(t, "document.odt", "", b, allProfiles); r.Decision != Allow {
		t.Fatalf("empty script marker: %+v", r)
	}
}

func TestODFEncryptionManifestIsRejected(t *testing.T) {
	original := mustReadCorpus(t, "document.odt")
	b := rewriteFixture(t, original, func(name, body string) string {
		if name == "META-INF/manifest.xml" {
			return strings.Replace(body, "</manifest:manifest>", "<manifest:encryption-data/></manifest:manifest>", 1)
		}
		return body
	})
	if r := inspectFixture(t, "document.odt", "", b, allProfiles); r.Decision == Allow {
		t.Fatal("encrypted ODF accepted")
	}
}

func mustReadCorpus(t testing.TB, name string) []byte {
	t.Helper()
	root, err := os.OpenRoot("testdata")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	b, err := root.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestEMLRequiresBoundedPassiveSinglePartMail(t *testing.T) {
	base := "From: sender@example.test\r\nTo: reader@example.test\r\nDate: Tue, 20 Oct 2026 12:00:00 +0000\r\n"
	cases := []struct {
		headers, body string
		decision      Decision
	}{
		{"Content-Type: text/plain\r\n", "notes", Allow},
		{"Content-Type: text/plain\r\nContent-Transfer-Encoding: base64\r\n", base64.StdEncoding.EncodeToString([]byte("notes")), Allow},
		{"Content-Type: text/plain\r\nContent-Transfer-Encoding: quoted-printable\r\n", "notes=20data", Allow},
		{"Content-Type: text/html\r\n", "<html/>", Restricted},
		{"Content-Type: multipart/mixed; boundary=x\r\n", "--x\r\nencapsulated\r\n--x--", Restricted},
		{"Content-Type: text/plain; charset=iso-8859-1\r\n", "notes", Restricted},
		{"Content-Type: text/plain\r\nContent-Transfer-Encoding: unknown\r\n", "notes", Restricted},
		{"Content-Type: text/plain\r\nContent-Transfer-Encoding: base64\r\n", "invalid~", Reject},
		{"Content-Type: text/plain\r\n", "", Reject},
		{"Content-Type: text/plain\r\nContent-Type: text/html\r\n", "notes", Reject},
	}
	for i, tt := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			r := inspectFixture(t, "message.eml", "message/rfc822", []byte(base+tt.headers+"\r\n"+tt.body), allProfiles)
			if r.Decision != tt.decision {
				t.Fatalf("decision %+v, expected %s", r, tt.decision)
			}
		})
	}
	for _, bad := range []string{"not mail", strings.Replace(base, "sender@example.test", "<invalid", 1) + "Content-Type: text/plain\r\n\r\nnotes", strings.Replace(base, "Tue, 20 Oct 2026 12:00:00 +0000", "invalid", 1) + "Content-Type: text/plain\r\n\r\nnotes"} {
		if r := inspectFixture(t, "message.eml", "", []byte(bad), allProfiles); r.Decision == Allow {
			t.Fatal("malformed message accepted")
		}
	}
}

func TestZIPPasswordUnknownEncodingAndMalformedMetadata(t *testing.T) {
	valid := archiveFixture(t, []zipEntry{{"notes.txt", "notes", 0}})
	central := bytes.Index(valid, []byte{'P', 'K', 1, 2})
	for _, mutation := range []struct {
		offset int
		value  uint16
	}{{6, 1}, {8, 99}, {central + 8, 1}, {central + 10, 99}, {26, 255}, {28, 1025}} {
		b := bytes.Clone(valid)
		binary.LittleEndian.PutUint16(b[mutation.offset:mutation.offset+2], mutation.value)
		if r := inspectFixture(t, "bundle.zip", "", b, allProfiles); r.Decision == Allow {
			t.Fatal("ambiguous/opaque ZIP accepted")
		}
	}
}

func TestDeflateIsFullyConsumedAndExpansionIsBounded(t *testing.T) {
	for _, n := range []int{100, 100000} {
		compressed := deflatedFixture(t, strings.Repeat("a", n))
		r := inspectFixture(t, "bundle.zip", "", compressed, allProfiles)
		want := Allow
		if n == 100000 {
			want = Indeterminate
		}
		if r.Decision != want {
			t.Fatalf("expansion %d: %+v", n, r)
		}
	}
}

func TestPlainTextUTF8AtReadBoundaries(t *testing.T) {
	b := append(bytes.Repeat([]byte("a"), (64<<10)-1), []byte("ação")...)
	if r := inspectFixture(t, "notes.txt", "", b, allProfiles); r.Decision != Allow {
		t.Fatalf("UTF-8 crossing a read: %+v", r)
	}
	b = append(bytes.Repeat([]byte("a"), (64<<10)-1), 0xc3)
	if r := inspectFixture(t, "notes.txt", "", b, allProfiles); r.Decision == Allow {
		t.Fatal("incomplete UTF-8 accepted")
	}
}

func TestSpreadsheetDataCannotCarryExecutableFormulas(t *testing.T) {
	for _, value := range []string{"=cmd|x", "@SUM(1)", " +cmd", "-cmd", "＝cmd", "＠cmd"} {
		r := inspectFixture(t, "costs.csv", "", []byte("id,value\n1,"+value+"\n"), allProfiles)
		if r.Decision != Reject || r.Code != Active {
			t.Fatalf("active formula %q: %+v", value, r)
		}
	}
	if r := inspectFixture(t, "costs.csv", "", []byte("value,note\n-12,negative\n+15,positive\n"), allProfiles); r.Decision != Allow {
		t.Fatalf("numeric data: %+v", r)
	}
	b := rewriteFixture(t, officeFixture(t, "xlsx"), func(name, body string) string {
		if name == "xl/worksheets/sheet1.xml" {
			return strings.Replace(body, "<v>12</v>", "<f>WEBSERVICE(&quot;https://example.test&quot;)</f><v>12</v>", 1)
		}
		return body
	})
	if r := inspectFixture(t, "costs.xlsx", "", b, allProfiles); r.Decision == Allow {
		t.Fatal("active XLSX formula accepted")
	}
}

func TestArchiveDeclaredResourceBudgetBoundaries(t *testing.T) {
	cases := []struct {
		label                string
		expanded, compressed uint64
		want                 error
	}{
		{"expanded maximum", MaxExpandedBytes, (MaxExpandedBytes + MaxExpansionRatio - 1) / MaxExpansionRatio, nil},
		{"expanded excessive", MaxExpandedBytes + 1, MaxExpandedBytes, errBudget},
		{"compressed maximum", 100, uint64(MaxContainerBytes), nil},
		{"compressed excessive", 100, uint64(MaxContainerBytes) + 1, errBudget},
		{"ratio maximum", 128 * MaxExpansionRatio, 128, nil},
		{"ratio excessive", 128*MaxExpansionRatio + 1, 128, errBudget},
	}
	for _, tt := range cases {
		t.Run(tt.label, func(t *testing.T) {
			file := &zip.File{FileHeader: zip.FileHeader{Name: "notes.txt", CompressedSize64: tt.compressed, UncompressedSize64: tt.expanded}}
			if got := zipEntryBudget(file); got != tt.want {
				t.Fatalf("budget result %v, expected %v", got, tt.want)
			}
		})
	}
}

func deflatedFixture(t testing.TB, body string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	entry, err := w.Create("notes.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.WriteString(entry, body); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestSecurityRelationshipRepresentations(t *testing.T) {
	for _, ext := range []string{"docx", "xlsx", "pptx"} {
		for _, suffix := range []string{".rels", ".RELS", ".Rels"} {
			for _, typ := range []string{"attachedTemplate", "vbaProject", "oleObject", "activeX", "externalLink", "package"} {
				t.Run(ext+suffix+typ, func(t *testing.T) {
					original := mustReadCorpus(t, "document."+ext)
					r, err := zip.NewReader(bytes.NewReader(original), int64(len(original)))
					if err != nil {
						t.Fatal(err)
					}
					var entries []zipEntry
					for _, f := range r.File {
						rd, err := f.Open()
						if err != nil {
							t.Fatal(err)
						}
						b, err := io.ReadAll(rd)
						if err != nil {
							t.Fatal(err)
						}
						_ = rd.Close()
						entries = append(entries, zipEntry{f.Name, string(b), 0})
					}
					entries = append(entries, zipEntry{"custom/_rels/item.xml" + suffix, `<Relationships xmlns="` + packageRelsNS + `"><Relationship Id="attack" Type="` + officeRelsNS + typ + `" Target="../[Content_Types].xml" TargetMode = "Internal"/></Relationships>`, 0})
					if got := inspectFixture(t, "document."+ext, "", archiveFixture(t, entries), allProfiles); got.Decision == Allow {
						t.Fatal("relationship bypass admitted")
					}
				})
			}
		}
	}
}

func TestSecurityODFReferences(t *testing.T) {
	for _, ref := range []string{"/etc/passwd", `C:\Windows\win.ini`, "../secret", "%2e%2e/secret", "file:///secret", "http://example.test/a", "https://example.test/a", "//example.test/a", "content.xml?q=x", "missing.png", "%252e%252e/secret", "%ZZ"} {
		t.Run(ref, func(t *testing.T) {
			b := rewriteFixture(t, mustReadCorpus(t, "document.odt"), func(name, body string) string {
				if name == "content.xml" {
					return strings.Replace(body, "<office:body>", `<x xmlns:r="http://www.w3.org/1999/xlink" r:href = "`+ref+`"/><office:body>`, 1)
				}
				return body
			})
			if got := inspectFixture(t, "document.odt", "", b, allProfiles); got.Decision == Allow {
				t.Fatal("unsafe reference admitted")
			}
		})
	}
}

func TestSecurityWorksheetNamespaces(t *testing.T) {
	for _, body := range []string{
		`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><f>1+1</f></sheetData></worksheet>`,
		`<worksheet xmlns="http://purl.oclc.org/ooxml/spreadsheetml/main"><f>1+1</f></worksheet>`,
		`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><f xmlns="">1+1</f></worksheet>`,
		`<worksheet xmlns="urn:unexpected"/>`,
	} {
		b := rewriteFixture(t, mustReadCorpus(t, "document.xlsx"), func(name, original string) string {
			if strings.HasPrefix(name, "xl/worksheets/") {
				return body
			}
			return original
		})
		if got := inspectFixture(t, "document.xlsx", "", b, allProfiles); got.Decision == Allow {
			t.Fatalf("unsafe worksheet admitted: %s", body)
		}
	}
	if got := inspectFixture(t, "document.xlsx", "", mustReadCorpus(t, "document.xlsx"), allProfiles); got.Decision != Allow {
		t.Fatalf("valid workbook: %+v", got)
	}
}

func TestSecurityRelocatedWorksheetCannotHideFormula(t *testing.T) {
	original := mustReadCorpus(t, "document.xlsx")
	r, err := zip.NewReader(bytes.NewReader(original), int64(len(original)))
	if err != nil {
		t.Fatal(err)
	}
	var entries []zipEntry
	for _, f := range r.File {
		rd, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rd)
		_ = rd.Close()
		if err != nil {
			t.Fatal(err)
		}
		name, body := f.Name, string(b)
		if strings.HasPrefix(name, "xl/worksheets/") {
			name = "xl/data/passive.xml"
			body = `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><f xmlns="">1+1</f></worksheet>`
		}
		body = strings.ReplaceAll(body, "worksheets/sheet1.xml", "data/passive.xml")
		entries = append(entries, zipEntry{name, body, 0})
	}
	if got := inspectFixture(t, "document.xlsx", "", archiveFixture(t, entries), allProfiles); got.Decision == Allow {
		t.Fatal("relocated worksheet bypassed formula policy")
	}
}

func TestSecurityODFInternalReferencesRemainUsable(t *testing.T) {
	for _, ref := range []string{"#section", "content.xml", "./content.xml"} {
		b := rewriteFixture(t, mustReadCorpus(t, "document.odt"), func(name, body string) string {
			if name == "content.xml" {
				return strings.Replace(body, "<office:body>", `<x xmlns:r="http://www.w3.org/1999/xlink" r:href = '`+ref+`'/><office:body>`, 1)
			}
			return body
		})
		if got := inspectFixture(t, "document.odt", "", b, allProfiles); got.Decision != Allow {
			t.Fatalf("internal %s: %+v", ref, got)
		}
	}
}

func TestSecurityODFResolvesPackagedImage(t *testing.T) {
	b := rewriteFixture(t, mustReadCorpus(t, "document.odt"), func(name, body string) string {
		if name == "content.xml" {
			return strings.Replace(body, "</office:text>", `<text:p><draw:frame><draw:image xmlns:link="http://www.w3.org/1999/xlink" link:href="./Pictures/preview.png"/></draw:frame></text:p></office:text>`, 1)
		}
		if name == "META-INF/manifest.xml" {
			return strings.Replace(body, "</manifest:manifest>", `<manifest:file-entry manifest:full-path="Pictures/preview.png" manifest:media-type="image/png"/></manifest:manifest>`, 1)
		}
		return body
	})
	r, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	var entries []zipEntry
	for _, f := range r.File {
		rd, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(rd)
		_ = rd.Close()
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, zipEntry{f.Name, string(body), 0})
	}
	entries = append(entries, zipEntry{"Pictures/preview.png", string(mustReadCorpus(t, "image.png")), 0})
	if got := inspectFixture(t, "document.odt", "", archiveFixture(t, entries), allProfiles); got.Decision != Allow {
		t.Fatalf("packaged image: %+v", got)
	}
}
