package worker

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"

	"github.com/nicrepository/nchat/services/notification-service/internal/config"
	"github.com/nicrepository/nchat/services/notification-service/internal/domain"
)

// The Web Push provider adapter (issue #746).
//
// # What it is not
//
// It is not a policy. It does not know about work schedules, mutes, quiet
// hours, event types or recipients, and it cannot: nothing in PushMessage says
// who the message is for or why it is being sent. By the time anything reaches
// this file the decision has been made twice — once by the policy engine, once
// by the delivery layer that read the eligible endpoints — and all that is left
// is to encrypt, POST, and say what came back.
//
// # Why a library
//
// RFC 8291 message encryption and RFC 8292 VAPID are cryptographic protocols
// with a working, widely deployed Go implementation. Composing ECDH, HKDF and
// AES-GCM by hand here to avoid a dependency would be writing new cryptographic
// code to save a go.mod line, which is a bad trade at any exchange rate.
// github.com/SherClockHolmes/webpush-go is MIT, and its only two dependencies
// were already in this module's graph: golang-jwt/jwt/v5, which this service
// already uses to validate access tokens, and golang.org/x/crypto.
//
// The library is used for exactly one thing — encrypt and send — and every
// decision around it is made here: which HTTP client, which destinations are
// permitted, what a status means, what is allowed out in an error.

// PushResultClass is what one attempt against one endpoint amounted to.
//
// A closed set, and the only vocabulary that leaves this file. Every value is
// something the delivery layer acts on differently, which is what stops it
// being a taxonomy for its own sake.
type PushResultClass string

const (
	// PushDelivered: the push service accepted the message. It is not a
	// promise that a person saw it — no push service offers one — only that
	// this endpoint must not be sent to again for this notification.
	PushDelivered PushResultClass = "delivered"
	// PushSubscriptionGone: 404 or 410. The endpoint is finished. Retire the
	// subscription and stop trying it, here and for every future notification.
	PushSubscriptionGone PushResultClass = "permanent_subscription_failure"
	// PushRateLimited: 429. The endpoint is fine and we are asking too often.
	PushRateLimited PushResultClass = "rate_limited"
	// PushProviderUnavailable: 5xx. Their problem, and a temporary one.
	PushProviderUnavailable PushResultClass = "provider_unavailable"
	// PushTransientFailure: no response arrived at all — a timeout, a reset, a
	// DNS failure, a cancelled context.
	PushTransientFailure PushResultClass = "transient_failure"
	// PushInvalidRequest: any other 4xx, a refused destination, an
	// unencryptable message. Something about this request will be wrong in the
	// same way next time, so it is never retried.
	PushInvalidRequest PushResultClass = "invalid_payload_or_configuration"
)

// Retryable reports whether another attempt against this endpoint could
// succeed. The three transient classes are the retryable ones; delivered is
// done, and the other two are finished for opposite reasons.
func (c PushResultClass) Retryable() bool {
	switch c {
	case PushRateLimited, PushProviderUnavailable, PushTransientFailure:
		return true
	default:
		return false
	}
}

// PushMessage is one already-authorised delivery.
//
// Five fields, and none of them is an identifier. The sender cannot name the
// notification, the subscription, the recipient or the workspace, so it cannot
// log one, cannot make a decision that depends on one, and cannot become a
// second place where identity is reasoned about. Endpoint, P256dh and Auth are
// the capability to reach a browser and are never logged or returned.
type PushMessage struct {
	Endpoint string
	P256dh   string
	Auth     string
	Payload  []byte
	// TTL is how much longer the notification is worth delivering. The push
	// service holds an undelivered message for at most this long.
	TTL time.Duration
}

// PushResult is what came back, in operational terms only.
//
// Deliberately absent: the response body, the provider's error text, any
// header other than the one below, and the endpoint. A push service's error
// body quotes the request it is complaining about — which is to say the
// endpoint, and endpoints are capability URLs. Nothing that could carry one
// crosses this boundary, so nothing downstream has to remember not to log it.
type PushResult struct {
	Class PushResultClass
	// StatusCode is the response status, or zero when no response arrived. A
	// bounded integer is the whole of what the provider is allowed to say.
	StatusCode int
	// RetryAfter is the normalised Retry-After, set only for PushRateLimited
	// and only when the header was present and well formed. Zero means the
	// delivery layer's own backoff decides.
	RetryAfter time.Duration
	// Latency is how long the attempt took, response body drained included.
	Latency time.Duration
}

// PushSender is the seam the delivery layer is written against.
//
// One method, no error return: an attempt against a push service does not fail
// in a way the caller can distinguish from a result — a timeout *is* a result —
// and a second channel for failure would only invite one of the two to be
// handled and the other forgotten.
type PushSender interface {
	Send(ctx context.Context, message PushMessage) PushResult
}

// maxDrainedResponseBytes bounds what is read from a push service's response.
//
// The body is never used: the status is the answer. It is drained anyway so the
// connection can be reused rather than torn down, and drained through a limit
// so a hostile or broken endpoint cannot stream indefinitely into a reader that
// only wants to reach EOF.
const maxDrainedResponseBytes = 4096

