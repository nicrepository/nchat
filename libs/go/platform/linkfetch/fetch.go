package linkfetch

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
	// MaxDocumentBytes is the ceiling on the HTML actually read. Open Graph
	// lives in <head>, so this is generous: parsing also stops at <body>, and
	// whichever bound is reached first ends the read. It is applied to the
	// *decoded* stream, so a compression bomb expands into this limit and no
	// further.
	MaxDocumentBytes = 512 << 10

	// MaxImageBytes is the ceiling on an og:image download, decoded bytes again.
	// A card thumbnail is a few hundred kilobytes at most; a page whose image is
	// larger than this simply gets a card without one.
	MaxImageBytes = 3 << 20

	// MaxRedirects bounds a chain. Three covers the http→https→www→canonical
	// shape real sites use and turns a redirect loop into a refusal. It is
	// small on purpose: under issue #807 every hop is a new destination that
	// needs its own clearance, so a longer chain is more waiting, not more
	// reach.
	MaxRedirects = 3

	// maxResponseHeaderBytes bounds what a server may send before the body
	// starts, so headers alone cannot exhaust memory.
	maxResponseHeaderBytes = 64 << 10

	// The per-phase budgets. They exist so a server that accepts a connection
	// and then says nothing is cut off at the phase it stalls in, rather than
	// holding a socket until the overall deadline. Slowloris ends here.
	dialTimeout           = 3 * time.Second
	tlsHandshakeTimeout   = 3 * time.Second
	responseHeaderTimeout = 4 * time.Second

	// DefaultTimeout is the whole-exchange budget when a caller supplies none.
	DefaultTimeout = 5 * time.Second

	// userAgent identifies the fetch. It is a fixed string carrying no user,
	// workspace or deployment identity.
	userAgent = "nchat-linkpreview/1.0 (+https://github.com/nicrepository/nchat)"
)

// Resolver looks a host up. It is a parameter so tests can drive the address
// policy deterministically, without DNS and without a network.
type Resolver func(ctx context.Context, host string) ([]netip.Addr, error)

// LookupAddrs is the production resolver.
func LookupAddrs(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// Connector opens the connection once the destination has been accepted. It is
// always given an address literal that AddrAllowed has just approved.
type Connector func(ctx context.Context, network, address string) (net.Conn, error)

// HopPolicy is the caller's veto over a redirect, asked after the address and
// URL rules have already accepted the hop. Returning an error refuses the hop;
// the whole fetch then fails with that error wrapped in ErrRedirectRefused, so
// the caller can tell "the destination itself was fine and I said no" from
// every other refusal. nil means SSRF policy only.
type HopPolicy func(next *url.URL) error

// Fetcher performs the controlled requests the features are allowed to make.
type Fetcher struct {
	client *http.Client
}

// NewFetcher builds the production fetcher. timeout bounds one whole exchange,
// redirects included; <= 0 selects DefaultTimeout.
func NewFetcher(timeout time.Duration) *Fetcher {
	return NewFetcherWith(timeout, LookupAddrs, (&net.Dialer{Timeout: dialTimeout}).DialContext)
}

// NewFetcherWith is NewFetcher with the resolver and the final connect step
// supplied.
//
// It exists so a test can exercise the real address policy — the resolver, the
// checks, every rule — and still have the accepted connection land on a local
// httptest server. Production always passes a plain net.Dialer, so there is no
// path in which the policy is applied to one address and the connection made
// to another.
func NewFetcherWith(timeout time.Duration, resolve Resolver, connect Connector) *Fetcher {
	if timeout <= 0 {
		timeout = DefaultTimeout
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
	return &Fetcher{client: &http.Client{Transport: transport, Timeout: timeout}}
}

// safeDialer is where the SSRF policy is enforced.
//
// Resolving and connecting happen here, in that order, with the check between
// them and the connection made to the address that was checked. Nothing
// re-resolves the name afterwards, so the address that was judged is the
// address that is used — which is what makes DNS rebinding and every other
// time-of-check/time-of-use variant inapplicable rather than merely unlikely.
type safeDialer struct {
	resolve Resolver
	connect Connector
}

// dial resolves, validates, and connects — in that order, with nothing between
// the validation and the connection that could change the destination.
func (d *safeDialer) dial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("%w: destination is not permitted", ErrURLNotAllowed)
	}
	addrs, err := ResolveAndValidate(ctx, d.resolve, host)
	if err != nil {
		return nil, err
	}
	return d.connectValidated(ctx, network, addrs, port)
}

