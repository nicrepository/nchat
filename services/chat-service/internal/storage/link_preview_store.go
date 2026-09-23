package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nicrepository/nchat/libs/go/platform/urlsafety"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// The rich preview queue (issue #807 §15-21).
//
// Same shape as the scan queue: a claim with a lease, an attempt counter that
// stretches the retry, a deadline that ends the waiting, and a partial index
// that is empty once every row is terminal. Keyed by (workspace, canonical URL)
// because preview metadata is workspace-scoped by design.

const (
	// linkPreviewLease outlives one fetch — the fetcher's whole-exchange budget
	// is seconds — with margin for the image download and the database writes.
	linkPreviewLease = 60 * time.Second
	// linkPreviewBackoffSteps caps the retry stretch: lease × min(attempts, steps).
	linkPreviewBackoffSteps = 5
	// linkPreviewMaxAttempts bounds retries of a transient failure. Three fetch
	// attempts over a few minutes is generous for a page that is merely slow and
	// stops well short of hammering one that is down.
	linkPreviewMaxAttempts = 3
	// LinkPreviewDeadline is how long a preview may stay queued or fetching —
	// waiting on redirect hops to clear, for instance — before it fails. The
	// link itself has been clickable the whole time.
	LinkPreviewDeadline = 30 * time.Minute
	// LinkPreviewTTL is how long a ready preview is served before it is fetched
	// again the next time a message names the URL. Independent of the safety
	// TTL by design: a page's title changes on a different clock from its
	// reputation.
	LinkPreviewTTL = 24 * time.Hour

	// Failure reasons, mirroring the CHECK on chat.link_previews.
	PreviewFailureBlocked         = "blocked"
	PreviewFailureRedirectRefused = "redirect_refused"
	PreviewFailureTimeout         = "timeout"
	PreviewFailureUpstream        = "upstream"
	PreviewFailureUnsupported     = "unsupported_content"
	PreviewFailureNoMetadata      = "no_metadata"
	PreviewFailureDeadline        = "deadline"
	PreviewFailureDisabled        = "disabled"
	PreviewFailureNotSafe         = "not_safe"
)

// LinkPreviewRow is one stored preview, image bytes excluded.
type LinkPreviewRow struct {
	ID           string
	WorkspaceID  string
	CanonicalURL string
	State        string
	SiteName     string
	Title        string
	Description  string
	HasImage     bool
	ImageWidth   int
	ImageHeight  int
	UpdatedAt    time.Time
}

// LinkPreviewJob is one claimed preview. ClaimID is the claim's identity: the
// store accepts a completion or failure only from the claim that holds it.
type LinkPreviewJob struct {
	ID           string
	WorkspaceID  string
	CanonicalURL string
	Attempts     int
	ClaimID      string
}

// LinkPreviewResult is what a successful fetch stores.
type LinkPreviewResult struct {
	SiteName         string
	Title            string
	Description      string
	ImageData        []byte
	ImageContentType string
	ImageWidth       int
	ImageHeight      int
}

// LinkPreviewImage is the derived asset as served.
type LinkPreviewImage struct {
	Data        []byte
	ContentType string
}

// ErrLinkPreviewConflict reports that a compare-and-set on a preview row lost:
// the row is no longer fetching, or another claim holds it now. The caller's
// result is discarded; the current claim owns the outcome.
var ErrLinkPreviewConflict = errors.New("link preview: superseded")

