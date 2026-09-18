package service

import (
	"bytes"
	"context"
	"log/slog"
	"slices"
	"testing"

	"github.com/nicrepository/nchat/libs/go/platform/urlsafety"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// The classification is the single fold both doors read. These assert the fold
// itself, so a URL that is decided-but-silent can never be confused with a URL
// nothing has decided, and — since issue #807 — so a condemned URL blocks its
// own link and nothing else.
func TestAggregateLinkDecisionClassifiesEveryVerdict(t *testing.T) {
	const safeURL = "https://example.test/cleared"
	const quietURL = "https://example.test/quiet"
	const unknownURL = "https://example.test/unknown"
	const badURL = "https://example.test/bad"

	for _, test := range []struct {
		name             string
		urls             []string
		verdicts         map[string]urlsafety.Verdict
		wantSafe         []string
		wantInconclusive []string
		wantUndecided    []string
		wantMalicious    []string
		wantAggregate    domain.MessageLinkSafety
	}{
		{
			name:          "a cleared URL holds nothing up",
			urls:          []string{safeURL},
			verdicts:      map[string]urlsafety.Verdict{safeURL: urlsafety.VerdictSafe},
			wantSafe:      []string{safeURL},
			wantAggregate: domain.MessageLinkSafetySafe,
		},
		{
			name:             "an inconclusive URL is decided, not pending",
			urls:             []string{quietURL},
			verdicts:         map[string]urlsafety.Verdict{quietURL: urlsafety.VerdictInconclusive},
			wantInconclusive: []string{quietURL},
			wantAggregate:    domain.MessageLinkSafetyInconclusive,
		},
		{
			name:          "an absent verdict is undecided",
			urls:          []string{unknownURL},
			verdicts:      map[string]urlsafety.Verdict{},
			wantUndecided: []string{unknownURL},
			wantAggregate: domain.MessageLinkSafetyNone,
		},
		{
			// The whole reason the default arm exists: a value this version does not
			// recognise must land in undecided, never read as a clearance.
			name:          "an unrecognised verdict is undecided",
			urls:          []string{unknownURL},
			verdicts:      map[string]urlsafety.Verdict{unknownURL: urlsafety.Verdict("from-the-future")},
			wantUndecided: []string{unknownURL},
			wantAggregate: domain.MessageLinkSafetyNone,
		},
		{
			// A condemned URL no longer refuses the message: it is one blocked link
			// beside the others, each keeping its own state.
			name: "a condemned URL blocks only itself",
			urls: []string{badURL, safeURL, unknownURL},
			verdicts: map[string]urlsafety.Verdict{
				badURL: urlsafety.VerdictMalicious, safeURL: urlsafety.VerdictSafe,
			},
			wantMalicious: []string{badURL},
			wantSafe:      []string{safeURL},
			wantUndecided: []string{unknownURL},
			wantAggregate: domain.MessageLinkSafetyMalicious,
		},
		{
			name: "the groups are kept apart in one pass",
			urls: []string{safeURL, quietURL, unknownURL},
			verdicts: map[string]urlsafety.Verdict{
				safeURL: urlsafety.VerdictSafe, quietURL: urlsafety.VerdictInconclusive,
			},
			wantSafe:         []string{safeURL},
			wantInconclusive: []string{quietURL},
			wantUndecided:    []string{unknownURL},
			wantAggregate:    domain.MessageLinkSafetyNone,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			decision := aggregateLinkDecision(test.urls, test.verdicts)
			if !slices.Equal(decision.SafeURLs, test.wantSafe) {
				t.Fatalf("safe = %q, want %q", decision.SafeURLs, test.wantSafe)
			}
			if !slices.Equal(decision.InconclusiveURLs, test.wantInconclusive) {
				t.Fatalf("inconclusive = %q, want %q", decision.InconclusiveURLs, test.wantInconclusive)
			}
			if !slices.Equal(decision.UndecidedURLs, test.wantUndecided) {
				t.Fatalf("undecided = %q, want %q", decision.UndecidedURLs, test.wantUndecided)
			}
			if !slices.Equal(decision.MaliciousURLs, test.wantMalicious) {
				t.Fatalf("malicious = %q, want %q", decision.MaliciousURLs, test.wantMalicious)
			}
			if !slices.Equal(decision.URLs, test.urls) {
				t.Fatalf("every URL must be recorded, got %q", decision.URLs)
			}
			if got := decision.aggregateState(); got != test.wantAggregate {
				t.Fatalf("aggregateState() = %q, want %q", got, test.wantAggregate)
			}
			if decision.messageStatus() != "" {
				t.Fatal("no link decision may withhold a message")
			}
		})
	}
}

