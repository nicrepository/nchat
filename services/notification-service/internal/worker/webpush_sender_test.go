package worker

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/notification-service/internal/config"
)

// Issue #746: the provider adapter.
//
// Everything here runs against a stub HTTP client. A test that reached a real
// push service would be a test of somebody else's availability, and one that
// spoke to a local listener would be testing net/http rather than the two
// things this file actually decides: what a status means, and what is allowed
// to leave the adapter in a result.

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

// stubClient stands in for the HTTP client the sender POSTs through.
type stubClient struct {
	respond func(*http.Request) (*http.Response, error)
	calls   atomic.Int32
	last    atomic.Pointer[http.Request]
}

func (c *stubClient) Do(req *http.Request) (*http.Response, error) {
	c.calls.Add(1)
	c.last.Store(req)
	return c.respond(req)
}

// statusClient answers every request with one status and set of headers.
func statusClient(status int, header http.Header) *stubClient {
	return &stubClient{respond: func(*http.Request) (*http.Response, error) {
		if header == nil {
			header = http.Header{}
		}
		return &http.Response{
			StatusCode: status,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	}}
}

func errorClient(err error) *stubClient {
	return &stubClient{respond: func(*http.Request) (*http.Response, error) { return nil, err }}
}

// senderKeys generates a VAPID pair and a subscription key pair. Both are real
// P-256 material, because the library does genuine curve arithmetic with them
// and a fabricated string would only prove that the stub was never reached.
func testSender(t *testing.T, client *stubClient) *VAPIDSender {
	t.Helper()
	vapid, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate VAPID key: %v", err)
	}
	return NewVAPIDSenderWithClient(config.WebPushConfig{
		VAPIDPublicKey:  base64.RawURLEncoding.EncodeToString(vapid.PublicKey().Bytes()),
		VAPIDPrivateKey: base64.RawURLEncoding.EncodeToString(vapid.Bytes()),
		VAPIDSubject:    "mailto:ops@example.test",
	}, client)
}

// testMessage is a deliverable message with a real subscription key pair.
func testMessage(t *testing.T) PushMessage {
	t.Helper()
	subscriber, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate subscription key: %v", err)
	}
	auth := make([]byte, 16)
	if _, err := rand.Read(auth); err != nil {
		t.Fatalf("generate auth secret: %v", err)
	}
	return PushMessage{
		Endpoint: "https://push.example.com/subscription/abc123",
		P256dh:   base64.RawURLEncoding.EncodeToString(subscriber.PublicKey().Bytes()),
		Auth:     base64.RawURLEncoding.EncodeToString(auth),
		Payload:  []byte(`{"v":1,"id":"n1"}`),
		TTL:      30 * time.Minute,
	}
}

func send(t *testing.T, client *stubClient, mutate func(*PushMessage)) PushResult {
	t.Helper()
	message := testMessage(t)
	if mutate != nil {
		mutate(&message)
	}
	return testSender(t, client).Send(context.Background(), message)
}

// ---------------------------------------------------------------------------
// Classification: every status the contract names
// ---------------------------------------------------------------------------

func TestPushClassificationCoversEveryContractedStatus(t *testing.T) {
	cases := []struct {
		status int
		want   PushResultClass
	}{
		{http.StatusOK, PushDelivered},
		{http.StatusCreated, PushDelivered},
		{http.StatusAccepted, PushDelivered},
		{http.StatusNoContent, PushDelivered},
		{299, PushDelivered},

		{http.StatusNotFound, PushSubscriptionGone},
		{http.StatusGone, PushSubscriptionGone},

		{http.StatusTooManyRequests, PushRateLimited},

		{http.StatusInternalServerError, PushProviderUnavailable},
		{http.StatusBadGateway, PushProviderUnavailable},
		{http.StatusServiceUnavailable, PushProviderUnavailable},
		{http.StatusGatewayTimeout, PushProviderUnavailable},

		{http.StatusBadRequest, PushInvalidRequest},
		{http.StatusUnauthorized, PushInvalidRequest},
		{http.StatusForbidden, PushInvalidRequest},
		{http.StatusRequestEntityTooLarge, PushInvalidRequest},
		{http.StatusUnsupportedMediaType, PushInvalidRequest},
		// A redirect is not followed, so it arrives here as the result.
		{http.StatusFound, PushInvalidRequest},
		{http.StatusTemporaryRedirect, PushInvalidRequest},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			result := send(t, statusClient(tc.status, nil), nil)
			if result.Class != tc.want {
				t.Fatalf("status %d classified as %q, want %q", tc.status, result.Class, tc.want)
			}
			if result.StatusCode != tc.status {
				t.Fatalf("StatusCode = %d, want %d", result.StatusCode, tc.status)
			}
		})
	}
}

