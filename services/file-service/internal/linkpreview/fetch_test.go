package linkpreview

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"
)

// publicAddr is the address every test hostname resolves to unless the case is
// about a blocked destination. It is a real public address and is never dialled
// — the connector below redirects the accepted connection to the local test
// server, so the policy runs for real and no test touches the internet.
const publicAddr = "93.184.216.34"

// fixedResolver answers every lookup with the same addresses.
func fixedResolver(addrs ...string) resolver {
	parsed := make([]netip.Addr, 0, len(addrs))
	for _, raw := range addrs {
		parsed = append(parsed, netip.MustParseAddr(raw))
	}
	return func(context.Context, string) ([]netip.Addr, error) {
		return parsed, nil
	}
}

// hostResolver answers per hostname, for the redirect cases where the first hop
// is public and a later one is not.
func hostResolver(byHost map[string]string) resolver {
	return func(_ context.Context, host string) ([]netip.Addr, error) {
		raw, ok := byHost[host]
		if !ok {
			return nil, fmt.Errorf("no such host")
		}
		return []netip.Addr{netip.MustParseAddr(raw)}, nil
	}
}

// recordingConnector sends every accepted connection to target and records the
// address the policy approved, which is what the rebinding test asserts on.
type recordingConnector struct {
	target string
	mu     sync.Mutex
	dialed []string
}

func (c *recordingConnector) connect(ctx context.Context, network, address string) (net.Conn, error) {
	c.mu.Lock()
	c.dialed = append(c.dialed, address)
	c.mu.Unlock()
	return (&net.Dialer{}).DialContext(ctx, network, c.target)
}

func (c *recordingConnector) addresses() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.dialed...)
}

// fetchAgainst runs one fetch of rawURL against server, using resolve.
func fetchAgainst(
	t *testing.T, server *httptest.Server, resolve resolver, rawURL string,
) (*recordingConnector, *url.URL, []byte, error) {
	t.Helper()
	connector := &recordingConnector{target: strings.TrimPrefix(server.URL, "http://")}
	target, err := canonicalURL(rawURL)
	if err != nil {
		t.Fatalf("canonicalURL(%q): %v", rawURL, err)
	}
	fetcher := newFetcherWith(5*time.Second, resolve, connector.connect)
	final, body, err := fetcher.fetch(context.Background(), target)
	return connector, final, body, err
}

func htmlServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestFetchReadsHTMLFromPublicHost(t *testing.T) {
	server := htmlServer(t, "<html><head><title>ok</title></head></html>")

	_, final, body, err := fetchAgainst(t, server, fixedResolver(publicAddr), "http://example.com/page")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !strings.Contains(string(body), "<title>ok</title>") {
		t.Fatalf("unexpected body %q", body)
	}
	if final.Host != "example.com" {
		t.Fatalf("expected the final URL to keep the requested host, got %q", final)
	}
}

// TestFetchConnectsToTheValidatedAddress is the anti-rebinding assertion.
//
// The address the policy accepted must be the address the connection is made
// to. If the transport re-resolved the hostname after the check — the classic
// TOCTOU shape — the connector would have been handed a hostname instead.
func TestFetchConnectsToTheValidatedAddress(t *testing.T) {
	server := htmlServer(t, "<html><head><title>ok</title></head></html>")

	connector, _, _, err := fetchAgainst(t, server, fixedResolver(publicAddr), "http://example.com/page")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	dialed := connector.addresses()
	if len(dialed) != 1 {
		t.Fatalf("expected exactly one dial, got %v", dialed)
	}
	if dialed[0] != net.JoinHostPort(publicAddr, "80") {
		t.Fatalf("expected the dial to use the validated address, got %q", dialed[0])
	}
}

// TestFetchRefusesWhenAnyResolvedAddressIsPrivate covers the multi-answer case.
// One private address in the set poisons the name: otherwise an attacker only
// has to be asked twice.
func TestFetchRefusesWhenAnyResolvedAddressIsPrivate(t *testing.T) {
	cases := map[string][]string{
		"private only":          {"10.0.0.5"},
		"public then private":   {publicAddr, "127.0.0.1"},
		"private then public":   {"192.168.1.1", publicAddr},
		"public then metadata":  {publicAddr, "169.254.169.254"},
		"public then ipv6 ula":  {publicAddr, "fd00:ec2::254"},
		"ipv4 mapped loopback":  {"::ffff:127.0.0.1"},
		"no answer at all":      {},
		"public then multicast": {publicAddr, "224.0.0.1"},
	}
	server := htmlServer(t, "<html><head><title>should not be reached</title></head></html>")
	for name, addrs := range cases {
		t.Run(name, func(t *testing.T) {
			connector, _, _, err := fetchAgainst(
				t, server, fixedResolver(addrs...), "http://example.com/page",
			)
			if !errors.Is(err, ErrURLNotAllowed) {
				t.Fatalf("expected the destination to be refused, got %v", err)
			}
			if dialed := connector.addresses(); len(dialed) != 0 {
				t.Fatalf("expected no connection to be attempted, dialled %v", dialed)
			}
		})
	}
}

