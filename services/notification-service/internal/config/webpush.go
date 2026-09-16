package config

import (
	"crypto/ecdh"
	"encoding/base64"
	"errors"
	"strings"

	platformconfig "github.com/nicrepository/nchat/libs/go/platform/config"
)

// Web Push delivery configuration (issue #746).
//
// Four values, and every one of them is required before a single push can be
// sent: the VAPID pair that identifies this application server to a push
// service (RFC 8292), the contact the push service is told to reach when
// something is wrong with our traffic, and how long a notification is worth
// delivering at all.
//
// There is deliberately no NOTIFICATION_WEBPUSH_ENABLED. A second switch would
// let a deployment be "enabled" with no usable keys, which is a configuration
// that can only fail at the moment it matters. Configured is enabled: the app
// wiring builds a deliverer when Ready reports true and hands the worker
// nothing when it does not, and a worker with no delivery channel refuses to
// start with a reason on the readiness probe.
const notificationDefaultPushTTLSeconds = 14400

// Why the keys are checked with crypto/ecdh rather than by their shape.
//
// A length and a leading 0x04 prove nothing. A 32-byte scalar can be zero or
// larger than the curve order; a 65-byte string beginning 0x04 can name a point
// that is not on P-256; and two individually valid keys can belong to different
// pairs. Every one of those passes a structural check, starts a worker, and
// fails at the first notification — where the failure is classified as a
// permanent delivery error and the outbox row is retired. A configuration
// mistake becomes lost notifications.
//
// crypto/ecdh answers all of it and none of it is written here:
// NewPrivateKey rejects the wrong length, the zero scalar and anything at or
// past the order; NewPublicKey rejects the wrong length, a compressed encoding
// and a point off the curve; and PublicKey.Equal decides whether the two are a
// pair. There is no elliptic-curve arithmetic in this package and there must
// never be.
var (
	errVAPIDPrivateKey = errors.New("vapid private key")
	errVAPIDPublicKey  = errors.New("vapid public key")
	errVAPIDMismatch   = errors.New("vapid key pair")
)

// WebPushConfig is what the Web Push delivery channel needs to exist.
type WebPushConfig struct {
	// VAPIDPublicKey and VAPIDPrivateKey are base64url, as every VAPID
	// generator emits them. They are secrets in the same sense the SMTP
	// password is: read from the environment, never logged, never returned in a
	// reason string.
	VAPIDPublicKey  string
	VAPIDPrivateKey string
	// VAPIDSubject is the `sub` claim of the VAPID JWT: a mailto: or https:
	// URI a push service can use to contact whoever operates this deployment.
	VAPIDSubject string
	// TTLSeconds is how long a notification is worth delivering, counted from
	// the instant the event occurred rather than from the attempt. It bounds
	// both halves of expiry: an event past it is never sent, and one inside it
	// carries the remainder as the push service's own TTL.
	TTLSeconds int
}

func loadWebPush() WebPushConfig {
	return WebPushConfig{
		VAPIDPublicKey:  strings.TrimSpace(platformconfig.GetString("NOTIFICATION_VAPID_PUBLIC_KEY", "")),
		VAPIDPrivateKey: strings.TrimSpace(platformconfig.GetString("NOTIFICATION_VAPID_PRIVATE_KEY", "")),
		VAPIDSubject:    strings.TrimSpace(platformconfig.GetString("NOTIFICATION_VAPID_SUBJECT", "")),
		TTLSeconds:      platformconfig.GetInt("NOTIFICATION_PUSH_TTL_SECONDS", notificationDefaultPushTTLSeconds),
	}.Normalized()
}

// Normalized bounds the TTL. The lower bound is a minute, because a shorter one
// expires events inside the worker's own retry ladder and would retire them
// before a provider outage could recover; the upper is a day, past which a
// "new message" notification is an artefact rather than news.
func (c WebPushConfig) Normalized() WebPushConfig {
	c.TTLSeconds = clampSeconds(c.TTLSeconds, 60, 86400, notificationDefaultPushTTLSeconds)
	return c
}

