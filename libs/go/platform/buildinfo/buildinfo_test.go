package buildinfo

import "testing"

func TestCurrentReturnsBuildVariables(t *testing.T) {
	originalVersion := Version
	originalCommit := Commit
	t.Cleanup(func() {
		Version = originalVersion
		Commit = originalCommit
	})

	Version = "1.2.3"
	Commit = "abc123"

	info := Current()

	if info.Version != "1.2.3" {
		t.Fatalf("expected version 1.2.3, got %q", info.Version)
	}
	if info.Commit != "abc123" {
		t.Fatalf("expected commit abc123, got %q", info.Commit)
	}
}

func TestCurrentDefaultsEmptyBuildVariables(t *testing.T) {
	originalVersion := Version
	originalCommit := Commit
	t.Cleanup(func() {
		Version = originalVersion
		Commit = originalCommit
	})

	Version = ""
	Commit = ""

	info := Current()

	if info.Version != "0.0.0" {
		t.Fatalf("expected default version 0.0.0, got %q", info.Version)
	}
	if info.Commit != "dev" {
		t.Fatalf("expected default commit dev, got %q", info.Commit)
	}
}
