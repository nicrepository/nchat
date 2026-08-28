package storage

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// avatarStoredExtension is the only extension the store writes and serves.
const avatarStoredExtension = ".png"

// ErrAvatarObjectNotFound is returned by Open when the requested file is absent.
var ErrAvatarObjectNotFound = errors.New("avatar object not found")

// FilesystemAvatarStore persists avatar images as opaque-named files in a single
// fixed directory. It is the only component that touches the avatar filesystem,
// so every path is built from a server-generated id — a client-supplied name
// never reaches the disk.
type FilesystemAvatarStore struct {
	dir string
}

// NewFilesystemAvatarStore creates the backing directory (0700) if needed and
// returns a store rooted at it. The directory is expected to sit on a persistent
// volume in production; see the auth-service deployment manifest.
func NewFilesystemAvatarStore(dir string) (*FilesystemAvatarStore, error) {
	clean := filepath.Clean(dir)
	if clean == "" || clean == "." {
		return nil, fmt.Errorf("avatar store: empty directory")
	}
	if err := os.MkdirAll(clean, 0o700); err != nil {
		return nil, fmt.Errorf("avatar store: create dir: %w", err)
	}
	return &FilesystemAvatarStore{dir: clean}, nil
}

// Put writes data under a fresh opaque object name and returns that name
// (e.g. "3f6b…c1e.png"). The write is atomic: bytes land in a temp file that is
// fsync'd and renamed into place, so a crash mid-write never leaves a partial
// avatar to be served. Permissions are 0600.
func (s *FilesystemAvatarStore) Put(data []byte) (string, error) {
	id, err := randomObjectID()
	if err != nil {
		return "", err
	}
	name := id + avatarStoredExtension
	finalPath := filepath.Join(s.dir, name)

	tmp, err := os.CreateTemp(s.dir, "."+id+"-*.tmp")
	if err != nil {
		return "", fmt.Errorf("avatar store: temp file: %w", err)
	}
	tmpPath := tmp.Name()
	// Best-effort cleanup if we bail before the rename.
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("avatar store: chmod: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("avatar store: write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("avatar store: sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("avatar store: close: %w", err)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return "", fmt.Errorf("avatar store: rename: %w", err)
	}
	committed = true
	return name, nil
}

// Delete removes the object named name. A missing file is not an error, so
// removal is idempotent and safe to run after the DB reference is already gone.
func (s *FilesystemAvatarStore) Delete(name string) error {
	path, ok := s.resolve(name)
	if !ok {
		// An unparseable name cannot correspond to anything we wrote; treat it
		// as already absent rather than surfacing a path error.
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("avatar store: delete: %w", err)
	}
	return nil
}

// Open returns a reader for the named object. It rejects any name that is not a
// bare "<hex>.png" living directly in the store dir, so path traversal
// (../, absolute paths, nested dirs) can never escape the directory.
func (s *FilesystemAvatarStore) Open(name string) (io.ReadCloser, error) {
	path, ok := s.resolve(name)
	if !ok {
		return nil, ErrAvatarObjectNotFound
	}
	f, err := os.Open(path) //nolint:gosec // path is validated to be a bare object name inside s.dir
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrAvatarObjectNotFound
		}
		return nil, fmt.Errorf("avatar store: open: %w", err)
	}
	return f, nil
}

// resolve validates an object name and maps it to an absolute path inside the
// store directory. It returns ok=false for anything that is not a plain
// hex-id + ".png", which is exactly the shape Put produces.
func (s *FilesystemAvatarStore) resolve(name string) (string, bool) {
	if !validAvatarObjectName(name) {
		return "", false
	}
	return filepath.Join(s.dir, name), true
}

// validAvatarObjectName accepts only "<lowercase-hex>.png" with a non-empty,
// bounded id and no path separators of any kind.
func validAvatarObjectName(name string) bool {
	if !strings.HasSuffix(name, avatarStoredExtension) {
		return false
	}
	id := strings.TrimSuffix(name, avatarStoredExtension)
	if len(id) < 16 || len(id) > 64 {
		return false
	}
	for _, r := range id {
		isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')
		if !isHex {
			return false
		}
	}
	return true
}

// randomObjectID returns 128 bits of hex — an unguessable capability that also
// serves as the public URL segment, so avatar URLs are not enumerable.
func randomObjectID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("avatar store: random id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
