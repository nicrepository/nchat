package linkpreview

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

const (
	// maxBodyBytes is the ceiling on the HTML actually read. Open Graph lives in
	// <head>, so this is generous: parsing also stops at <body>, and whichever
	// bound is reached first ends the read. It is applied to the *decoded*
	// stream, so a compression bomb expands into this limit and no further.
	maxBodyBytes = 512 << 10

	// maxRedirects bounds a chain. Five covers the http→https→www→canonical
	// shape real sites use and turns a redirect loop into a refusal.
	maxRedirects = 5

	// maxResponseHeaderBytes bounds what a server may send before the body
	// starts, so headers alone cannot exhaust memory.
	maxResponseHeaderBytes = 64 << 10

	// The per-phase budgets. They exist so a server that accepts a connection
	// and then says nothing is cut off at the phase it stalls in, rather than
	// holding a socket until the overall deadline. Slowloris ends here.
	dialTimeout           = 3 * time.Second
	tlsHandshakeTimeout   = 3 * time.Second
	responseHeaderTimeout = 4 * time.Second

	// allowedMediaType is the whole allowlist. A preview is read out of an HTML
	// document; nothing else is fetched and nothing else is interpreted as one.
	allowedMediaType = "text/html"

	// userAgent identifies the fetch. It is a fixed string carrying no user,
	// workspace or deployment identity.
	userAgent = "nchat-linkpreview/1.0 (+https://github.com/nicrepository/nchat)"
)

// resolver looks a host up. It is a field so tests can drive the address policy
// deterministically, without DNS and without a network.
type resolver func(ctx context.Context, host string) ([]netip.Addr, error)

// lookupAddrs is the production resolver.
func lookupAddrs(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// fetcher performs the one controlled request the feature is allowed to make.
type fetcher struct {
	client *http.Client
}

func newFetcher(timeout time.Duration, resolve resolver) *fetcher {
	return newFetcherWith(timeout, resolve, (&net.Dialer{Timeout: dialTimeout}).DialContext)
}

// newFetcherWith is newFetcher with the final connect step supplied.
//
// It exists so a test can exercise the real address policy — the resolver, the
// checks, every rule — and still have the accepted connection land on a local
// httptest server. Production always passes a plain net.Dialer, so there is no
// path in which the policy is applied to one address and the connection made
// to another.
func newFetcherWith(timeout time.Duration, resolve resolver, connect connector) *fetcher {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	dialer := &safeDialer{resolve: resolve, connect: connect}
	transport := &http.Transport{
		DialContext: dialer.dial,
		// Explicitly nil, and not by omission: http.ProxyFromEnvironment would
		// hand the connection to a proxy, which would then resolve the hostname
		// and connect on this service's behalf. That is exactly the decision
		// safeDialer exists to make, so no proxy may make it instead.
		Proxy: nil,
		// One document per request and nothing to reuse a connection for. Not
		// pooling them keeps the socket cost of a burst of previews bounded by
		// the requests in flight.
		DisableKeepAlives:      true,
		TLSHandshakeTimeout:    tlsHandshakeTimeout,
		ResponseHeaderTimeout:  responseHeaderTimeout,
		ExpectContinueTimeout:  time.Second,
		MaxResponseHeaderBytes: maxResponseHeaderBytes,
		// TLSClientConfig is left at its default on purpose: certificates are
		// verified, against the hostname from the URL. Connecting to a
		// validated IP does not change that — the transport still derives
		// ServerName from the request, so SNI and verification stay correct
		// without anything being skipped.
	}
	return &fetcher{client: &http.Client{
		Transport:     transport,
		Timeout:       timeout,
		CheckRedirect: checkRedirect,
	}}
}

// safeDialer is where the SSRF policy is enforced.
//
// Resolving and connecting happen here, in that order, with the check between
// them and the connection made to the address that was checked. Nothing
// re-resolves the name afterwards, so the address that was judged is the
// address that is used — which is what makes DNS rebinding and every other
// time-of-check/time-of-use variant inapplicable rather than merely unlikely.
type safeDialer struct {
	resolve resolver
	connect connector
}

// connector opens the connection once the destination has been accepted. It is
// always given an address literal that addrAllowed has just approved.
type connector func(ctx context.Context, network, address string) (net.Conn, error)

// dial resolves, validates, and connects — in that order, with nothing between
// the validation and the connection that could change the destination.
//
// The two halves are named but deliberately still adjacent: the security
// property is the relationship between them, so they are read together. What
// resolveAndValidate returns is a set of addresses, never a hostname, which is
// what makes it impossible for the connect step to re-resolve anything.
func (d *safeDialer) dial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("%w: destination is not permitted", ErrURLNotAllowed)
	}
	addrs, err := d.resolveAndValidate(ctx, host)
	if err != nil {
		return nil, err
	}
	return d.connectValidated(ctx, network, addrs, port)
}