func TestFetchReportsDNSFailureAsUpstream(t *testing.T) {
	server := htmlServer(t, "<html></html>")
	resolve := func(context.Context, string) ([]netip.Addr, error) {
		return nil, errors.New("lookup example.com: no such host")
	}

	_, _, _, err := fetchAgainst(t, server, resolve, "http://example.com/page")
	if !errors.Is(err, ErrUpstream) {
		t.Fatalf("expected an upstream error, got %v", err)
	}
	// The resolver's message names the host; the classified error must not.
	if strings.Contains(err.Error(), "example.com") {
		t.Fatalf("resolver detail leaked into the error: %v", err)
	}
}

// redirectServer answers /start with a redirect to location and /ok with HTML.
func redirectServer(t *testing.T, location string, status int) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, location, status)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, "<html><head><title>arrived</title></head></html>")
	}))
	t.Cleanup(server.Close)
	return server
}

func TestFetchFollowsPublicRedirect(t *testing.T) {
	server := redirectServer(t, "http://elsewhere.example/ok", http.StatusFound)
	resolve := hostResolver(map[string]string{
		"example.com":       publicAddr,
		"elsewhere.example": "8.8.8.8",
	})

	_, final, body, err := fetchAgainst(t, server, resolve, "http://example.com/start")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !strings.Contains(string(body), "arrived") {
		t.Fatalf("expected the redirect target's body, got %q", body)
	}
	if final.Host != "elsewhere.example" {
		t.Fatalf("expected the final URL to be the redirect target, got %q", final)
	}
}

// TestFetchRefusesRedirectToBlockedDestination is the requirement that every
// hop is revalidated. The first hop is a legitimate public host; the second is
// not, and only the policy applied at the second dial can catch it.
func TestFetchRefusesRedirectToBlockedDestination(t *testing.T) {
	cases := map[string]struct {
		location string
		want     error
	}{
		"loopback":       {"http://internal.example/admin", ErrURLNotAllowed},
		"metadata by ip": {"http://169.254.169.254/latest/meta-data/", ErrURLNotAllowed},
		"metadata byname": {
			"http://metadata.google.internal/computeMetadata/v1/", ErrURLNotAllowed,
		},
		"private ipv4":    {"http://192.168.0.1/", ErrURLNotAllowed},
		"ipv6 loopback":   {"http://[::1]/", ErrURLNotAllowed},
		"file scheme":     {"file:///etc/passwd", ErrURLNotAllowed},
		"gopher scheme":   {"gopher://example.com/x", ErrURLNotAllowed},
		"nondefault port": {"http://elsewhere.example:9200/_cluster/health", ErrURLNotAllowed},
		"userinfo":        {"http://root:root@elsewhere.example/", ErrURLNotAllowed},
	}
	resolve := hostResolver(map[string]string{
		"example.com":              publicAddr,
		"elsewhere.example":        "8.8.8.8",
		"internal.example":         "127.0.0.1",
		"metadata.google.internal": "169.254.169.254",
	})
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			server := redirectServer(t, testCase.location, http.StatusFound)
			_, _, body, err := fetchAgainst(t, server, resolve, "http://example.com/start")
			if !errors.Is(err, testCase.want) {
				t.Fatalf("expected %v, got %v (body %q)", testCase.want, err, body)
			}
		})
	}
}

func TestFetchRefusesRedirectLoop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.com/start", http.StatusFound)
	}))
	t.Cleanup(server.Close)

	_, _, _, err := fetchAgainst(t, server, fixedResolver(publicAddr), "http://example.com/start")
	if !errors.Is(err, ErrUpstream) {
		t.Fatalf("expected a redirect loop to be refused, got %v", err)
	}
}

