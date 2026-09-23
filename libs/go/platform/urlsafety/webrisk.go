package urlsafety

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Google Web Risk, the primary reputation source (issue #928).
//
// # Why it displaced Cloudflare as the primary
//
// Cloudflare URL Scanner is a scanner, not a list: it fetches the page and
// forms an opinion, and it is entitled to decline. Production showed the two
// ways that makes it unusable as the *only* positive authority —
//
//	https://www.youtube.com/@YouTube  HTTP 200, task.success=false,
//	  hasVerdicts=false, "Refusing to scan: hostname was recently scanned or
//	  too many scans to hostname in the last days."
//	https://example.com/              HTTP 200, task.success=true,
//	  hasVerdicts=false
//
// — a refusal to scan at all, and a scan that ran and produced no verdict. Both
// are honestly inconclusive, and this package refuses to promote either into a
// clearance, so an ordinary link stayed an interstitial forever. The fix is not
// to weaken that rule. It is to ask a source that answers the question actually
// being asked.
//
// # The contract
//
//	GET https://webrisk.googleapis.com/v1/uris:search
//	    ?uri={canonical}&threatTypes=MALWARE&threatTypes=SOCIAL_ENGINEERING
//	X-Goog-Api-Key: {key}
//
// answering, on 200,
//
//	{}                                              -- no list names this URI
//	{"threat":{"threatTypes":["MALWARE"],"expireTime":"..."}}
//
// One request carries every configured threat type, so there is no partial
// consultation to reason about: either the whole question was asked and
// answered, or nothing was.
//
// # What SAFE means here, exactly
//
// "Not named by the Web Risk lists consulted at this instant." It is not a
// statement that the destination is harmless, and nothing in this package or its
// documentation may present it as one. That is a weaker claim than a scanner's
// clearance and a far more available one, which is the whole trade: a link that
// no list condemns becomes clickable, and the interstitial goes back to meaning
// "nobody could tell us".
//
// # Why the key is a header
//
// Google accepts the API key either as a `key` query parameter or in the
// X-Goog-Api-Key header. The header is the only acceptable one here: a key in a
// URL reaches every transport error message, every proxy access log and every
// stack trace that names the request. With the header, there is no string in
// this file's request path that a log could leak.
const (
	// webRiskBaseURL is the provider's address, not a deployment choice — the
	// same reasoning as cloudflareBaseURL. An operator-supplied endpoint would be
	// a way to point a security control at something that always answers "{}".
	webRiskBaseURL = "https://webrisk.googleapis.com/v1/uris:search"

	// webRiskTimeout bounds one lookup.
	//
	// Much shorter than the Cloudflare exchange, because the shapes differ: this
	// is a list lookup that either answers promptly or is not going to, and it
	// sits on the worker's critical path ahead of a fallback that also needs
	// time. Exceeding it is a failure, never a clearance.
	webRiskTimeout = 5 * time.Second
)

// webRiskThreatTypes is the question asked, and it is a constant rather than
// configuration for the same reason VerdictTTL is: which lists condemn a URL is
// the meaning of the verdict, not a per-deployment knob. Narrowing it in one
// environment would make "safe" mean something different there.
//
// MALWARE and SOCIAL_ENGINEERING are the two that describe a link a person was
// asked to click. UNWANTED_SOFTWARE is deliberately absent: it condemns
// installers rather than pages, and RF-21 blocks navigation.
var webRiskThreatTypes = []string{"MALWARE", "SOCIAL_ENGINEERING"}

// ProviderGoogleWebRisk is the Name() of this adapter.
const ProviderGoogleWebRisk = "google_webrisk"

// WebRiskProvider looks a canonical URL up in Google Web Risk.
//
// It implements URLReputationProvider and nothing else. It is synchronous —
// there is no scan to start and no id to poll — so it never returns
// ErrCheckInProgress and never issues a ProviderRef, which is the property the
// composition in fallback.go routes on.
type WebRiskProvider struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

// NewWebRiskProvider builds the lookup client. The key is required: a client
// without one could only ever produce failures, so it is refused here rather
// than at the first link somebody sends.
func NewWebRiskProvider(apiKey string) (*WebRiskProvider, error) {
	return newWebRiskProvider(webRiskBaseURL, apiKey, &http.Client{Timeout: webRiskTimeout})
}

// newWebRiskProvider is NewWebRiskProvider with the endpoint and HTTP client
// supplied, so a test can reach an httptest server without the production
// address being configurable.
func newWebRiskProvider(baseURL, apiKey string, client *http.Client) (*WebRiskProvider, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("url safety: google web risk api key is required")
	}
	if client == nil {
		client = &http.Client{Timeout: webRiskTimeout}
	}
	return &WebRiskProvider{baseURL: baseURL, apiKey: strings.TrimSpace(apiKey), client: client}, nil
}

