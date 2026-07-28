package storage_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/storage"
)

func newTestStore(t *testing.T, handler http.Handler) (*storage.SeaweedFSStore, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	store, err := storage.NewSeaweedFSStore(server.URL, 5*time.Second)
	if err != nil {
		t.Fatalf("build store: %v", err)
	}
	return store, server
}

func TestNewSeaweedFSStoreRejectsUnusableEndpoints(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		timeout  time.Duration
	}{
		{name: "empty", endpoint: "", timeout: time.Second},
		{name: "no scheme", endpoint: "filer:8888", timeout: time.Second},
		{name: "file scheme", endpoint: "file:///etc", timeout: time.Second},
		//nolint:gosec // G101: inert fixture asserting that a credentialed endpoint is refused.
		{name: "credentials", endpoint: "http://u:p@filer:8888", timeout: time.Second},
		{name: "query", endpoint: "http://filer:8888?a=b", timeout: time.Second},
		{name: "fragment", endpoint: "http://filer:8888#f", timeout: time.Second},
		{name: "control character", endpoint: "http://filer:8888\x7f", timeout: time.Second},
		{name: "zero timeout", endpoint: "http://filer:8888", timeout: 0},
		{name: "negative timeout", endpoint: "http://filer:8888", timeout: -time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := storage.NewSeaweedFSStore(tt.endpoint, tt.timeout); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestPutSendsAMultipartRequestToTheDerivedPath(t *testing.T) {
	var (
		gotPath   string
		gotMethod string
		gotBody   []byte
		gotField  string
	)
	store, _ := newTestStore(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "multipart/form-data" {
			t.Errorf("expected multipart/form-data, got %q", r.Header.Get("Content-Type"))
			return
		}
		reader := multipart.NewReader(r.Body, params["boundary"])
		part, err := reader.NextPart()
		if err != nil {
			t.Errorf("read part: %v", err)
			return
		}
		gotField = part.FormName()
		gotBody, _ = io.ReadAll(part)
		w.WriteHeader(http.StatusCreated)
	}))

	payload := bytes.Repeat([]byte("ciphertext"), 1000)
	written, err := store.Put(context.Background(), testStorageObject, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if written != int64(len(payload)) {
		t.Fatalf("expected %d bytes written, got %d", len(payload), written)
	}
	if gotMethod != http.MethodPost || gotPath != "/"+testStorageObject {
		t.Fatalf("unexpected request %s %s", gotMethod, gotPath)
	}
	if gotField != "file" || !bytes.Equal(gotBody, payload) {
		t.Fatalf("unexpected part %q of %d bytes", gotField, len(gotBody))
	}
}

func TestPutRejectsNonSuccessStatuses(t *testing.T) {
	store, _ := newTestStore(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		http.Error(w, strings.Repeat("secret internal detail ", 100), http.StatusInternalServerError)
	}))

	_, err := store.Put(context.Background(), testStorageObject, strings.NewReader("data"))
	if !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
	if strings.Contains(err.Error(), "secret internal detail") {
		t.Fatal("the storage error body must never reach the returned error")
	}
}

func TestPutPropagatesSourceFailures(t *testing.T) {
	store, _ := newTestStore(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusCreated)
	}))
	_, err := store.Put(context.Background(), testStorageObject,
		&errReader{err: errors.New("stream broke")})
	if err == nil {
		t.Fatal("expected an error when the source fails mid-stream")
	}
}

func TestOpenStreamsTheStoredObject(t *testing.T) {
	store, _ := newTestStore(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		_, _ = w.Write([]byte("stored-ciphertext"))
	}))

	body, err := store.Open(context.Background(), testStorageObject)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = body.Close() }()
	content, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(content) != "stored-ciphertext" {
		t.Fatalf("unexpected content %q", content)
	}
}