func TestFetchRefusesTooManyRedirects(t *testing.T) {
	var hops int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hops++
		http.Redirect(w, r, fmt.Sprintf("http://example.com/hop%d", hops), http.StatusFound)
	}))
	t.Cleanup(server.Close)

	_, _, _, err := fetchAgainst(t, server, fixedResolver(publicAddr), "http://example.com/start")
	if !errors.Is(err, ErrUpstream) {
		t.Fatalf("expected the hop limit to be enforced, got %v", err)
	}
	if hops > maxRedirects+1 {
		t.Fatalf("expected at most %d hops, server saw %d", maxRedirects+1, hops)
	}
}

func TestFetchContentTypePolicy(t *testing.T) {
	cases := map[string]struct {
		contentType string
		want        error
	}{
		"html":              {"text/html", nil},
		"html with charset": {"text/html; charset=utf-8", nil},
		"html uppercase":    {"TEXT/HTML; Charset=UTF-8", nil},
		"html padded":       {"  text/html  ", nil},
		"absent":            {"", ErrUnsupportedContentType},
		"malformed":         {"text/html; charset=", ErrUnsupportedContentType},
		"octet stream":      {"application/octet-stream", ErrUnsupportedContentType},
		"pdf":               {"application/pdf", ErrUnsupportedContentType},
		"png":               {"image/png", ErrUnsupportedContentType},
		"video":             {"video/mp4", ErrUnsupportedContentType},
		"json":              {"application/json", ErrUnsupportedContentType},
		"plain text":        {"text/plain", ErrUnsupportedContentType},
		"xhtml":             {"application/xhtml+xml", ErrUnsupportedContentType},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if testCase.contentType == "" {
					// Go would sniff a type for a non-empty body, so an absent
					// header needs an empty one.
					w.Header()["Content-Type"] = nil
					w.WriteHeader(http.StatusOK)
					return
				}
				w.Header().Set("Content-Type", testCase.contentType)
				_, _ = fmt.Fprint(w, "<html><head><title>x</title></head></html>")
			}))
			t.Cleanup(server.Close)

			_, _, _, err := fetchAgainst(t, server, fixedResolver(publicAddr), "http://example.com/page")
			if testCase.want == nil {
				if err != nil {
					t.Fatalf("expected %q to be accepted, got %v", testCase.contentType, err)
				}
				return
			}
			if !errors.Is(err, testCase.want) {
				t.Fatalf("expected %v for %q, got %v", testCase.want, testCase.contentType, err)
			}
		})
	}
}

func TestFetchRefusesUnexpectedStatus(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusForbidden,
		http.StatusInternalServerError, http.StatusNoContent} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(status)
		}))
		_, _, _, err := fetchAgainst(t, server, fixedResolver(publicAddr), "http://example.com/page")
		server.Close()
		if !errors.Is(err, ErrUpstream) {
			t.Fatalf("expected status %d to be refused, got %v", status, err)
		}
	}
}

// --- body limit ----------------------------------------------------------
//
// The rule under test is "return the whole body or refuse it, never a prefix".
// A truncated document is not a short document: Open Graph tags sit at the top
// of a page, so a body cut off at the limit would still parse and still yield a
// title, and that fragment would be cached and served as though the page had
// been read.

// htmlOfSize returns a valid document of exactly size bytes, whose Open Graph
// tags are in the first few hundred. The padding is a comment so the filler
// cannot itself be read as metadata.
func htmlOfSize(t *testing.T, size int) string {
	t.Helper()
	const head = `<html><head><meta property="og:title" content="early"></head><!--`
	const tail = `--></html>`
	if size < len(head)+len(tail) {
		t.Fatalf("size %d is smaller than the document skeleton", size)
	}
	return head + strings.Repeat("p", size-len(head)-len(tail)) + tail
}

