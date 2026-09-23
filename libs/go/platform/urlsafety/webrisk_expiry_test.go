package urlsafety

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

// The evidence ceiling a threat match carries (issue #928).
//
// A condemnation is evidence with a lifetime, and the lifetime is the shorter of
// what the provider grants and what this deployment is willing to reuse. These
// assert both directions of that "shorter of", plus the three ways a stated
// expiry can fail to be usable — and, in every one of them, that nothing becomes
// safe.

// threatBody builds a match with the given expireTime, or none when raw is "".
func threatBody(raw string) string {
	if raw == "" {
		return `{"threat":{"threatTypes":["MALWARE"]}}`
	}
	return `{"threat":{"threatTypes":["MALWARE"],"expireTime":"` + raw + `"}}`
}

func TestWebRiskReportsTheProviderStatedExpiry(t *testing.T) {
	expiry := time.Now().Add(90 * time.Minute).UTC().Truncate(time.Second)
	provider, _ := webRiskServer(t, respondJSON(threatBody(expiry.Format(time.RFC3339))))

	result, err := provider.Check(context.Background(), "https://bad.test/x", "")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Verdict != ReputationMalicious {
		t.Fatalf("verdict = %q, want %q", result.Verdict, ReputationMalicious)
	}
	if !result.ExpiresAt.Equal(expiry) {
		t.Fatalf("ExpiresAt = %s, want %s", result.ExpiresAt, expiry)
	}
}

// No stated expiry is not an unbounded one. The local window governs, which is
// the conservative direction: fifteen minutes is far inside any expiry Web Risk
// actually issues.
func TestWebRiskAbsentExpiryLeavesTheLocalWindowInCharge(t *testing.T) {
	provider, _ := webRiskServer(t, respondJSON(threatBody("")))

	result, err := provider.Check(context.Background(), "https://bad.test/x", "")

	if err != nil || result.Verdict != ReputationMalicious {
		t.Fatalf("verdict = %q, err = %v", result.Verdict, err)
	}
	if !result.ExpiresAt.IsZero() {
		t.Fatalf("ExpiresAt = %s, want the zero time", result.ExpiresAt)
	}
}

// A stated expiry this client cannot read, or one the provider itself says has
// already passed, is a response it does not understand or evidence it may not
// act on. Both are refused — and refusing degrades to an interstitial, never to
// a clearance.
func TestWebRiskUnusableExpiryIsRefusedAndNeverSafe(t *testing.T) {
	for name, raw := range map[string]string{
		"not a timestamp":  "soon",
		"wrong format":     "2026-09-22 10:00:00",
		"empty-ish":        "   ",
		"already expired":  time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
		"expiring exactly": time.Now().Add(-time.Second).UTC().Format(time.RFC3339),
	} {
		t.Run(name, func(t *testing.T) {
			provider, _ := webRiskServer(t, respondJSON(threatBody(raw)))

			result, err := provider.Check(context.Background(), "https://bad.test/x", "")

			if name == "empty-ish" {
				// Whitespace trims to absent, which is the documented "no limit
				// stated" case rather than a malformed one.
				if err != nil || result.Verdict != ReputationMalicious {
					t.Fatalf("verdict = %q, err = %v", result.Verdict, err)
				}
				return
			}
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("err = %v, want ErrUnavailable", err)
			}
			if FailureReason(err) != ReasonMalformed {
				t.Fatalf("reason = %q, want %q", FailureReason(err), ReasonMalformed)
			}
			if result.Verdict == ReputationSafe {
				t.Fatal("an unusable expiry must never produce a clearance")
			}
		})
	}
}

// A clearance carries no expiry of its own. Web Risk states when a *match* stops
// being current, not how long an absence of one lasts, and deriving the second
// from the first would be a lifetime this deployment granted itself.
func TestWebRiskClearanceCarriesNoProviderExpiry(t *testing.T) {
	provider, _ := webRiskServer(t, respondJSON(`{}`))

	result, err := provider.Check(context.Background(), "https://ok.test/", "")

	if err != nil || result.Verdict != ReputationSafe {
		t.Fatalf("verdict = %q, err = %v", result.Verdict, err)
	}
	if !result.ExpiresAt.IsZero() {
		t.Fatalf("a clearance must state no provider expiry, got %s", result.ExpiresAt)
	}
}

// --- the cache half: the shorter of the two, always -------------------------