// ResolveAndValidate returns the addresses host resolves to, and only if every
// one of them is a permitted destination.
//
// It fails closed across the whole answer: a name resolving to one public and
// one private address is refused outright, because accepting it would let an
// attacker win the race simply by being asked twice. Exported so the safety
// worker can ask the same question of a hostname before handing it to a public
// reputation provider.
func ResolveAndValidate(ctx context.Context, resolve Resolver, host string) ([]netip.Addr, error) {
	addrs, err := resolve(ctx, host)
	if err != nil {
		// A name that does not resolve is an upstream problem, not a policy
		// one, and the resolver's message is not repeated.
		return nil, fmt.Errorf("%w: host could not be resolved", ErrUpstream)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("%w: destination is not permitted", ErrURLNotAllowed)
	}
	for _, addr := range addrs {
		if !AddrAllowed(addr) {
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

// checkRedirect re-applies the URL rules at every hop and then asks the caller.
//
// The address policy needs no help here — each hop to a new host is a new
// dial through safeDialer, so a redirect into private space is refused by the
// same check as the original request. What this adds is the part the dialer
// cannot see: the scheme, the credentials, the port, how many hops have
// happened, and whether the caller's own policy vouches for the destination.
func checkRedirect(hop HopPolicy) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= MaxRedirects {
			return fmt.Errorf("%w: too many redirects", ErrUpstream)
		}
		if err := CheckRequestURL(req.URL); err != nil {
			return err
		}
		if hop == nil {
			return nil
		}
		if err := hop(req.URL); err != nil {
			return &hopRefusal{cause: err}
		}
		return nil
	}
}

// hopRefusal carries the caller's own reason for vetoing a redirect through the
// HTTP client, which would otherwise wrap it in a *url.Error naming the URL.
// Its message names nothing; its chain matches both ErrRedirectRefused and the
// caller's sentinel, so a caller can tell "wait for clearance" from "condemned".
type hopRefusal struct{ cause error }

func (r *hopRefusal) Error() string { return ErrRedirectRefused.Error() }

func (r *hopRefusal) Unwrap() []error { return []error{ErrRedirectRefused, r.cause} }

// fetchKind is what a request expects back: the media types it accepts and how
// much of the decoded body it will read.
type fetchKind struct {
	accept   string
	allowed  func(mediaType string) bool
	maxBytes int64
}

var (
	documentKind = fetchKind{
		accept:   "text/html",
		allowed:  func(mediaType string) bool { return mediaType == "text/html" },
		maxBytes: MaxDocumentBytes,
	}
	imageKind = fetchKind{
		accept:   "image/jpeg, image/png, image/gif",
		allowed:  func(mediaType string) bool { return strings.HasPrefix(mediaType, "image/") },
		maxBytes: MaxImageBytes,
	}
)

// FetchDocument performs the one HTML request a preview makes and returns the
// final URL and the whole body. hop may be nil.
func (f *Fetcher) FetchDocument(ctx context.Context, target *url.URL, hop HopPolicy) (*url.URL, []byte, error) {
	return f.fetch(ctx, target, hop, documentKind)
}

// FetchImage downloads an og:image under the image ceilings. The bytes are
// remote and untrusted; DeriveThumbnail is what turns them into something a
// browser may be shown. hop may be nil.
func (f *Fetcher) FetchImage(ctx context.Context, target *url.URL, hop HopPolicy) ([]byte, error) {
	_, body, err := f.fetch(ctx, target, hop, imageKind)
	return body, err
}

// fetch is a sequence of four decisions — build, send, judge the response, read
// it — each of which lives in its own function. The point of the split is that
// "how much of a body may be read" is a rule with its own consequences, and it
// should be readable and testable without a server in front of it.
func (f *Fetcher) fetch(ctx context.Context, target *url.URL, hop HopPolicy, kind fetchKind) (*url.URL, []byte, error) {
	if err := CheckRequestURL(target); err != nil {
		return nil, nil, err
	}
	request, err := newRequest(ctx, target, kind.accept)
	if err != nil {
		return nil, nil, err
	}
	// A shallow copy per exchange so the hop policy is request-scoped while the
	// transport — and its dialer — is shared. http.Client is a plain struct and
	// documents that copying it is fine.
	client := *f.client
	client.CheckRedirect = checkRedirect(hop)
	response, err := client.Do(request)
	if err != nil {
		return nil, nil, classifyTransportError(err)
	}
	defer func() { _ = response.Body.Close() }()

	if err := checkResponse(response, kind); err != nil {
		return nil, nil, err
	}
	body, err := readBoundedBody(response.Body, kind.maxBytes)
	if err != nil {
		return nil, nil, err
	}
	final := response.Request.URL
	if final == nil {
		final = target
	}
	return final, body, nil
}

// newRequest builds the one request a fetch makes.
//
// It carries only what the fetch needs. No cookie, no credential and no header
// derived from any caller's request: whatever is at the other end learns
// nothing about who asked.
func newRequest(ctx context.Context, target *url.URL, accept string) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("%w: url is malformed", ErrInvalidURL)
	}
	request.Header.Set("User-Agent", userAgent)
	request.Header.Set("Accept", accept)
	return request, nil
}