// maxRetryAfterSeconds caps a Retry-After this service will honour.
//
// A day. Longer than any real push service asks for, and short enough that a
// malformed or hostile value cannot park a notification past its own TTL. The
// delivery layer clamps again against the worker's retry ceiling; this is the
// bound on what is even parsed.
const maxRetryAfterSeconds = 86400

// errBlockedPushDestination is returned by the dialer for an address the push
// client is not permitted to reach. It carries no address: the message is a
// refusal, not a reconnaissance report.
var errBlockedPushDestination = errors.New("push destination is not permitted")

// VAPIDSender encrypts and POSTs one notification to one endpoint.
type VAPIDSender struct {
	options webpush.Options
}

// NewVAPIDSender builds a sender on a validated configuration.
//
// cfg must already have passed WebPushConfig.Ready; the app wiring is what
// enforces that, and it declines to build a delivery channel at all when it has
// not. Nothing here re-reports a bad key, because nothing here should be
// reachable with one.
func NewVAPIDSender(cfg config.WebPushConfig, timeout time.Duration) *VAPIDSender {
	return NewVAPIDSenderWithClient(cfg, newPushHTTPClient(timeout))
}

// NewVAPIDSenderWithClient is NewVAPIDSender with the HTTP client supplied.
//
// It exists so the send path can be exercised against a stub instead of a push
// service. That is the only reason: a test that reached FCM would be a test of
// somebody else's availability, and one that spoke to a local listener would
// still be testing net/http rather than this file's classification.
func NewVAPIDSenderWithClient(cfg config.WebPushConfig, client webpush.HTTPClient) *VAPIDSender {
	return &VAPIDSender{
		options: webpush.Options{
			HTTPClient:      client,
			Subscriber:      cfg.VAPIDSubject,
			VAPIDPublicKey:  cfg.VAPIDPublicKey,
			VAPIDPrivateKey: cfg.VAPIDPrivateKey,
			// Normal, not high. A chat notification is worth waking a device
			// for on ordinary terms; declaring everything urgent is how a
			// deployment gets its urgency ignored.
			Urgency: webpush.UrgencyNormal,
		},
	}
}

// Send encrypts the payload for one subscription and posts it.
//
// The endpoint is validated again here, against the same rule the registration
// path applies. It is redundant by construction and kept anyway: it is the last
// point before an outbound request, and it costs one URL parse to guarantee
// that a row which reached the table by some other route than the API cannot
// send this client somewhere a browser would never have asked for.
func (s *VAPIDSender) Send(ctx context.Context, message PushMessage) PushResult {
	if err := domain.ValidateEndpoint(message.Endpoint); err != nil {
		return PushResult{Class: PushInvalidRequest}
	}

	started := time.Now()
	response, err := webpush.SendNotificationWithContext(ctx, message.Payload,
		subscriptionOf(message), s.optionsFor(message))
	if err != nil {
		return PushResult{Class: classifySendError(err), Latency: time.Since(started)}
	}
	// Drained and closed here rather than through a helper: the body is never
	// read for its content — the status is the answer — but a body left open
	// leaks a connection, and a body read without a bound lets a hostile
	// endpoint stream indefinitely into a reader that only wants EOF.
	defer func() {
		_, _ = io.CopyN(io.Discard, response.Body, maxDrainedResponseBytes)
		_ = response.Body.Close()
	}()

	return PushResult{
		Class:      classifyPushStatus(response.StatusCode),
		StatusCode: response.StatusCode,
		RetryAfter: retryAfter(response),
		Latency:    time.Since(started),
	}
}

// optionsFor copies the sender's options and stamps this message's TTL.
//
// A copy, because Options is shared across every concurrent send and the TTL is
// the one field that differs per message. Mutating the sender's own would be a
// data race that happened to be invisible most of the time.
func (s *VAPIDSender) optionsFor(message PushMessage) *webpush.Options {
	options := s.options
	options.TTL = ttlSeconds(message.TTL)
	return &options
}

// ttlSeconds converts the remaining validity into the header's integer seconds.
//
// Never negative: the delivery layer refuses an expired notification before it
// gets here, and a negative TTL would be rejected by the push service as a
// malformed request rather than understood as "expired". A remainder under a
// second becomes zero, which is the protocol's own way of saying "deliver now
// or discard" — exactly what a notification with milliseconds left means.
func ttlSeconds(ttl time.Duration) int {
	if ttl <= 0 {
		return 0
	}
	return int(ttl.Seconds())
}

func subscriptionOf(message PushMessage) *webpush.Subscription {
	return &webpush.Subscription{
		Endpoint: message.Endpoint,
		Keys:     webpush.Keys{P256dh: message.P256dh, Auth: message.Auth},
	}
}

