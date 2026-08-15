package urlsafety

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Recovering a submission whose outcome this process never learned.
//
// # The window this exists for
//
// Submitting is two steps that cannot be one: the provider accepts the scan and
// answers with a uuid, and then that uuid is written down. A crash between them
// leaves a URL that *has* been submitted and a database that cannot prove it.
// Resubmitting on that evidence is how one logical scan becomes two provider
// scans, billed twice, for every restart that lands in the gap.
//
// Cloudflare's URL Scanner has no idempotency token — there is no request key to
// send that would make a repeated POST return the first scan — so the gap cannot
// be closed by the submit call itself. What the provider does offer is a search
// over the account's own scans, and that is what this file uses: before ever
// submitting again, ask whether the scan we may have created already exists.
//
// # The documented contract
//
//	GET {base}/accounts/{account_id}/urlscanner/v2/search?q={query}&size={n}
//	Authorization: Bearer {token}
//
// answering, unenveloped,
//
//	{"tasks":[{"uuid":"...","url":"...","time":"...","visibility":"unlisted","success":true}]}
//
// The path is account-scoped, which is what confines the answer to scans this
// deployment's own credentials created — there is no cross-account result to
// filter out, because there is no cross-account result.
//
// Nothing here invents a parameter or a field. `q` and `size` are the two the
// search takes, and the task fields read below are the ones it returns.
const (
	// searchLookbackLimit bounds how many results one reconciliation reads.
	//
	// Small on purpose. The question is "did the scan I may have started exist",
	// and the answer is among the newest few; a larger page would be a larger
	// response to parse for no additional certainty.
	searchLookbackLimit = 10

	// searchClockTolerance absorbs the difference between this process's clock
	// and the provider's when deciding whether a scan is new enough to be ours.
	//
	// It only ever makes the filter more permissive, and the URL equality check
	// is what actually establishes identity, so a generous value costs nothing:
	// a scan of a *different* URL is refused regardless of when it happened.
	searchClockTolerance = 2 * time.Minute
)

// ErrSearchUnsupported reports a provider client that cannot search.
//
// Its own error so a caller can tell "this deployment cannot reconcile" from
// "reconciliation ran and found nothing" — the two have different remedies, and
// neither is ever a reason to submit again.
var ErrSearchUnsupported = errors.New("url safety: provider client cannot search scans")

// ScanRecord is one scan the provider reports for a URL.
//
// Deliberately only what identity is decided from. There is no verdict here: a
// search result is used to recover a scan *id*, and the verdict is then read
// through the ordinary result endpoint, which applies the strict checks that
// decide whether an answer may clear a URL. Reconciliation must not become a
// second, weaker path to a verdict.
type ScanRecord struct {
	UUID        string
	URL         string
	SubmittedAt time.Time
	Visibility  string
}

// searchResponse is the part of the search answer this client reads.
type searchResponse struct {
	Tasks []struct {
		UUID       string `json:"uuid"`
		URL        string `json:"url"`
		Time       string `json:"time"`
		Visibility string `json:"visibility"`
	} `json:"tasks"`
}

// FindRecentScan returns the scan this deployment most plausibly created for
// canonicalURL at or after `since`, or ErrNotCheckable when there is none.
//
// Every result is filtered before it can be adopted, and each filter closes a
// way the wrong scan could be picked up:
//
//   - the reported url must canonicalize to the same URL. This is the identity
//     check; without it a search that matched loosely would hand back a scan of
//     a different page, and the verdict read from it would clear the wrong one;
//   - the scan must not predate the attempt being reconciled. A scan from last
//     week is somebody else's history, not the submission whose outcome is
//     unknown;
//   - the visibility must be the one this client submits. A public scan is not
//     one of ours, because nothing here ever submits a public one;
//   - the uuid must be present and non-empty.
//
// When several survive, the newest is taken. That is deterministic and it is the
// one the uncertain attempt most likely produced; the alternative — refusing to
// choose — would leave the URL stuck and eventually cause the resubmission this
// whole path exists to avoid. The caller is told how many matched so it can
// report the ambiguity as a metric.
func (c *CloudflareScanner) FindRecentScan(
	ctx context.Context, canonicalURL string, since time.Time,
) (ScanRecord, int, error) {
	query, err := scanSearchQuery(canonicalURL)
	if err != nil {
		return ScanRecord{}, 0, err
	}
	endpoint := c.accountPath("/urlscanner/v2/search") + "?" + url.Values{
		"q":    []string{query},
		"size": []string{strconv.Itoa(searchLookbackLimit)},
	}.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ScanRecord{}, 0, ErrUnavailable
	}
	c.authorize(request)

	response, err := c.do(ctx, request)
	if err != nil {
		return ScanRecord{}, 0, err
	}
	defer func() { _ = response.Body.Close() }()

	// Anything but 200 is a failed lookup, never "there is no such scan". A 429
	// or a 5xx says the provider could not answer; treating that as absence is
	// exactly how a throttled search would cause a duplicate submission.
	if response.StatusCode != http.StatusOK {
		return ScanRecord{}, 0, ErrUnavailable
	}
	var decoded searchResponse
	if err := decodeExactlyOne(response.Body, &decoded); err != nil {
		return ScanRecord{}, 0, ErrUnavailable
	}
	return selectReconcilableScan(decoded, canonicalURL, since)
}