// A body with no links has no opinion, and admission asks only about the
// undecided URLs.
func TestLinkDecisionAdmitsOnlyUndecidedURLs(t *testing.T) {
	if got := (linkDecision{}).aggregateState(); got != domain.MessageLinkSafetyNone {
		t.Fatalf("empty decision aggregate = %q", got)
	}
	decision := linkDecision{
		URLs: []string{"a", "b", "c"}, SafeURLs: []string{"a"},
		InconclusiveURLs: []string{"b"}, UndecidedURLs: []string{"c"},
	}
	if !slices.Equal(decision.admissionURLs(), []string{"c"}) {
		t.Fatalf("admission = %q", decision.admissionURLs())
	}
	if decision.fingerprint("body") == "" || (linkDecision{}).fingerprint("body") != "" {
		t.Fatal("fingerprint must bind a body with links and nothing else")
	}
}

// A trailing closer belongs to the URL only when the URL itself opened it.
// Getting this wrong either truncates a real link or swallows the punctuation
// after it, and both change which URL was actually scanned.
func TestTrailingCloserBelongsToTheURLOnlyWhenOpened(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		want []string
	}{
		{
			name: "a closer the URL opened is kept",
			body: "see https://example.test/wiki/Foo_(bar) now",
			want: []string{"https://example.test/wiki/Foo_(bar)"},
		},
		{
			name: "a closer the URL never opened is punctuation",
			body: "(see https://example.test/page) now",
			want: []string{"https://example.test/page"},
		},
		{
			// The depth counter, not a boolean: the inner pair closes itself, so the
			// final closer still matches the outer opener and stays in the URL.
			name: "nested pairs are balanced by depth",
			body: "see https://example.test/a(b(c)d) now",
			want: []string{"https://example.test/a(b(c)d)"},
		},
		{
			// A closer with nothing open must not drive the depth negative, or a
			// later opener would look already-closed.
			name: "an unopened closer inside does not go negative",
			body: "see https://example.test/a)b(c) now",
			want: []string{"https://example.test/a)b(c)"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := scanURLCandidates(test.body); !slices.Equal(got, test.want) {
				t.Fatalf("scanURLCandidates(%q) = %q, want %q", test.body, got, test.want)
			}
		})
	}
}

// A cancelled context means the process is going away, and the step that failed
// failed *because* of that. Logging it turns an ordinary shutdown into a burst
// of warnings that look like an incident.
func TestLogFailureIsSilentAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	NewLinkReconcileService(nil, nil, logger).
		logFailure(ctx, "reconcile", storage.InconclusiveScan{}, context.Canceled)
	(&LinkScanService{logger: logger}).
		logFailure(ctx, "poll", storage.LinkScanJob{}, context.Canceled)

	if buf.Len() != 0 {
		t.Fatalf("a cancelled step logged: %s", buf.String())
	}

	// The same call outside cancellation still reports, so the guard above is a
	// shutdown rule and not a silenced logger.
	NewLinkReconcileService(nil, nil, logger).
		logFailure(t.Context(), "reconcile", storage.InconclusiveScan{}, context.Canceled)
	if buf.Len() == 0 {
		t.Fatal("a live failure was not logged")
	}
}

// The publisher is wired after the hub exists, so a service built without one
// must accept it later. Nothing else in the package proves the setter is
// connected to the field the send path reads.
func TestSetPublisherIsWiredAfterConstruction(t *testing.T) {
	scanService := &LinkScanService{}
	if scanService.publisher != nil {
		t.Fatal("a freshly built scan service already had a publisher")
	}
	scanService.SetPublisher(nopMessageEventPublisher{})
	if scanService.publisher == nil {
		t.Fatal("SetPublisher did not attach the broadcaster")
	}
}

type nopMessageEventPublisher struct{}

func (nopMessageEventPublisher) PublishMessageCreated(
	context.Context, string, string, string, domain.Message,
) {
}
