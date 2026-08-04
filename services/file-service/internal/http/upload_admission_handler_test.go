package httpapi_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	platformlog "github.com/nicrepository/nchat/libs/go/platform/log"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	httpapi "github.com/nicrepository/nchat/services/file-service/internal/http"
)

// Cluster-wide upload admission, exercised through the router.
//
// The property that matters most is negative and easy to lose in a refactor:
// when capacity is gone, the handler must answer without reading one byte of
// the request body. Every test here that asserts a refusal also asserts that
// the body was never touched.

// countingBody records how much of the request body the server actually read.
type countingBody struct {
	inner io.Reader
	reads atomic.Int64
	bytes atomic.Int64
}

func newCountingBody(payload string) *countingBody {
	return &countingBody{inner: strings.NewReader(payload)}
}

func (b *countingBody) Read(p []byte) (int, error) {
	b.reads.Add(1)
	n, err := b.inner.Read(p)
	b.bytes.Add(int64(n))
	return n, err
}

func (b *countingBody) Close() error { return nil }

// stubAdmission answers with a fixed verdict and counts what it handed out.
type stubAdmission struct {
	mu       sync.Mutex
	err      error
	acquired int
	released int
}

func (s *stubAdmission) Acquire(_ context.Context, userID string, _ int64) (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	if userID == "" {
		return nil, domain.ErrUnauthorized
	}
	s.acquired++
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.released++
		})
	}, nil
}

func (s *stubAdmission) counts() (acquired, released int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.acquired, s.released
}

// slotAdmission is a real counting gate, so a request genuinely has to wait for
// another to finish before it can be admitted.
type slotAdmission struct {
	mu    sync.Mutex
	inUse int
	limit int
}

func (s *slotAdmission) Acquire(context.Context, string, int64) (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inUse >= s.limit {
		return nil, domain.ErrClusterAtCapacity
	}
	s.inUse++
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.inUse--
		})
	}, nil
}

func admissionRouter(t *testing.T, useCases httpapi.AttachmentUseCases, admission httpapi.UploadAdmission) http.Handler {
	t.Helper()
	return httpapi.NewRouter(enabledConfig(), platformlog.New("file-service", "test"),
		httpapi.RouterDependencies{
			TokenValidator: staticValidator{token: testToken},
			Attachments:    useCases,
			Admission:      admission,
		})
}

// uploadRequestWithBody builds a real multipart upload whose body reads are
// observable.
func uploadRequestWithBody(t *testing.T, path string, body io.ReadCloser, contentType string) *http.Request {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, body)
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Authorization", "Bearer "+testToken)
	return request
}

func TestUploadRefusedByUserCapacityAnswers429WithoutReadingTheBody(t *testing.T) {
	admission := &stubAdmission{err: domain.ErrUserAtCapacity}
	router := admissionRouter(t, readyUseCases(), admission)

	reader, contentType := multipartBody(t, fileOf("payload that must never be read"))
	body := newCountingBody(readAll(t, reader))
	response := httptest.NewRecorder()

	router.ServeHTTP(response, uploadRequestWithBody(t, channelUploadPath(testChannelID), body, contentType))

	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d: %s", response.Code, response.Body.String())
	}
	if got := errorCode(t, response); got != "rate_limited" {
		t.Fatalf("expected a stable rate_limited code, got %q", got)
	}
	if response.Header().Get("Retry-After") == "" {
		t.Fatal("a refused upload must say when to try again")
	}
	if body.reads.Load() != 0 {
		t.Fatalf("the body must not be read when admission is refused, got %d reads", body.reads.Load())
	}
	// Nothing about capacity, holders or topology may leak.
	for _, forbidden := range []string{"slot", "replica", "connection", "pool", "concurrent uploads:"} {
		if strings.Contains(strings.ToLower(response.Body.String()), forbidden) {
			t.Fatalf("the response must not describe internal capacity: %s", response.Body.String())
		}
	}
}

func TestUploadRefusedByClusterCapacityAnswers503WithoutReadingTheBody(t *testing.T) {
	admission := &stubAdmission{err: domain.ErrClusterAtCapacity}
	router := admissionRouter(t, readyUseCases(), admission)

	reader, contentType := multipartBody(t, fileOf("payload"))
	body := newCountingBody(readAll(t, reader))
	response := httptest.NewRecorder()

	router.ServeHTTP(response, uploadRequestWithBody(t, dmUploadPath(testDMID), body, contentType))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Retry-After") == "" {
		t.Fatal("a refused upload must say when to try again")
	}
	if body.reads.Load() != 0 {
		t.Fatalf("the body must not be read when the cluster is full, got %d reads", body.reads.Load())
	}
}

// An admission backend that cannot answer must fail closed, never admit.
func TestUploadFailsClosedWhenAdmissionIsUnavailable(t *testing.T) {
	admission := &stubAdmission{err: domain.ErrAdmissionUnavailable}
	router := admissionRouter(t, readyUseCases(), admission)

	reader, contentType := multipartBody(t, fileOf("payload"))
	body := newCountingBody(readAll(t, reader))
	response := httptest.NewRecorder()

	router.ServeHTTP(response, uploadRequestWithBody(t, channelUploadPath(testChannelID), body, contentType))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", response.Code)
	}
	if body.reads.Load() != 0 {
		t.Fatalf("an undecidable admission must not read the body, got %d reads", body.reads.Load())
	}
}

