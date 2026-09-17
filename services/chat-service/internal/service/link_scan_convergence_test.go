package service_test

import (
	"context"
	"net/netip"
	"testing"

	"github.com/nicrepository/nchat/libs/go/platform/urlsafety"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Convergence and policy in the scan worker (issue #807): the deadline sweep,
// the pre-provider refusals, the disabled drain and a synchronous provider.

// syncProvider answers on the first Check, like a lookup API would.
type syncProvider struct {
	verdict urlsafety.ReputationVerdict
	calls   int
}

func (p *syncProvider) Check(context.Context, string, string) (urlsafety.ReputationResult, error) {
	p.calls++
	return urlsafety.ReputationResult{Verdict: p.verdict, Provider: "sync"}, nil
}

func (p *syncProvider) CircuitState() urlsafety.BreakerState { return urlsafety.BreakerClosed }

func TestSweepTerminalisesExpiredTargetsBeforeAnyProviderWork(t *testing.T) {
	queue := newFakeQueue()
	queue.expired = []string{"https://late.example/a", "https://late.example/b"}
	provider := &fakeProvider{}
	svc := service.NewLinkScanService(queue, provider, &fakePublisher{}, nil)

	if _, err := svc.ProcessDue(context.Background()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if submits, polls := provider.counts(); submits != 0 || polls != 0 {
		t.Fatal("the sweep must not touch the provider")
	}
	if len(queue.expired) != 0 {
		t.Fatal("expired targets were not consumed")
	}
}

func TestPolicyRefusesSensitiveAndInternalURLsWithoutTheProvider(t *testing.T) {
	for name, tc := range map[string]struct {
		url    string
		reason string
	}{
		"magic link":    {"https://app.example.com/magic-link?token=abc", storage.TerminalReasonSensitive},
		"signed url":    {"https://bucket.s3.amazonaws.com/f?X-Amz-Signature=x", storage.TerminalReasonSensitive},
		"internal name": {"https://wiki.internal/page", storage.TerminalReasonInternal},
	} {
		t.Run(name, func(t *testing.T) {
			queue := newFakeQueue(storage.LinkScanJob{CanonicalURL: tc.url})
			provider := &fakeProvider{}
			svc := service.NewLinkScanService(queue, provider, &fakePublisher{}, nil)

			if _, err := svc.ProcessDue(context.Background()); err != nil {
				t.Fatalf("ProcessDue: %v", err)
			}
			if submits, _ := provider.counts(); submits != 0 {
				t.Fatal("a refused URL reached the provider")
			}
			if queue.policyTerminals[tc.url] != tc.reason {
				t.Fatalf("terminal = %v, want %s", queue.policyTerminals, tc.reason)
			}
			if len(queue.begun) != 0 {
				t.Fatal("no submission intent may be recorded for a refused URL")
			}
		})
	}
}

func TestPolicyRefusesAHostThatResolvesIntoAPrivateNetwork(t *testing.T) {
	const url = "https://looks-public.example.com/x"
	queue := newFakeQueue(storage.LinkScanJob{CanonicalURL: url})
	provider := &fakeProvider{}
	svc := service.NewLinkScanService(queue, provider, &fakePublisher{}, nil)
	svc.SetHostResolver(func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("10.0.0.5")}, nil
	})

	if _, err := svc.ProcessDue(context.Background()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if submits, _ := provider.counts(); submits != 0 {
		t.Fatal("a host resolving into RFC1918 reached the provider")
	}
	if queue.policyTerminals[url] != storage.TerminalReasonInternal {
		t.Fatalf("terminal = %v", queue.policyTerminals)
	}

	// A public resolution, or a name that does not resolve, proceeds to the
	// provider: reputation, not reachability, is the question.
	for name, resolve := range map[string]func(context.Context, string) ([]netip.Addr, error){
		"public": func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
		},
		"nxdomain": func(context.Context, string) ([]netip.Addr, error) { return nil, context.DeadlineExceeded },
	} {
		t.Run(name, func(t *testing.T) {
			queue := newFakeQueue(storage.LinkScanJob{CanonicalURL: url})
			provider := &fakeProvider{}
			svc := service.NewLinkScanService(queue, provider, &fakePublisher{}, nil)
			svc.SetHostResolver(resolve)
			if _, err := svc.ProcessDue(context.Background()); err != nil {
				t.Fatalf("ProcessDue: %v", err)
			}
			if submits, _ := provider.counts(); submits != 1 {
				t.Fatalf("submits = %d", submits)
			}
		})
	}
}

func TestDisabledSafetyDrainsPendingTargetsWithoutAProvider(t *testing.T) {
	queue := newFakeQueue(storage.LinkScanJob{CanonicalURL: "https://waiting.example/a"})
	svc := service.NewLinkScanService(queue, nil, &fakePublisher{}, nil)

	if moved, err := svc.ProcessDue(context.Background()); err != nil || moved != 0 {
		t.Fatalf("ProcessDue: %d %v", moved, err)
	}
	if len(queue.drainedDisabled) != 1 || queue.claims != 0 {
		t.Fatalf("drained = %v claims = %d", queue.drainedDisabled, queue.claims)
	}

	// The flag off with a provider wired behaves the same: no exchange, drain.
	provider := &fakeProvider{}
	queue = newFakeQueue(storage.LinkScanJob{CanonicalURL: "https://waiting.example/b"})
	svc = service.NewLinkScanService(queue, provider, &fakePublisher{}, nil)
	svc.SetSafetyEnabled(false)
	if _, err := svc.ProcessDue(context.Background()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if submits, _ := provider.counts(); submits != 0 || len(queue.drainedDisabled) != 1 {
		t.Fatalf("submits = %d drained = %v", submits, queue.drainedDisabled)
	}
	// And enabling it again with a provider resumes the exchanges.
	svc.SetSafetyEnabled(true)
	queue.jobs = []storage.LinkScanJob{{CanonicalURL: "https://waiting.example/c"}}
	if _, err := svc.ProcessDue(context.Background()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if submits, _ := provider.counts(); submits != 1 {
		t.Fatalf("submits after re-enable = %d", submits)
	}
}

func TestASynchronousProviderIsRecordedInOnePass(t *testing.T) {
	for _, verdict := range []urlsafety.ReputationVerdict{urlsafety.ReputationSafe, urlsafety.ReputationMalicious, urlsafety.ReputationUnknown} {
		t.Run(string(verdict), func(t *testing.T) {
			queue := newFakeQueue(storage.LinkScanJob{CanonicalURL: "https://sync.example/a"})
			provider := &syncProvider{verdict: verdict}
			svc := service.NewLinkScanService(queue, provider, &fakePublisher{}, nil)

			if _, err := svc.ProcessDue(context.Background()); err != nil {
				t.Fatalf("ProcessDue: %v", err)
			}
			submitted, verdicts := queue.snapshot()
			// The ref is bound first so the verdict write goes through the usual
			// compare-and-set, then the verdict lands, in the same pass.
			if submitted["https://sync.example/a"] != "direct" {
				t.Fatalf("submitted = %v", submitted)
			}
			if verdicts["https://sync.example/a"] != verdict.LegacyVerdict() {
				t.Fatalf("verdicts = %v", verdicts)
			}
			if provider.calls != 1 {
				t.Fatalf("provider calls = %d", provider.calls)
			}
		})
	}
}

// Failures in the sweep or in recording a policy refusal are logged and the
// pass goes on; a lost compare-and-set is another worker's win, not an error.
func TestSweepAndPolicyFailuresDoNotStopThePass(t *testing.T) {
	queue := newFakeQueue(storage.LinkScanJob{CanonicalURL: "https://ok.example/a"})
	queue.terminalizeErr = errBoom
	provider := &fakeProvider{}
	svc := service.NewLinkScanService(queue, provider, &fakePublisher{}, nil)
	if _, err := svc.ProcessDue(context.Background()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if submits, _ := provider.counts(); submits != 1 {
		t.Fatalf("a failed sweep must not block the claims: submits = %d", submits)
	}

	for name, err := range map[string]error{"lease lost": storage.ErrLinkScanConflict, "outage": errBoom} {
		t.Run(name, func(t *testing.T) {
			queue := newFakeQueue(storage.LinkScanJob{CanonicalURL: "https://wiki.internal/page"})
			queue.terminalErr = err
			svc := service.NewLinkScanService(queue, &fakeProvider{}, &fakePublisher{}, nil)
			if _, err := svc.ProcessDue(context.Background()); err != nil {
				t.Fatalf("ProcessDue: %v", err)
			}
		})
	}
}
