-- 000025_link_scan_capacity.up.sql
-- RF-21 (issue #135): make the provider submission recoverable, and make the
-- cost of provider work something the system can refuse.
--
-- Two findings, one migration, because both are about the same thing: what this
-- deployment is willing to spend at Cloudflare, and what it does when it cannot
-- prove what it already spent.
--
-- ===========================================================================
-- 1. The submission window
-- ===========================================================================
--
-- Submitting is two writes that cannot be one:
--
--   POST to the provider  ->  provider accepts and answers a uuid
--   UPDATE link_scans     ->  the uuid becomes ours
--
-- A crash between them leaves a URL that *has* been submitted and a row that
-- cannot prove it. Before this migration the row was indistinguishable from one
-- that had never been submitted at all — `scan_uuid IS NULL` meant both — so
-- recovery submitted again, and one logical scan became two provider scans for
-- every restart that landed in the gap.
--
-- Cloudflare's URL Scanner has no idempotency token, so the gap cannot be closed
-- by the POST. What closes it is recording the *intent* before the call, so the
-- three situations stop being one:
--
--   scan_uuid IS NULL, submit_attempt_started_at IS NULL   -> never submitted
--   scan_uuid IS NULL, submit_attempt_started_at IS NOT NULL -> outcome unknown
--   scan_uuid IS NOT NULL                                   -> submitted, polling
--
-- The middle state is the new one, and it is terminal until reconciliation
-- resolves it. A worker that finds it does not submit — ever, under any
-- configuration. It asks the provider's search whether the scan exists and only
-- adopts a uuid it can identify as the same URL. There is deliberately no
-- transition back to "never submitted": once a POST may have reached Cloudflare,
-- no amount of elapsed time makes another one safe, and Cloudflare has no
-- idempotency token that would make it free.
--
-- The cost of that choice is stated rather than hidden: a scan the provider
-- accepted, whose uuid neither the local write nor the search recovered, leaves
-- its messages withheld until somebody looks. See docs/api/link-safety.md.
--
-- ===========================================================================
-- 2. The cost of new provider work
-- ===========================================================================
--
-- The message limiter counts messages. The provider bills scans, and one message
-- may carry ten URLs, so the two were never the same unit — a sender could spend
-- ten times their apparent budget by writing ten links, and repeat it through
-- create, DM, edit and forward independently.
--
-- The unit that costs money is a canonical URL nobody has a fresh verdict or an
-- active job for. link_scan_budget_usage counts exactly that, in fixed windows,
-- and the same table carries the deployment-wide cap on provider submissions —
-- one mechanism, because both answer "may this replica spend one more".

BEGIN;

-- ---------------------------------------------------------------------------
-- The submission window
-- ---------------------------------------------------------------------------

-- When the current submission attempt was handed to the provider.
--
-- NULL means no attempt is outstanding. Non-NULL with no scan_uuid is the
-- uncertain state: the provider may or may not have accepted, and this is the
-- timestamp the search filters from — a scan older than the attempt is somebody
-- else's history, not the submission whose outcome is unknown.
ALTER TABLE chat.link_scans
    ADD COLUMN submit_attempt_started_at TIMESTAMPTZ;

-- The compare-and-set token for one attempt.
--
-- A worker whose lease expired mid-submission may still be holding a uuid it is
-- about to write. Matching this on the write is what stops that late answer from
-- overwriting the attempt that replaced it: the row moved on, the generation
-- changed, and the stale write finds nothing to update.
ALTER TABLE chat.link_scans
    ADD COLUMN submit_generation INTEGER NOT NULL DEFAULT 0;

-- An attempt is either outstanding or finished; a row that has a scan id has no
-- attempt in flight. Stated as a constraint so the three states above stay three
-- states rather than drifting into four.
ALTER TABLE chat.link_scans
    ADD CONSTRAINT link_scans_submit_attempt_check
    CHECK (scan_uuid IS NULL OR submit_attempt_started_at IS NULL);

-- The uncertain backlog, for the reconciliation pass. Partial, so it holds
-- nothing at all in the steady state where every submission was confirmed.
CREATE INDEX idx_link_scans_submit_uncertain
    ON chat.link_scans (submit_attempt_started_at)
    WHERE status = 'pending' AND scan_uuid IS NULL AND submit_attempt_started_at IS NOT NULL;

-- ---------------------------------------------------------------------------
-- Capacity accounting
-- ---------------------------------------------------------------------------
--
-- One table for every fixed-window budget, because they differ only in what they
-- are keyed by:
--
--   ('workspace', '<workspace uuid>')  new canonical URLs one workspace may
--                                      introduce per window, shared by create,
--                                      DM, edit and forward so no endpoint is a
--                                      way around the others;
--   ('provider_submit', 'global')      submissions this deployment may make to
--                                      Cloudflare per window, shared by every
--                                      replica because a per-process limiter is
--                                      not a limit at all.
--
-- The window is a truncated timestamp, so a counter is a row and reserving is one
-- statement. Fixed windows rather than a token bucket: the reservation has to be
-- atomic against concurrent replicas, and `INSERT ... ON CONFLICT DO UPDATE ...
-- WHERE` is that in a single round trip. A bucket would need the refill computed
-- from a stored timestamp, which is a read-modify-write and a lock.
CREATE TABLE chat.link_scan_budget_usage (
    -- A closed set, mirrored by the CHECK below. It is never a metric label.
    scope_type   TEXT        NOT NULL,
    -- The workspace id for a workspace budget, the literal 'global' for a
    -- deployment-wide one. Never logged and never a metric label: it identifies
    -- a tenant.
    scope_key    TEXT        NOT NULL,
    -- Start of the fixed window, truncated by the caller to the configured
    -- window length so two replicas computing it agree.
    window_start TIMESTAMPTZ NOT NULL,
    used         INTEGER     NOT NULL DEFAULT 0,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (scope_type, scope_key, window_start),

    CONSTRAINT link_scan_budget_scope_check
        CHECK (scope_type IN ('workspace', 'provider_submit')),
    CONSTRAINT link_scan_budget_used_check CHECK (used >= 0)
);

-- Retention. Windows are only ever read for "now", so everything older is dead
-- weight; this index is what lets a bounded delete find it without a table scan.
-- Cleanup is lazy — the worker drops expired windows a bounded number at a time —
-- rather than a scheduled job, so there is nothing extra to operate.
CREATE INDEX idx_link_scan_budget_usage_window
    ON chat.link_scan_budget_usage (window_start);

COMMIT;
