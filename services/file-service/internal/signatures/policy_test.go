package signatures

import (
	"strings"
	"testing"
	"time"
)

func validEvidence() Evidence {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	return Evidence{Generation: strings.Repeat("a", 64), Engine: Engine, DailyVersion: 28085,
		DatabaseTime: now.Add(-time.Hour), LoadedAt: now.Add(-time.Minute), CompletedAt: now}
}

func TestEvidenceFailsClosed(t *testing.T) {
	now := validEvidence().CompletedAt
	cases := map[string]func(*Evidence){
		"missing generation":     func(e *Evidence) { e.Generation = "" },
		"uppercase generation":   func(e *Evidence) { e.Generation = strings.Repeat("A", 64) },
		"malformed generation":   func(e *Evidence) { e.Generation = strings.Repeat("z", 64) },
		"engine incompatible":    func(e *Evidence) { e.Engine = "1.3.0" },
		"unknown daily":          func(e *Evidence) { e.DailyVersion = 0 },
		"missing time":           func(e *Evidence) { e.DatabaseTime = time.Time{} },
		"future database":        func(e *Evidence) { e.DatabaseTime = now.Add(time.Second) },
		"future load":            func(e *Evidence) { e.LoadedAt = now.Add(time.Second) },
		"load before build":      func(e *Evidence) { e.LoadedAt = e.DatabaseTime.Add(-time.Second) },
		"expired":                func(e *Evidence) { e.DatabaseTime = now.Add(-2 * time.Hour) },
		"missing completion":     func(e *Evidence) { e.CompletedAt = time.Time{} },
		"completion before load": func(e *Evidence) { e.CompletedAt = e.LoadedAt.Add(-time.Second) },
		"future completion":      func(e *Evidence) { e.CompletedAt = now.Add(time.Second) },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			e := validEvidence()
			change(&e)
			if e.ValidCompletion(now, 2*time.Hour) == nil {
				t.Fatal("invalid evidence accepted")
			}
		})
	}
	for _, age := range []time.Duration{0, -time.Second, time.Hour} {
		if validEvidence().ValidCompletion(now, age) == nil {
			t.Fatal("missing/expired policy accepted")
		}
	}
	e := validEvidence()
	if e.ValidCompletion(now, time.Hour+time.Nanosecond) != nil {
		t.Fatal("one nanosecond before expiry rejected")
	}
	if e.ValidCompletion(now, 2*time.Hour) != nil {
		t.Fatal("valid generation rejected")
	}
}
