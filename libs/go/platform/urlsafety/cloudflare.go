package urlsafety

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The provider contract, as documented by Cloudflare for the URL Scanner
// (Radar → Investigate → URL Scanner):
//
// Submit:
//
//	POST {base}/accounts/{account_id}/urlscanner/v2/scan
//	Authorization: Bearer {token}
//	Content-Type: application/json
//	{"url":"https://example.com/path","visibility":"Unlisted"}
//
// answering, unenveloped,
//
//	{"uuid":"...","api":"...","url":"...","visibility":"unlisted","message":"..."}
//
// Result:
//
//	GET {base}/accounts/{account_id}/urlscanner/v2/result/{scan_id}
//	Authorization: Bearer {token}
//
// answering, unenveloped,
//
//	{"task":{"uuid":"...","success":true,...},"verdicts":{"overall":{"malicious":false,...}},...}
//
// with the documented progress signal being the status code itself: "While the
// scan is in progress, the HTTP status code will be 404; once it is finished, it
// will be 200."
//
// The token permission is Account → URL Scanner → Edit — Edit rather than Read
// because submitting a scan creates one. Domain Intelligence (/intel/domain) is
// no longer part of this path at all: it answered about a domain, which is
// exactly the granularity RF-21 may not decide at. A hostname-level fast deny is
// tracked separately as issue #526 and is deliberately absent here.
//
// Nothing below invents a field, a parameter or a status. What is not in that
// contract is treated as an unusable answer, never as a clearance.
const (
	// cloudflareBaseURL is the API root. It is a constant and not configuration:
	// it is the provider's address, not a deployment choice, and an
	// operator-supplied endpoint would be a way to point a security check at
	// something that always answers "safe".
	cloudflareBaseURL = "https://api.cloudflare.com/client/v4"

	// requestTimeout bounds one HTTP exchange with the provider — a single
	// submit, or a single result read. It is not the budget for a whole scan:
	// that one is bounded by the caller's polling schedule, because a scan takes
	// far longer than any interactive request may.
	requestTimeout = 10 * time.Second

	// maxResponseBytes bounds what is read from the provider. A finished scan
	// report carries the whole page inventory, so this is generous compared to
	// the handful of fields actually parsed, and exists only so an unexpected
	// response cannot exhaust memory.
	maxResponseBytes = 1 << 20

	// scanVisibility keeps NChat's submissions out of the provider's public
	// listing. A canonical URL carries the path and the query, which is exactly
	// where internal identifiers and resource names live, so the default
	// ("Public", which lists the scan and makes it searchable by anyone) is not
	// acceptable for a URL a user pasted into a private conversation.
	//
	// Cloudflare documents the submit value capitalised and echoes it back
	// lower-cased; both spellings are the same setting.
	scanVisibility = "Unlisted"
)

// ErrScanPending marks a scan the provider has accepted but not finished.
//
// It is its own error and not an ErrUnavailable because the two demand opposite
// things from a caller: unavailable means the attempt failed and may be retried
// as a fresh attempt, pending means this exact scan is still running and must be
// read again by its id. Neither is ever a clearance.
var ErrScanPending = errors.New("url safety: scan still in progress")

// ErrScanInconclusive marks a scan the provider confirms is the one requested
// and confirms has reached a terminal state, but which produced no verdict this
// package can act on — the real production case this exists for is
// task.success=false with task.status="finished".
//
// It is its own error and neither of the other two: not ErrScanPending, because
// the scan is not running and asking again will not change the answer; not
// ErrUnavailable, because the exchange itself succeeded and the identity of the
// scan is confirmed — this is a known, terminal, unusable answer, not a failure
// to obtain one. A caller must stop polling this scan id and must never treat
// this as a clearance in either direction.
var ErrScanInconclusive = errors.New("url safety: scan finished without a usable verdict")

// taskStatusFinished is the only task.status value verdictFromReport trusts as
// "the provider is truly done with this scan". It gates ErrScanInconclusive:
// without it, a success=false report with no other information is refused as
// ErrUnavailable rather than assumed to be terminal.
const taskStatusFinished = "finished"

// CloudflareScanner submits URLs to Cloudflare URL Scanner and reads verdicts.
type CloudflareScanner struct {
	baseURL   string
	accountID string
	token     string
	client    *http.Client
}

