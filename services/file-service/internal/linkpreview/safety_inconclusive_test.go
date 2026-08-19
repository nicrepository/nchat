package linkpreview

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/nicrepository/nchat/libs/go/platform/urlsafety"
)

// The preview gate under the issue #135 policy change.
//
// # The one sentence this file exists to defend
//
// Publishing a message and fetching the URL it carries are different
// authorisations, and issue #135 separated them. A message whose links could not
// be verified is now delivered to everyone — it is `active`, its recipients read
// it, its mentions fire — and this service's permission to open a connection to
// those links is still revoked.
//
// So the tempting inference, "the message went out, therefore the link is fine",
// is exactly the one that must be impossible. It is not merely absent from the
// code: there is no parameter on any function in this package through which a
// message's status could arrive. The gate consults the verdict store and nothing
// else, and only an explicit, unexpired `safe` opens it.
//
// The whole table, in one place:
//
//	verdict        server-side fetch
//	safe           YES
//	pending        NO
//	malicious      NO
//	inconclusive   NO
//	unavailable    NO
//	absent         NO

// TestInconclusiveVerdictPreventsTheFetch is the case this issue introduced.
//
// An inconclusive scan is *terminal* — the provider confirmed it finished and
// produced nothing — which is precisely why the message is allowed out. It is
// still not a clearance, and the difference between those two facts is the whole
// feature.
func TestInconclusiveVerdictPreventsTheFetch(t *testing.T) {
	var requests atomic.Int64
	server := countingServer(t, &requests)
	safety := decided(urlsafety.VerdictInconclusive)
	service := serviceAgainst(t, server, newClock(), nil).WithURLSafety(safety)

	_, err := service.Preview(context.Background(), "http://example.com/page")

	if err == nil {
		t.Fatal("an inconclusive verdict produced a preview")
	}
	// Not reported as malicious: the provider alleged nothing about this link, and
	// a client shown a security warning for an operational refusal learns the
	// wrong thing. ErrSafetyUnavailable is the honest answer — no usable verdict.
	if !errors.Is(err, ErrSafetyUnavailable) {
		t.Fatalf("err = %v, want ErrSafetyUnavailable", err)
	}
	if errors.Is(err, ErrMaliciousURL) {
		t.Fatal("an unverified link was reported to the caller as malicious")
	}
	// The assertion that matters: nothing left this process. Not a GET, not a
	// HEAD, not a redirect hop — the counting server saw no request at all.
	if requests.Load() != 0 {
		t.Fatalf("an inconclusive link was fetched %d time(s)", requests.Load())
	}
}

// Every non-clearance, in one table, so adding a verdict to the shared package
// cannot quietly become "allowed" here. The default branch in checkSafety is
// what this holds in place.
func TestOnlyAnExplicitClearanceAuthorisesAFetch(t *testing.T) {
	for name, verdict := range map[string]urlsafety.Verdict{
		"inconclusive": urlsafety.VerdictInconclusive,
		"unknown":      urlsafety.VerdictUnknown,
		"zero value":   urlsafety.Verdict(""),
		"future value": urlsafety.Verdict("probably_fine"),
	} {
		t.Run(name, func(t *testing.T) {
			var requests atomic.Int64
			server := countingServer(t, &requests)
			service := serviceAgainst(t, server, newClock(), nil).
				WithURLSafety(decided(verdict))

			if _, err := service.Preview(context.Background(), "http://example.com/page"); err == nil {
				t.Fatalf("verdict %q authorised a preview", verdict)
			}
			if requests.Load() != 0 {
				t.Fatalf("verdict %q caused %d fetch(es)", verdict, requests.Load())
			}
		})
	}
}

// The inference this issue makes dangerous, stated as a property of the API.
//
// A published message is the only new thing in the world after #135, and the
// preview path cannot see one. This asserts the shape rather than a behaviour:
// the gate's inputs are a URL and the verdict store, so there is nowhere for
// "the message is active" to enter the decision — and the decision it reaches
// for an unverified link is refusal, whatever is true of any message carrying it.
func TestAPublishedMessageDoesNotAuthoriseAFetch(t *testing.T) {
	var requests atomic.Int64
	server := countingServer(t, &requests)
	// The exact production state after #135: the message is out, and the URL's
	// scan is terminal with no verdict.
	safety := decided(urlsafety.VerdictInconclusive)
	service := serviceAgainst(t, server, newClock(), nil).WithURLSafety(safety)

	for attempt := 0; attempt < 3; attempt++ {
		if _, err := service.Preview(context.Background(), "http://example.com/page"); err == nil {
			t.Fatal("a published message's link was previewed")
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("the link was fetched %d time(s)", requests.Load())
	}
	// The verdict is re-consulted every time rather than being remembered as a
	// decision about the message. A refusal cached as an authorisation is how this
	// would eventually leak.
	if safety.calls.Load() != 3 {
		t.Fatalf("the verdict store was consulted %d times, want once per request",
			safety.calls.Load())
	}
	// And an unverified link is never re-queued as new provider work: the scan
	// already ran and already finished. Recovery is reconciliation, in the worker,
	// and never a resubmission from the preview path.
	if safety.submits.Load() != 0 {
		t.Fatalf("a terminal scan was queued again %d time(s)", safety.submits.Load())
	}
}

// Once reconciliation does obtain a clearance, the preview works again — which
// is the reason the file-service trigger was opened at all. Nothing else in the
// system had to change for this: the gate reads the store, and the store now has
// an answer.
func TestAReconciledClearanceRestoresThePreview(t *testing.T) {
	var requests atomic.Int64
	server := countingServer(t, &requests)
	safety := decided(urlsafety.VerdictInconclusive)
	service := serviceAgainst(t, server, newClock(), nil).WithURLSafety(safety)

	if _, err := service.Preview(context.Background(), "http://example.com/page"); err == nil {
		t.Fatal("an inconclusive link was previewed")
	}

	// Reconciliation lands: the row is now 'done' with a safe verdict.
	safety.verdict = urlsafety.VerdictSafe

	preview, err := service.Preview(context.Background(), "http://example.com/page")
	if err != nil {
		t.Fatalf("Preview after reconciliation: %v", err)
	}
	if preview.Title != "Page" {
		t.Fatalf("preview = %+v", preview)
	}
	if requests.Load() != 1 {
		t.Fatalf("fetches = %d, want exactly one, after the clearance", requests.Load())
	}
}
