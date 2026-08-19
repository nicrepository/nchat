package service

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
)

// The backend half of the shared URL-boundary corpus (issue #135, CQ-001).
//
// # Why this exists
//
// chat-service decides which URLs get *scanned*. The web client decides which get
// a clickable *anchor*. Those are two implementations of the same boundary rules,
// in two languages, and a disagreement between them is not a cosmetic bug: it is a
// link a reader can click, pointing at an address the safety check never saw.
//
// The concrete regression: the backend trimmed every trailing bracket while the
// client kept balanced ones, so
//
//	https://example.test/wiki/Function_(mathematics)
//
// was scanned as ".../Function_(mathematics" and rendered as
// ".../Function_(mathematics)". Two different URLs, one of them unchecked.
//
// # How the corpus prevents it
//
// libs/testdata/link-safety/autolink-corpus.json holds one expectation per input
// for both sides. This file asserts the backend half; autolink.corpus.test.ts
// asserts the client half and the cross-invariant. Neither side may edit its own
// expectations without the other test failing, which is the point — two
// independently maintained lists would drift back apart.
//
// The corpus is data only: no logic, no code generation, nothing executable.

// corpusCase mirrors one entry of the shared fixture.
type corpusCase struct {
	Name              string   `json:"name"`
	Input             string   `json:"input"`
	BackendCandidates []string `json:"backendCandidates"`
	FrontendHrefs     []string `json:"frontendHrefs"`
	Note              string   `json:"note"`
}

type corpusFile struct {
	Cases []corpusCase `json:"cases"`
}

func loadCorpus(t *testing.T) corpusFile {
	t.Helper()
	// A fixed, repository-relative path with no caller input in it. Named as a
	// constant so gosec can see there is nothing variable to inject.
	const path = "../../../../libs/testdata/link-safety/autolink-corpus.json"
	raw, err := os.ReadFile(path) //nolint:gosec // fixed test fixture path, no external input
	if err != nil {
		t.Fatalf("read shared corpus: %v", err)
	}
	var corpus corpusFile
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatalf("parse shared corpus: %v", err)
	}
	if len(corpus.Cases) == 0 {
		t.Fatal("the shared corpus is empty")
	}
	return corpus
}

// TestScanURLCandidatesMatchesTheSharedCorpus pins the backend's boundary rules.
func TestScanURLCandidatesMatchesTheSharedCorpus(t *testing.T) {
	for _, testCase := range loadCorpus(t).Cases {
		t.Run(testCase.Name, func(t *testing.T) {
			got := scanURLCandidates(testCase.Input)
			want := testCase.BackendCandidates
			if want == nil {
				want = []string{}
			}
			if got == nil {
				got = []string{}
			}
			if !slices.Equal(got, want) {
				t.Fatalf("scanURLCandidates(%q)\n got  %q\n want %q\n%s",
					testCase.Input, got, want, testCase.Note)
			}
		})
	}
}

// TestEveryClientAnchorIsABackendCandidate is the invariant itself.
//
// It is asserted here as well as on the client side, against the same data, so
// that a change to the *backend* scanner which silently stopped extracting
// something the client still anchors fails in this package rather than waiting
// for someone to run the web suite.
func TestEveryClientAnchorIsABackendCandidate(t *testing.T) {
	for _, testCase := range loadCorpus(t).Cases {
		t.Run(testCase.Name, func(t *testing.T) {
			candidates := scanURLCandidates(testCase.Input)
			for _, href := range testCase.FrontendHrefs {
				if !slices.Contains(candidates, href) {
					t.Fatalf("the client would anchor %q, which the backend never extracted from %q\n"+
						"backend candidates: %q\n"+
						"an anchor to a URL that was not scanned is the whole of CQ-001",
						href, testCase.Input, candidates)
				}
			}
		})
	}
}

// The client is allowed to draw fewer links than the backend scans, and several
// corpus cases rely on that. This states the direction explicitly so nobody
// "fixes" the asymmetry the wrong way round.
func TestTheClientMayUnderLinkButNeverOverLink(t *testing.T) {
	corpus := loadCorpus(t)
	underLinked := 0
	for _, testCase := range corpus.Cases {
		if len(testCase.FrontendHrefs) < len(testCase.BackendCandidates) {
			underLinked++
		}
		if len(testCase.FrontendHrefs) > len(testCase.BackendCandidates) {
			t.Fatalf("%s: the corpus expects more client anchors than backend candidates",
				testCase.Name)
		}
	}
	if underLinked == 0 {
		t.Fatal("no corpus case exercises under-linking; the divergence cases have been lost")
	}
}