// NewCloudflareScanner builds the provider client. Both credentials are
// required: a client without them could only ever produce failures, so it is
// refused here rather than at the first message someone tries to send.
func NewCloudflareScanner(accountID, token string) (*CloudflareScanner, error) {
	return newCloudflareScanner(cloudflareBaseURL, accountID, token, &http.Client{Timeout: requestTimeout})
}

// newCloudflareScanner is NewCloudflareScanner with the base URL and the HTTP
// client supplied, so a test can reach an httptest server without the
// production address being configurable.
func newCloudflareScanner(baseURL, accountID, token string, client *http.Client) (*CloudflareScanner, error) {
	accountID, token = strings.TrimSpace(accountID), strings.TrimSpace(token)
	if accountID == "" || token == "" {
		return nil, errors.New("url safety: cloudflare account id and api token are required")
	}
	if client == nil {
		client = &http.Client{Timeout: requestTimeout}
	}
	return &CloudflareScanner{
		baseURL:   strings.TrimSuffix(baseURL, "/"),
		accountID: accountID,
		token:     token,
		client:    client,
	}, nil
}

// submitRequest is the whole body NChat sends.
//
// Two fields, and the omissions are the point. customHeaders, referer,
// customagent and screenshotsResolutions are all documented and all absent: the
// provider fetches the URL on its own behalf, so anything NChat added would be
// NChat's data leaving with the submission. In particular nothing here can carry
// a user's cookies, the caller's Authorization header, or any internal header —
// there is no field in this struct that could hold one.
type submitRequest struct {
	URL        string `json:"url"`
	Visibility string `json:"visibility"`
}

// submitResponse is the part of the submit answer this client reads.
type submitResponse struct {
	UUID string `json:"uuid"`
}

// resultResponse is the part of the finished report this client reads.
//
// task.success and verdicts.overall are pointers so that "the provider did not
// send this field" is distinguishable from "the provider sent false". A missing
// verdict must never read as a clean one, and a non-pointer bool would make
// those two the same value.
type resultResponse struct {
	Task struct {
		UUID    string `json:"uuid"`
		Success *bool  `json:"success"`
		// Status is the provider's own lifecycle label for the scan (observed
		// value: "finished"). It is what distinguishes a terminal report with no
		// verdict from one that simply cannot be trusted yet.
		Status string `json:"status"`
		// Time and TimeEnd are when the provider says the scan happened, RFC3339.
		//
		// They exist here for one reason: a verdict is evidence with an age, and
		// reconciliation may adopt a scan that ran hours ago. Dating such a
		// clearance from the moment it was *adopted* would hand a full fresh
		// lifetime to stale evidence — see EvidenceTime and Service.Reconcile.
		//
		// Both are optional. Cloudflare documents `time` on the task; `timeEnd` is
		// read when present because a scan's conclusion is a better statement of
		// when the verdict was formed than its submission. Neither is trusted to
		// exist, and the caller has a conservative fallback when both are absent.
		Time    string `json:"time"`
		TimeEnd string `json:"timeEnd"`
	} `json:"task"`
	Verdicts struct {
		Overall *struct {
			// HasVerdicts is a pointer for the same reason Malicious is: "the
			// provider did not send this field" must not read the same as "the
			// provider sent false". A finished scan with no verdicts at all is
			// exactly the report this package must refuse rather than clear.
			HasVerdicts *bool `json:"hasVerdicts"`
			Malicious   *bool `json:"malicious"`
		} `json:"overall"`
	} `json:"verdicts"`
}

// SubmitScan asks the provider to scan a canonical URL and returns the scan id.
//
// The URL travels in the JSON body and nowhere else, so it cannot reshape the
// path, and the token travels in a header and never in a URL, so it cannot reach
// an access log, a referrer or an error message.
func (c *CloudflareScanner) SubmitScan(ctx context.Context, canonicalURL string) (string, error) {
	body, err := json.Marshal(submitRequest{URL: canonicalURL, Visibility: scanVisibility})
	if err != nil {
		return "", ErrUnavailable
	}
	endpoint := c.accountPath("/urlscanner/v2/scan")
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", ErrUnavailable
	}
	request.Header.Set("Content-Type", "application/json")
	c.authorize(request)

	response, err := c.do(ctx, request)
	if err != nil {
		return "", err
	}
	defer func() { _ = response.Body.Close() }()

	// The provider documents 200 and 201 for an accepted submission. Anything
	// else — including 401 and 403, which mean this deployment's credentials are
	// wrong, and 429, which means the quota is spent — is a failure and never a
	// scan id. A misconfigured token that produced a usable answer is the exact
	// silent failure this package exists to prevent.
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated {
		return "", ErrUnavailable
	}
	var decoded submitResponse
	if err := decodeExactlyOne(response.Body, &decoded); err != nil {
		return "", ErrUnavailable
	}
	if strings.TrimSpace(decoded.UUID) == "" {
		return "", ErrUnavailable
	}
	return decoded.UUID, nil
}

