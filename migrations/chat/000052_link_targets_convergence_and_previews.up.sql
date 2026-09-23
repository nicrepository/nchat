-- 000052_link_targets_convergence_and_previews.up.sql
-- Issue #807: Link Safety per URL target with mandatory convergence, and the
-- workspace-scoped rich preview store.
--
-- chat.link_scans already is the per-URL target table: one row per canonical
-- URL, shared by every message that names it. What it lacked was a way to stop
-- waiting. A row could sit `pending` forever — provider outage, uncertain
-- submission, quota — and every message naming it sat `pending_link_scan` with
-- it. Two columns fix that:
--
--   deadline_at      when a non-terminal row must stop waiting. The worker's
--                    sweep turns a pending row past its deadline into
--                    `unknown`, which is terminal: no clearance, no fetch, but
--                    a definite answer the client can act on (interstitial).
--   terminal_reason  why a terminal row ended where it did. A closed set; a
--                    diagnostic column, never a metric label.
--
-- `unknown` is distinct from `inconclusive` on purpose. inconclusive means the
-- provider answered "finished, no verdict" and can be reconciled against a
-- scan id; unknown means nobody answered before the deadline, or the policy
-- refused to ask (sensitive URL, internal host, feature disabled). Both read as
-- "unknown" to a client; only inconclusive has a scan to reconcile.
--
-- Messages themselves are no longer withheld: new messages are created
-- `active` and each link carries its own state. The `pending_link_scan` status
-- stays in the CHECK for the rows written by the previous version, which the
-- legacy resolver drains now that every target converges.
--
-- nchat:blue-green contract-phase the only DROP here is the status CHECK, and
-- it is re-added one statement later with a superset of the values: nothing a
-- slot running the previous release writes becomes invalid. That slot reads an
-- `unknown` row as "not terminal" and keeps waiting on it, which is the
-- behaviour it already had for a pending row — no worse, and converged as soon
-- as the new slot is live.

BEGIN;

-- The deadline a pending row gets when nothing else gives it one. Five minutes
-- is storage.LinkScanPendingDeadline in the service; the value is repeated
-- here on purpose — a migration cannot import a Go constant — and
-- TestLinkScanPendingDeadlineMatchesTheSchema holds the two together.
--
-- The DEFAULT is the blue-green contract: a slot running the previous release
-- still writes `INSERT INTO chat.link_scans (canonical_url)` without knowing
-- the column, and that row must converge like any other. Added in two steps
-- so the default reaches only rows written from now on: the existing rows are
-- backfilled below, and only the pending ones — a terminal row has no
-- deadline to keep.
ALTER TABLE chat.link_scans
    ADD COLUMN deadline_at TIMESTAMPTZ;

ALTER TABLE chat.link_scans
    ALTER COLUMN deadline_at SET DEFAULT (now() + interval '5 minutes');

ALTER TABLE chat.link_scans
    ADD COLUMN terminal_reason TEXT;

ALTER TABLE chat.link_scans
    DROP CONSTRAINT link_scans_status_check;

ALTER TABLE chat.link_scans
    ADD CONSTRAINT link_scans_status_check
    CHECK (status IN ('pending', 'safe', 'malicious', 'inconclusive', 'unknown'));

ALTER TABLE chat.link_scans
    ADD CONSTRAINT link_scans_terminal_reason_check
    CHECK (terminal_reason IS NULL OR terminal_reason IN (
        'provider', 'deadline', 'sensitive', 'internal', 'disabled', 'reopened'
    ));

-- Every pending row inherits a deadline, including the ones the previous
-- version left waiting. This is what drains the messages issue #566 found
-- stuck: within one deadline they all become unknown and the legacy resolver
-- publishes them.
UPDATE chat.link_scans
   SET deadline_at = now() + interval '5 minutes'
 WHERE status = 'pending'
   AND deadline_at IS NULL;

