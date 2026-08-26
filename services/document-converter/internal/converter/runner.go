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

func NewLibreOfficeRunner(command, workDir string, timeout time.Duration) *LibreOfficeRunner {
	return &LibreOfficeRunner{command: command, workDir: workDir, timeout: timeout}
}

func (r *LibreOfficeRunner) Convert(ctx context.Context, format Format, document []byte) ([]byte, error) {
	if err := os.MkdirAll(r.workDir, 0o700); err != nil {
		return nil, fmt.Errorf("%w: create work root", ErrConversionFailed)
	}
	requestDir, err := os.MkdirTemp(r.workDir, "request-")
	if err != nil {
		return nil, fmt.Errorf("%w: create request directory", ErrConversionFailed)
	}
	defer func() { _ = os.RemoveAll(requestDir) }()

	inputPath := filepath.Join(requestDir, "input."+string(format))
	outputDir := filepath.Join(requestDir, "output")
	profileDir := filepath.Join(requestDir, "profile")
	if err := os.Mkdir(outputDir, 0o700); err != nil {
		return nil, fmt.Errorf("%w: create output directory", ErrConversionFailed)
	}
	if err := os.Mkdir(profileDir, 0o700); err != nil {
		return nil, fmt.Errorf("%w: create profile directory", ErrConversionFailed)
	}
	if err := os.WriteFile(inputPath, document, 0o600); err != nil {
		return nil, fmt.Errorf("%w: write input", ErrConversionFailed)
	}

	commandCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, r.command,
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

	file, err := os.Open(filepath.Join(outputDir, "input.pdf"))
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