// selectReconcilableScan applies the identity filters and picks the newest
// survivor.
func selectReconcilableScan(
	response searchResponse, canonicalURL string, since time.Time,
) (ScanRecord, int, error) {
	earliest := since.Add(-searchClockTolerance)
	var best ScanRecord
	matches := 0
	for _, task := range response.Tasks {
		record, ok := eligibleScan(task.UUID, task.URL, task.Time, task.Visibility, canonicalURL, earliest)
		if !ok {
			continue
		}
		matches++
		if best.UUID == "" || record.SubmittedAt.After(best.SubmittedAt) {
			best = record
		}
	}
	if matches == 0 {
		// Not an error and not a clearance: there is simply nothing to adopt. The
		// caller stays uncertain and asks again rather than submitting.
		return ScanRecord{}, 0, ErrNotCheckable
	}
	return best, matches, nil
}

// eligibleScan reports whether one search result may be adopted as ours.
func eligibleScan(
	uuid, reportedURL, reportedTime, visibility, canonicalURL string, earliest time.Time,
) (ScanRecord, bool) {
	uuid = strings.TrimSpace(uuid)
	if uuid == "" {
		return ScanRecord{}, false
	}
	// Canonicalized on both sides rather than compared as written: the provider
	// echoes the URL it was given, and a spelling difference that
	// CanonicalizeURL already treats as the same resource must not defeat the
	// match. A URL that will not canonicalize cannot be ours — nothing here ever
	// submitted one.
	reported, err := CanonicalizeURL(reportedURL)
	if err != nil || reported != canonicalURL {
		return ScanRecord{}, false
	}
	if !strings.EqualFold(strings.TrimSpace(visibility), scanVisibility) {
		return ScanRecord{}, false
	}
	submittedAt, err := time.Parse(time.RFC3339, strings.TrimSpace(reportedTime))
	if err != nil || submittedAt.Before(earliest) {
		return ScanRecord{}, false
	}
	return ScanRecord{
		UUID: uuid, URL: reported, SubmittedAt: submittedAt, Visibility: visibility,
	}, true
}

// scanSearchQuery builds the provider's search expression for one exact URL.
//
// The URL is untrusted input that a user chose, and it is being placed inside a
// quoted term in the provider's filter syntax. Building that by concatenation is
// how a URL containing a quote stops being a value and starts being syntax — a
// term the sender wrote, evaluated by the provider, changing which scans the
// answer describes. So the value is escaped for that destination, and anything
// that cannot be safely represented is refused rather than encoded optimistically.
func scanSearchQuery(canonicalURL string) (string, error) {
	term, err := escapeSearchTerm(canonicalURL)
	if err != nil {
		return "", err
	}
	return "task.url:" + term, nil
}

// escapeSearchTerm renders a value as one quoted term.
//
// Two characters are structural inside a quoted term — the quote that ends it
// and the backslash that escapes — and both are backslash-escaped. Control
// characters are refused outright instead: a canonical URL cannot contain one
// (CanonicalizeURL parses and percent-encodes before this is ever reached), so
// their presence means the value did not come from where it is supposed to, and
// a newline in particular is the classic way to append a second clause.
func escapeSearchTerm(value string) (string, error) {
	if value == "" || len(value) > maxURLLength {
		return "", ErrNotCheckable
	}
	var escaped strings.Builder
	escaped.Grow(len(value) + 2)
	escaped.WriteByte('"')
	for _, r := range value {
		switch {
		case r == '"' || r == '\\':
			escaped.WriteByte('\\')
			escaped.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			return "", ErrNotCheckable
		default:
			escaped.WriteRune(r)
		}
	}
	escaped.WriteByte('"')
	return escaped.String(), nil
}
