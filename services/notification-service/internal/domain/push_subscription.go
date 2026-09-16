package domain

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Status is the lifecycle state of one Web Push subscription.
//
// Three states and no more. Anything a fourth would express — "retrying",
// "degraded" — is already carried by FailureCount, and a state nothing decides
// on is a state that drifts out of agreement with the column that does.
type Status string

const (
	// StatusActive is deliverable.
	StatusActive Status = "active"
	// StatusInvalid means the push service reported the endpoint gone. Terminal
	// for delivery; the row is kept so reconcile and diagnosis can see why.
	StatusInvalid Status = "invalid"
	// StatusDisabled means the owner turned this subscription off.
	StatusDisabled Status = "disabled"
)

// InvalidationReason says why a subscription stopped being deliverable.
//
// A closed set rather than free text, because the alternative is a column that
// ends up holding whatever a push service wrote in a response body — which is
// remote content, in a table this service logs and serialises.
type InvalidationReason string

const (
	// ReasonNotFound is the provider's 404: it does not know this endpoint.
	ReasonNotFound InvalidationReason = "not_found"
	// ReasonGone is the provider's 410: the subscription was revoked.
	ReasonGone InvalidationReason = "gone"
	// ReasonUserDisabled is the owner's own decision.
	ReasonUserDisabled InvalidationReason = "user_disabled"
)

// Outcome is what one delivery attempt authorises the lifecycle to do.
type Outcome int

const (
	// OutcomeSucceeded: the push service accepted the message.
	OutcomeSucceeded Outcome = iota
	// OutcomeTransient: this attempt failed and the next one may not. The
	// subscription stays active; scheduling the retry is the delivery layer's
	// job, not this package's.
	OutcomeTransient
	// OutcomeInvalidated: the endpoint will never accept another message.
	OutcomeInvalidated
)

// DeliveryResult is a classified attempt. Reason is set only for
// OutcomeInvalidated.
type DeliveryResult struct {
	Outcome Outcome
	Reason  InvalidationReason
}

// DeliveryApplication is what happened when an attempt's outcome was applied to
// the subscription it was about.
//
// Every lifecycle write is a compare-and-set on (id, generation, status), so
// "it changed nothing" is an ordinary result. What it is not is a single fact:
// the subscription may have rotated to a live new endpoint, or been disabled,
// or already been retired, or deleted — and a caller holding a 410 has to tell
// the first of those from the rest. Retiring nothing because a *newer* endpoint
// replaced the dead one is not a reason to give up on the notification; it is a
// reason to look again.
type DeliveryApplication int

const (
	// ApplicationRecorded: the compare-and-set matched. The answer was written
	// to the endpoint lifetime it was actually about.
	ApplicationRecorded DeliveryApplication = iota
	// ApplicationSuperseded: nothing matched, and the subscription is active
	// now. The answer describes an endpoint the browser has already replaced,
	// and the replacement is live and has not been delivered to.
	ApplicationSuperseded
	// ApplicationInactive: nothing matched, and the subscription is not active.
	// Disabled by its owner or already retired: nothing to retire, and nothing
	// to deliver to either.
	ApplicationInactive
	// ApplicationMissing: the subscription no longer exists.
	ApplicationMissing
)

// Deliverable reports whether this subscription still has a live endpoint that
// the notification has not reached.
//
// Only ApplicationSuperseded does. It is the one outcome that means "the answer
// you brought is about something that no longer exists, and something that does
// exist is still owed a delivery" — which is what stops a stale 404 or 410 from
// retiring a notification that a rotated browser could still receive.
func (a DeliveryApplication) Deliverable() bool { return a == ApplicationSuperseded }

// ClassifyDeliveryStatus maps a push service's HTTP status onto the transition
// it authorises (RFC 8030 §5, and the Web Push protocol's use of 404/410).
//
// Only 404 and 410 may retire a subscription. Everything else — 429 from a rate
// limiter, any 5xx, and status 0, which is how the caller reports "no response
// arrived at all": a timeout, a reset, a DNS failure — is transient. Treating
// any of those as permanent would silently unsubscribe real people during a
// provider incident, and nothing in the response distinguishes "your endpoint is
// dead" from "we are having a bad afternoon" except these two codes.
func ClassifyDeliveryStatus(statusCode int) DeliveryResult {
	switch {
	case statusCode >= http.StatusOK && statusCode < http.StatusMultipleChoices:
		return DeliveryResult{Outcome: OutcomeSucceeded}
	case statusCode == http.StatusNotFound:
		return DeliveryResult{Outcome: OutcomeInvalidated, Reason: ReasonNotFound}
	case statusCode == http.StatusGone:
		return DeliveryResult{Outcome: OutcomeInvalidated, Reason: ReasonGone}
	default:
		return DeliveryResult{Outcome: OutcomeTransient}
	}
}

// PushSubscription is one persisted subscription, in the only shape anything
// above the storage layer sees.
//
// It is the reconcile answer and nothing else. Endpoint, p256dh and auth are
// absent because together they are the capability to push to somebody's browser;
// failure_count, last_success_at and invalidated_at are absent because they are
// operational history a client has no use for. What is never read cannot be
// logged, serialised or carried into a span by code written later — which is a
// stronger guarantee than remembering to strip it.
type PushSubscription struct {
	ID       string
	DeviceID string
	// Generation names the endpoint/key lifetime this row currently holds. It
	// is the one field here that is not part of the reconcile answer: a delivery
	// attempt has to capture it alongside the id, and hand both back, so that an
	// answer describing a replaced endpoint cannot be applied to the one that
	// replaced it. The HTTP layer deliberately does not serialise it — it is a
	// concurrency token, not something a browser can act on.
	Generation int64
	Status     Status
	CreatedAt  time.Time
	LastSeenAt time.Time
}

