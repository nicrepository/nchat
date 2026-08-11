package linkpreview

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countingObserver records the labels the service emits, so the closed set can
// be asserted on rather than assumed.
type countingObserver struct {
	mu      sync.Mutex
	results []string
}

func (o *countingObserver) ObserveLinkPreview(result string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.results = append(o.results, result)
}

func (o *countingObserver) seen() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.results...)
}

// serviceAgainst builds a service whose accepted connections land on server.
func serviceAgainst(
	t *testing.T, server *httptest.Server, clock *fakeClock, observer Observer,
) *Service {
	t.Helper()
	connector := &recordingConnector{target: strings.TrimPrefix(server.URL, "http://")}
	fetcher := newFetcherWith(2*time.Second, fixedResolver(publicAddr), connector.connect)
	return newService(fetcher, 15*time.Minute, observer, clock.Now)
}

func TestServiceReturnsPreviewForAValidPage(t *testing.T) {
	server := htmlServer(t, `<html><head>
		<meta property="og:title" content="Example page">
		<meta property="og:description" content="What it is about">
		<meta property="og:image" content="/card.png">
		<meta property="og:site_name" content="Example">
	</head></html>`)
	observer := &countingObserver{}
	service := serviceAgainst(t, server, newClock(), observer)

	preview, err := service.Preview(context.Background(), "http://example.com/page")
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if preview.Title != "Example page" || preview.SiteName != "Example" {
		t.Fatalf("unexpected preview %+v", preview)
	}
	if preview.ImageURL != "http://example.com/card.png" {
		t.Fatalf("image: %q", preview.ImageURL)
	}
	// The canonical requested URL, never the redirect target: a card claiming a
	// different address than the link the user sees is a phishing primitive.
	if preview.URL != "http://example.com/page" {
		t.Fatalf("url: %q", preview.URL)
	}
	if got := observer.seen(); len(got) != 1 || got[0] != resultSuccess {
		t.Fatalf("expected one success, got %v", got)
	}
}

// TestServiceCachesSuccess: the second request must not reach the network.
func TestServiceCachesSuccess(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<html><head><meta property="og:title" content="cached"></head></html>`)
	}))
	t.Cleanup(server.Close)
	clock := newClock()
	observer := &countingObserver{}
	service := serviceAgainst(t, server, clock, observer)

	for range 3 {
		preview, err := service.Preview(context.Background(), "http://example.com/page")
		if err != nil {
			t.Fatalf("Preview: %v", err)
		}
		if preview.Title != "cached" {
			t.Fatalf("title: %q", preview.Title)
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("expected one upstream request, got %d", got)
	}
	if got := observer.seen(); len(got) != 3 ||
		got[0] != resultSuccess || got[1] != resultHit || got[2] != resultHit {
		t.Fatalf("expected success then two hits, got %v", got)
	}
}

func TestServiceRefetchesAfterTTL(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<html><head><meta property="og:title" content="cached"></head></html>`)
	}))
	t.Cleanup(server.Close)
	clock := newClock()
	service := serviceAgainst(t, server, clock, nil)

	if _, err := service.Preview(context.Background(), "http://example.com/page"); err != nil {
		t.Fatalf("Preview: %v", err)
	}
	clock.advance(16 * time.Minute)
	if _, err := service.Preview(context.Background(), "http://example.com/page"); err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("expected the entry to expire and be refetched, got %d requests", got)
	}
}

