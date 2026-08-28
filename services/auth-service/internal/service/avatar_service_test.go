package service_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/service"
)

// fakeObjectStore records puts and deletes so tests can assert cleanup ordering.
type fakeObjectStore struct {
	files    map[string][]byte
	putName  string
	putErr   error
	deleted  []string
	putCalls int
}

func newFakeObjectStore() *fakeObjectStore {
	return &fakeObjectStore{files: map[string][]byte{}}
}

func (f *fakeObjectStore) Put(data []byte) (string, error) {
	f.putCalls++
	if f.putErr != nil {
		return "", f.putErr
	}
	name := f.putName
	if name == "" {
		name = "aaaaaaaaaaaaaaaa.png"
	}
	f.files[name] = data
	return name, nil
}

func (f *fakeObjectStore) Delete(name string) error {
	f.deleted = append(f.deleted, name)
	delete(f.files, name)
	return nil
}

// fakeAvatarUserStore records avatar_url swaps.
type fakeAvatarUserStore struct {
	current  string
	setErr   error
	clearErr error
	setURL   string
	cleared  bool
}

func (f *fakeAvatarUserStore) SetAvatarURL(_ context.Context, _, url string) (string, error) {
	if f.setErr != nil {
		return "", f.setErr
	}
	prev := f.current
	f.current = url
	f.setURL = url
	return prev, nil
}

func (f *fakeAvatarUserStore) ClearAvatarURL(_ context.Context, _ string) (string, error) {
	if f.clearErr != nil {
		return "", f.clearErr
	}
	prev := f.current
	f.current = ""
	f.cleared = true
	return prev, nil
}

func validPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	img.Set(0, 0, color.RGBA{R: 1, G: 2, B: 3, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return buf.Bytes()
}

const avatarBase = "/api/auth/avatars"

func TestAvatarService_Upload_PersistsSameOriginURL(t *testing.T) {
	obj := newFakeObjectStore()
	obj.putName = "1111111111111111.png"
	users := &fakeAvatarUserStore{}
	svc := service.NewAvatarService(obj, users, avatarBase)

	url, err := svc.Upload(context.Background(), "user-1", bytes.NewReader(validPNG(t)))
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	want := avatarBase + "/1111111111111111.png"
	if url != want {
		t.Fatalf("expected %q, got %q", want, url)
	}
	if users.setURL != want {
		t.Fatalf("db not updated with url, got %q", users.setURL)
	}
}

func TestAvatarService_Upload_RejectsBadImageWithoutTouchingStores(t *testing.T) {
	obj := newFakeObjectStore()
	users := &fakeAvatarUserStore{}
	svc := service.NewAvatarService(obj, users, avatarBase)

	_, err := svc.Upload(context.Background(), "user-1", bytes.NewReader([]byte("not an image")))
	if !service.IsAvatarUnsupported(err) {
		t.Fatalf("expected unsupported, got %v", err)
	}
	if obj.putCalls != 0 || users.setURL != "" {
		t.Fatal("a rejected image must not write a file or update the db")
	}
}

func TestAvatarService_Upload_CompensatesWhenDBFails(t *testing.T) {
	obj := newFakeObjectStore()
	obj.putName = "2222222222222222.png"
	users := &fakeAvatarUserStore{setErr: errors.New("db down")}
	svc := service.NewAvatarService(obj, users, avatarBase)

	_, err := svc.Upload(context.Background(), "user-1", bytes.NewReader(validPNG(t)))
	if err == nil {
		t.Fatal("expected error")
	}
	// The just-written file must be removed so a failed upload leaves nothing.
	if len(obj.deleted) != 1 || obj.deleted[0] != "2222222222222222.png" {
		t.Fatalf("expected compensating delete, got %v", obj.deleted)
	}
}

func TestAvatarService_Upload_DeletesReplacedFileAfterCommit(t *testing.T) {
	obj := newFakeObjectStore()
	obj.putName = "3333333333333333.png"
	users := &fakeAvatarUserStore{current: avatarBase + "/0000000000000000.png"}
	svc := service.NewAvatarService(obj, users, avatarBase)

	if _, err := svc.Upload(context.Background(), "user-1", bytes.NewReader(validPNG(t))); err != nil {
		t.Fatalf("upload: %v", err)
	}
	if len(obj.deleted) != 1 || obj.deleted[0] != "0000000000000000.png" {
		t.Fatalf("expected old file deleted, got %v", obj.deleted)
	}
}

func TestAvatarService_Upload_IgnoresForeignPreviousURL(t *testing.T) {
	obj := newFakeObjectStore()
	obj.putName = "4444444444444444.png"
	// A previous value that this service did not produce must never be deleted.
	users := &fakeAvatarUserStore{current: "https://external.example/pic.png"}
	svc := service.NewAvatarService(obj, users, avatarBase)

	if _, err := svc.Upload(context.Background(), "user-1", bytes.NewReader(validPNG(t))); err != nil {
		t.Fatalf("upload: %v", err)
	}
	if len(obj.deleted) != 0 {
		t.Fatalf("foreign previous URL must not trigger a delete, got %v", obj.deleted)
	}
}

func TestAvatarService_Upload_PutErrorSurfaces(t *testing.T) {
	obj := newFakeObjectStore()
	obj.putErr = errors.New("disk full")
	users := &fakeAvatarUserStore{}
	svc := service.NewAvatarService(obj, users, avatarBase)
	if _, err := svc.Upload(context.Background(), "user-1", bytes.NewReader(validPNG(t))); err == nil {
		t.Fatal("expected put error to surface")
	}
	if users.setURL != "" {
		t.Fatal("db must not be updated when the file write fails")
	}
}

func TestAvatarService_Remove_ClearsAndDeletes(t *testing.T) {
	obj := newFakeObjectStore()
	users := &fakeAvatarUserStore{current: avatarBase + "/5555555555555555.png"}
	svc := service.NewAvatarService(obj, users, avatarBase)

	if err := svc.Remove(context.Background(), "user-1"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !users.cleared {
		t.Fatal("db avatar_url not cleared")
	}
	if len(obj.deleted) != 1 || obj.deleted[0] != "5555555555555555.png" {
		t.Fatalf("expected file deleted, got %v", obj.deleted)
	}
}

func TestAvatarService_Remove_IdempotentWithNoAvatar(t *testing.T) {
	obj := newFakeObjectStore()
	users := &fakeAvatarUserStore{current: ""}
	svc := service.NewAvatarService(obj, users, avatarBase)

	if err := svc.Remove(context.Background(), "user-1"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if len(obj.deleted) != 0 {
		t.Fatalf("no file should be deleted, got %v", obj.deleted)
	}
}

func TestAvatarService_Remove_PropagatesNotFound(t *testing.T) {
	obj := newFakeObjectStore()
	users := &fakeAvatarUserStore{clearErr: domain.ErrNotFound}
	svc := service.NewAvatarService(obj, users, avatarBase)

	if err := svc.Remove(context.Background(), "user-1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestAvatarService_PurgeForAnonymization_RemovesFileAndReference(t *testing.T) {
	obj := newFakeObjectStore()
	users := &fakeAvatarUserStore{current: avatarBase + "/6666666666666666.png"}
	svc := service.NewAvatarService(obj, users, avatarBase)

	if err := svc.PurgeForAnonymization(context.Background(), "user-1"); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if !users.cleared || len(obj.deleted) != 1 {
		t.Fatalf("anonymization purge must clear ref and delete file: cleared=%v deleted=%v", users.cleared, obj.deleted)
	}
}