// Registration is the client-supplied half of a registration request.
//
// Exactly the four fields a browser's PushSubscription carries. There is no
// UserID, WorkspaceID, Status, FailureCount or timestamp here, and that absence
// is the mass-assignment control: a field that does not exist on the type cannot
// be bound from a body, whatever the body contains.
type Registration struct {
	DeviceID string
	Endpoint string
	P256dh   string
	Auth     string
}

// Client-supplied bounds. All three are protocol facts rather than taste:
//
//   - an endpoint is a URL, capped at the same 2048 bytes this repository
//     already uses for a client-submitted URL (file-service's
//     linkpreview.MaxURLLength), which is also comfortably inside a btree entry
//     so the unique index on endpoint cannot fail on size;
//   - p256dh is an uncompressed P-256 point: 65 bytes, RFC 8291 §4;
//   - auth is a 16-byte authentication secret, RFC 8291 §3.
//
// The key lengths are exact, so a malformed key is refused here rather than
// discovered by a delivery attempt months later.
const (
	MaxDeviceIDBytes = 128
	MaxEndpointBytes = 2048
	// MaxKeyBytes bounds the encoded form of either key before it is decoded,
	// so a huge string is rejected without being base64-decoded first.
	MaxKeyBytes = 128

	p256dhBytes = 65
	authBytes   = 16
	// uncompressedPointPrefix is X9.62's tag for an uncompressed EC point.
	uncompressedPointPrefix = 0x04
)

// ErrInvalidRegistration is every rejection of a client-supplied registration.
//
// One error for all of them on purpose: the handler turns it into a single fixed
// message. Which field was wrong is a detail the caller's own code already
// knows, and spelling it out is how a validation endpoint becomes an oracle.
var ErrInvalidRegistration = errors.New("invalid push subscription registration")

// deviceIDPattern bounds the browser/device instance identifier the client
// mints. It is an opaque token this service only ever compares, so the alphabet
// is restricted to what a UUID or a random base64url string needs — enough for
// every client, and narrow enough that the value can never carry a control
// character, whitespace or markup into a log line or a JSON response.
var deviceIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

// Validate reports whether the registration may be persisted.
func (r Registration) Validate() error {
	if !deviceIDPattern.MatchString(r.DeviceID) {
		return ErrInvalidRegistration
	}
	if err := ValidateEndpoint(r.Endpoint); err != nil {
		return err
	}
	if !validKey(r.P256dh, p256dhBytes) || !isUncompressedPoint(r.P256dh) {
		return ErrInvalidRegistration
	}
	if !validKey(r.Auth, authBytes) {
		return ErrInvalidRegistration
	}
	return nil
}

// ValidateEndpoint accepts only an absolute https URL naming a host.
//
// Exported because the delivery layer applies it a second time, to the endpoint
// it just read from the table rather than to the one a client just sent (issue
// #746). Nothing in the write path can produce a row that fails it, so the
// second call is defence in depth against a row that arrived some other way —
// a restored dump, a repair script — reaching an http: or schemeless
// destination through a client that would happily dereference it.
func ValidateEndpoint(endpoint string) error {
	if endpoint == "" || len(endpoint) > MaxEndpointBytes {
		return ErrInvalidRegistration
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || !isPushEndpointURL(parsed) {
		return ErrInvalidRegistration
	}
	return nil
}

// isPushEndpointURL holds a parsed endpoint to the four properties a push
// endpoint has.
//
// https is not a preference: RFC 8030 push endpoints are https, and accepting
// any other scheme would persist a destination the delivery layer would later be
// asked to request — including file: and http: targets inside the cluster.
// Userinfo is refused because credentials do not belong in a stored endpoint,
// and a fragment because a push service never addresses one.
//
// The host test is Hostname() and not Host, and the difference is the whole
// point: url.Parse accepts an authority with only a port, so "https://:443/s"
// has a non-empty Host and no host at all. That would have been stored as a
// perfectly valid-looking endpoint that resolves to nothing — or, once a
// delivery client dereferences it, to whatever the local default is.
func isPushEndpointURL(parsed *url.URL) bool {
	return parsed.Scheme == "https" &&
		parsed.Hostname() != "" &&
		parsed.User == nil &&
		parsed.Fragment == ""
}

// validKey checks that value decodes to exactly want bytes.
func validKey(value string, want int) bool {
	if value == "" || len(value) > MaxKeyBytes {
		return false
	}
	decoded, ok := decodeKey(value)
	return ok && len(decoded) == want
}

// isUncompressedPoint checks p256dh's leading X9.62 tag. Length alone would
// accept 65 arbitrary bytes; a real ECDH public key always starts with 0x04.
func isUncompressedPoint(value string) bool {
	decoded, ok := decodeKey(value)
	return ok && len(decoded) > 0 && decoded[0] == uncompressedPointPrefix
}

// decodeKey reads a browser-supplied key.
//
// The Push API serialises keys as unpadded base64url, and that is the first
// alphabet tried. The standard alphabet is accepted as well because plenty of
// client code encodes the raw ArrayBuffer with btoa instead of the URL-safe
// variant, and refusing those would be rejecting a correct key over its
// transport encoding. Padding is optional in both.
func decodeKey(value string) ([]byte, bool) {
	trimmed := strings.TrimRight(value, "=")
	if decoded, err := base64.RawURLEncoding.DecodeString(trimmed); err == nil {
		return decoded, true
	}
	decoded, err := base64.RawStdEncoding.DecodeString(trimmed)
	return decoded, err == nil
}
