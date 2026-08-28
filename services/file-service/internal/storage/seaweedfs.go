package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nicrepository/nchat/services/file-service/internal/domain"
)

// errRedirectNotAllowed refuses every redirect the storage endpoint might
// answer with. A redirect would let whatever is at the configured address
// retarget the request at an arbitrary host, which is the SSRF shape this
// service must not have. The filer serves object content itself, so no
// legitimate flow depends on following one; if a deployment ever needs it, the
// allowed targets have to be reviewed and pinned first.
var errRedirectNotAllowed = errors.New("storage endpoint attempted a redirect")

// objectKeyPattern bounds what may become a storage path. Keys are always
// derived from a server-generated UUID (domain.StorageObjectKey), so this is a
// second line of defence rather than input validation: nothing with a dot
// segment, a scheme, a query or a control character can reach the wire.
var objectKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9/_-]{0,127}$`)

// SeaweedFSStore talks to the SeaweedFS filer HTTP API — the access path this
// repository already validates in scripts/poc/seaweedfs-poc.sh and
// scripts/dev/dev-env-validate.sh, and the one infra/k8s/base/configmap.yaml
// configures as SEAWEEDFS_FILER_URL. No new protocol is introduced.
//
// The endpoint comes from configuration alone and is never influenced by a
// request; a caller only ever supplies an object key, which is validated
// against objectKeyPattern and joined onto the configured origin.
type SeaweedFSStore struct {
	endpoint *url.URL
	client   *http.Client
}

// NewSeaweedFSStore validates the configured endpoint and builds a client with
// an overall timeout and redirects disabled.
func NewSeaweedFSStore(rawEndpoint string, timeout time.Duration) (*SeaweedFSStore, error) {
	endpoint, err := url.Parse(strings.TrimSpace(rawEndpoint))
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") ||
		endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, errors.New("invalid SeaweedFS filer endpoint")
	}
	if timeout <= 0 {
		return nil, errors.New("SeaweedFS timeout must be positive")
	}
	return &SeaweedFSStore{
		endpoint: &url.URL{Scheme: endpoint.Scheme, Host: endpoint.Host},
		client: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return errRedirectNotAllowed
			},
		},
	}, nil
}

// Put streams body to the filer as a multipart upload and returns the number of
// bytes written. Nothing is buffered: the multipart envelope is produced into a
// pipe while the request is in flight, so a 50 MiB object costs one chunk of
// memory, not 50 MiB.
func (s *SeaweedFSStore) Put(ctx context.Context, key string, body io.Reader) (int64, error) {
	target, err := s.objectURL(key)
	if err != nil {
		return 0, err
	}

	pipeReader, pipeWriter := io.Pipe()
	writer := multipart.NewWriter(pipeWriter)
	var written atomic.Int64

	go func() {
		part, partErr := writer.CreateFormFile("file", path.Base(key))
		if partErr != nil {
			_ = pipeWriter.CloseWithError(partErr)
			return
		}
		n, copyErr := io.Copy(part, body)
		written.Store(n)
		if copyErr != nil {
			_ = pipeWriter.CloseWithError(copyErr)
			return
		}
		if closeErr := writer.Close(); closeErr != nil {
			_ = pipeWriter.CloseWithError(closeErr)
			return
		}
		_ = pipeWriter.Close()
	}()
	// Unblocks the goroutine if the request fails before the body is consumed.
	defer func() { _ = pipeReader.Close() }()

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, pipeReader)
	if err != nil {
		return 0, fmt.Errorf("build storage request: %w", err)
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())

	response, err := s.client.Do(request)
	if err != nil {
		return 0, storageRequestError("write", err)
	}
	defer drainAndClose(response)
	if !isSuccess(response.StatusCode) {
		return 0, fmt.Errorf("%w: storage write returned status %d", domain.ErrUnavailable, response.StatusCode)
	}
	return written.Load(), nil
}

// Open returns the stored ciphertext stream. The caller owns closing it.
func (s *SeaweedFSStore) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	target, err := s.objectURL(key)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("build storage request: %w", err)
	}
	response, err := s.client.Do(request)
	if err != nil {
		return nil, storageRequestError("read", err)
	}
	if response.StatusCode == http.StatusNotFound {
		drainAndClose(response)
		return nil, domain.ErrNotFound
	}
	if !isSuccess(response.StatusCode) {
		drainAndClose(response)
		return nil, fmt.Errorf("%w: storage read returned status %d", domain.ErrUnavailable, response.StatusCode)
	}
	return response.Body, nil
}

// OpenRange returns the stored ciphertext stream from offset to the end of the
// object. The caller owns closing it, and closing early is the normal case: a
// reader that only needs one chunk stops reading and the connection is dropped,
// so the filer never sends more than the transfer window ahead.
//
// A response that is not 206 is refused rather than used. That check is the
// whole point of the method: a store that ignored the Range header would answer
// 200 with the object from byte zero, and those bytes decrypted at the offset
// the caller asked about would be silent garbage — or, worse, a chunk opened at
// the wrong index. There is no fallback to reading from the start.
func (s *SeaweedFSStore) OpenRange(ctx context.Context, key string, offset int64) (io.ReadCloser, error) {
	if offset < 0 {
		return nil, fmt.Errorf("%w: negative storage offset", domain.ErrInvalidInput)
	}
	target, err := s.objectURL(key)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("build storage request: %w", err)
	}
	// The offset is derived from the envelope's fixed chunk geometry, never from
	// a client header, so this value is the service's own arithmetic.
	request.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))

	response, err := s.client.Do(request)
	if err != nil {
		return nil, storageRequestError("read", err)
	}
	if response.StatusCode == http.StatusNotFound {
		drainAndClose(response)
		return nil, domain.ErrNotFound
	}
	if response.StatusCode != http.StatusPartialContent {
		drainAndClose(response)
		return nil, fmt.Errorf("%w: storage range read returned status %d",
			domain.ErrUnavailable, response.StatusCode)
	}
	return response.Body, nil
}

// Delete removes an object. An already-absent object is a success: Delete is
// the compensation path and must be idempotent.
func (s *SeaweedFSStore) Delete(ctx context.Context, key string) error {
	target, err := s.objectURL(key)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, target, nil)
	if err != nil {
		return fmt.Errorf("build storage request: %w", err)
	}
	response, err := s.client.Do(request)
	if err != nil {
		return storageRequestError("delete", err)
	}
	defer drainAndClose(response)
	if response.StatusCode == http.StatusNotFound || isSuccess(response.StatusCode) {
		return nil
	}
	return fmt.Errorf("%w: storage delete returned status %d", domain.ErrUnavailable, response.StatusCode)
}

// Ping is the readiness probe for the storage dependency.
func (s *SeaweedFSStore) Ping(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.endpoint.String()+"/", nil)
	if err != nil {
		return fmt.Errorf("build storage request: %w", err)
	}
	response, err := s.client.Do(request)
	if err != nil {
		return storageRequestError("ping", err)
	}
	defer drainAndClose(response)
	if response.StatusCode >= http.StatusInternalServerError {
		return fmt.Errorf("%w: storage ping returned status %d", domain.ErrUnavailable, response.StatusCode)
	}
	return nil
}

func (s *SeaweedFSStore) objectURL(key string) (string, error) {
	if s == nil || s.endpoint == nil {
		return "", domain.ErrDependenciesUnavailable
	}
	if !objectKeyPattern.MatchString(key) || strings.Contains(key, "..") {
		return "", fmt.Errorf("%w: invalid storage object key", domain.ErrInvalidInput)
	}
	target := *s.endpoint
	target.Path = "/" + key
	return target.String(), nil
}

// storageRequestError keeps the storage endpoint, its hostname and any driver
// detail out of the returned error: callers only learn which operation failed.
func storageRequestError(operation string, err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return fmt.Errorf("storage %s canceled: %w", operation, context.Canceled)
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("%w: storage %s timed out", domain.ErrUnavailable, operation)
	case errors.Is(err, errRedirectNotAllowed):
		return fmt.Errorf("%w: storage %s was redirected", domain.ErrUnavailable, operation)
	default:
		return fmt.Errorf("%w: storage %s failed", domain.ErrUnavailable, operation)
	}
}

// drainAndClose bounds how much of an error body is read before the connection
// is returned to the pool. The bytes are discarded, never logged or forwarded.
func drainAndClose(response *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 512))
	_ = response.Body.Close()
}

func isSuccess(status int) bool {
	return status >= http.StatusOK && status < http.StatusMultipleChoices
}