// TestServiceCachesFailuresBriefly: a failing URL retried in a loop must not
// cost a full timeout each time, and must recover once the short TTL passes.
func TestServiceCachesFailuresBriefly(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	clock := newClock()
	service := serviceAgainst(t, server, clock, nil)

	for range 3 {
		if _, err := service.Preview(context.Background(), "http://example.com/page"); err == nil {
			t.Fatal("expected an error")
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("expected the failure to be cached, got %d requests", got)
	}

	clock.advance(negativeTTL + time.Second)
	if _, err := service.Preview(context.Background(), "http://example.com/page"); err == nil {
		t.Fatal("expected an error")
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("expected the negative entry to expire, got %d requests", got)
	}
}

// TestServiceMetadataSufficiency: any one supported field is enough for a card.
//
// Each case declares exactly one thing, so a field left out of the "is there
// anything to show" rule surfaces here as a 404 for a page that plainly had
// something to show.
func TestServiceMetadataSufficiency(t *testing.T) {
	cases := map[string]struct {
		head string
		want Preview
	}{
		"only og:title": {
			`<meta property="og:title" content="Only a title">`,
			Preview{URL: "http://example.com/page", Title: "Only a title"},
		},
		"only og:description": {
			`<meta property="og:description" content="Only a description">`,
			Preview{URL: "http://example.com/page", Description: "Only a description"},
		},
		"only og:image": {
			`<meta property="og:image" content="/card.png">`,
			Preview{URL: "http://example.com/page", ImageURL: "http://example.com/card.png"},
		},
		"only og:site_name": {
			`<meta property="og:site_name" content="Example">`,
			Preview{URL: "http://example.com/page", SiteName: "Example"},
		},
		"only html title": {
			`<title>Only an html title</title>`,
			Preview{URL: "http://example.com/page", Title: "Only an html title"},
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			server := htmlServer(t, "<html><head>"+testCase.head+"</head></html>")
			observer := &countingObserver{}
			service := serviceAgainst(t, server, newClock(), observer)

			preview, err := service.Preview(context.Background(), "http://example.com/page")
			if err != nil {
				t.Fatalf("expected a preview, got %v", err)
			}
			if preview != testCase.want {
				t.Fatalf("preview = %+v, want %+v", preview, testCase.want)
			}
			if got := observer.seen(); len(got) != 1 || got[0] != resultSuccess {
				t.Fatalf("expected one success, got %v", got)
			}
		})
	}
}

// TestServiceReportsNoMetadata is the other half of the rule: a page that
// declares none of the supported fields still has no card to draw.
func TestServiceReportsNoMetadata(t *testing.T) {
	for name, document := range map[string]string{
		"empty head":        `<html><head></head><body>nothing to preview</body></html>`,
		"no head at all":    `<html><body>nothing to preview</body></html>`,
		"unsupported og":    `<html><head><meta property="og:type" content="article"></head></html>`,
		"empty content":     `<html><head><meta property="og:title" content=""></head></html>`,
		"whitespace title":  `<html><head><title>   </title></head></html>`,
		"metadata in body":  `<html><head></head><body><meta property="og:title" content="late"></body></html>`,
		"unusable og:image": `<html><head><meta property="og:image" content="javascript:alert(1)"></head></html>`,
	} {
		t.Run(name, func(t *testing.T) {
			server := htmlServer(t, document)
			observer := &countingObserver{}
			service := serviceAgainst(t, server, newClock(), observer)

			_, err := service.Preview(context.Background(), "http://example.com/page")
			if !errors.Is(err, ErrNoMetadata) {
				t.Fatalf("expected ErrNoMetadata, got %v", err)
			}
			if got := observer.seen(); len(got) != 1 || got[0] != resultNoMetadata {
				t.Fatalf("expected one no_metadata, got %v", got)
			}
		})
	}
}

// TestPreviewHasMetadata states the rule directly, so a field added to Preview
// without being counted fails here as well as through the service.
func TestPreviewHasMetadata(t *testing.T) {
	cases := map[string]struct {
		preview Preview
		want    bool
	}{
		"empty":              {Preview{}, false},
		"url only":           {Preview{URL: "https://example.com/"}, false},
		"title":              {Preview{Title: "t"}, true},
		"description":        {Preview{Description: "d"}, true},
		"image":              {Preview{ImageURL: "https://example.com/i.png"}, true},
		"site name":          {Preview{SiteName: "s"}, true},
		"site name plus url": {Preview{URL: "https://example.com/", SiteName: "s"}, true},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			if got := testCase.preview.hasMetadata(); got != testCase.want {
				t.Fatalf("hasMetadata() = %v, want %v", got, testCase.want)
			}
		})
	}
}

// TestServiceRefusesBlockedURLWithoutTouchingTheNetwork covers the whole point
// of the feature: a request naming an internal destination is answered from the
// policy, and the observer records it under the one label an operator can alert
// on.
func TestServiceRefusesBlockedURLWithoutTouchingTheNetwork(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, "<html></html>")
	}))
	t.Cleanup(server.Close)
	observer := &countingObserver{}
	service := serviceAgainst(t, server, newClock(), observer)

	for _, raw := range []string{
		"http://127.0.0.1/admin",
		"http://169.254.169.254/latest/meta-data/",
		"http://10.0.0.1/",
		"http://[::1]/",
		"file:///etc/passwd",
		"http://example.com:2379/v2/keys",
	} {
		if _, err := service.Preview(context.Background(), raw); !errors.Is(err, ErrURLNotAllowed) {
			t.Fatalf("expected %q to be refused, got %v", raw, err)
		}
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("expected no upstream request, got %d", got)
	}
	for _, result := range observer.seen() {
		if result != resultBlocked {
			t.Fatalf("expected every outcome to be %q, got %v", resultBlocked, observer.seen())
		}
	}
}