// GetScanResult reads a submitted scan.
//
// Four outcomes: a finished report with a usable verdict yields VerdictSafe or
// VerdictMalicious; a scan still running yields ErrScanPending; a scan the
// provider confirms is finished and confirms is the one requested, but which
// produced no usable verdict, yields ErrScanInconclusive; everything else — an
// untrusted or malformed answer, a transport failure — yields ErrUnavailable. A
// pending scan is not a verdict, an inconclusive scan is not a verdict, and a
// failure is not a verdict, so none of the three can be stored as one.
//
// It is GetScanReport with the evidence time discarded, which is right for the
// polling path: a scan submitted a moment ago has an age indistinguishable from
// now. Reconciliation, which adopts scans that may be hours old, uses
// GetScanReport instead.
func (c *CloudflareScanner) GetScanResult(ctx context.Context, scanID string) (Verdict, error) {
	verdict, _, err := c.GetScanReport(ctx, scanID)
	return verdict, err
}

// GetScanReport reads a submitted scan and also reports when the provider says
// it concluded.
//
// The second return is the evidence time, and it is zero whenever the provider
// did not supply a usable one. It is never a local clock reading: a caller that
// substituted its own "now" for a missing provider timestamp would be inventing
// freshness, which is precisely the finding this exists to close. A zero time is
// an honest "the provider did not say", and the caller decides what to do with
// that — see Service.Reconcile, which falls back to the scan's submission time
// and refuses to clear anything when even that is unavailable.
func (c *CloudflareScanner) GetScanReport(
	ctx context.Context, scanID string,
) (Verdict, time.Time, error) {
	if strings.TrimSpace(scanID) == "" {
		return VerdictUnknown, time.Time{}, ErrUnavailable
	}
	endpoint := c.accountPath("/urlscanner/v2/result/" + url.PathEscape(scanID))
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return VerdictUnknown, time.Time{}, ErrUnavailable
	}
	c.authorize(request)

	response, err := c.do(ctx, request)
	if err != nil {
		return VerdictUnknown, time.Time{}, err
	}
	defer func() { _ = response.Body.Close() }()

	// The documented progress signal is the status code: 404 while the scan is
	// in progress, 200 once it is finished.
	if response.StatusCode == http.StatusNotFound {
		return VerdictUnknown, time.Time{}, ErrScanPending
	}
	if response.StatusCode != http.StatusOK {
		return VerdictUnknown, time.Time{}, ErrUnavailable
	}
	var decoded resultResponse
	if err := decodeExactlyOne(response.Body, &decoded); err != nil {
		return VerdictUnknown, time.Time{}, ErrUnavailable
	}
	verdict, err := verdictFromReport(decoded, scanID)
	return verdict, reportEvidenceTime(decoded), err
}