// Name satisfies URLReputationProvider.
func (w *WebRiskProvider) Name() string { return ProviderGoogleWebRisk }

// webRiskResponse is the part of the answer this client reads.
//
// Threat is a pointer so "the lists named nothing" (`{}`) is distinguishable
// from "the field arrived empty", and threatTypes is read rather than assumed:
// a threat object that names no type is a contradiction, and a contradiction is
// refused rather than resolved in either direction.
type webRiskResponse struct {
	Threat *struct {
		ThreatTypes []string `json:"threatTypes"`
		// ExpireTime is when Google says the match stops being current, RFC3339.
		//
		// It bounds the condemnation it arrives with: a threat match may only
		// found a MALICIOUS verdict while the evidence is still current, so the
		// verdict's lifetime is the shorter of this and VerdictTTL. See
		// webRiskEvidenceExpiry for what an absent, unparseable or already-past
		// value means, and why none of those can turn a condemnation into a
		// clearance.
		ExpireTime string `json:"expireTime"`
	} `json:"threat"`
}

// Check looks the URL up and reports what the lists say.
//
// providerRef is accepted and ignored: this provider is synchronous, so there
// is never an outstanding check to resume. The composition never hands it one.
//
// Only an HTTP 200 whose body parses as exactly one Web Risk document is an
// answer at all. Every other outcome — a status this contract does not name, a
// body with trailing data, a threat object naming no type, a caller's deadline
// elapsing — is a failed exchange carrying a closed reason, and a failed
// exchange is never a clearance.
func (w *WebRiskProvider) Check(
	ctx context.Context, canonicalURL, _ string,
) (ReputationResult, error) {
	result := ReputationResult{Provider: w.Name()}
	request, err := w.newLookupRequest(ctx, canonicalURL)
	if err != nil {
		return result, err
	}
	response, err := w.client.Do(request)
	if err != nil {
		// The caller going away is reported as itself: it is a fact about this
		// process, not about the URL. Everything else is flattened rather than
		// wrapped — a transport error names the endpoint it dialled, and even
		// though the key travels in a header, an error this package returns is
		// reason enough to carry nothing but a closed label.
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, unavailable(reasonTimeout)
	}
	defer func() { _ = response.Body.Close() }()
	if reason := webRiskStatusReason(response.StatusCode); reason != "" {
		return result, unavailable(reason)
	}
	// decodeExactlyOne, shared with the Cloudflare adapter: it bounds the read,
	// and it refuses a body carrying anything after the first document. A
	// lookup answer is a handful of fields, so the shared ceiling is generous
	// here; what matters is the refusal, because `{}` followed by garbage
	// would otherwise decode cleanly into a clearance.
	var decoded webRiskResponse
	if err := decodeExactlyOne(response.Body, &decoded); err != nil {
		return result, unavailable(reasonMalformed)
	}
	result.Verdict, result.ThreatCategories, result.ExpiresAt, err = webRiskVerdict(decoded, time.Now())
	return result, err
}

// newLookupRequest builds the one request this provider makes.
//
// The URL under test travels in a query parameter and the key travels in a
// header, so neither can reshape the path and only one of them can ever reach a
// log — and it is not the key.
func (w *WebRiskProvider) newLookupRequest(
	ctx context.Context, canonicalURL string,
) (*http.Request, error) {
	query := url.Values{"uri": []string{canonicalURL}}
	for _, threatType := range webRiskThreatTypes {
		query.Add("threatTypes", threatType)
	}
	request, err := http.NewRequestWithContext(
		ctx, http.MethodGet, w.baseURL+"?"+query.Encode(), nil)
	if err != nil {
		return nil, unavailable(reasonMalformed)
	}
	request.Header.Set("X-Goog-Api-Key", w.apiKey)
	request.Header.Set("Accept", "application/json")
	return request, nil
}

