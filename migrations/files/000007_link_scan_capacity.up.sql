-- 000007_link_scan_capacity.up.sql
-- RF-21 (issue #135): the same two corrections as chat/000025, applied to
-- file-service's own queue.
--
-- The findings are not chat-specific. The preview pipeline submits to the same
-- provider, bills the same account, and had the same gap between "the provider
-- accepted" and "we wrote the uuid down" — so a restart in that window
-- resubmitted, and a client refreshing previews could introduce unbounded new
-- URLs with nothing counting them.
--
-- The state machine gains one state:
--
--   submit_pending   --(intent recorded)--> submitting
--   submitting       --(uuid persisted)---> polling
--   submitting       --(outcome unknown)--> submit_uncertain
--   submit_uncertain --(scan found)-------> polling
--   polling          --(verdict)----------> done
--
-- There is deliberately no edge back to submit_pending. Once a POST may have
-- reached the provider, no elapsed time makes another one safe.
--
-- `submitting` and `submit_uncertain` are the same situation seen at different
-- times — an attempt is outstanding, and after the lease lapses nobody knows its
-- outcome — but they are distinct rows to read: one has a live lease, the other
-- does not. Neither is ever a reason to submit again before the provider's own
-- search has been asked.
--
-- Why file-service does not reuse chat's budget table: it does not reuse chat's
-- queue either. The two services share the decision through
-- libs/go/platform/urlsafety and share nothing in storage, which is the
-- convention files/000001 and files/000006 already state.

BEGIN;

ALTER TABLE files.link_scans
    DROP CONSTRAINT link_scans_state_check;

ALTER TABLE files.link_scans
    ADD CONSTRAINT link_scans_state_check
    CHECK (state IN ('submit_pending', 'submitting', 'submit_uncertain', 'polling', 'done'));

-- Only 'polling' requires a scan id. 'submitting' and 'submit_uncertain' are
-- precisely the states where there is not one yet — that is what they mean.
ALTER TABLE files.link_scans
    ADD COLUMN submit_attempt_started_at TIMESTAMPTZ;

ALTER TABLE files.link_scans
    ADD COLUMN submit_generation INTEGER NOT NULL DEFAULT 0;

-- An outstanding attempt and a confirmed scan id are mutually exclusive.
ALTER TABLE files.link_scans
    ADD CONSTRAINT link_scans_submit_attempt_check
    CHECK (scan_uuid IS NULL OR submit_attempt_started_at IS NULL);

CREATE INDEX idx_files_link_scans_submit_uncertain
    ON files.link_scans (submit_attempt_started_at)
    WHERE state = 'submit_uncertain';

-- The same fixed-window counter as chat/000025, for the two budgets this service
-- can actually enforce.
--
-- file-service has no trustworthy workspace on a preview request — the preview
-- is fetched for a link, not for a tenant — so it does not invent one. The
-- scopes here are service-wide: how many brand-new URLs this service may
-- introduce per window, and how many submissions it may make to the provider.
-- Per-user fairness stays where it already is, in the request limiter.
CREATE TABLE files.link_scan_budget_usage (
    scope_type   TEXT        NOT NULL,
    scope_key    TEXT        NOT NULL,
    window_start TIMESTAMPTZ NOT NULL,
    used         INTEGER     NOT NULL DEFAULT 0,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (scope_type, scope_key, window_start),

    CONSTRAINT link_scan_budget_scope_check
        CHECK (scope_type IN ('service', 'provider_submit')),
    CONSTRAINT link_scan_budget_used_check CHECK (used >= 0)
);

CREATE INDEX idx_files_link_scan_budget_usage_window
    ON files.link_scan_budget_usage (window_start);

COMMIT;