func TestOpenMapsMissingObjectsToNotFound(t *testing.T) {
	store, _ := newTestStore(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	if _, err := store.Open(context.Background(), testStorageObject); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestOpenRejectsUnexpectedStatuses(t *testing.T) {
	store, _ := newTestStore(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	if _, err := store.Open(context.Background(), testStorageObject); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}

func TestDeleteIsIdempotent(t *testing.T) {
	var method atomic.Value
	store, _ := newTestStore(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method.Store(r.Method)
		w.WriteHeader(http.StatusNotFound)
	}))
	if err := store.Delete(context.Background(), testStorageObject); err != nil {
		t.Fatalf("deleting an absent object must succeed, got %v", err)
	}
	if method.Load() != http.MethodDelete {
		t.Fatalf("expected DELETE, got %v", method.Load())
	}
}

func TestDeleteReportsUnexpectedStatuses(t *testing.T) {
	store, _ := newTestStore(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	if err := store.Delete(context.Background(), testStorageObject); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}

func TestPingAcceptsAnyNonServerError(t *testing.T) {
	store, _ := newTestStore(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			t.Errorf("expected the endpoint root, got %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	if err := store.Ping(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPingFailsOnServerErrors(t *testing.T) {
	store, _ := newTestStore(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	if err := store.Ping(context.Background()); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}

// A redirect is the SSRF shape this client must refuse: following it would let
// whatever answers the configured endpoint retarget the request anywhere.
func TestRedirectsAreRefused(t *testing.T) {
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("the client must not follow a redirect")
		w.WriteHeader(http.StatusOK)
	}))
	defer elsewhere.Close()

	store, _ := newTestStore(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		http.Redirect(w, r, elsewhere.URL+"/anything", http.StatusFound)
	}))

	if _, err := store.Open(context.Background(), testStorageObject); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable for a redirected read, got %v", err)
	}
	if _, err := store.Put(context.Background(), testStorageObject, strings.NewReader("x")); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable for a redirected write, got %v", err)
	}
	if err := store.Delete(context.Background(), testStorageObject); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable for a redirected delete, got %v", err)
	}
	if err := store.Ping(context.Background()); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable for a redirected ping, got %v", err)
	}
}

func TestContextCancellationIsHonoured(t *testing.T) {
	release := make(chan struct{})
	store, _ := newTestStore(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(func() { close(release) })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := store.Open(ctx, testStorageObject); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if _, err := store.Put(ctx, testStorageObject, strings.NewReader("x")); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if err := store.Delete(ctx, testStorageObject); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if err := store.Ping(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestTimeoutsAreReportedAsUnavailable(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	defer close(release)

	store, err := storage.NewSeaweedFSStore(server.URL, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("build store: %v", err)
	}
	if _, err := store.Open(context.Background(), testStorageObject); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable on timeout, got %v", err)
	}
}

// The key is always server-derived, but the client validates it anyway so a
// future caller cannot turn it into a path, a scheme or a traversal.
func TestObjectKeysAreValidated(t *testing.T) {
	store, _ := newTestStore(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("an invalid key must never reach the network")
		w.WriteHeader(http.StatusOK)
	}))

	keys := []string{
		"",
		"../../etc/passwd",
		"nchat/../secret",
		"nchat/attachments/../../x",
		"http://evil.example/x",
		"nchat attachments/x",
		"NCHAT/Attachments/x",
		"nchat/attachments/x?y=z",
		"nchat/attachments/" + strings.Repeat("a", 200),
		"/leading-slash",
	}
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			if _, err := store.Put(context.Background(), key, strings.NewReader("x")); !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("expected ErrInvalidInput, got %v", err)
			}
			if _, err := store.Open(context.Background(), key); !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("expected ErrInvalidInput, got %v", err)
			}
			if err := store.Delete(context.Background(), key); !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("expected ErrInvalidInput, got %v", err)
			}
		})
	}
}

func TestNilStoreFailsClosed(t *testing.T) {
	var store *storage.SeaweedFSStore
	if _, err := store.Put(context.Background(), testStorageObject, strings.NewReader("x")); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}

type errReader struct{ err error }

func (r *errReader) Read([]byte) (int, error) { return 0, r.err }
