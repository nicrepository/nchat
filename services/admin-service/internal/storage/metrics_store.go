package storage

import (
	"context"
	"fmt"

	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
)

// PGXMetricsStore reads the dashboard's operational counters (issue #581).
//
// It is read-only and aggregate-only. Every statement below is a COUNT or a
// SUM: no row identifier, no message body, no filename, no e-mail address and
// no URL is selected, so there is nothing in the result set that could leak
// into a dashboard by accident. That is a property of the query, not of the
// caller, which is why the projection is fixed here and there is no method
// that takes a predicate.
type PGXMetricsStore struct {
	pool Pool
}

func NewPGXMetricsStore(pool Pool) *PGXMetricsStore {
	return &PGXMetricsStore{pool: pool}
}

// platformCountersQuery collects every dashboard counter in one round trip.
//
// One statement with scalar subqueries rather than twelve queries: the
// dashboard is a single request and it must stay a single request against the
// database too, or the "one endpoint, not one per card" rule just moves the
// N+1 one layer down.
//
// The time windows are computed once, in the CTE, so every "last 24 h" counter
// covers exactly the same period. Deriving now() per subquery would let two
// cards on the same screen disagree about where the window starts.
//
// Cost notes, which are the reason each predicate looks the way it does:
//
//   - the two message and attachment windows scan by created_at, covered by
//     the BRIN indexes added in the migration that accompanies this issue.
//     BRIN suits both tables exactly: they are append-only and their physical
//     order tracks created_at, so the summary is a few kilobytes;
//   - the session counters use idx_user_sessions_user_revoked;
//   - the remaining tables are small by construction — channels, conversations
//     and calls are bounded by workspace size, not by traffic;
//   - nothing here joins. Twelve independent aggregates are twelve independent
//     scans of small relations, which is cheaper and far more predictable than
//     one query that tries to reuse them.
const platformCountersQuery = `
	WITH window_start AS (
	    SELECT now() - interval '24 hours' AS since
	)
	SELECT
	    (SELECT count(*) FROM auth.users WHERE deleted_at IS NULL),
	    (SELECT count(DISTINCT s.user_id)
	       FROM auth.user_sessions AS s
	      WHERE s.revoked_at IS NULL
	        AND s.idle_expires_at > now()
	        AND (s.absolute_expires_at IS NULL OR s.absolute_expires_at > now())),
	    (SELECT count(DISTINCT s.user_id)
	       FROM auth.user_sessions AS s, window_start AS w
	      WHERE s.last_seen_at >= w.since),
	    (SELECT count(*) FROM chat.channels WHERE status = 'active'),
	    (SELECT count(*) FROM chat.dm_conversations WHERE type = 'group'  AND status = 'active'),
	    (SELECT count(*) FROM chat.dm_conversations WHERE type = 'direct' AND status = 'active'),
	    (SELECT count(*) FROM chat.messages AS m, window_start AS w WHERE m.created_at >= w.since),
	    (SELECT count(*) FROM chat.calls WHERE status IN ('ringing', 'active')),
	    (SELECT count(*)
	       FROM files.attachments AS a, window_start AS w
	      WHERE a.created_at >= w.since AND a.deleted_at IS NULL),
	    (SELECT count(*)
	       FROM files.attachments AS a, window_start AS w
	      WHERE a.status = 'rejected' AND a.updated_at >= w.since),
	    (SELECT count(*)
	       FROM chat.messages AS m, window_start AS w
	      WHERE m.link_safety_state = 'malicious' AND m.created_at >= w.since),
	    (SELECT COALESCE(sum(a.ciphertext_size_bytes), 0)
	       FROM files.attachments AS a
	      WHERE a.deleted_at IS NULL)`

// PlatformCounters runs the aggregate.
//
// The error is returned rather than swallowed: the caller turns it into "these
// cards are unavailable" and keeps the health section, which is the partial
// failure this dashboard is designed around. What it must never do is turn a
// failed aggregate into zeros.
func (s *PGXMetricsStore) PlatformCounters(ctx context.Context) (domain.PlatformCounters, error) {
	if s == nil || s.pool == nil {
		return domain.PlatformCounters{}, domain.ErrUnavailable
	}
	var counters domain.PlatformCounters
	err := s.pool.QueryRow(ctx, platformCountersQuery).Scan(
		&counters.UsersTotal,
		&counters.UsersActiveNow,
		&counters.UsersActive24h,
		&counters.ChannelsActive,
		&counters.GroupsActive,
		&counters.DirectActive,
		&counters.Messages24h,
		&counters.CallsActive,
		&counters.Uploads24h,
		&counters.FilesBlocked24h,
		&counters.LinksBlocked24h,
		&counters.StorageBytes,
	)
	if err != nil {
		// The driver's message is not propagated: it can carry the DSN, the
		// host and the statement. The caller only needs to know the aggregate
		// did not run.
		return domain.PlatformCounters{}, fmt.Errorf("%w: platform counters unavailable", domain.ErrUnavailable)
	}
	return counters, nil
}
