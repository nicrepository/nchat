package documentguard

import (
	"archive/zip"
	"bytes"
	"context"
	"testing"
)

func packageReader(t *testing.T, entries map[string]string) *zip.Reader {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for name, body := range entries {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := zip.NewReader(bytes.NewReader(b.Bytes()), int64(b.Len()))
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func TestArchiveInspectsAllPartsBeforeProcessing(t *testing.T) {
	valid := map[string]string{"mimetype": "application/vnd.oasis.opendocument.text", "content.xml": `<x xmlns:l="` + XLinkNS + `" l:href="Pictures/a.png"/>`, "Pictures/a.png": "image"}
	index, err := ArchiveXML(context.Background(), packageReader(t, valid))
	if err != nil || !index.Has("Pictures/a.png") || index.MIME != valid["mimetype"] {
		t.Fatalf("valid index: %+v/%v", index, err)
	}
	for _, entries := range []map[string]string{
		{"../escape": "x"}, {"/etc/passwd": "x"}, {`C:\absolute`: "x"}, {"a/../x": "x"}, {"a": "x", "A": "y"},
		{"word/embeddings/a": "x"}, {"word/vbaProject.bin": "x"}, {"Basic/macro.xml": "<x/>"},
		{"content.xml": "<malformed>"}, {"mimetype": string(bytes.Repeat([]byte("x"), 257))},
		{"word/document.xml": "<x/>", "_rels/.RELS": `<Relationships xmlns="` + RelationshipsNS + `"><Relationship Type="https://type.test/attachedTemplate" Target="word/document.xml"/></Relationships>`},
	} {
		if _, err := ArchiveXML(context.Background(), packageReader(t, entries)); err == nil {
			t.Fatalf("unsafe package: %v", entries)
		}
	}
}
func TestArchiveMetadataBudgets(t *testing.T) {
	for _, n := range []int{0, MaxEntries + 1} {
		if _, err := ArchiveXML(context.Background(), &zip.Reader{File: make([]*zip.File, n)}); err == nil {
			t.Fatalf("entry count %d", n)
		}
	}
	for _, size := range []uint64{MaxXMLBytes, MaxXMLBytes + 1} {
		f := &zip.File{FileHeader: zip.FileHeader{Name: "image.bin", UncompressedSize64: size, CompressedSize64: size}}
		var total uint64
		err := archiveEntry(f, &total)
		if (err == nil) != (size == MaxXMLBytes) {
			t.Fatalf("size %d: %v", size, err)
		}
	}
	f := &zip.File{FileHeader: zip.FileHeader{Name: "image.bin", UncompressedSize64: MaxExpansionRate, CompressedSize64: 1}}
	var total uint64
	if err := archiveEntry(f, &total); err != nil {
		t.Fatal(err)
	}
	f.UncompressedSize64 = MaxExpansionRate + 1
	if err := archiveEntry(f, &total); err == nil {
		t.Fatal("expansion ratio not bounded")
	}
	total = MaxExpandedBytes
	f.UncompressedSize64 = 1
	if err := archiveEntry(f, &total); err == nil {
		t.Fatal("total expansion not bounded")
	}
}
