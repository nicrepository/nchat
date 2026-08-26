package converter

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLibreOfficeRunnerReturnsPDFAndCleansRequestDirectory(t *testing.T) {
	root := t.TempDir()
	command := filepath.Join(root, "soffice")
	script := "#!/bin/sh\nout=''\nwhile [ $# -gt 0 ]; do\n  if [ \"$1\" = '--outdir' ]; then shift; out=$1; fi\n  shift\ndone\nprintf '%%PDF-1.7\\n%%%%EOF' > \"$out/input.pdf\"\n"
	if err := os.WriteFile(command, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "work")
	runner := NewLibreOfficeRunner(command, work, 2*time.Second)
	pdf, err := runner.Convert(context.Background(), FormatDOCX, zipDocument(t, map[string]string{"word/document.xml": "x"}))
	if err != nil || string(pdf) != "%PDF-1.7\n%%EOF" {
		t.Fatalf("Convert = %q, %v", pdf, err)
	}
	entries, err := os.ReadDir(work)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary request directories remain: %v", entries)
	}
}

func TestLibreOfficeRunnerKillsTimeoutAndCleansRequestDirectory(t *testing.T) {
	root := t.TempDir()
	command := filepath.Join(root, "soffice")
	if err := os.WriteFile(command, []byte("#!/bin/sh\nsleep 5\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "work")
	runner := NewLibreOfficeRunner(command, work, 20*time.Millisecond)
	started := time.Now()
	_, err := runner.Convert(context.Background(), FormatDOCX, []byte("document"))
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("error = %v, want timeout", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("timeout returned after %v; child process was not killed", elapsed)
	}
	entries, readErr := os.ReadDir(work)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary request directories remain: %v", entries)
	}
}