-- The previous release also *reopens* rows: an expired verdict goes back to
-- `pending` through an UPDATE that does not mention deadline_at, so the row
-- would keep a lapsed deadline (terminalised at once, never given its five
-- minutes) or none at all (a row terminal before this migration, which the
-- backfill above did not touch). The DEFAULT does not apply to UPDATEs, so this
-- trigger does what the new release's statements do themselves: a row entering
-- `pending` without the statement setting a deadline gets a fresh one. A
-- statement that sets deadline_at explicitly is left alone.
CREATE FUNCTION chat.link_scans_pending_deadline() RETURNS TRIGGER
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.status = 'pending'
       AND (NEW.deadline_at IS NULL
            OR (TG_OP = 'UPDATE'
                AND OLD.status <> 'pending'
                AND NEW.deadline_at IS NOT DISTINCT FROM OLD.deadline_at)) THEN
        NEW.deadline_at := now() + interval '5 minutes';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER link_scans_pending_deadline
    BEFORE INSERT OR UPDATE OF status, deadline_at ON chat.link_scans
    FOR EACH ROW EXECUTE FUNCTION chat.link_scans_pending_deadline();

-- The invariant issue #807 exists for, as a fact of the schema: no pending row
-- without an end. Safe to add because the DEFAULT and the trigger above make
-- every writer — this release's and the previous one's — satisfy it before the
-- row is checked, and the backfill made the existing rows satisfy it.
ALTER TABLE chat.link_scans
    ADD CONSTRAINT link_scans_pending_deadline_check
    CHECK (status <> 'pending' OR deadline_at IS NOT NULL);

-- The sweep's predicate and nothing else. Partial so it is empty whenever
-- nothing is pending.
CREATE INDEX idx_link_scans_deadline
    ON chat.link_scans (deadline_at)
    WHERE status = 'pending';

-- Expiry reopens unknown rows as well as safe/malicious ones, so the freshness
-- index covers all three terminal-with-lifetime states.
DROP INDEX chat.idx_link_scans_decided_at;
CREATE INDEX idx_link_scans_decided_at
    ON chat.link_scans (decided_at)
    WHERE status IN ('safe', 'malicious', 'unknown');

-- ---------------------------------------------------------------------------
-- link_previews: one rich preview per (workspace, canonical URL)
-- ---------------------------------------------------------------------------
--
-- Workspace-scoped by key, not by filter. Preview metadata is what a page said
-- about itself and is usually public, but a URL can name a private document
-- whose title alone is confidential, and a tenant must never learn what
-- another tenant's link says. Keying by workspace makes the isolation a
-- property of the row rather than of every query.
--
-- The derived image lives inline, bounded by CHECK. It is a thumbnail this
-- service produced — re-encoded, never the remote bytes — and at most 160 KiB,
-- so a row is small enough for the table and the browser gets it from an
-- authenticated NChat route rather than from the remote host.
--
-- Only a target with an explicit `safe` verdict may have a row here; the
-- worker enforces it and the read path re-checks it. A preview never says
-- anything about safety and is never consulted for it.
CREATE TABLE chat.link_previews (
    id                 UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id       UUID        NOT NULL REFERENCES chat.workspaces (id) ON DELETE CASCADE,
    canonical_url      TEXT        NOT NULL REFERENCES chat.link_scans (canonical_url) ON DELETE CASCADE,
    state              TEXT        NOT NULL DEFAULT 'queued',
    site_name          TEXT,
    title              TEXT,
    description        TEXT,
    image_data         BYTEA,
    image_content_type TEXT,
    image_width        INTEGER,
    image_height       INTEGER,
    attempts           SMALLINT    NOT NULL DEFAULT 0,
    -- NULL counts as due, so a freshly queued row is claimable on the next pass.
    next_attempt_at    TIMESTAMPTZ,
    lease_until        TIMESTAMPTZ,
    -- The claim's identity (not a secret). Set to a fresh value by every claim and required,
    -- together with state = 'fetching', by every completion or failure, so a
    -- worker whose lease expired and was reclaimed cannot overwrite the claim
    -- that superseded it. lease_until alone cannot tell two claims apart.
    claim_id           UUID,
    -- A queued or fetching row past this instant is failed by the sweep. No
    -- non-terminal state without a deadline, exactly like link_scans.
    deadline_at        TIMESTAMPTZ NOT NULL,
    fetched_at         TIMESTAMPTZ,
    expires_at         TIMESTAMPTZ,
    failure_reason     TEXT,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT link_previews_workspace_url_unique UNIQUE (workspace_id, canonical_url),
    CONSTRAINT link_previews_state_check
        CHECK (state IN ('queued', 'fetching', 'ready', 'unsupported', 'failed')),
    CONSTRAINT link_previews_failure_reason_check
        CHECK (failure_reason IS NULL OR failure_reason IN (
            'blocked', 'redirect_refused', 'timeout', 'upstream', 'unsupported_content',
            'no_metadata', 'deadline', 'disabled', 'not_safe'
        )),
    CONSTRAINT link_previews_text_length_check
        CHECK (char_length(COALESCE(site_name, '')) <= 100
           AND char_length(COALESCE(title, '')) <= 300
           AND char_length(COALESCE(description, '')) <= 1000),
    CONSTRAINT link_previews_image_bytes_check
        CHECK (image_data IS NULL OR octet_length(image_data) <= 163840),
    CONSTRAINT link_previews_image_shape_check
        CHECK ((image_data IS NULL) = (image_content_type IS NULL)
           AND (image_data IS NULL) = (image_width IS NULL)
           AND (image_data IS NULL) = (image_height IS NULL))
);

