package preview

import (
	"context"
	"io"
	"os"
	"testing"
)

// The sandbox's containment is decided entirely by what this configuration
// leaves nil: go-pdfium mounts the host's root directory read-write and wires
// the process's stdout and stderr into the module for every field a caller does
// not set. These are therefore not style assertions — each one is a hole that
// opens the moment someone "simplifies" the struct literal.
func TestSandboxConfigGrantsTheModuleNoFilesystemAndNoOutput(t *testing.T) {
	config := sandboxConfig(context.Background())

	if config.FSConfig == nil {
		t.Fatal("a nil FSConfig makes go-pdfium mount the host root into the module")
	}
	if config.Stdout == nil || config.Stderr == nil {
		t.Fatal("nil Stdout/Stderr send the module's output to the process's streams")
	}
	if config.Stdout != io.Discard || config.Stderr != io.Discard {
		t.Fatal("the module's output must be discarded, not routed anywhere")
	}
	if config.Stdout == os.Stdout || config.Stderr == os.Stderr {
		t.Fatal("the module must never write to the process's own streams")
	}
	if config.RuntimeConfig == nil {
		t.Fatal("a nil RuntimeConfig drops the memory limit and the context close")
	}
	// One instance, so one render at a time per process: the memory ceiling is
	// a ceiling for the pod, not per concurrent document.
	if config.MaxTotal != 1 || config.MaxIdle != 1 {
		t.Fatalf("sandbox pool allows %d instances, want exactly one", config.MaxTotal)
	}
	if config.Context == nil {
		t.Fatal("the module's context is what makes the timeout able to close it")
	}
}