// resolveAndValidate returns the addresses host resolves to, and only if every
// one of them is a permitted destination.
//
// It fails closed across the whole answer: a name resolving to one public and
// one private address is refused outright, because accepting it would let an
// attacker win the race simply by being asked twice.
func (d *safeDialer) resolveAndValidate(ctx context.Context, host string) ([]netip.Addr, error) {
	addrs, err := d.resolve(ctx, host)
	if err != nil {
		// A name that does not resolve is an upstream problem, not a policy
		// one, and the resolver's message is not repeated.
		return nil, fmt.Errorf("%w: host could not be resolved", ErrUpstream)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("%w: destination is not permitted", ErrURLNotAllowed)
	}
	for _, addr := range addrs {
		if !addrAllowed(addr) {
			return nil, fmt.Errorf("%w: destination is not permitted", ErrURLNotAllowed)
		}
	}
	return addrs, nil
}

// connectValidated dials the addresses that were just accepted.
//
// It takes []netip.Addr and not a hostname, and that signature is the point:
// there is nothing here that could be resolved, so the address the policy
// approved is necessarily the address the connection uses. Re-introducing a
// hostname at this step is what would re-introduce DNS rebinding.
func (d *safeDialer) connectValidated(
	ctx context.Context, network string, addrs []netip.Addr, port string,
) (net.Conn, error) {
	var lastErr error
	for _, addr := range addrs {
		conn, err := d.connect(ctx, network, net.JoinHostPort(addr.Unmap().String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// checkRedirect re-applies the URL rules at every hop.
//
// The address policy needs no help here — each hop to a new host is a new
// dial through safeDialer, so a redirect into private space is refused by the
// same check as the original request. What this adds is the part the dialer
// cannot see: the scheme, the credentials, the port, and how many hops have
// happened.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("%w: too many redirects", ErrUpstream)
	}
	return checkRequestURL(req.URL)
}

// fetch performs the request and returns the final URL and the whole body.
//
// It is a sequence of four decisions — build, send, judge the response, read it
// — each of which lives in its own function. The point of the split is that
// "how much of a body may be read" is a rule with its own consequences, and it
// should be readable and testable without a server in front of it.
func (f *fetcher) fetch(ctx context.Context, target *url.URL) (*url.URL, []byte, error) {
	request, err := newDocumentRequest(ctx, target)
	if err != nil {
		return nil, nil, err
	}
	response, err := f.client.Do(request)
	if err != nil {
		return nil, nil, classifyTransportError(err)
	}
	defer func() { _ = response.Body.Close() }()

	if err := checkResponse(response); err != nil {
		return nil, nil, err
	}
	body, err := readBoundedBody(response.Body)
	if err != nil {
		return nil, nil, err
	}
	final := response.Request.URL
	if final == nil {
		final = target
	}
	return final, body, nil
}

// newDocumentRequest builds the one request this feature makes.
//
// It carries only what a document fetch needs. No cookie, no credential and no
// header derived from the caller's request: whatever is at the other end learns
// nothing about who asked.
func newDocumentRequest(ctx context.Context, target *url.URL) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("%w: url is malformed", ErrInvalidURL)
	}
	request.Header.Set("User-Agent", userAgent)
	request.Header.Set("Accept", "text/html")
	return request, nil
}

