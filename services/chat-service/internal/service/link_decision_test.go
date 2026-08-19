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
// nothing has decided — the distinction the edit path depends on.
func TestAggregateLinkDecisionClassifiesEveryVerdict(t *testing.T) {
	const safeURL = "https://example.test/cleared"
	const quietURL = "https://example.test/quiet"
	const unknownURL = "https://example.test/unknown"

	for _, test := range []struct {
		name             string
		urls             []string
		verdicts         map[string]urlsafety.Verdict
		wantInconclusive []string
		wantUndecided    []string
	}{
		{
			name:     "a cleared URL holds nothing up",
			urls:     []string{safeURL},
			verdicts: map[string]urlsafety.Verdict{safeURL: urlsafety.VerdictSafe},
		},
		{
			name:             "an inconclusive URL is decided, not pending",
			urls:             []string{quietURL},
			verdicts:         map[string]urlsafety.Verdict{quietURL: urlsafety.VerdictInconclusive},
			wantInconclusive: []string{quietURL},
		},
		{
			name:          "an absent verdict is undecided",
			urls:          []string{unknownURL},
			verdicts:      map[string]urlsafety.Verdict{},
			wantUndecided: []string{unknownURL},
		},
		{
			// The whole reason the default arm exists: a value this version does not
			// recognise must land in undecided, never read as a clearance.
			name:          "an unrecognised verdict is undecided",
			urls:          []string{unknownURL},
			verdicts:      map[string]urlsafety.Verdict{unknownURL: urlsafety.Verdict("from-the-future")},
			wantUndecided: []string{unknownURL},
		},
		{
			name: "the groups are kept apart in one pass",
			urls: []string{safeURL, quietURL, unknownURL},
			verdicts: map[string]urlsafety.Verdict{
				safeURL: urlsafety.VerdictSafe, quietURL: urlsafety.VerdictInconclusive,
			},
			wantInconclusive: []string{quietURL},
			wantUndecided:    []string{unknownURL},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			decision, err := aggregateLinkDecision(test.urls, test.verdicts)
			if err != nil {
				t.Fatalf("aggregateLinkDecision returned %v", err)
			}
			if !slices.Equal(decision.InconclusiveURLs, test.wantInconclusive) {
				t.Fatalf("inconclusive = %q, want %q", decision.InconclusiveURLs, test.wantInconclusive)
			}
			if !slices.Equal(decision.UndecidedURLs, test.wantUndecided) {
				t.Fatalf("undecided = %q, want %q", decision.UndecidedURLs, test.wantUndecided)
			}
		})
	}
}

// One condemned URL refuses the message whatever else it carries, and refuses it
// without classifying the rest.
func TestAggregateLinkDecisionRefusesACondemnedURL(t *testing.T) {
	const badURL = "https://example.test/bad"
	const quietURL = "https://example.test/quiet"

	decision, err := aggregateLinkDecision(
		[]string{badURL, quietURL},
		map[string]urlsafety.Verdict{
			badURL: urlsafety.VerdictMalicious, quietURL: urlsafety.VerdictInconclusive,
		},
	)
	if err != domain.ErrMaliciousURL {
		t.Fatalf("err = %v, want ErrMaliciousURL", err)
	}
	if len(decision.URLs) != 0 || len(decision.InconclusiveURLs) != 0 {
		t.Fatalf("a refused decision carried state: %+v", decision)
	}
}

// editState is where inconclusive and undecided stop being interchangeable: an
// edit publishes over the first and waits on the second.
func TestEditStateSeparatesDecidedFromUndecided(t *testing.T) {
	const someURL = "https://example.test/a"

	for _, test := range []struct {
		name      string
		decision  linkDecision
		wantState domain.MessageLinkSafety
		wantOK    bool
	}{
		{
			name:      "an undecided URL defers the edit",
			decision:  linkDecision{URLs: []string{someURL}, UndecidedURLs: []string{someURL}},
			wantState: domain.MessageLinkSafetyNone,
		},
		{
			name:      "an inconclusive URL publishes with the marker",
			decision:  linkDecision{URLs: []string{someURL}, InconclusiveURLs: []string{someURL}},
			wantState: domain.MessageLinkSafetyInconclusive,
			wantOK:    true,
		},
		{
			name:      "a fully cleared body publishes safe",
			decision:  linkDecision{URLs: []string{someURL}},
			wantState: domain.MessageLinkSafetySafe,
			wantOK:    true,
		},
		{
			name:      "a body with no links has no opinion",
			decision:  linkDecision{},
			wantState: domain.MessageLinkSafetyNone,
			wantOK:    true,
		},
		{
			// Undecided wins over inconclusive: one URL nothing has decided is
			// enough to hold the edit, whatever the others say.
			name: "undecided outranks inconclusive",
			decision: linkDecision{
				URLs:             []string{someURL},
				InconclusiveURLs: []string{someURL},
				UndecidedURLs:    []string{someURL},
			},
			wantState: domain.MessageLinkSafetyNone,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			state, ok := test.decision.editState()
			if state != test.wantState || ok != test.wantOK {
				t.Fatalf("editState() = (%q, %t), want (%q, %t)",
					state, ok, test.wantState, test.wantOK)
			}
		})
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