-- The preview worker's claim predicate. Partial so it holds nothing once every
-- preview is terminal.
CREATE INDEX idx_link_previews_due
    ON chat.link_previews (next_attempt_at NULLS FIRST, created_at)
    WHERE state IN ('queued', 'fetching');

-- The deadline sweep's predicate and order (TerminalizeExpiredLinkPreviews):
-- open rows past their deadline, oldest first. Distinct from the claim index
-- above, which is keyed by next_attempt_at; a sweep over that one would read
-- every open row to find the expired ones. Partial on the open states only —
-- a predicate on now() cannot be indexed — so it is empty once every preview
-- is terminal.
CREATE INDEX idx_link_previews_deadline
    ON chat.link_previews (deadline_at, created_at)
    WHERE state IN ('queued', 'fetching');

-- "Which previews name this URL", the direction the safety worker reads when a
-- verdict flips to malicious and every preview of that URL has to go.
CREATE INDEX idx_link_previews_url
    ON chat.link_previews (canonical_url);

-- ---------------------------------------------------------------------------
-- link_fanouts: durable continuation of a target's realtime fan-out
-- ---------------------------------------------------------------------------
--
-- A verdict on a URL pasted into thousands of messages is announced to each of
-- them, one bounded page per pass, and the cursor lives here rather than in a
-- goroutine: a pass that fails or a process that restarts resumes from the last
-- message announced instead of starting over or giving up. One row per
-- (target, workspace, kind): a target fan-out (workspace_id NULL, every
-- workspace) or one workspace's preview announcement. A newer announcement
-- for the same key restarts the cursor — the state it announces supersedes.
CREATE TABLE chat.link_fanouts (
    id               UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    canonical_url    TEXT        NOT NULL REFERENCES chat.link_scans (canonical_url) ON DELETE CASCADE,
    workspace_id     UUID        REFERENCES chat.workspaces (id) ON DELETE CASCADE,
    -- Generated so the uniqueness below treats "every workspace" as one value;
    -- a UNIQUE over a nullable column would admit any number of NULL rows.
    workspace_key    TEXT        GENERATED ALWAYS AS (COALESCE(workspace_id::text, '')) STORED,
    kind             TEXT        NOT NULL,
    -- The last message announced; NULL means from the start. Messages are
    -- announced in id order, so the cursor is deterministic across retries.
    after_message_id UUID,
    attempts         SMALLINT    NOT NULL DEFAULT 0,
    lease_until      TIMESTAMPTZ,
    -- The claim's identity (not a secret), minted by every claim and by every
    -- restart, and required by every advance or finish: a pass whose lease
    -- lapsed and was reclaimed, or whose fan-out was restarted by a newer
    -- announcement, cannot move or end the continuation that superseded it.
    claim_id         UUID,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT link_fanouts_key_unique UNIQUE (canonical_url, workspace_key, kind),
    CONSTRAINT link_fanouts_kind_check CHECK (kind IN ('target', 'preview'))
);

-- The continuation worker's claim predicate and order. A row is due when it
-- has no lease or its lease lapsed, so the index leads with lease_until (NULLS
-- FIRST puts the unleased rows where a `lease_until IS NULL OR lease_until <=
-- now()` range scan starts) and carries updated_at, the claim order: the
-- fan-out that has waited longest goes first, so no single popular target
-- starves the others. Not partial: a predicate on now() cannot be indexed.
CREATE INDEX idx_link_fanouts_due
    ON chat.link_fanouts (lease_until NULLS FIRST, updated_at);

COMMIT;