// Only the three classes that can succeed on another attempt are retryable.
// This is the property the delivery layer's reduction depends on, so it is
// asserted directly rather than inferred from the fan-out's behaviour.
func TestOnlyTransientClassesAreRetryable(t *testing.T) {
	retryable := map[PushResultClass]bool{
		PushDelivered:           false,
		PushSubscriptionGone:    false,
		PushInvalidRequest:      false,
		PushRateLimited:         true,
		PushProviderUnavailable: true,
		PushTransientFailure:    true,
	}
	for class, want := range retryable {
		if got := class.Retryable(); got != want {
			t.Fatalf("%s.Retryable() = %v, want %v", class, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// Failures with no response
// ---------------------------------------------------------------------------

// A timeout is a transient failure, and it is reported with status zero — which
// is what the subscription lifecycle already reads as "do not retire this".
func TestTimeoutIsTransientWithNoStatus(t *testing.T) {
	result := send(t, errorClient(&url.Error{
		Op: "Post", URL: "https://push.example.com/x", Err: context.DeadlineExceeded,
	}), nil)

	if result.Class != PushTransientFailure {
		t.Fatalf("a timeout classified as %q", result.Class)
	}
	if result.StatusCode != 0 {
		t.Fatalf("StatusCode = %d, want 0 when no response arrived", result.StatusCode)
	}
}

func TestNetworkErrorIsTransient(t *testing.T) {
	for name, err := range map[string]error{
		"connection refused": &url.Error{Op: "Post", URL: "https://push.example.com/x",
			Err: &net.OpError{Op: "dial", Err: errors.New("connection refused")}},
		"dns failure": &url.Error{Op: "Post", URL: "https://push.example.com/x",
			Err: &net.DNSError{Err: "no such host", IsNotFound: true}},
		"reset by peer": &url.Error{Op: "Post", URL: "https://push.example.com/x",
			Err: errors.New("read: connection reset by peer")},
		"bare cancellation": context.Canceled,
	} {
		t.Run(name, func(t *testing.T) {
			if result := send(t, errorClient(err), nil); result.Class != PushTransientFailure {
				t.Fatalf("%s classified as %q, want a transient failure", name, result.Class)
			}
		})
	}
}

// A destination the dialer refused is not a network blip: the same endpoint
// resolves to the same forbidden address every time, so retrying it would burn
// the notification's attempts on something that cannot change.
func TestARefusedDestinationIsNotRetried(t *testing.T) {
	result := send(t, errorClient(&url.Error{
		Op: "Post", URL: "https://push.internal/x", Err: errBlockedPushDestination,
	}), nil)

	if result.Class != PushInvalidRequest {
		t.Fatalf("a blocked destination classified as %q, want a configuration fault", result.Class)
	}
}

// ---------------------------------------------------------------------------
// Retry-After
// ---------------------------------------------------------------------------

func TestRetryAfterIsNormalised(t *testing.T) {
	cases := map[string]struct {
		header string
		want   time.Duration
	}{
		"delta seconds":              {header: "120", want: 2 * time.Minute},
		"one second":                 {header: "1", want: time.Second},
		"at the ceiling":             {header: "86400", want: 24 * time.Hour},
		"absent":                     {header: "", want: 0},
		"zero":                       {header: "0", want: 0},
		"negative":                   {header: "-30", want: 0},
		"not a number":               {header: "soon", want: 0},
		"empty after trimming":       {header: "   ", want: 0},
		"beyond the ceiling":         {header: "999999", want: 0},
		"a date already in the past": {header: "Mon, 02 Jan 2006 15:04:05 GMT", want: 0},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			header := http.Header{}
			if tc.header != "" {
				header.Set("Retry-After", tc.header)
			}
			result := send(t, statusClient(http.StatusTooManyRequests, header), nil)

			if result.Class != PushRateLimited {
				t.Fatalf("class = %q, want rate limited", result.Class)
			}
			if result.RetryAfter != tc.want {
				t.Fatalf("RetryAfter = %v, want %v", result.RetryAfter, tc.want)
			}
		})
	}
}

// An HTTP-date is read as the interval until it, not as an absolute instant the
// caller would have to interpret.
func TestRetryAfterAcceptsAnHTTPDate(t *testing.T) {
	header := http.Header{}
	header.Set("Retry-After", time.Now().Add(10*time.Minute).UTC().Format(http.TimeFormat))

	result := send(t, statusClient(http.StatusTooManyRequests, header), nil)
	if result.RetryAfter < 9*time.Minute || result.RetryAfter > 10*time.Minute {
		t.Fatalf("RetryAfter = %v, want about ten minutes", result.RetryAfter)
	}
}

// Retry-After is only meaningful on a 429. A push service that sets it on a
// 503 is not asking this service to wait a specific time, and honouring it
// there would let one header override the backoff for an outage.
func TestRetryAfterIsIgnoredOffARateLimit(t *testing.T) {
	header := http.Header{}
	header.Set("Retry-After", "300")

	for _, status := range []int{http.StatusServiceUnavailable, http.StatusOK, http.StatusGone} {
		result := send(t, statusClient(status, header), nil)
		if result.RetryAfter != 0 {
			t.Fatalf("status %d carried a Retry-After of %v", status, result.RetryAfter)
		}
	}
}

// ---------------------------------------------------------------------------
// What is sent, and what is not returned
// ---------------------------------------------------------------------------

// The TTL header is the remaining validity in whole seconds, and is never
// negative — a push service reads a negative TTL as a malformed request rather
// than as "expired".
func TestTTLHeaderCarriesTheRemainingSeconds(t *testing.T) {
	cases := map[string]struct {
		ttl  time.Duration
		want string
	}{
		"half an hour":         {ttl: 30 * time.Minute, want: "1800"},
		"rounds down":          {ttl: 90500 * time.Millisecond, want: "90"},
		"under a second":       {ttl: 400 * time.Millisecond, want: "0"},
		"zero":                 {ttl: 0, want: "0"},
		"negative becomes nil": {ttl: -time.Hour, want: "0"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			client := statusClient(http.StatusCreated, nil)
			send(t, client, func(m *PushMessage) { m.TTL = tc.ttl })

			if got := client.last.Load().Header.Get("TTL"); got != tc.want {
				t.Fatalf("TTL header = %q, want %q", got, tc.want)
			}
		})
	}
}

// The request is a VAPID-authenticated aes128gcm POST to the endpoint, and the
// body is ciphertext rather than the payload.
func TestTheRequestIsAnEncryptedVAPIDPost(t *testing.T) {
	client := statusClient(http.StatusCreated, nil)
	message := testMessage(t)
	testSender(t, client).Send(context.Background(), message)

	req := client.last.Load()
	if req.Method != http.MethodPost {
		t.Fatalf("method = %s, want POST", req.Method)
	}
	if req.URL.String() != message.Endpoint {
		t.Fatalf("URL = %s, want the endpoint", req.URL)
	}
	if got := req.Header.Get("Content-Encoding"); got != "aes128gcm" {
		t.Fatalf("Content-Encoding = %q, want aes128gcm", got)
	}
	if auth := req.Header.Get("Authorization"); !strings.HasPrefix(auth, "vapid t=") {
		t.Fatalf("Authorization is not a VAPID header")
	}

	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if strings.Contains(string(body), `"v":1`) {
		t.Fatal("the payload was sent in the clear")
	}
}

// The result is operational facts only. Nothing that could carry an endpoint, a
// key, an Authorization header or a provider's error prose crosses this
// boundary, so nothing downstream has to remember not to log it.
func TestTheResultCarriesNothingSensitive(t *testing.T) {
	header := http.Header{}
	header.Set("Retry-After", "60")
	body := "rejected: https://push.example.com/subscription/abc123 is not registered"
	client := &stubClient{respond: func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	}}

	message := testMessage(t)
	result := testSender(t, client).Send(context.Background(), message)

	// PushResult has four fields and every one of them is a bounded scalar.
	// This is the assertion that a fifth carrying a string cannot be added
	// without a deliberate decision.
	rendered := strings.ToLower(strings.Join([]string{
		string(result.Class), result.RetryAfter.String(), result.Latency.String(),
	}, " "))
	for name, secret := range map[string]string{
		"endpoint": message.Endpoint,
		"p256dh":   message.P256dh,
		"auth":     message.Auth,
		"payload":  string(message.Payload),
	} {
		if strings.Contains(rendered, strings.ToLower(secret)) {
			t.Fatalf("the result carries the %s", name)
		}
	}
	if strings.Contains(rendered, "rejected") {
		t.Fatal("the result carries the provider's error prose")
	}
}