// QueueLinkPreviews records that these URLs, all of which the caller has just
// seen with a fresh safe verdict, need a preview in this workspace.
//
// Idempotent: an existing queued, fetching or fresh ready row is left alone. A
// terminal row past its TTL — ready and expired, or failed — is requeued, which
// is how a preview refreshes: the next message naming the URL asks again.
// Nothing here checks the verdict; the worker re-checks it at claim time, and
// the read path re-checks it again. A queue entry is a request, not a grant.
func (s *PGXMessageStore) QueueLinkPreviews(ctx context.Context, workspaceID string, canonicalURLs []string) error {
	if len(canonicalURLs) == 0 || workspaceID == "" {
		return nil
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO chat.link_previews AS p (workspace_id, canonical_url, deadline_at)
		SELECT $1::uuid, url, now() + ($3 * interval '1 second')
		FROM unnest($2::text[]) AS urls(url)
		ON CONFLICT (workspace_id, canonical_url) DO UPDATE
		   SET state = 'queued', attempts = 0, next_attempt_at = NULL, lease_until = NULL,
		       failure_reason = NULL, deadline_at = EXCLUDED.deadline_at, updated_at = now()
		 WHERE p.state IN ('failed', 'unsupported')
		    OR (p.state = 'ready' AND p.expires_at <= now())`,
		workspaceID, uniqueSortedURLs(canonicalURLs), LinkPreviewDeadline.Seconds())
	if err != nil {
		return fmt.Errorf("queue link previews: %w", err)
	}
	return nil
}

// ClaimDueLinkPreviews leases up to batchSize due previews, in one statement.
//
// The claim is the schedule and the attempt counter, as in the scan queue. Only
// a URL whose target is currently `safe` and fresh is claimed: a preview of a
// URL whose clearance lapsed or flipped is failed by the claim itself rather
// than fetched, and no fetch of a non-safe URL is reachable from here.
func (s *PGXMessageStore) ClaimDueLinkPreviews(ctx context.Context, batchSize int) ([]LinkPreviewJob, error) {
	if batchSize <= 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, claimDueLinkPreviewsQuery,
		batchSize, linkPreviewLease.Seconds(), linkPreviewBackoffSteps, urlsafety.VerdictTTL.Seconds())
	if err != nil {
		return nil, fmt.Errorf("claim due link previews: %w", err)
	}
	defer rows.Close()
	var jobs []LinkPreviewJob
	for rows.Next() {
		var job LinkPreviewJob
		if err := rows.Scan(&job.ID, &job.WorkspaceID, &job.CanonicalURL, &job.Attempts, &job.ClaimID); err != nil {
			return nil, fmt.Errorf("scan link preview job: %w", err)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("claim due link previews: %w", err)
	}
	return jobs, nil
}

// claimDueLinkPreviewsQuery claims only previews whose target is safe *and*
// fresh — the same definition every other reader applies (freshVerdictSQL). A
// clearance that expired is no clearance: the row waits until a new verdict
// lands, and fails at its deadline if none does. Each claim mints a new claim
// id; the claim that holds it is the only one whose outcome is accepted. A row
// past its deadline is never claimed (openWithinDeadlineSQL), whatever the sweep
// has or has not reached yet.
var claimDueLinkPreviewsQuery = `
	WITH due AS (
		SELECT p.id
		FROM chat.link_previews p
		JOIN chat.link_scans ls ON ls.canonical_url = p.canonical_url
		WHERE ` + openWithinDeadlineSQL("p") + `
		  AND (p.next_attempt_at IS NULL OR p.next_attempt_at <= now())
		  AND (p.lease_until IS NULL OR p.lease_until <= now())
		  AND ` + safeFreshVerdictSQL("ls", "$4") + `
		ORDER BY p.next_attempt_at NULLS FIRST, p.created_at
		LIMIT $1
		FOR UPDATE OF p SKIP LOCKED
	)
	UPDATE chat.link_previews p
	   SET state = 'fetching',
	       attempts = LEAST(p.attempts + 1, 32767),
	       lease_until = now() + ($2 * interval '1 second'),
	       claim_id = gen_random_uuid(),
	       next_attempt_at = now() + ($2 * LEAST(p.attempts + 1, $3) * interval '1 second'),
	       updated_at = now()
	  FROM due
	 WHERE p.id = due.id
	RETURNING p.id::text, p.workspace_id::text, p.canonical_url, p.attempts, p.claim_id::text`

// CompleteLinkPreview stores a fetched preview and returns the row for the
// announcement. Compare-and-set on the claim identity and the deadline: a row
// that is no longer fetching, that another claim has since taken, or whose
// deadline passed while the fetch was running answers ErrLinkPreviewConflict
// and is left exactly as it is — the last case for the sweep to end.
func (s *PGXMessageStore) CompleteLinkPreview(ctx context.Context, claim LinkPreviewJob, result LinkPreviewResult) (LinkPreviewRow, error) {
	var imageData []byte
	var imageType *string
	var imageWidth, imageHeight *int
	if len(result.ImageData) > 0 {
		imageData, imageType = result.ImageData, &result.ImageContentType
		imageWidth, imageHeight = &result.ImageWidth, &result.ImageHeight
	}
	row := s.pool.QueryRow(ctx, `
		UPDATE chat.link_previews p
		   SET state = 'ready', site_name = NULLIF($2, ''), title = NULLIF($3, ''),
		       description = NULLIF($4, ''), image_data = $5, image_content_type = $6,
		       image_width = $7, image_height = $8,
		       fetched_at = now(), expires_at = now() + ($9 * interval '1 second'),
		       lease_until = NULL, claim_id = NULL, next_attempt_at = NULL, failure_reason = NULL, updated_at = now()
		 WHERE `+ownedClaimSQL("$1", "$10")+`
		RETURNING `+linkPreviewColumns,
		claim.ID, result.SiteName, result.Title, result.Description,
		imageData, imageType, imageWidth, imageHeight, LinkPreviewTTL.Seconds(), claim.ClaimID)
	return scanLinkPreviewRow(row)
}

// FailLinkPreview records a failed attempt. A terminal reason, or the attempt
// ceiling, ends the row; otherwise it stays queued for the retry the claim
// already scheduled. unsupported is the terminal outcome for a page that cannot
// have a preview (wrong content type, no metadata) rather than one that failed.
//
// Same claim identity and deadline as CompleteLinkPreview: a stale worker's
// failure cannot requeue or end a row another claim is working on, and a
// failure — retry or terminal — reported after the deadline is not this
// outcome's to record: the row ends as deadline, through the sweep, and only
// there.
func (s *PGXMessageStore) FailLinkPreview(ctx context.Context, claim LinkPreviewJob, reason string, terminal bool) (LinkPreviewRow, error) {
	row := s.pool.QueryRow(ctx, `
		UPDATE chat.link_previews p
		   SET state = CASE
		                 WHEN $3::boolean OR attempts >= $4 THEN
		                   CASE WHEN $2 IN ('unsupported_content', 'no_metadata') THEN 'unsupported' ELSE 'failed' END
		                 ELSE 'queued'
		               END,
		       failure_reason = $2, lease_until = NULL, claim_id = NULL, updated_at = now()
		 WHERE `+ownedClaimSQL("$1", "$5")+`
		RETURNING `+linkPreviewColumns,
		claim.ID, reason, terminal, linkPreviewMaxAttempts, claim.ClaimID)
	return scanLinkPreviewRow(row)
}

// ownedClaimSQL is the compare-and-set every claim outcome runs under: the row
// is still fetching, still inside its deadline, and still carries the claim id
// this claim was handed. A NULL claim id (row never claimed, or already
// settled) matches nothing; a lapsed deadline matches nothing either, so the
// only transition left for an expired row is the sweep's.
func ownedClaimSQL(idParam, claimParam string) string {
	return "p.id = " + idParam + "::uuid AND p.state = 'fetching' AND p.deadline_at > now() AND p.claim_id = " + claimParam + "::uuid"
}

// openWithinDeadlineSQL is a preview that may still be worked on: queued or
// fetching, and not yet past its deadline. The claim is predicated on it; the
// sweep is predicated on its complement (open and past the deadline), so the
// two never contend for a row and the deadline is a fact of the state machine
// rather than a courtesy of the sweep running first.
func openWithinDeadlineSQL(alias string) string {
	return alias + ".state IN ('queued', 'fetching') AND " + alias + ".deadline_at > now()"
}

// TerminalizeExpiredLinkPreviews fails every queued or fetching preview past its
// deadline and returns them for announcement. Runs on every pass regardless of
// the flag, for the same reason the scan sweep does.
func (s *PGXMessageStore) TerminalizeExpiredLinkPreviews(ctx context.Context) ([]LinkPreviewRow, error) {
	return s.failPreviewsWhere(ctx, `p.deadline_at <= now()`, PreviewFailureDeadline)
}

// DrainLinkPreviewsDisabled fails every non-terminal preview when the feature
// is off, so switching it off leaves no row waiting on a worker that will never
// run. Ready previews are kept: turning the flag back on must not refetch them.
func (s *PGXMessageStore) DrainLinkPreviewsDisabled(ctx context.Context) ([]LinkPreviewRow, error) {
	return s.failPreviewsWhere(ctx, `TRUE`, PreviewFailureDisabled)
}

// RevokeLinkPreviews fails every preview of a URL that is no longer safe — a
// recheck that flipped to malicious, a clearance that was withdrawn — and
// removes the stored image. Returns the rows so every message drawing the card
// is told to take it down.
func (s *PGXMessageStore) RevokeLinkPreviews(ctx context.Context, canonicalURL string) ([]LinkPreviewRow, error) {
	rows, err := s.pool.Query(ctx, `
		UPDATE chat.link_previews p
		   SET state = 'failed', failure_reason = $2, image_data = NULL, image_content_type = NULL,
		       image_width = NULL, image_height = NULL, lease_until = NULL, updated_at = now()
		 WHERE p.canonical_url = $1 AND p.state <> 'failed'
		RETURNING `+linkPreviewColumns, canonicalURL, PreviewFailureNotSafe)
	if err != nil {
		return nil, fmt.Errorf("revoke link previews: %w", err)
	}
	defer rows.Close()
	return scanLinkPreviewRows(rows)
}

func (s *PGXMessageStore) failPreviewsWhere(ctx context.Context, predicate, reason string) ([]LinkPreviewRow, error) {
	rows, err := s.pool.Query(ctx, `
		WITH due AS (
			SELECT p.id FROM chat.link_previews p
			WHERE p.state IN ('queued', 'fetching') AND `+predicate+`
			ORDER BY p.created_at
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		UPDATE chat.link_previews p
		   SET state = 'failed', failure_reason = $1, lease_until = NULL, updated_at = now()
		  FROM due
		 WHERE p.id = due.id
		RETURNING `+linkPreviewColumns, reason, maxTerminalizeBatch)
	if err != nil {
		return nil, fmt.Errorf("fail link previews: %w", err)
	}
	defer rows.Close()
	return scanLinkPreviewRows(rows)
}

// LoadLinkPreviews returns the previews this workspace holds for the URLs, in
// one query. Scoped to the workspace by key: another tenant's row for the same
// URL is not visible here.
func (s *PGXMessageStore) LoadLinkPreviews(ctx context.Context, workspaceID string, canonicalURLs []string) (map[string]LinkPreviewRow, error) {
	if len(canonicalURLs) == 0 || workspaceID == "" {
		return map[string]LinkPreviewRow{}, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+linkPreviewColumns+`
		FROM chat.link_previews p
		WHERE p.workspace_id = $1::uuid AND p.canonical_url = ANY($2::text[])`,
		workspaceID, uniqueSortedURLs(canonicalURLs))
	if err != nil {
		return nil, fmt.Errorf("load link previews: %w", err)
	}
	defer rows.Close()
	list, err := scanLinkPreviewRows(rows)
	if err != nil {
		return nil, err
	}
	previews := make(map[string]LinkPreviewRow, len(list))
	for _, preview := range list {
		previews[preview.CanonicalURL] = preview
	}
	return previews, nil
}

// LinkPreviewImage serves a derived thumbnail to an active member of the
// workspace that owns it. The authorisation is in the statement: a caller who is
// not a member, or a preview from another workspace, reads as not found.
//
// The target is re-checked to be safe: an image derived while a URL was cleared
// must stop being served the moment the URL is condemned, even before the
// revocation sweep reaches the row.
func (s *PGXMessageStore) LinkPreviewImage(ctx context.Context, workspaceID, userID, previewID string) (LinkPreviewImage, error) {
	var image LinkPreviewImage
	err := s.pool.QueryRow(ctx, `
		SELECT p.image_data, p.image_content_type
		FROM chat.link_previews p
		JOIN chat.workspace_members wm
		  ON wm.workspace_id = p.workspace_id AND wm.user_id = $2::uuid AND wm.status = 'active'
		JOIN chat.link_scans ls ON ls.canonical_url = p.canonical_url
		WHERE p.id = $3::uuid AND p.workspace_id = $1::uuid
		  AND p.state = 'ready' AND p.image_data IS NOT NULL
		  AND `+safeFreshVerdictSQL("ls", "$4"),
		workspaceID, userID, previewID, urlsafety.VerdictTTL.Seconds()).Scan(&image.Data, &image.ContentType)
	if errors.Is(err, pgx.ErrNoRows) {
		return LinkPreviewImage{}, domain.ErrNotFound
	}
	if err != nil {
		return LinkPreviewImage{}, fmt.Errorf("link preview image: %w", err)
	}
	return image, nil
}

// LinkPreviewBacklog counts non-terminal previews for the gauge.
func (s *PGXMessageStore) LinkPreviewBacklog(ctx context.Context) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM chat.link_previews WHERE state IN ('queued', 'fetching')`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("link preview backlog: %w", err)
	}
	return count, nil
}

// linkPreviewColumns is the RETURNING/SELECT list, qualified so it reads the
// same inside an UPDATE ... FROM as in a plain SELECT.
const linkPreviewColumns = `p.id::text, p.workspace_id::text, p.canonical_url, p.state,
	COALESCE(p.site_name, ''), COALESCE(p.title, ''), COALESCE(p.description, ''),
	p.image_data IS NOT NULL, COALESCE(p.image_width, 0), COALESCE(p.image_height, 0), p.updated_at`

func scanLinkPreviewRow(row pgx.Row) (LinkPreviewRow, error) {
	var preview LinkPreviewRow
	err := row.Scan(&preview.ID, &preview.WorkspaceID, &preview.CanonicalURL, &preview.State,
		&preview.SiteName, &preview.Title, &preview.Description,
		&preview.HasImage, &preview.ImageWidth, &preview.ImageHeight, &preview.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return LinkPreviewRow{}, ErrLinkPreviewConflict
	}
	if err != nil {
		return LinkPreviewRow{}, fmt.Errorf("scan link preview: %w", err)
	}
	return preview, nil
}

func scanLinkPreviewRows(rows pgx.Rows) ([]LinkPreviewRow, error) {
	var previews []LinkPreviewRow
	for rows.Next() {
		preview, err := scanLinkPreviewRow(rows)
		if err != nil {
			return nil, err
		}
		previews = append(previews, preview)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("link previews: %w", err)
	}
	return previews, nil
}