// bodyServer replies with body, optionally declaring a length and optionally
// gzipping it. A server that declares no length is answering chunked, which is
// the shape an endless body actually takes.
func bodyServer(t *testing.T, body string, declareLength, compress bool) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if !compress {
			if declareLength {
				w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			}
			_, _ = io.WriteString(w, body)
			return
		}
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			t.Errorf("expected the transport to offer gzip")
		}
		w.Header().Set("Content-Encoding", "gzip")
		writer := gzip.NewWriter(w)
		defer func() { _ = writer.Close() }()
		_, _ = io.WriteString(writer, body)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestFetchBodyLimit(t *testing.T) {
	cases := map[string]struct {
		size          int
		declareLength bool
		compress      bool
		wantErr       error
	}{
		// A: exactly at the limit is a complete document and is accepted.
		"exactly at the limit": {int(maxBodyBytes), true, false, nil},
		"one below the limit":  {int(maxBodyBytes) - 1, true, false, nil},
		// B: one byte past it is not.
		"one byte over the limit": {int(maxBodyBytes) + 1, true, false, ErrUpstream},
		// C: chunked, so the declared length cannot be what catches it.
		"chunked over the limit": {int(maxBodyBytes) * 3, false, false, ErrUpstream},
		"chunked one byte over":  {int(maxBodyBytes) + 1, false, false, ErrUpstream},
		// D: no declared length, within the limit, must still work.
		"chunked within the limit": {int(maxBodyBytes) / 2, false, false, nil},
		// E and F: the limit is judged on the decompressed size. A body that
		// compresses to a few kilobytes and expands past the limit is refused.
		"gzip expanding over limit": {int(maxBodyBytes) * 4, false, true, ErrUpstream},
		"gzip within the limit":     {int(maxBodyBytes) / 2, false, true, nil},
		"gzip exactly at the limit": {int(maxBodyBytes), false, true, nil},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			document := htmlOfSize(t, testCase.size)
			server := bodyServer(t, document, testCase.declareLength, testCase.compress)

			_, _, body, err := fetchAgainst(
				t, server, fixedResolver(publicAddr), "http://example.com/page",
			)
			if testCase.wantErr != nil {
				if !errors.Is(err, testCase.wantErr) {
					t.Fatalf("expected %v, got %v (read %d bytes)", testCase.wantErr, err, len(body))
				}
				// G: refusing must mean refusing. A caller that got an error and
				// a prefix could still parse the prefix.
				if len(body) != 0 {
					t.Fatalf("a refused response returned %d bytes of body", len(body))
				}
				return
			}
			if err != nil {
				t.Fatalf("fetch: %v", err)
			}
			if len(body) != len(document) {
				t.Fatalf("read %d bytes, expected the whole %d-byte document", len(body), len(document))
			}
		})
	}
}

// TestFetchRefusesOversizedBodyWhoseMetadataFitsBeforeTheCut is the assertion
// that a refusal is not merely "we failed to reach the tags in time".
//
// The Open Graph tags are in the first hundred bytes, so a truncating reader
// would have everything it needs long before the limit and would happily
// produce a preview. The whole document is still refused, and no metadata is
// extracted from it.
func TestFetchRefusesOversizedBodyWhoseMetadataFitsBeforeTheCut(t *testing.T) {
	document := htmlOfSize(t, int(maxBodyBytes)*2)
	if !strings.Contains(document[:200], `og:title`) {
		t.Fatal("the fixture must carry its metadata before the cut to be meaningful")
	}
	server := bodyServer(t, document, false, false)

	_, _, body, err := fetchAgainst(t, server, fixedResolver(publicAddr), "http://example.com/page")

	if !errors.Is(err, ErrUpstream) {
		t.Fatalf("expected the oversized response to be refused, got %v", err)
	}
	if len(body) != 0 {
		t.Fatalf("expected no body, got %d bytes", len(body))
	}
}

// TestFetchRefusesDeclaredOversizedBody stops before reading anything: a
// response that has already announced itself as too large is not read at all.
func TestFetchRefusesDeclaredOversizedBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Content-Length", fmt.Sprint(maxBodyBytes*4))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(make([]byte, maxBodyBytes*4))
	}))
	t.Cleanup(server.Close)

	_, _, _, err := fetchAgainst(t, server, fixedResolver(publicAddr), "http://example.com/page")
	if !errors.Is(err, ErrUpstream) {
		t.Fatalf("expected an oversized response to be refused, got %v", err)
	}
}

// TestReadBoundedBody exercises the rule without a server in front of it, which
// is the reason it is a function of its own.
func TestReadBoundedBody(t *testing.T) {
	cases := map[string]struct {
		size    int
		wantErr bool
	}{
		"empty":            {0, false},
		"small":            {16, false},
		"one below limit":  {int(maxBodyBytes) - 1, false},
		"exactly at limit": {int(maxBodyBytes), false},
		"one byte over":    {int(maxBodyBytes) + 1, true},
		"far over":         {int(maxBodyBytes) * 3, true},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			data, err := readBoundedBody(bytes.NewReader(make([]byte, testCase.size)))
			if testCase.wantErr {
				if !errors.Is(err, ErrUpstream) {
					t.Fatalf("expected ErrUpstream, got %v", err)
				}
				if data != nil {
					t.Fatalf("a refused body returned %d bytes", len(data))
				}
				return
			}
			if err != nil {
				t.Fatalf("readBoundedBody: %v", err)
			}
			if len(data) != testCase.size {
				t.Fatalf("read %d bytes, want %d", len(data), testCase.size)
			}
		})
	}
}