// reportEvidenceTime reads when the provider says the scan concluded.
//
// `timeEnd` is preferred over `time` because a verdict is formed when the scan
// finishes, not when it was queued; `time` is the fallback because Cloudflare
// documents it and `timeEnd` is not guaranteed. An unparseable or absent value
// yields the zero time rather than a guess.
//
// Nothing here consults a clock. Deriving a missing provider timestamp from the
// local time is exactly how stale evidence acquires a fresh lifetime.
func reportEvidenceTime(report resultResponse) time.Time {
	for _, raw := range []string{report.Task.TimeEnd, report.Task.Time} {
		if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(raw)); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

// verdictFromReport turns a decoded report into a verdict, refuses it as
// untrusted, or reports it as a known terminal scan with no usable verdict.
//
// The identity check runs first and unconditionally: a report cannot be
// inconclusive, safe or malicious about a scan it does not demonstrably
// describe. task.uuid has to be present and has to equal the id requested —
// this is the one check that matters most, because a cached, misrouted or
// substituted response describing a *different* URL's scan would otherwise
// clear or condemn this one.
//
// Past that, every remaining check is a way the answer could still fail to
// carry a usable verdict even though it does describe the right scan:
//
//   - task.success had to be present and true. It was previously only rejected
//     when explicitly false, so a report that omitted the field — a truncated
//     write, a proxy's error envelope, a future response shape — was read as a
//     completed scan;
//   - verdicts.overall.hasVerdicts had to be present and true. This is the field
//     the production incident this function was rewritten for turned on: a
//     report can have task.success=false, task.status="finished" and
//     hasVerdicts=false all at once — the scan genuinely ran and genuinely
//     produced nothing, and that is not the same fact as "the exchange failed";
//   - verdicts.overall.malicious had to be present, once the two checks above
//     passed.
//
// A failure of the identity check is always ErrUnavailable — an untrusted
// answer is never inconclusive, whatever else it says. A failure of one of the
// later checks is ErrScanInconclusive when the provider's own task.status says
// "finished" — a report this package has independent evidence is terminal — and
// ErrUnavailable otherwise, because there is no basis yet for treating a scan
// that has not confirmed it is done as anything but a failed exchange to retry.
// Neither outcome is ever a verdict, never cached as a clearance, never
// persisted as one.
func verdictFromReport(report resultResponse, scanID string) (Verdict, error) {
	reportedID := strings.TrimSpace(report.Task.UUID)
	if reportedID == "" || reportedID != strings.TrimSpace(scanID) {
		return VerdictUnknown, ErrUnavailable
	}
	finished := strings.TrimSpace(report.Task.Status) == taskStatusFinished

	if report.Task.Success == nil || !*report.Task.Success {
		if finished {
			return VerdictUnknown, ErrScanInconclusive
		}
		return VerdictUnknown, ErrUnavailable
	}
	if report.Verdicts.Overall == nil ||
		report.Verdicts.Overall.HasVerdicts == nil || !*report.Verdicts.Overall.HasVerdicts {
		if finished {
			return VerdictUnknown, ErrScanInconclusive
		}
		return VerdictUnknown, ErrUnavailable
	}
	if report.Verdicts.Overall.Malicious == nil {
		return VerdictUnknown, ErrUnavailable
	}
	if *report.Verdicts.Overall.Malicious {
		return VerdictMalicious, nil
	}
	return VerdictSafe, nil
}

// do performs a request and separates the caller going away from the provider
// failing.
//
// The caller's own context ending is reported as itself, because that is the
// caller going away rather than a fact about the URL. Everything else —
// including this client's own timeout, which also wraps context.DeadlineExceeded
// — is flattened: the transport's message names the URL it dialled, which
// carries the account identifier, so it is dropped rather than wrapped. Asking
// ctx and not the error is what keeps those two cases apart.
func (c *CloudflareScanner) do(ctx context.Context, request *http.Request) (*http.Response, error) {
	response, err := c.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrUnavailable
	}
	return response, nil
}

func (c *CloudflareScanner) accountPath(suffix string) string {
	return c.baseURL + "/accounts/" + url.PathEscape(c.accountID) + suffix
}

// authorize attaches the credentials. Never logged, never in a URL, never echoed
// back to a client.
func (c *CloudflareScanner) authorize(request *http.Request) {
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Accept", "application/json")
}

// decodeExactlyOne decodes one JSON document and refuses anything after it.
//
// json.Decoder.Decode stops at the end of the first value and reports success,
// so `{"uuid":"a"}garbage` and `{"uuid":"a"}{"uuid":"b"}` both decode cleanly
// and silently discard the rest. For a security verdict that is not acceptable:
// a body carrying a second document is a body this client does not understand,
// and understanding half of it is how a proxy or a partial write turns into an
// answer. So the stream must be at EOF once the value is read.
func decodeExactlyOne(body io.Reader, destination any) error {
	decoder := json.NewDecoder(io.LimitReader(body, maxResponseBytes))
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		return errors.New("url safety: unexpected trailing data in provider response")
	}
	return nil
}