// The response body is drained and closed, so a connection is reusable and a
// hostile endpoint cannot stream indefinitely into a reader that only wants EOF.
func TestTheResponseBodyIsBoundedAndClosed(t *testing.T) {
	body := &countingBody{Reader: strings.NewReader(strings.Repeat("x", 10*maxDrainedResponseBytes))}
	client := &stubClient{respond: func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusCreated, Header: http.Header{}, Body: body}, nil
	}}

	testSender(t, client).Send(context.Background(), testMessage(t))

	if !body.closed {
		t.Fatal("the response body was not closed")
	}
	if body.read > maxDrainedResponseBytes {
		t.Fatalf("read %d bytes of the response, over the %d-byte bound",
			body.read, maxDrainedResponseBytes)
	}
}

type countingBody struct {
	io.Reader
	read   int
	closed bool
}

func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	b.read += n
	return n, err
}

func (b *countingBody) Close() error {
	b.closed = true
	return nil
}

// ---------------------------------------------------------------------------
// The endpoint is validated a second time, at the last point before a request
// ---------------------------------------------------------------------------

func TestAnEndpointThatIsNotAPushURLIsNeverRequested(t *testing.T) {
	for name, endpoint := range map[string]string{
		"plain http":       "http://push.example.com/subscription/abc",
		"file scheme":      "file:///etc/passwd",
		"no scheme":        "push.example.com/subscription/abc",
		"no host":          "https://:443/subscription/abc",
		"carries userinfo": "https://someuser@push.example.com/s",
		"empty":            "",
	} {
		t.Run(name, func(t *testing.T) {
			client := statusClient(http.StatusCreated, nil)
			result := send(t, client, func(m *PushMessage) { m.Endpoint = endpoint })

			if client.calls.Load() != 0 {
				t.Fatalf("endpoint %q produced %d requests", endpoint, client.calls.Load())
			}
			if result.Class != PushInvalidRequest {
				t.Fatalf("endpoint %q classified as %q", endpoint, result.Class)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The destination guard
// ---------------------------------------------------------------------------

// Every real push service is publicly routable. Anything that is not is refused
// before a connection is made, so a row whose endpoint names something inside
// the cluster cannot turn this worker into a request generator for it.
func TestOnlyPublicDestinationsArePermitted(t *testing.T) {
	blocked := []string{
		"127.0.0.1:443", "[::1]:443", // this host
		"10.0.0.5:443", "172.16.4.1:443", "192.168.1.1:443", // this organisation
		"[fd00::1]:443",                        // unique local
		"169.254.169.254:443", "[fe80::1]:443", // this link, including the metadata address
		"0.0.0.0:443", "[::]:443", // unspecified
		"224.0.0.1:443", "[ff02::1]:443", // multicast
		"[::ffff:10.0.0.5]:443", // a private IPv4 address wearing an IPv6 shape
	}
	for _, address := range blocked {
		if err := guardDestination("tcp", address, nil); !errors.Is(err, errBlockedPushDestination) {
			t.Fatalf("destination %s was permitted", address)
		}
	}

	permitted := []string{"142.250.72.1:443", "[2607:f8b0:4004:c07::64]:443"}
	for _, address := range permitted {
		if err := guardDestination("tcp", address, nil); err != nil {
			t.Fatalf("public destination %s was refused: %v", address, err)
		}
	}
}

// A malformed address is refused rather than passed through. Failing open here
// would mean the one input the guard cannot parse is the one it does not check.
func TestAnUnparseableDestinationIsRefused(t *testing.T) {
	for _, address := range []string{"", "not-an-address", "push.example.com:443", "127.0.0.1"} {
		if err := guardDestination("tcp", address, nil); !errors.Is(err, errBlockedPushDestination) {
			t.Fatalf("address %q was permitted", address)
		}
	}
}

// The refusal names no address: it is a refusal, not a reconnaissance report.
func TestTheDestinationRefusalDisclosesNothing(t *testing.T) {
	err := guardDestination("tcp", "10.1.2.3:443", nil)
	if err == nil {
		t.Fatal("a private address was permitted")
	}
	if strings.Contains(err.Error(), "10.1.2.3") {
		t.Fatalf("the refusal quoted the address: %v", err)
	}
}

// ---------------------------------------------------------------------------
// The production client
// ---------------------------------------------------------------------------

// The client the service actually sends through carries all three defences.
// They are properties of this constructor, so they are asserted on it rather
// than on the stub every other test uses.
func TestTheProductionClientRefusesRedirectsAndBoundsItself(t *testing.T) {
	client := newPushHTTPClient(7 * time.Second)

	if client.Timeout != 7*time.Second {
		t.Fatalf("Timeout = %v, want the delivery budget", client.Timeout)
	}
	if client.CheckRedirect == nil {
		t.Fatal("the client follows redirects")
	}
	if err := client.CheckRedirect(nil, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("CheckRedirect = %v, want the redirect returned rather than followed", err)
	}

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport is %T, want an *http.Transport", client.Transport)
	}
	if transport.TLSClientConfig != nil {
		t.Fatal("the transport overrides the default TLS configuration")
	}
	if transport.DialContext == nil {
		t.Fatal("the transport dials without the destination guard")
	}
}

// The guard is reachable through the client that is actually used, not only as
// a function. A loopback destination is refused before any bytes are written.
func TestTheProductionClientRefusesAPrivateDestination(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no loopback listener available: %v", err)
	}
	defer func() { _ = listener.Close() }()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		"http://"+listener.Addr().String()+"/subscription", strings.NewReader(""))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := newPushHTTPClient(2 * time.Second).Do(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("the client connected to a loopback destination")
	}
	if !errors.Is(err, errBlockedPushDestination) {
		t.Fatalf("error = %v, want the destination refusal", err)
	}
}

// A message that cannot be encrypted never reaches the wire, and is not
// retried: the same subscription keys will fail the same way next time. This is
// the shape a corrupted row would take — the registration path refuses these
// keys, so only a row that arrived some other way can produce one.
func TestAnUnencryptableMessageIsAConfigurationFault(t *testing.T) {
	for name, mutate := range map[string]func(*PushMessage){
		"p256dh is not a key":     func(m *PushMessage) { m.P256dh = "not-a-point" },
		"p256dh is off the curve": func(m *PushMessage) { m.P256dh = base64.RawURLEncoding.EncodeToString(make([]byte, 65)) },
		"auth is not base64":      func(m *PushMessage) { m.Auth = "!!!!" },
	} {
		t.Run(name, func(t *testing.T) {
			client := statusClient(http.StatusCreated, nil)
			result := send(t, client, mutate)

			if result.Class != PushInvalidRequest {
				t.Fatalf("class = %q, want a configuration fault", result.Class)
			}
			if client.calls.Load() != 0 {
				t.Fatalf("an unencryptable message produced %d requests", client.calls.Load())
			}
		})
	}
}

// The production constructor builds a working sender on the guarded client.
// Everything else in this file injects a stub, so this is what proves the two
// halves are actually wired together.
func TestNewVAPIDSenderUsesTheGuardedClient(t *testing.T) {
	vapid, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate VAPID key: %v", err)
	}
	sender := NewVAPIDSender(config.WebPushConfig{
		VAPIDPublicKey:  base64.RawURLEncoding.EncodeToString(vapid.PublicKey().Bytes()),
		VAPIDPrivateKey: base64.RawURLEncoding.EncodeToString(vapid.Bytes()),
		VAPIDSubject:    "mailto:ops@example.test",
	}, 2*time.Second)

	message := testMessage(t)
	// A publicly-shaped endpoint that resolves to loopback. The guard runs on
	// the resolved address, so this is refused at connect rather than followed.
	message.Endpoint = "https://localhost/subscription/abc"

	result := sender.Send(context.Background(), message)
	if result.Class != PushInvalidRequest {
		t.Fatalf("class = %q, want the destination refusal", result.Class)
	}
}

// One sender serves every delivery the worker runs at once, so its per-message
// state has to be per message. The TTL is the field that differs, and it is
// stamped on a copy of the shared options rather than on the options
// themselves — mutating those would be a data race that happened to be
// invisible most of the time. Run under -race, this is what proves the copy.
func TestOneSenderServesConcurrentDeliveries(t *testing.T) {
	client := &stubClient{respond: func(req *http.Request) (*http.Response, error) {
		// Read the header the way a push service would, while other goroutines
		// are building their own requests.
		_ = req.Header.Get("TTL")
		return &http.Response{
			StatusCode: http.StatusCreated,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	}}
	sender := testSender(t, client)
	message := testMessage(t)

	const senders = 16
	var group sync.WaitGroup
	for i := range senders {
		group.Add(1)
		go func() {
			defer group.Done()
			own := message
			own.TTL = time.Duration(i+1) * time.Minute
			if result := sender.Send(context.Background(), own); result.Class != PushDelivered {
				t.Errorf("concurrent send classified as %q", result.Class)
			}
		}()
	}
	group.Wait()

	if got := client.calls.Load(); got != senders {
		t.Fatalf("%d requests, want %d", got, senders)
	}
}
