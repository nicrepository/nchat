package urlsafety

import "errors"

// Why an exchange failed, as a closed set (issue #928 §14).
//
// # Why an error carries a label at all
//
// With one provider, "the provider failed" was enough: there was nothing to
// choose between and one counter said how often it happened. With a primary and
// a fallback there is a decision behind every failure — an exhausted Web Risk
// quota, a key the project rejects, and a Cloudflare hostname refusal each send
// the pipeline down the same path but need completely different remedies, and an
// operator who cannot tell them apart cannot act on any of them.
//
// # Why it is not the provider's message
//
// A provider's own text is unbounded, attacker-influenceable through the URL
// submitted, and occasionally carries the request it describes. Putting it in a
// metric label would be an unbounded-cardinality series; putting it in a log
// would be the provider's payload in this deployment's logs. So every adapter
// normalises to one of the values below before anything leaves it, and the
// original string is never stored, wrapped or returned.
const (
	// ReasonUnavailable is the unclassified failure: a status the contract does
	// not name, an outage, a transport error.
	ReasonUnavailable = "unavailable"
	// ReasonTimeout is the provider not answering inside the client's budget.
	ReasonTimeout = "timeout"
	// ReasonRateLimited is a quota refusal — HTTP 429. It feeds the breaker
	// rather than being retried immediately, because retrying is what makes it
	// last longer.
	ReasonRateLimited = "rate_limited"
	// ReasonAuthError is a credential or project the provider refuses — 401 or
	// 403. Operationally closed: it is a configuration fault, not a transient
	// one, and hammering it changes nothing.
	ReasonAuthError = "auth_error"
	// ReasonMalformed is an answer this client cannot parse or cannot reconcile
	// with the contract — bad JSON, trailing data, a threat naming no type.
	ReasonMalformed = "malformed"
	// ReasonHostnameLimit is the Cloudflare refusal that motivated issue #928:
	// the hostname was scanned too recently to scan again. It is a refusal to
	// look, not a finding, and it is counted apart from every other failure
	// because it is the one an operator must never read as a verdict.
	ReasonHostnameLimit = "hostname_limit"
	// ReasonCircuitOpen is this deployment declining to ask, because the last
	// several exchanges failed.
	ReasonCircuitOpen = "circuit_open"
)

// Unexported aliases so the adapters read without the package-level prefix on
// every line. The exported names above are the vocabulary; these are the same
// values.
const (
	reasonUnavailable   = ReasonUnavailable
	reasonTimeout       = ReasonTimeout
	reasonRateLimited   = ReasonRateLimited
	reasonAuthError     = ReasonAuthError
	reasonMalformed     = ReasonMalformed
	reasonHostnameLimit = ReasonHostnameLimit
)

// reasonedError is an ErrUnavailable that remembers which kind it was.
//
// It wraps ErrUnavailable rather than replacing it, so every existing caller —
// and there are several, in two services — keeps working unchanged through
// errors.Is. The reason is additive: available to whoever asks for it, invisible
// to whoever does not.
type reasonedError struct {
	reason string
}

func (e reasonedError) Error() string { return "url safety: verdict unavailable: " + e.reason }

// Unwrap is what makes errors.Is(err, ErrUnavailable) true.
func (e reasonedError) Unwrap() error { return ErrUnavailable }

// Reason names the closed category.
func (e reasonedError) Reason() string { return e.reason }

// unavailable builds a failure carrying a closed reason.
func unavailable(reason string) error { return reasonedError{reason: reason} }

// FailureReason reports the closed category of a failed exchange, for a metric
// label or a structured log field.
//
// It never returns the provider's own words and never returns an open value: an
// error this package did not label reports ReasonUnavailable, which is the
// honest answer for "it failed and nothing said how".
func FailureReason(err error) string {
	var reasoned reasonedError
	switch {
	case errors.As(err, &reasoned):
		return reasoned.Reason()
	case errors.Is(err, ErrCircuitOpen):
		return ReasonCircuitOpen
	default:
		return ReasonUnavailable
	}
}
