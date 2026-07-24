package storage_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/services/auth-service/internal/storage"
)

func newStore(t *testing.T) (*storage.FilesystemAvatarStore, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := storage.NewFilesystemAvatarStore(dir)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return s, dir
}

func TestFilesystemAvatarStore_PutOpenDeleteRoundTrip(t *testing.T) {
	s, dir := newStore(t)
	data := []byte("fake-png-bytes")

	name, err := s.Put(data)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if !strings.HasSuffix(name, ".png") || strings.ContainsAny(name, "/\\") {
		t.Fatalf("unexpected object name %q", name)
	}
	// File exists with restrictive perms.
	info, err := os.Stat(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("expected 0600, got %v", info.Mode().Perm())
	}

	rc, err := s.Open(name)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(got) != string(data) {
		t.Fatalf("round trip mismatch")
	}

	if err := s.Delete(name); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.Open(name); !errors.Is(err, storage.ErrAvatarObjectNotFound) {
		t.Fatalf("expected not-found after delete, got %v", err)
	}
}

func TestFilesystemAvatarStore_PutGeneratesUniqueOpaqueNames(t *testing.T) {
	s, _ := newStore(t)
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		name, err := s.Put([]byte("x"))
		if err != nil {
			t.Fatalf("put: %v", err)
		}
		if seen[name] {
			t.Fatalf("duplicate object name %q", name)
		}
		seen[name] = true
	}
}

func TestFilesystemAvatarStore_DeleteIsIdempotent(t *testing.T) {
	s, _ := newStore(t)
	if err := s.Delete("deadbeefdeadbeef.png"); err != nil {
		t.Fatalf("delete of absent file must succeed, got %v", err)
	}
}

func TestFilesystemAvatarStore_RejectsTraversalAndBadNames(t *testing.T) {
	s, dir := newStore(t)
	// Plant a secret outside/inside to prove it is never reachable.
	secret := filepath.Join(dir, "..", "secret.txt")
	_ = os.WriteFile(secret, []byte("top secret"), 0o600)
	t.Cleanup(func() { _ = os.Remove(secret) })

	for _, name := range []string{
		"../secret.txt",
		"..%2fsecret.txt",
		"/etc/passwd",
		"nested/dir/a.png",
		"a.png/../b.png",
		"..\\secret.txt",
		"a.svg",
		"a.png.exe",
		"short.png",            // id too short
		"UPPERCASEHEX00.png",   // non-lowercase-hex
		"zzzzzzzzzzzzzzzz.png", // non-hex chars
		"",
		".png",
	} {
		if _, err := s.Open(name); !errors.Is(err, storage.ErrAvatarObjectNotFound) {
			t.Fatalf("name %q must resolve to not-found, got %v", name, err)
		}
		if err := s.Delete(name); err != nil {
			t.Fatalf("delete of invalid name %q must be a no-op, got %v", name, err)
		}
	}
	// The planted secret is untouched.
	if _, err := os.Stat(secret); err != nil {
		t.Fatalf("secret file was affected: %v", err)
	}
}

func TestNewFilesystemAvatarStore_RejectsEmptyDir(t *testing.T) {
	if _, err := storage.NewFilesystemAvatarStore(""); err == nil {
		t.Fatal("empty dir must be rejected")
	}
}

func TestFilesystemAvatarStore_PutFailsWhenDirIsUnwritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	dir := t.TempDir()
	s, err := storage.NewFilesystemAvatarStore(dir)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	// Make the directory non-writable so the temp-file create fails. Directory
	// modes legitimately carry the execute/traverse bit, so gosec's file-mode
	// rule does not apply here.
	if err := os.Chmod(dir, 0o500); err != nil { //nolint:gosec // directory mode, not a file
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) //nolint:gosec // directory mode, not a file
	if _, err := s.Put([]byte("x")); err == nil {
		t.Fatal("expected put to fail on an unwritable directory")
	}
}

func TestFilesystemAvatarStore_OpenValidButMissingReturnsNotFound(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.Open("deadbeefdeadbeef.png"); !errors.Is(err, storage.ErrAvatarObjectNotFound) {
		t.Fatalf("expected not-found for a well-formed but absent name, got %v", err)
	}
}

func TestFilesystemAvatarStore_CreatesDirWithRestrictivePerms(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "avatars")
	if _, err := storage.NewFilesystemAvatarStore(dir); err != nil {
		t.Fatalf("new store: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("expected 0700, got %v", info.Mode().Perm())
	}
}