// checkResponse decides whether the response is worth reading at all.
//
// The declared length is a short-circuit and never the bound: it saves reading
// a body that has already announced itself as too large, and a server that lies
// about it or declares nothing is caught by readBoundedBody instead.
func checkResponse(response *http.Response, kind fetchKind) error {
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: unexpected upstream status", ErrUpstream)
	}
	if err := checkContentType(response.Header.Get("Content-Type"), kind.allowed); err != nil {
		return err
	}
	if response.ContentLength > kind.maxBytes {
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
func readBoundedBody(body io.Reader, maxBytes int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxBytes+1))
	if err != nil {
		return nil, classifyTransportError(err)
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%w: response is too large", ErrUpstream)
	}
	return data, nil
}

// checkContentType applies the allowlist.
//
// Parameters are stripped rather than matched on, so "text/html; charset=utf-8"
// is the same decision as "text/html", and the comparison is case-insensitive
// because the header is. An absent or unparseable type is refused: this package
// never guesses that unlabelled bytes are a document.
func checkContentType(header string, allowed func(string) bool) error {
	if strings.TrimSpace(header) == "" {
		return fmt.Errorf("%w: response declared no content type", ErrUnsupportedContentType)
	}
	mediaType, _, err := mime.ParseMediaType(header)
	if err != nil {
		return fmt.Errorf("%w: response content type is malformed", ErrUnsupportedContentType)
	}
	if !allowed(strings.ToLower(strings.TrimSpace(mediaType))) {
		return fmt.Errorf("%w: response media type is not accepted", ErrUnsupportedContentType)
	}
	return nil
}

// classifyTransportError turns whatever the client returned into one of this
// package's classes. The original message is dropped: it routinely names the
// address that was dialled, which is the one thing a caller must not learn.
func classifyTransportError(err error) error {
	var refusal *hopRefusal
	if errors.As(err, &refusal) {
		return refusal
	}
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