func TestServiceReportsInvalidURL(t *testing.T) {
	server := htmlServer(t, "<html></html>")
	observer := &countingObserver{}
	service := serviceAgainst(t, server, newClock(), observer)

	if _, err := service.Preview(context.Background(), "   "); !errors.Is(err, ErrInvalidURL) {
		t.Fatalf("expected ErrInvalidURL, got %v", err)
	}
	if got := observer.seen(); len(got) != 1 || got[0] != resultInvalidURL {
		t.Fatalf("expected one invalid_url, got %v", got)
	}
}

func TestServiceReportsUnsupportedContentType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = fmt.Fprint(w, "%PDF-1.4")
	}))
	t.Cleanup(server.Close)
	observer := &countingObserver{}
	service := serviceAgainst(t, server, newClock(), observer)

	_, err := service.Preview(context.Background(), "http://example.com/doc")
	if !errors.Is(err, ErrUnsupportedContentType) {
		t.Fatalf("expected ErrUnsupportedContentType, got %v", err)
	}
	if got := observer.seen(); len(got) != 1 || got[0] != resultUnsupportedType {
		t.Fatalf("expected one unsupported_content_type, got %v", got)
	}
}

// TestServiceDoesNotCacheCallerCancellation: a caller going away is not an
// answer about the URL, so it must neither be stored nor counted.
//
// The synchronisation is explicit rather than timed. The handler signals that
// the request has actually arrived and then blocks on its own request context,
// so the test cancels at a point it knows the fetch has reached — mid-I/O,
// after the connection and after the request was written. A sleep would only
// have made that likely, and on a loaded machine it could fire before the
// request left or after it completed, testing a different thing each run.
func TestServiceDoesNotCacheCallerCancellation(t *testing.T) {
	var (
		started  = make(chan struct{})
		observed = make(chan struct{})
		requests atomic.Int64
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		close(started)
		// Block until the client hangs up. Waiting on the request context means
		// the handler observes the cancellation itself and returns, so no
		// goroutine is left parked when the test ends.
		<-r.Context().Done()
		close(observed)
	}))
	t.Cleanup(server.Close)
	observer := &countingObserver{}
	service := serviceAgainst(t, server, newClock(), observer)

	type result struct {
		preview Preview
		err     error
	}
	results := make(chan result, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		preview, err := service.Preview(ctx, "http://example.com/page")
		results <- result{preview, err}
	}()

	// The request has provably reached the upstream and is in flight.
	waitFor(t, started, "the request to reach the upstream")
	cancel()

	// The deadlines below only stop a broken implementation from hanging the
	// suite; they are not how the test synchronises.
	select {
	case got := <-results:
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("expected the cancellation to surface, got %v", got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Preview did not return promptly after cancellation")
	}
	waitFor(t, observed, "the upstream handler to observe the cancellation")

	if got := requests.Load(); got != 1 {
		t.Fatalf("expected exactly one upstream request, got %d", got)
	}
	if got := observer.seen(); len(got) != 0 {
		t.Fatalf("expected no outcome to be counted, got %v", got)
	}
	if _, ok := service.cache.get("http://example.com/page"); ok {
		t.Fatal("expected nothing to be cached for a cancelled request")
	}
}

// waitFor blocks until done is closed, failing the test rather than hanging the
// suite if it never is.
func waitFor(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// TestResultForCoversEveryErrorClass keeps the metric label set closed: a new
// error class must be given a label deliberately rather than silently folding
// into upstream_error.
func TestResultForCoversEveryErrorClass(t *testing.T) {
	cases := map[error]string{
		nil:                        resultSuccess,
		ErrInvalidURL:              resultInvalidURL,
		ErrURLNotAllowed:           resultBlocked,
		ErrUnsupportedContentType:  resultUnsupportedType,
		ErrTimeout:                 resultTimeout,
		ErrNoMetadata:              resultNoMetadata,
		ErrUpstream:                resultUpstreamError,
		errors.New("unclassified"): resultUpstreamError,
	}
	for err, want := range cases {
		if got := resultFor(err); got != want {
			t.Fatalf("resultFor(%v) = %q, want %q", err, got, want)
		}
	}
}
