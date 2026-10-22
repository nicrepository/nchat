package service_test

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/filepolicy"
)

func workerOfficePackage(t *testing.T) []byte {
	t.Helper()
	original := enterpriseCorpus(t, "document.docx")
	r, err := zip.NewReader(bytes.NewReader(original), int64(len(original)))
	if err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	w := zip.NewWriter(&buffer)
	for _, f := range r.File {
		source, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(io.LimitReader(source, filepolicy.MaxTextBytes+1))
		_ = source.Close()
		if readErr != nil || int64(len(body)) > filepolicy.MaxTextBytes {
			t.Fatalf("bounded fixture read: %v", readErr)
		}
		out, err := w.Create(f.Name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = out.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	out, err := w.Create("word/_rels/extra.xml.RELS")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = out.Write([]byte(`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="r1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/attachedTemplate" Target="document.xml"/></Relationships>`)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestWorkerRevalidatesRelationshipPolicyBeforeScanner(t *testing.T) {
	for _, tt := range []struct {
		name   string
		body   []byte
		status domain.Status
		scans  int
	}{
		{"passive", enterpriseCorpus(t, "document.docx"), domain.StatusClean, 1},
		{"uppercase relationship", workerOfficePackage(t), domain.StatusRejected, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newScanFixture(t)
			job := f.storeAttachment(t, tt.body)
			f.queue.rows[0].job.Filename = "document.docx"
			f.queue.rows[0].job.PolicyVersion = filepolicy.Version
			f.service.SetFormatProfiles(filepolicy.Profiles{Office: true})
			if _, err := f.service.ProcessDue(context.Background()); err != nil {
				t.Fatal(err)
			}
			status, _ := f.queue.statusOf(job.AttachmentID)
			if status != tt.status || f.scanner.callCount() != tt.scans {
				t.Fatalf("worker: %s/%d; want %s/%d", status, f.scanner.callCount(), tt.status, tt.scans)
			}
		})
	}
}

func TestWorkerRevalidatesXMLReferencesBeforeScanner(t *testing.T) {
	f := newScanFixture(t)
	// Standalone XML has no package resource namespace; an absolute URI cannot
	// be approved even when the scanner would return a negative malware verdict.
	data := []byte(`<root xmlns:link="http://www.w3.org/1999/xlink"><image link:href="%2e%2e/private"/></root>`)
	job := f.storeAttachment(t, data)
	f.queue.rows[0].job.Filename = "data.xml"
	f.queue.rows[0].job.PolicyVersion = filepolicy.Version
	f.service.SetFormatProfiles(filepolicy.Profiles{Data: true})
	if _, err := f.service.ProcessDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, _ := f.queue.statusOf(job.AttachmentID)
	if status != domain.StatusRejected || f.scanner.callCount() != 0 {
		t.Fatalf("external reference: %s/%d", status, f.scanner.callCount())
	}
}