// classifyPushStatus is the single authority on what a push service's status
// means (RFC 8030 §5, and the Web Push protocol's use of 404 and 410).
//
// One function, one switch, no second opinion anywhere in this package. The
// mapping onto the subscription lifecycle — which of these retires an endpoint
// — is domain.ClassifyDeliveryStatus, written by issue #745, and the delivery
// layer calls that rather than deriving it a second time from this.
func classifyPushStatus(status int) PushResultClass {
	switch {
	case status >= http.StatusOK && status < http.StatusMultipleChoices:
		return PushDelivered
	case status == http.StatusNotFound, status == http.StatusGone:
		return PushSubscriptionGone
	case status == http.StatusTooManyRequests:
		return PushRateLimited
	case status >= http.StatusInternalServerError:
		return PushProviderUnavailable
	default:
		// Every other 4xx, and every 3xx. A redirect reaches here because
		// redirects are not followed: a push service does not issue one, and an
		// endpoint that does is trying to send this client somewhere else.
		return PushInvalidRequest
	}
}

// classifySendError decides what a failure with no response means.
//
// The distinction is between "the request went out and nothing came back" and
// "the request could not be made". http.Client wraps everything on the wire in
// *url.Error, so that is the signature of the first; a context that ended is
// the same thing seen from this side. Anything else happened before the POST —
// an unencryptable message, a destination the dialer refused — and will happen
// again identically, so it is a configuration fault and not a retry.
func classifySendError(err error) PushResultClass {
	var urlErr *url.Error
	switch {
	case errors.Is(err, errBlockedPushDestination):
		return PushInvalidRequest
	case errors.As(err, &urlErr),
		errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, context.Canceled):
		return PushTransientFailure
	default:
		return PushInvalidRequest
	}
}

// retryAfter normalises the header on a rate-limited response.
//
// Both forms RFC 7231 §7.1.3 allows are read: delta-seconds, and an HTTP date
// converted to the remaining interval. A missing, malformed, negative or
// absurdly distant value yields zero, which tells the delivery layer to use its
// own backoff — the safe fallback, because a provider is never allowed to make
// this service wait less than its own policy says or longer than a day.
func retryAfter(response *http.Response) time.Duration {
	if response.StatusCode != http.StatusTooManyRequests {
		return 0
	}
	header := strings.TrimSpace(response.Header.Get("Retry-After"))
	if header == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(header); err == nil {
		return boundedRetryAfter(time.Duration(seconds) * time.Second)
	}
	when, err := http.ParseTime(header)
	if err != nil {
		return 0
	}
	return boundedRetryAfter(time.Until(when))
}

// boundedRetryAfter keeps a provider-supplied delay inside what this service
// will honour. Anything outside is discarded rather than clamped: a value the
// provider cannot have meant is not evidence of what it did mean.
func boundedRetryAfter(delay time.Duration) time.Duration {
	if delay <= 0 || delay > maxRetryAfterSeconds*time.Second {
		return 0
	}
	return delay
}

// newPushHTTPClient builds the only client this service sends push through.
//
// Three properties, each defending something different:
//
//   - Timeout bounds the whole exchange. The delivery context bounds it too,
//     and both are kept: a client with no timeout of its own is one deadline
//     away from holding a worker slot forever.
//   - CheckRedirect refuses to follow. Endpoints are chosen by browsers but
//     stored on our side, and a stored endpoint that answers 302 is either
//     broken or pointing this client at somewhere it was never asked to go.
//     The 3xx is returned as the result and classified as a configuration
//     fault.
//   - Control rejects destinations outside the public internet. Every real push
//     service is publicly routable; nothing legitimate is lost, and a row whose
//     endpoint names something inside the cluster cannot turn this worker into
//     an authenticated request generator for it.
//
// TLSClientConfig is left at its default, so certificates are verified against
// the hostname from the URL. Nothing here weakens that.
func newPushHTTPClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second, Control: guardDestination}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: timeout,
			ExpectContinueTimeout: time.Second,
			MaxIdleConnsPerHost:   2,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// guardDestination is net.Dialer's hook, called with the address the connection
// is about to be made to.
//
// It runs after resolution and before connect, on the literal that will be
// used, which is what makes DNS rebinding inapplicable rather than merely
// unlikely: there is no name left to re-resolve between the check and the
// connection.
func guardDestination(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return errBlockedPushDestination
	}
	addr, err := netip.ParseAddr(host)
	if err != nil || !isPublicAddr(addr) {
		return errBlockedPushDestination
	}
	return nil
}

// isPublicAddr reports whether an address is on the public internet.
//
// Unmap first, so an IPv4 address arriving as ::ffff:10.0.0.1 is judged as the
// private IPv4 address it is rather than as an unremarkable IPv6 one. The rest
// is the standard set of things that are not the public internet: this host,
// this link, this organisation, and everything that is not a single
// destination at all.
func isPublicAddr(addr netip.Addr) bool {
	addr = addr.Unmap()
	return addr.IsValid() &&
		!addr.IsUnspecified() &&
		!addr.IsLoopback() &&
		!addr.IsPrivate() &&
		!addr.IsLinkLocalUnicast() &&
		!addr.IsLinkLocalMulticast() &&
		!addr.IsInterfaceLocalMulticast() &&
		!addr.IsMulticast()
}