// Ready reports whether Web Push can be delivered, and names what is missing
// when it cannot.
//
// The reason is a fixed phrase naming a variable, never a value: a message that
// echoed a malformed key would put key material in a log line, which is the one
// place this package must never put it. Which of the two keys is wrong is said,
// because an operator needs it and it discloses nothing.
func (c WebPushConfig) Ready() (bool, string) {
	if c.VAPIDPublicKey == "" || c.VAPIDPrivateKey == "" || c.VAPIDSubject == "" {
		return false, "NOTIFICATION_VAPID_PUBLIC_KEY, NOTIFICATION_VAPID_PRIVATE_KEY and NOTIFICATION_VAPID_SUBJECT are required by Web Push delivery"
	}
	if reason := vapidPairReason(c.VAPIDPublicKey, c.VAPIDPrivateKey); reason != "" {
		return false, reason
	}
	if !validVAPIDSubject(c.VAPIDSubject) {
		return false, "NOTIFICATION_VAPID_SUBJECT must be a mailto: or https: URI"
	}
	return true, ""
}

// vapidPairReason names what is wrong with the key pair, or nothing.
//
// Which of the three it is gets said, because an operator needs it and it
// discloses nothing: "the private key is not a key", "the public key is not a
// point on P-256" and "these two are not each other's" are three different
// mistakes with three different fixes. What is never said is a byte of either
// key — the errors below are fixed sentences, and no decoded value reaches a
// string.
func vapidPairReason(publicKey, privateKey string) string {
	switch validateVAPIDPair(publicKey, privateKey) {
	case nil:
		return ""
	case errVAPIDPrivateKey:
		return "NOTIFICATION_VAPID_PRIVATE_KEY is not a valid P-256 private key"
	case errVAPIDPublicKey:
		return "NOTIFICATION_VAPID_PUBLIC_KEY is not a valid P-256 public key"
	default:
		return "NOTIFICATION_VAPID_PUBLIC_KEY is not the public key of NOTIFICATION_VAPID_PRIVATE_KEY"
	}
}

// validateVAPIDPair proves the configured keys are a usable P-256 pair.
//
// The private key is parsed first because it is the one that has to be a
// secret worth having: a scalar the curve refuses is not a key at all. The
// public key is parsed second, and then the two are compared by deriving the
// public key from the private one — which is the only check that catches the
// mistake nothing structural can, two perfectly good keys from different pairs.
func validateVAPIDPair(publicKey, privateKey string) error {
	privateBytes, ok := decodeVAPIDKey(privateKey)
	if !ok {
		return errVAPIDPrivateKey
	}
	private, err := ecdh.P256().NewPrivateKey(privateBytes)
	if err != nil {
		return errVAPIDPrivateKey
	}

	publicBytes, ok := decodeVAPIDKey(publicKey)
	if !ok {
		return errVAPIDPublicKey
	}
	public, err := ecdh.P256().NewPublicKey(publicBytes)
	if err != nil {
		return errVAPIDPublicKey
	}

	if !private.PublicKey().Equal(public) {
		return errVAPIDMismatch
	}
	return nil
}

// validVAPIDSubject accepts the two forms RFC 8292 §2.1 admits. A bare e-mail
// address is refused rather than repaired: the Web Push library would prepend
// "mailto:" to whatever it is given, including a URL with a scheme it does not
// recognise, and a subject that silently became nonsense is worse than one that
// refused to start.
func validVAPIDSubject(value string) bool {
	if strings.HasPrefix(value, "mailto:") {
		return len(value) > len("mailto:")
	}
	return strings.HasPrefix(value, "https://") && len(value) > len("https://")
}

// decodeVAPIDKey reads a key in the base64url alphabet, padded or not.
//
// URL-safe only, and that is a compatibility requirement rather than taste:
// webpush-go decodes VAPID keys with base64.URLEncoding and base64.RawURLEncoding
// and nothing else, so a key written in the standard alphabet would be accepted
// here and rejected there — a configuration that validates at startup and fails
// at the first send, which is the whole class of defect this validation exists
// to remove. Every VAPID generator emits base64url; this is what they emit.
func decodeVAPIDKey(value string) ([]byte, bool) {
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(value, "="))
	return decoded, err == nil
}
