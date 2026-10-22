package filepolicy

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var allProfiles = Profiles{Data: true, Office: true, Archives: true}

func TestSyntheticNativeCorpus(t *testing.T) {
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() == "README.md" {
			continue
		}
		t.Run(entry.Name(), func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join("testdata", entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			r := inspectFixture(t, entry.Name(), "application/octet-stream", b, allProfiles)
			if r.Decision != Allow {
				t.Fatalf("native synthetic corpus: %+v", r)
			}
		})
	}
}

type zipEntry struct {
	name, body string
	mode       fs.FileMode
}

func archiveFixture(t testing.TB, entries []zipEntry) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for _, entry := range entries {
		h := &zip.FileHeader{Name: entry.name, Method: zip.Store}
		if entry.mode != 0 {
			h.SetMode(entry.mode)
		}
		r, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = io.WriteString(r, entry.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func officeFixture(t testing.TB, id string) []byte {
	t.Helper()
	return mustReadCorpus(t, "document."+id)
}

func inspectFixture(t testing.TB, name, declared string, b []byte, p Profiles) Result {
	t.Helper()
	r, err := Inspect(context.Background(), bytes.NewReader(b), int64(len(b)), name, declared, p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestClassificationUsesContentAndStructure(t *testing.T) {
	zipBytes := archiveFixture(t, []zipEntry{{"notes.txt", "business notes", 0}})
	cases := []struct {
		name, mime, body string
		decision         Decision
		code             Code
	}{
		{"notes.TXT", "application/octet-stream", "ação e custos\n", Allow, Recognized},
		{"invoice.pdf.exe", "application/pdf", "%PDF-1.7\n%%EOF", Reject, Active},
		{"invoice.pdf", "application/pdf", "MZexecutable", Reject, Active},
		{"invoice.pdf", "application/pdf", "not a pdf", Reject, Invalid},
		{"invoice.pdf", "application/pdf", "%PDF-1.7\ntruncated", Reject, Invalid},
		{"invoice.pdf", "application/octet-stream", "%PDF-1.7\n%%EOF", Allow, Recognized},
		{"invoice.pdf", "application/pdf", "%PDF-1.7\n/Encrypt true\n%%EOF", Reject, Active},
		{"notes.txt", "text/plain", "#!/bin/sh\ncommands", Reject, Active},
		{"notes.txt", "text/plain", "<script>alert(1)</script>", Reject, Invalid},
		{"notes.txt", "text/plain", "\x00\x01binary", Reject, Invalid},
		{"data.json", "text/plain", `{"cost":12}`, Allow, Recognized},
		{"data.json", "application/json", `{broken}`, Reject, Invalid},
		{"data.xml", "text/xml", `<?xml version="1.0"?><cost value="12"/>`, Allow, Recognized},
		{"document.xml", "application/xml", `<!DOCTYPE x [<!ENTITY e SYSTEM "http://127.0.0.1/">]><x>&e;</x>`, Reject, Active},
		{"document.xml", "application/xml", `<x xmlns:xi="http://www.w3.org/2001/XInclude"><xi:include href="file:///etc/passwd"/></x>`, Reject, Active},
		{"data.xml", "application/xml", `<x/><y/>`, Reject, Invalid},
		{"data.xml", "application/xml", `<x>`, Reject, Invalid},
		{"data.csv", "text/plain", "id,cost\n1,12\n", Allow, Recognized},
		{"data.csv", "text/csv", "just a sentence", Reject, Invalid},
		{"data.csv", "text/csv", "a,b\nc,d,e\n", Reject, Invalid},
		{"data.tsv", "text/plain", "id\tcost\n1\t12\n", Allow, Recognized},
		{"notes.md", "text/plain", "# Costs\nQuarterly report", Allow, Recognized},
		{"secret.pem", "text/plain", "private key", Reject, Active},
		{"script.svg", "image/svg+xml", "<svg/>", Reject, Active},
		{"macro.xlsm", "application/zip", string(zipBytes), Reject, Active},
		{"legacy.msg", "application/octet-stream", "\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1", Restricted, Opaque},
		{"report.xlsx", "application/zip", string(zipBytes), Reject, Invalid},
		{"arquivo.docx", "application/zip", "PK\x03\x04truncated", Reject, Invalid},
		{"mystery.sap", "application/octet-stream", "plain looking unknown data", Indeterminate, Unsupported},
	}
	for i, tt := range cases {
		t.Run(fmt.Sprintf("%02d/%s", i, tt.name), func(t *testing.T) {
			r := inspectFixture(t, tt.name, tt.mime, []byte(tt.body), allProfiles)
			if r.Decision != tt.decision || r.Code != tt.code {
				t.Fatalf("got %s/%s; want %s/%s", r.Decision, r.Code, tt.decision, tt.code)
			}
		})
	}
}

func TestOfficeContainersAndMIMEAliases(t *testing.T) {
	for _, id := range []string{"docx", "xlsx", "pptx"} {
		t.Run(id, func(t *testing.T) {
			b := officeFixture(t, id)
			r := inspectFixture(t, "Report."+strings.ToUpper(id), "application/octet-stream", b, allProfiles)
			if r.Decision != Allow || r.Format.ID != FormatID(id) || r.MIMEMismatch {
				t.Fatalf("classification: %+v", r)
			}
			r = inspectFixture(t, "Report."+id, "application/pdf", b, allProfiles)
			if r.Decision != Reject || r.Code != Mismatch || !r.MIMEMismatch {
				t.Fatalf("declaration must not authorize or misclassify: %+v", r)
			}
		})
	}
}

func TestProfileDefaultsDoNotOpenNewPermissions(t *testing.T) {
	for _, name := range []string{"data.json", "notes.md", "report.docx", "data.zip"} {
		r := inspectFixture(t, name, "", []byte("untrusted"), Profiles{})
		if r.Decision != Restricted || r.Code != Disabled {
			t.Fatalf("%s: %+v", name, r)
		}
	}
	if r := inspectFixture(t, "notes.txt", "", []byte("business notes"), Profiles{}); r.Decision != Allow {
		t.Fatalf("baseline text unexpectedly disabled: %+v", r)
	}
}

func TestArchiveGuards(t *testing.T) {
	cases := []struct {
		name    string
		entries []zipEntry
	}{
		{"traversal", []zipEntry{{"../notes.txt", "notes", 0}}},
		{"absolute", []zipEntry{{"/notes.txt", "notes", 0}}},
		{"backslash", []zipEntry{{"a\\notes.txt", "notes", 0}}},
		{"case collision", []zipEntry{{"notes.txt", "a", 0}, {"NOTES.txt", "b", 0}}},
		{"duplicate", []zipEntry{{"notes.txt", "a", 0}, {"notes.txt", "b", 0}}},
		{"symlink", []zipEntry{{"notes.txt", "target", fs.ModeSymlink | 0777}}},
		{"device", []zipEntry{{"notes.txt", "target", fs.ModeDevice | 0666}}},
		{"executable", []zipEntry{{"invoice.pdf", "MZpayload", 0}}},
		{"nested", []zipEntry{{"data.zip", "PK\x03\x04container", 0}}},
		{"unknown", []zipEntry{{"data.bin", "arbitrary content", 0}}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			r := inspectFixture(t, "data.zip", "application/zip", archiveFixture(t, tt.entries), allProfiles)
			if r.Decision == Allow {
				t.Fatal("unsafe container admitted")
			}
		})
	}
}

func TestEntryCountBoundary(t *testing.T) {
	entries := make([]zipEntry, MaxEntries+1)
	for i := range entries {
		entries[i] = zipEntry{fmt.Sprintf("note-%d.txt", i), "notes", 0}
	}
	if r := inspectFixture(t, "data.zip", "", archiveFixture(t, entries[:MaxEntries]), allProfiles); r.Decision != Allow {
		t.Fatalf("at limit: %+v", r)
	}
	if r := inspectFixture(t, "data.zip", "", archiveFixture(t, entries), allProfiles); r.Code != BudgetExceeded {
		t.Fatalf("above limit: %+v", r)
	}
}

func TestTextAndXMLBoundaries(t *testing.T) {
	for _, n := range []int{int(MaxTextBytes), int(MaxTextBytes) + 1} {
		r := inspectFixture(t, "data.json", "application/json", append(append([]byte(`"`), bytes.Repeat([]byte("a"), n-2)...), '"'), allProfiles)
		want := Allow
		if n > int(MaxTextBytes) {
			want = Indeterminate
		}
		if r.Decision != want {
			t.Fatalf("size %d: %+v", n, r)
		}
	}
	for _, depth := range []int{MaxXMLDepth, MaxXMLDepth + 1} {
		b := []byte(strings.Repeat("<x>", depth) + strings.Repeat("</x>", depth))
		r := inspectFixture(t, "data.xml", "", b, allProfiles)
		want := Allow
		if depth > MaxXMLDepth {
			want = Indeterminate
		}
		if r.Decision != want {
			t.Fatalf("depth %d: %+v", depth, r)
		}
	}
}

func TestCorruptionAndLocalCentralDisagreement(t *testing.T) {
	valid := archiveFixture(t, []zipEntry{{"notes.txt", "notes", 0}})
	for _, offset := range []int{0, 30, 39, len(valid) - 1} {
		t.Run(fmt.Sprint(offset), func(t *testing.T) {
			b := bytes.Clone(valid)
			b[offset] ^= 0xff
			if r := inspectFixture(t, "data.zip", "", b, allProfiles); r.Decision == Allow {
				t.Fatal("corrupted archive accepted")
			}
		})
	}
	if r := inspectFixture(t, "data.zip", "", valid[:len(valid)-4], allProfiles); r.Decision == Allow {
		t.Fatal("truncation accepted")
	}
}

func TestCancellationIsNotAnApproval(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, err := Inspect(ctx, bytes.NewReader([]byte("notes")), 5, "notes.txt", "", allProfiles)
	if err == nil || r.Decision == Allow {
		t.Fatalf("canceled inspection: %+v / %v", r, err)
	}
}

func FuzzInspect(f *testing.F) {
	f.Add("notes.txt", []byte("notes"))
	f.Add("report.xlsx", []byte("PK\x03\x04"))
	f.Add("data.xml", []byte("<x/>"))
	f.Fuzz(func(t *testing.T, name string, b []byte) {
		if len(b) > 64<<10 || len(name) > 512 {
			t.Skip()
		}
		r, err := Inspect(context.Background(), bytes.NewReader(b), int64(len(b)), name, "application/octet-stream", allProfiles)
		if err == nil && r.Decision == Allow && r.Version != Version {
			t.Fatal("unversioned approval")
		}
	})
}