// An unauthorised caller must be refused before admission is even consulted, so
// no slot is ever spent on a request that was never going to be accepted.
func TestUnauthorizedDestinationNeverConsumesASlot(t *testing.T) {
	useCases := readyUseCases()
	useCases.authorizeErr = domain.ErrNotFound
	admission := &stubAdmission{}
	router := admissionRouter(t, useCases, admission)

	reader, contentType := multipartBody(t, fileOf("payload"))
	body := newCountingBody(readAll(t, reader))
	response := httptest.NewRecorder()

	router.ServeHTTP(response, uploadRequestWithBody(t, channelUploadPath(testChannelID), body, contentType))

	if response.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", response.Code)
	}
	if acquired, _ := admission.counts(); acquired != 0 {
		t.Fatalf("an unauthorised caller must not reserve capacity, got %d acquisitions", acquired)
	}
	if body.reads.Load() != 0 {
		t.Fatalf("an unauthorised caller must not have their body read, got %d reads", body.reads.Load())
	}
}

// Every exit path gives the slot back: success and failure alike.
func TestAdmissionIsReleasedOnEveryPath(t *testing.T) {
	tests := []struct {
		name      string
		uploadErr error
	}{
		{name: "success"},
		{name: "read failure", uploadErr: domain.ErrInvalidInput},
		{name: "too large", uploadErr: domain.ErrTooLarge},
		{name: "storage failure", uploadErr: errors.New("seaweedfs unreachable")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			useCases := readyUseCases()
			useCases.uploadErr = tt.uploadErr
			admission := &stubAdmission{}
			router := admissionRouter(t, useCases, admission)

			response := httptest.NewRecorder()
			router.ServeHTTP(response, uploadRequest(t, channelUploadPath(testChannelID), fileOf("payload")))

			acquired, released := admission.counts()
			if acquired != 1 {
				t.Fatalf("expected one acquisition, got %d", acquired)
			}
			if released != 1 {
				t.Fatalf("the slot must be released on this path, got %d releases", released)
			}
		})
	}
}

// A held slot really blocks the next request, and freeing it really unblocks
// the one after — the behaviour a per-minute counter cannot provide.
func TestAnInFlightUploadHoldsItsSlotUntilItFinishes(t *testing.T) {
	admission := &slotAdmission{limit: 1}
	useCases := readyUseCases()
	// Hold the first upload inside the service, exactly as a slow client would.
	hold := make(chan struct{})
	router := admissionRouter(t, useCases, admission)

	// Signalled from inside the service, so the first request provably holds its
	// slot before the second one is sent. No sleeping, no polling.
	admitted := make(chan struct{})
	var signalOnce sync.Once
	useCases.uploadHook = func() {
		// Only the first request announces itself and waits; the retry after the
		// slot is freed must run straight through.
		signalOnce.Do(func() { close(admitted) })
		<-hold
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		first := httptest.NewRecorder()
		router.ServeHTTP(first, uploadRequest(t, channelUploadPath(testChannelID), fileOf("slow")))
	}()
	<-admitted

	blocked := httptest.NewRecorder()
	reader, contentType := multipartBody(t, fileOf("second"))
	body := newCountingBody(readAll(t, reader))
	router.ServeHTTP(blocked, uploadRequestWithBody(t, channelUploadPath(testChannelID), body, contentType))

	if blocked.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected the second upload to be refused, got %d", blocked.Code)
	}
	if body.reads.Load() != 0 {
		t.Fatalf("the refused upload must not have been read, got %d reads", body.reads.Load())
	}

	close(hold)
	<-done

	// The slot is back, so the next attempt is admitted.
	after := httptest.NewRecorder()
	router.ServeHTTP(after, uploadRequest(t, channelUploadPath(testChannelID), fileOf("third")))
	if after.Code != http.StatusCreated {
		t.Fatalf("expected the released slot to be reusable, got %d: %s", after.Code, after.Body.String())
	}
}

func readAll(t *testing.T, reader io.Reader) string {
	t.Helper()
	raw, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read multipart body: %v", err)
	}
	return string(raw)
}

// Admission answering "unauthenticated" is not a capacity problem, so it must
// not be dressed up as one: a 503 with a Retry-After would tell a caller with a
// broken token to keep trying.
func TestAdmissionRefusingAnUnidentifiedCallerAnswers401(t *testing.T) {
	admission := &stubAdmission{err: domain.ErrUnauthorized}
	router := admissionRouter(t, readyUseCases(), admission)

	reader, contentType := multipartBody(t, fileOf("payload"))
	body := newCountingBody(readAll(t, reader))
	response := httptest.NewRecorder()

	router.ServeHTTP(response, uploadRequestWithBody(t, channelUploadPath(testChannelID), body, contentType))

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", response.Code, response.Body.String())
	}
	if body.reads.Load() != 0 {
		t.Fatalf("the body must not be read, got %d reads", body.reads.Load())
	}
}

// A client that hangs up while admission is being decided gets the same refusal
// as a full cluster. There is nobody left to read it, but the slot accounting
// and the log must still be consistent.
func TestAdmissionCancelledByTheClientAnswersWithoutReadingTheBody(t *testing.T) {
	admission := &stubAdmission{err: context.Canceled}
	router := admissionRouter(t, readyUseCases(), admission)

	reader, contentType := multipartBody(t, fileOf("payload"))
	body := newCountingBody(readAll(t, reader))
	response := httptest.NewRecorder()

	router.ServeHTTP(response, uploadRequestWithBody(t, channelUploadPath(testChannelID), body, contentType))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", response.Code)
	}
	if body.reads.Load() != 0 {
		t.Fatalf("the body must not be read, got %d reads", body.reads.Load())
	}
}
