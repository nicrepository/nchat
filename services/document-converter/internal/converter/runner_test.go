package converter

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeScript writes a fake `soffice` script and makes it executable: 0600 for
// the write itself (gosec G306 flags anything more permissive there) and a
// separate os.Chmod for the exec bit the script actually needs, rather than a
// single 0700 os.WriteFile.
func writeScript(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	// r-x only (no write bit) — just enough for exec.CommandContext to run it.
	// gosec's G302 forbids any Chmod call that sets an execute bit at all,
	// which a fake soffice script fundamentally needs to be runnable; this is
	// test-only code operating solely inside t.TempDir(), cleaned up by the
	// test framework and never exposed to a real caller.
	if err := os.Chmod(path, 0o500); err != nil { // #nosec G302 -- the script must be executable to run at all; see comment above
		t.Fatal(err)
	}
}

func newTestRunner(t *testing.T, command, work string, timeout time.Duration) *LibreOfficeRunner {
	t.Helper()
	runner, err := NewLibreOfficeRunner(command, work, timeout)
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func TestNewLibreOfficeRunnerRejectsAnUnresolvableCommand(t *testing.T) {
	_, err := NewLibreOfficeRunner("this-command-does-not-exist-anywhere", t.TempDir(), time.Second)
	if err == nil {
		t.Fatal("want an error for a command that cannot be resolved via exec.LookPath")
	}
}

func TestLibreOfficeRunnerReturnsPDFAndCleansRequestDirectory(t *testing.T) {
	root := t.TempDir()
	command := filepath.Join(root, "soffice")
	script := "#!/bin/sh\nout=''\nwhile [ $# -gt 0 ]; do\n  if [ \"$1\" = '--outdir' ]; then shift; out=$1; fi\n  shift\ndone\nprintf '%%PDF-1.7\\n%%%%EOF' > \"$out/input.pdf\"\n"
	writeScript(t, command, script)
	work := filepath.Join(root, "work")
	runner := newTestRunner(t, command, work, 2*time.Second)
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
	writeScript(t, command, "#!/bin/sh\nsleep 5\n")
	work := filepath.Join(root, "work")
	runner := newTestRunner(t, command, work, 20*time.Millisecond)
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

func TestLibreOfficeRunnerReturnsConversionFailedForANonZeroExit(t *testing.T) {
	root := t.TempDir()
	command := filepath.Join(root, "soffice")
	writeScript(t, command, "#!/bin/sh\nexit 3\n")
	work := filepath.Join(root, "work")
	runner := newTestRunner(t, command, work, 2*time.Second)
	_, err := runner.Convert(context.Background(), FormatDOCX, zipDocument(t, map[string]string{"word/document.xml": "x"}))
	if !errors.Is(err, ErrConversionFailed) {
		t.Fatalf("error = %v, want ErrConversionFailed", err)
	}
}

func TestLibreOfficeRunnerReturnsConversionFailedWhenNoOutputIsProduced(t *testing.T) {
	root := t.TempDir()
	command := filepath.Join(root, "soffice")
	// Exits 0 without ever writing output/input.pdf — the runner's os.Open of
	// the expected output must fail cleanly rather than panic or hang.
	writeScript(t, command, "#!/bin/sh\nexit 0\n")
	work := filepath.Join(root, "work")
	runner := newTestRunner(t, command, work, 2*time.Second)
	_, err := runner.Convert(context.Background(), FormatDOCX, zipDocument(t, map[string]string{"word/document.xml": "x"}))
	if !errors.Is(err, ErrConversionFailed) {
		t.Fatalf("error = %v, want ErrConversionFailed", err)
	}
}

func TestLibreOfficeRunnerReturnsOutputTooLargeOverTheCap(t *testing.T) {
	root := t.TempDir()
	command := filepath.Join(root, "soffice")
	// MaxOutputBytes is 50<<20 (50MiB); 51 * 1MiB blocks of zeros comfortably
	// exceeds it while staying cheap to generate in a test.
	script := "#!/bin/sh\nout=''\nwhile [ $# -gt 0 ]; do\n  if [ \"$1\" = '--outdir' ]; then shift; out=$1; fi\n  shift\ndone\ndd if=/dev/zero of=\"$out/input.pdf\" bs=1M count=51 2>/dev/null\n"
	writeScript(t, command, script)
	work := filepath.Join(root, "work")
	runner := newTestRunner(t, command, work, 10*time.Second)
	_, err := runner.Convert(context.Background(), FormatDOCX, zipDocument(t, map[string]string{"word/document.xml": "x"}))
	if !errors.Is(err, ErrOutputTooLarge) {
		t.Fatalf("error = %v, want ErrOutputTooLarge", err)
	}
}

func TestLibreOfficeRunnerHonoursAnAlreadyCanceledContext(t *testing.T) {
	root := t.TempDir()
	command := filepath.Join(root, "soffice")
	writeScript(t, command, "#!/bin/sh\nsleep 5\n")
	work := filepath.Join(root, "work")
	runner := newTestRunner(t, command, work, 2*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := runner.Convert(ctx, FormatDOCX, zipDocument(t, map[string]string{"word/document.xml": "x"}))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}