// webRiskStatusReason names why a status code is not an answer, or "" when it
// is one.
//
// Every non-200 is a failure, and the distinctions exist only so the operator
// sees which one: an exhausted quota, a key the project rejects and an outage
// have different remedies but identical safety semantics. 200 is the single
// status this contract defines a body for; a 2xx that is not 200 is a response
// shape this client does not understand, and understanding half of it is how a
// proxy turns into an answer.
func webRiskStatusReason(status int) string {
	switch status {
	case http.StatusOK:
		return ""
	case http.StatusUnauthorized, http.StatusForbidden:
		return reasonAuthError
	case http.StatusTooManyRequests:
		return reasonRateLimited
	default:
		return reasonUnavailable
	}
}

// webRiskVerdict turns a decoded lookup into a verdict and the evidence
// lifetime that comes with it.
//
// Absent threat is the clearance, and it is the *only* clearance: it is reached
// solely from a 200 whose body parsed as exactly one document, which is what
// keeps "no threat field" from ever meaning "no answer". A threat object that
// names no recognised type is refused rather than read in either direction — it
// is neither the "no match" shape nor the "match" shape, so it is a response
// this client does not understand.
//
// A clearance carries no provider-stated expiry: Web Risk says when a *match*
// stops being current, not how long an absence of one lasts, and inventing a
// lifetime for the second from the first would be a clearance this deployment
// granted itself. VerdictTTL governs it, as it always has.
func webRiskVerdict(
	decoded webRiskResponse, now time.Time,
) (ReputationVerdict, []string, time.Time, error) {
	if decoded.Threat == nil {
		return ReputationSafe, nil, time.Time{}, nil
	}
	matched := matchedThreatTypes(decoded.Threat.ThreatTypes)
	if len(matched) == 0 {
		return ReputationUnknown, nil, time.Time{}, unavailable(reasonMalformed)
	}
	expiresAt, err := webRiskEvidenceExpiry(decoded.Threat.ExpireTime, now)
	if err != nil {
		return ReputationUnknown, nil, time.Time{}, err
	}
	return ReputationMalicious, matched, expiresAt, nil
}

// matchedThreatTypes reports which of the types this deployment asked about the
// provider actually named. A type nobody asked about is ignored rather than
// acted on: the question decides the answer's meaning.
func matchedThreatTypes(reported []string) []string {
	matched := make([]string, 0, len(webRiskThreatTypes))
	for _, threatType := range reported {
		for _, configured := range webRiskThreatTypes {
			if strings.EqualFold(strings.TrimSpace(threatType), configured) {
				matched = append(matched, configured)
			}
		}
	}
	return matched
}

// webRiskEvidenceExpiry reads how long a threat match stays current.
//
// Three cases, and the reasoning for each is about what the *pipeline* does
// with the answer, not about which one feels stricter in isolation:
//
//   - absent: the provider stated no limit, so VerdictTTL alone governs. Zero
//     is returned rather than a guess. This is not a relaxation: fifteen
//     minutes is far inside any expiry Web Risk actually issues, so the
//     fallback is the more conservative of the two either way;
//   - unparseable: refused as malformed, exactly like a threat naming no type
//     and exactly like trailing data in the body. A field this client cannot
//     read is a response it does not understand, and it has one rule for those.
//     Refusing does not lose the block — a refused exchange is retried, and a
//     provider that keeps answering unreadably opens the breaker and the target
//     converges to UNKNOWN at its deadline. UNKNOWN is an interstitial: no
//     href, no preview. There is no path from here to a clearance;
//   - already past: the match describes a threat the provider itself says is no
//     longer current, so it cannot found a condemnation now. Refused for the
//     same reason and with the same consequence — the next lookup asks again
//     and gets whatever is true then.
func webRiskEvidenceExpiry(raw string, now time.Time) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	expiresAt, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, unavailable(reasonMalformed)
	}
	if !expiresAt.After(now) {
		return time.Time{}, unavailable(reasonMalformed)
	}
	return expiresAt, nil
}
