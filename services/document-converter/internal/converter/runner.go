package converter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

type LibreOfficeRunner struct {
	command string
	workDir string
	timeout time.Duration
}

// NewLibreOfficeRunner resolves command via exec.LookPath so a misconfigured
// or missing converter binary fails fast at startup rather than on the first
// request, and so the value stored on the runner is always a path this
// process has already verified is executable — never an unresolved string
// taken as-is from configuration.
func NewLibreOfficeRunner(command, workDir string, timeout time.Duration) (*LibreOfficeRunner, error) {
	resolved, err := exec.LookPath(command)
	if err != nil {
		return nil, fmt.Errorf("resolve converter command %q: %w", command, err)
	}
	return &LibreOfficeRunner{command: resolved, workDir: workDir, timeout: timeout}, nil
}

func (r *LibreOfficeRunner) Convert(ctx context.Context, format Format, document []byte) ([]byte, error) {
	// r.workDir is a fixed config value set once at process startup (see
	// main.go's CONVERTER_WORK_DIR), never derived from a request — gosec's
	// taint analysis flags this anyway because `document []byte` is in scope
	// for the rest of the function.
	if err := os.MkdirAll(r.workDir, 0o700); err != nil { // #nosec G703 -- r.workDir is a trusted startup config value, not request-derived
		return nil, fmt.Errorf("%w: create work root", ErrConversionFailed)
	}
	requestDir, err := os.MkdirTemp(r.workDir, "request-")
	if err != nil {
		return nil, fmt.Errorf("%w: create request directory", ErrConversionFailed)
	}
	defer func() {
		// requestDir is a path os.MkdirTemp itself generated under r.workDir,
		// never anything derived from the request body — gosec's taint analysis
		// marks every statement in this function as suspect once `document
		// []byte` (the request body) is in scope, regardless of whether a given
		// path actually flows from it.
		_ = os.RemoveAll(requestDir) // #nosec G304,G703 -- requestDir comes from os.MkdirTemp above, not from the request
	}()

	// Every file operation from here on is scoped to requestDir through os.Root,
	// which resolves each name relative to that directory and refuses anything
	// that would escape it (including via a symlink) — this is what actually
	// answers gosec's G304/G703 "potential file inclusion via variable" finding,
	// not just silences it: it is gosec's own suggested autofix for exactly this
	// pattern.
	requestRoot, err := os.OpenRoot(requestDir)
	if err != nil {
		return nil, fmt.Errorf("%w: open request directory", ErrConversionFailed)
	}
	defer func() { _ = requestRoot.Close() }()

	inputName := "input." + string(format)
	if err := requestRoot.Mkdir("output", 0o700); err != nil {
		return nil, fmt.Errorf("%w: create output directory", ErrConversionFailed)
	}
	if err := requestRoot.Mkdir("profile", 0o700); err != nil {
		return nil, fmt.Errorf("%w: create profile directory", ErrConversionFailed)
	}
	if err := requestRoot.WriteFile(inputName, document, 0o600); err != nil {
		return nil, fmt.Errorf("%w: write input", ErrConversionFailed)
	}

	// LibreOffice is an external process, not something os.Root can sandbox —
	// it needs real filesystem paths on argv, not root-relative file handles.
	// Every component below is either r.workDir (a fixed, trusted config value)
	// or a name this function generated itself (requestDir via os.MkdirTemp, or
	// the "output"/"profile"/inputName literals), never anything taken from the
	// request body.
	outputDir := filepath.Join(requestDir, "output")
	profileDir := filepath.Join(requestDir, "profile")
	inputPath := filepath.Join(requestDir, inputName)

	commandCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	// r.command is resolved and verified executable once at startup by
	// NewLibreOfficeRunner (never from a request), and every argument below is
	// a fixed flag or a path this function generated itself (see above) — never
	// anything taken from the request body.
	cmd := exec.CommandContext(commandCtx, r.command, // #nosec G204,G702 -- see comment above
		"--headless", "--nologo", "--nodefault", "--nolockcheck", "--norestore",
		"-env:UserInstallation=file://"+profileDir,
		"--convert-to", "pdf", "--outdir", outputDir, inputPath,
	)
	cmd.Dir = requestDir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = time.Second
	if output, err := cmd.CombinedOutput(); err != nil {
		if errors.Is(commandCtx.Err(), context.DeadlineExceeded) {
			return nil, ErrTimeout
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		_ = output // LibreOffice output may contain document data; never log it.
		return nil, ErrConversionFailed
	}

	file, err := requestRoot.Open("output/input.pdf")
	if err != nil {
		return nil, ErrConversionFailed
	}
	defer func() { _ = file.Close() }()
	pdf, err := io.ReadAll(io.LimitReader(file, MaxOutputBytes+1))
	if err != nil {
		return nil, ErrConversionFailed
	}
	if len(pdf) > MaxOutputBytes {
		return nil, ErrOutputTooLarge
	}
	return pdf, nil
}