// TestReadBoundedBodyPropagatesReadFailures: a stream that breaks mid-read is a
// transport failure, not an oversized body.
func TestReadBoundedBodyPropagatesReadFailures(t *testing.T) {
	broken := io.MultiReader(
		bytes.NewReader([]byte("<html>")),
		iotest.ErrReader(errors.New("connection reset")),
	)

	if _, err := readBoundedBody(broken); !errors.Is(err, ErrUpstream) {
		t.Fatalf("expected ErrUpstream, got %v", err)
	}
}

// TestFetchTimesOutOnSlowResponseHeaders is the Slowloris case: the connection
// is accepted and then nothing is sent.
func TestFetchTimesOutOnSlowResponseHeaders(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, "<html></html>")
	}))
	t.Cleanup(func() {
		close(release)
		server.Close()
	})

	connector := &recordingConnector{target: strings.TrimPrefix(server.URL, "http://")}
	target, err := canonicalURL("http://example.com/page")
	if err != nil {
		t.Fatalf("canonicalURL: %v", err)
	}
	// A budget far below responseHeaderTimeout, so the test is quick and still
	// exercises the classification a stalled server produces.
	fetcher := newFetcherWith(150*time.Millisecond, fixedResolver(publicAddr), connector.connect)

	_, _, err = fetcher.fetch(context.Background(), target)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("expected a timeout, got %v", err)
	}
}

func TestFetchDoesNotLeakUpstreamDetail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(server.Close)

	_, _, _, err := fetchAgainst(t, server, fixedResolver(publicAddr), "http://example.com/page")
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, forbidden := range []string{publicAddr, "example.com", "418", "teapot", "127.0.0.1"} {
		if strings.Contains(strings.ToLower(err.Error()), strings.ToLower(forbidden)) {
			t.Fatalf("error leaked %q: %v", forbidden, err)
		}
	}
}

// TestFetchSendsNoCallerIdentity checks that nothing about the requester or the
// deployment travels to the remote host.
func TestFetchSendsNoCallerIdentity(t *testing.T) {
	var received http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = r.Header.Clone()
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, "<html></html>")
	}))
	t.Cleanup(server.Close)

	if _, _, _, err := fetchAgainst(
		t, server, fixedResolver(publicAddr), "http://example.com/page",
	); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	for _, header := range []string{"Authorization", "Cookie", "Referer", "X-Forwarded-For"} {
		if received.Get(header) != "" {
			t.Fatalf("%s must not be sent, got %q", header, received.Get(header))
		}
	}
	if !strings.HasPrefix(received.Get("User-Agent"), "nchat-linkpreview/") {
		t.Fatalf("unexpected user agent %q", received.Get("User-Agent"))
	}
}

// TestFetchVerifiesTLS is the assertion that certificate validation was not
// traded away to make connecting to a validated address work.
//
// httptest's server presents a self-signed certificate. A client with
// InsecureSkipVerify — or one that suppressed verification because the
// connection was made to an address rather than a name — would read this page
// happily. This one must refuse it.
func TestFetchVerifiesTLS(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, "<html><head><title>should not be trusted</title></head></html>")
	}))
	t.Cleanup(server.Close)

	connector := &recordingConnector{target: strings.TrimPrefix(server.URL, "https://")}
	target, err := canonicalURL("https://example.com/page")
	if err != nil {
		t.Fatalf("canonicalURL: %v", err)
	}
	fetcher := newFetcherWith(5*time.Second, fixedResolver(publicAddr), connector.connect)

	_, body, err := fetcher.fetch(context.Background(), target)
	if err == nil {
		t.Fatalf("expected an untrusted certificate to be refused, read %q", body)
	}
	if !errors.Is(err, ErrUpstream) {
		t.Fatalf("expected an upstream error, got %v", err)
	}
	// The handshake happened against the hostname from the URL, not the address
	// that was dialled: certificate verification is still name-based.
	if dialed := connector.addresses(); len(dialed) != 1 ||
		dialed[0] != net.JoinHostPort(publicAddr, "443") {
		t.Fatalf("expected one dial to the validated address, got %v", dialed)
	}
}

func TestCheckContentType(t *testing.T) {
	if err := checkContentType("text/html;charset=ISO-8859-1"); err != nil {
		t.Fatalf("expected a charset parameter to be ignored, got %v", err)
	}
	if err := checkContentType("text/htmlx"); !errors.Is(err, ErrUnsupportedContentType) {
		t.Fatalf("expected a near-miss type to be refused, got %v", err)
	}
}