// checkResponse decides whether the response is worth reading at all.
//
// The declared length is a short-circuit and never the bound: it saves reading
// a body that has already announced itself as too large, and a server that lies
// about it or declares nothing is caught by readBoundedBody instead.
func checkResponse(response *http.Response) error {
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: unexpected upstream status", ErrUpstream)
	}
	if err := checkContentType(response.Header.Get("Content-Type")); err != nil {
		return err
	}
	if response.ContentLength > maxBodyBytes {
		return fmt.Errorf("%w: response is too large", ErrUpstream)
	}
	return nil
}

// readBoundedBody returns the whole body, or refuses it for exceeding the
// limit. It never returns a partial one.
//
// That distinction is the whole function. A truncated document is not a short
// document: Open Graph tags sit near the top of a page, so a body cut off at
// the limit would still parse, still yield a title, and that fragment would
// then be cached and served as though the page had been read. Reading one byte
// past the limit is what makes "exactly at the limit" and "over the limit" two
// different answers rather than the same one.
//
// The limit applies to the decoded stream. The transport decompresses before
// this reader sees anything, so a compression bomb expands into the limit and
// is refused on its real size rather than on its compressed one.
func readBoundedBody(body io.Reader) ([]byte, error) {
	// maxBodyBytes+1 is a constant expression evaluated at compile time, so a
	// limit large enough to overflow the int64 io.LimitReader takes would fail
	// the build rather than silently wrap into a small or negative bound.
	data, err := io.ReadAll(io.LimitReader(body, maxBodyBytes+1))
	if err != nil {
		return nil, classifyTransportError(err)
	}
	if int64(len(data)) > maxBodyBytes {
		return nil, fmt.Errorf("%w: response is too large", ErrUpstream)
	}
	return data, nil
}

// checkContentType applies the allowlist.
//
// Parameters are stripped rather than matched on, so "text/html; charset=utf-8"
// is the same decision as "text/html", and the comparison is case-insensitive
// because the header is. An absent or unparseable type is refused: this service
// never guesses that unlabelled bytes are a document.
func checkContentType(header string) error {
	if strings.TrimSpace(header) == "" {
		return fmt.Errorf("%w: response declared no content type", ErrUnsupportedContentType)
	}
	mediaType, _, err := mime.ParseMediaType(header)
	if err != nil {
		return fmt.Errorf("%w: response content type is malformed", ErrUnsupportedContentType)
	}
	if !strings.EqualFold(strings.TrimSpace(mediaType), allowedMediaType) {
		return fmt.Errorf("%w: response is not html", ErrUnsupportedContentType)
	}
	return nil
}

// classifyTransportError turns whatever the client returned into one of this
// package's classes. The original message is dropped: it routinely names the
// address that was dialled, which is the one thing a caller must not learn.
func classifyTransportError(err error) error {
	switch {
	case errors.Is(err, ErrURLNotAllowed):
		return fmt.Errorf("%w: destination is not permitted", ErrURLNotAllowed)
	case errors.Is(err, ErrInvalidURL):
		return fmt.Errorf("%w: url is malformed", ErrInvalidURL)
	case errors.Is(err, context.DeadlineExceeded), isTimeout(err):
		return fmt.Errorf("%w: upstream did not answer in time", ErrTimeout)
	default:
		return fmt.Errorf("%w: upstream could not be read", ErrUpstream)
	}
}

func isTimeout(err error) bool {
	var timeout interface{ Timeout() bool }
	return errors.As(err, &timeout) && timeout.Timeout()
}