// cachedFor drives a Service with a frozen clock and reports how long a verdict
// stays usable after one Check.
func cachedFor(t *testing.T, result ReputationResult, now time.Time) time.Duration {
	t.Helper()
	clock := now
	service := newService(nil, nil, func() time.Time { return clock })
	service.provider = &stubProvider{name: "stub", script: []stubAnswer{{result: result}}}

	if _, err := service.Check(context.Background(), "https://x.test/", ""); err != nil {
		t.Fatalf("Check: %v", err)
	}
	// Walk the clock forward a second at a time until the entry stops being
	// served. Bounded by VerdictTTL plus one, so a cache that never expired
	// would fail rather than hang.
	for elapsed := time.Second; elapsed <= VerdictTTL+time.Second; elapsed += time.Second {
		clock = now.Add(elapsed)
		if _, ok := service.Lookup("https://x.test/"); !ok {
			return elapsed
		}
	}
	return VerdictTTL + time.Second
}

func TestServiceCachesForTheShorterOfTheTwoLifetimes(t *testing.T) {
	now := time.Now()
	for name, testCase := range map[string]struct {
		expiresAt time.Time
		want      time.Duration
	}{
		// The provider is stricter: its ceiling wins, and the verdict stops
		// being usable well before VerdictTTL would have ended it.
		"provider expiry before the local ttl": {now.Add(4 * time.Minute), 4 * time.Minute},
		// The provider is more generous: it changes nothing. A stated expiry is
		// a ceiling, never an extension.
		"provider expiry after the local ttl": {now.Add(3 * time.Hour), VerdictTTL},
		// No ceiling stated at all.
		"no provider expiry": {time.Time{}, VerdictTTL},
	} {
		t.Run(name, func(t *testing.T) {
			result := ReputationResult{
				Verdict: ReputationMalicious, Provider: "stub", ExpiresAt: testCase.expiresAt,
			}
			if got := cachedFor(t, result, now); got != testCase.want {
				t.Fatalf("usable for %s, want %s", got, testCase.want)
			}
		})
	}
}

// Expiry is not a verdict. When the ceiling passes, the cached answer is gone —
// reported as a miss, exactly like a URL nobody ever checked — and there is no
// state in which the entry survives as something weaker.
func TestExpiredEvidenceIsAMissAndNeverAClearance(t *testing.T) {
	now := time.Now()
	clock := now
	service := newService(nil, nil, func() time.Time { return clock })
	service.provider = &stubProvider{name: "stub", script: []stubAnswer{{
		result: ReputationResult{
			Verdict: ReputationMalicious, Provider: "stub", ExpiresAt: now.Add(2 * time.Minute),
		},
	}}}

	if _, err := service.Check(context.Background(), "https://bad.test/", ""); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if verdict, ok := service.Lookup("https://bad.test/"); !ok || verdict != VerdictMalicious {
		t.Fatalf("before expiry: verdict = %q, ok = %v", verdict, ok)
	}

	clock = now.Add(3 * time.Minute)

	verdict, ok := service.Lookup("https://bad.test/")
	if ok {
		t.Fatalf("expired evidence is still being served as %q", verdict)
	}
	if verdict == VerdictSafe {
		t.Fatal("expiry must never resolve towards safe")
	}
}

// An answer that is already expired when it arrives cannot found anything. The
// cache refuses a non-positive lifetime rather than storing an entry nobody
// could ever be served.
func TestAlreadyExpiredEvidenceIsNeverCached(t *testing.T) {
	now := time.Now()
	service := newService(nil, nil, func() time.Time { return now })
	service.provider = &stubProvider{name: "stub", script: []stubAnswer{{
		result: ReputationResult{
			Verdict: ReputationMalicious, Provider: "stub", ExpiresAt: now.Add(-time.Second),
		},
	}}}

	if _, err := service.Check(context.Background(), "https://bad.test/", ""); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if _, ok := service.Lookup("https://bad.test/"); ok {
		t.Fatal("evidence that arrived expired was cached")
	}
}

// The adapter's own timeout is unchanged by any of this: an expiry is read from
// a body, and a body that never arrives is still just a failure.
func TestWebRiskExpiryParsingDoesNotAffectTransportFailures(t *testing.T) {
	provider, _ := webRiskServer(t, respondStatus(http.StatusBadGateway, ``))
	result, err := provider.Check(context.Background(), "https://x.test/", "")
	if !errors.Is(err, ErrUnavailable) || !result.ExpiresAt.IsZero() {
		t.Fatalf("err = %v, ExpiresAt = %s", err, result.ExpiresAt)
	}
}
