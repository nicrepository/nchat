package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

// AvatarObjectStore is the blob half of avatar storage: opaque-named files that
// the service never lets the client name directly.
type AvatarObjectStore interface {
	Put(data []byte) (name string, err error)
	Delete(name string) error
}

// AvatarUserStore is the association half: the auth.users.avatar_url column.
type AvatarUserStore interface {
	SetAvatarURL(ctx context.Context, userID, url string) (previous string, err error)
	ClearAvatarURL(ctx context.Context, userID string) (previous string, err error)
}

// AvatarService owns the upload/replace/remove lifecycle and the consistency
// rules between the blob store and the database. auth-service — not the client
// and not another service — decides the persisted avatar_url value.
type AvatarService struct {
	objects AvatarObjectStore
	users   AvatarUserStore
	// baseURL is the same-origin, root-relative prefix under which avatars are
	// served, e.g. "/api/auth/avatars". The stored avatar_url is always
	// baseURL + "/" + objectName, so the client can never influence it.
	baseURL string
}

// NewAvatarService wires the blob store, the user store, and the public base URL.
func NewAvatarService(objects AvatarObjectStore, users AvatarUserStore, baseURL string) *AvatarService {
	return &AvatarService{objects: objects, users: users, baseURL: strings.TrimRight(baseURL, "/")}
}

// Upload validates and canonicalises the image, stores it, and points the user
// row at it. The ordering is deliberate for crash-consistency:
//
//  1. write the new file first — a crash here leaves an orphan file but no
//     dangling DB reference (an orphan is harmless and GC-able; a dangling
//     reference would render a broken avatar);
//  2. update the DB;
//  3. only after the DB commit, delete the file the row used to point at.
//
// If the DB update fails, the just-written file is removed so a failed upload
// leaves nothing behind. Atomicity across PostgreSQL and the filesystem is not
// achievable; this is the documented compensation.
func (s *AvatarService) Upload(ctx context.Context, userID string, r io.Reader) (string, error) {
	processed, err := ProcessAvatar(r)
	if err != nil {
		return "", err
	}

	name, err := s.objects.Put(processed)
	if err != nil {
		return "", fmt.Errorf("store avatar object: %w", err)
	}
	url := s.baseURL + "/" + name

	previous, err := s.users.SetAvatarURL(ctx, userID, url)
	if err != nil {
		// Compensate: the DB never took the reference, so drop the new file.
		_ = s.objects.Delete(name)
		return "", err
	}

	// The row no longer points at the previous file; removing it is secondary
	// cleanup and must not fail the request if it errors (it becomes an orphan
	// for a future sweep, never a broken avatar).
	if prevName := s.objectName(previous); prevName != "" && prevName != name {
		_ = s.objects.Delete(prevName)
	}
	return url, nil
}

// Remove clears the user's avatar and deletes the backing file. It is
// idempotent: a user with no avatar succeeds without touching the filesystem.
func (s *AvatarService) Remove(ctx context.Context, userID string) error {
	previous, err := s.users.ClearAvatarURL(ctx, userID)
	if err != nil {
		return err
	}
	if prevName := s.objectName(previous); prevName != "" {
		_ = s.objects.Delete(prevName)
	}
	return nil
}

// PurgeForAnonymization is the reusable hook the future anonymisation/hard-delete
// producer (RF-55/56) will call to strip a user's avatar: it clears the column
// and removes the file, tolerating an already-absent file. It is intentionally
// identical to Remove today but named for its call site so the integration point
// is explicit rather than incidental.
func (s *AvatarService) PurgeForAnonymization(ctx context.Context, userID string) error {
	return s.Remove(ctx, userID)
}

// objectName extracts the stored object name from a persisted avatar_url. It
// returns "" for anything that is not a URL this service produced (e.g. a
// legacy or externally-shaped value), so cleanup never deletes an unrelated
// file or misinterprets a foreign reference.
func (s *AvatarService) objectName(url string) string {
	if url == "" {
		return ""
	}
	prefix := s.baseURL + "/"
	if !strings.HasPrefix(url, prefix) {
		return ""
	}
	name := strings.TrimPrefix(url, prefix)
	if name == "" || strings.ContainsAny(name, "/\\") {
		return ""
	}
	return name
}

// IsAvatarTooLarge / IsAvatarUnsupported let handlers translate the sentinel
// processing errors to HTTP statuses without importing the image internals.
func IsAvatarTooLarge(err error) bool    { return errors.Is(err, domain.ErrAvatarTooLarge) }
func IsAvatarUnsupported(err error) bool { return errors.Is(err, domain.ErrAvatarUnsupported) }
