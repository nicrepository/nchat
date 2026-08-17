package linkpreview

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/nicrepository/nchat/services/file-service/internal/service"
)

// Fail-closed, when the check itself cannot answer.
//
// The three refusals below are deliberately different errors, and the
// distinction is the point: "we could not ask", "we chose not to ask right now"
// and "the answer is not ready" mean different things to a caller and to an
// operator. What they have in common is the only thing that matters for safety —
// no Open Graph fetch happens in any of them, because fetching first and asking
// afterwards is rendering the phishing page.

func TestAnUnreadableVerdictRefusesThePreview(t *testing.T) {
	var requests atomic.Int64
	server := countingServer(t, &requests)
	safety := &stubSafety{loadErr: errors.New("database unavailable")}
	service := serviceAgainst(t, server, newClock(), nil).WithURLSafety(safety)

	_, err := service.Preview(context.Background(), "http://example.com/page")

	if !errors.Is(err, ErrSafetyUnavailable) {
		t.Fatalf("want ErrSafetyUnavailable, got %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("the open graph fetch happened anyway: %d requests", requests.Load())
	}
}

// Failing to record the need for a scan is still an unavailable verdict, never
// a clearance: a URL whose scan nobody scheduled would otherwise be fetched on
// the strength of an error.
func TestAScanThatCannotBeQueuedRefusesThePreview(t *testing.T) {
	var requests atomic.Int64
	server := countingServer(t, &requests)
	safety := &stubSafety{submitErr: errors.New("database unavailable")}
	service := serviceAgainst(t, server, newClock(), nil).WithURLSafety(safety)

	_, err := service.Preview(context.Background(), "http://example.com/page")

	if !errors.Is(err, ErrSafetyUnavailable) {
		t.Fatalf("want ErrSafetyUnavailable, got %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("the open graph fetch happened anyway: %d requests", requests.Load())
	}
}

// A spent window or a full queue says nothing about the link. Reporting it as
// malicious would show a security warning for an operational condition — and
// reporting it as safe would be the bypass. Its own error, and still no fetch.
func TestACapacityRefusalIsItsOwnAnswer(t *testing.T) {
	for _, result := range []string{service.AdmissionServiceBudget, service.AdmissionBacklog} {
		t.Run(result, func(t *testing.T) {
			var requests atomic.Int64
			server := countingServer(t, &requests)
			safety := &stubSafety{admissionResult: result}
			preview := serviceAgainst(t, server, newClock(), nil).
				WithURLSafety(safety).
				WithScanCapacity(service.LinkScanCapacity{NewURLBudget: 1})

			_, err := preview.Preview(context.Background(), "http://example.com/page")

			if !errors.Is(err, ErrSafetyCapacity) {
				t.Fatalf("want ErrSafetyCapacity, got %v", err)
			}
			if errors.Is(err, ErrMaliciousURL) {
				t.Fatal("an operational refusal was reported as a security verdict")
			}
			if requests.Load() != 0 {
				t.Fatalf("the open graph fetch happened anyway: %d requests", requests.Load())
			}
		})
	}
}

// A cancelled request is a cancelled request, not a provider outage: reporting
// it as one would make every shutdown look like a Cloudflare incident.
func TestACancelledPreviewIsNotReportedAsAnOutage(t *testing.T) {
	for name, safety := range map[string]*stubSafety{
		"the verdict read is interrupted": {loadErr: errors.New("interrupted")},
		"the scan record is interrupted":  {submitErr: errors.New("interrupted")},
	} {
		t.Run(name, func(t *testing.T) {
			var requests atomic.Int64
			server := countingServer(t, &requests)
			preview := serviceAgainst(t, server, newClock(), nil).WithURLSafety(safety)

			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			_, err := preview.Preview(ctx, "http://example.com/page")

			if !errors.Is(err, context.Canceled) {
				t.Fatalf("err = %v, want the cancellation reported as itself", err)
			}
			if requests.Load() != 0 {
				t.Fatalf("the open graph fetch happened anyway: %d requests", requests.Load())
			}
		})
	}
}
