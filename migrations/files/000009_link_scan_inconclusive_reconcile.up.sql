-- 000009_link_scan_inconclusive_reconcile.up.sql
-- RF-21 (issue #135): allow exactly one deliberate way out of 'inconclusive',
-- and keep every other way shut.
--
-- 000008 made an inconclusive row immutable by UPDATE. That was the right shape
-- for a rolling deployment — an origin/develop worker whose claim predicate was
-- `state <> 'done'` would otherwise select an inconclusive row, resubmit it and
-- destroy the scan uuid — but it also blocks the recovery this issue asks for:
-- reconciliation reads the provider's *existing* scan for the URL and may
-- legitimately learn a verdict the earlier report did not carry.
--
-- So the blanket refusal becomes a single allowed transition. 000008 is left
-- exactly as applied; this migration replaces its trigger function.
--
--   inconclusive -> done             ALLOWED, and only under the conditions below
--   inconclusive -> submit_pending   REFUSED
--   inconclusive -> submitting       REFUSED
--   inconclusive -> submit_uncertain REFUSED
--   inconclusive -> polling          REFUSED
--   inconclusive -> inconclusive     ALLOWED for reconcile bookkeeping *only* —
--                                    a two-column allowlist, see below
--
-- # Why the check lives in the database
--
-- Because the thing it defends against is code this database cannot see. During a
-- RollingUpdate two worker versions run at once, and the older one's queries were
-- written before this state existed. A guard in Go protects only the process that
-- contains it. This one protects the row.
--
-- The legacy claim predicate is the concrete case: `WHERE state <> 'done'`
-- matches an inconclusive row, and its UPDATE sets state='submitting' and
-- clears scan_uuid. Both of those are refused below, so the legacy claim
-- silently affects zero rows and returns no job — the row keeps its uuid and its
-- attempt history, exactly as it did under 000008.
--
-- # Why the allowed transition is narrow
--
-- Permitting `-> done` in general would let any UPDATE that happens to set that
-- state also rewrite the scan uuid, which is the identity a verdict is bound to.
-- So the allowed transition additionally requires:
--
--   * the scan uuid is unchanged. A verdict may only ever be recorded for the
--     scan that produced it;
--   * a verdict text is present. 'done' with a NULL verdict is not an answer, and
--     link_scans_verdict_check would refuse it anyway — refusing it here as well
--     means the trigger never depends on constraint ordering;
--   * attempts do not decrease. Nothing may launder an inconclusive row's history
--     into a fresh-looking one.
--
-- Everything else about the row (verdict_expires_at, lease_until,
-- next_attempt_at, reconcile bookkeeping) is ordinary bookkeeping and is left to
-- the constraints that already govern it.

BEGIN;

CREATE OR REPLACE FUNCTION files.reject_inconclusive_link_scan_update()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    -- Bookkeeping that leaves the row inconclusive: the reconciliation attempt
    -- counter and its backoff, and nothing else at all.
    --
    -- This is an allowlist of two columns rather than a denylist, and it has to
    -- be. The legacy claim's UPDATE does not set `state` — it sets attempts,
    -- lease_until and next_attempt_at on whatever row it selected — so a branch
    -- that merely checked "the state did not change" would have handed an
    -- inconclusive row straight back to an origin/develop worker, resubmitted it
    -- and destroyed the scan uuid. Naming the two columns that may move is what
    -- makes every other UPDATE, including that one, fall through to the refusal
    -- at the bottom.
    IF NEW.state = 'inconclusive' THEN
        IF NEW.url_digest IS DISTINCT FROM OLD.url_digest
           OR NEW.canonical_url IS DISTINCT FROM OLD.canonical_url
           OR NEW.scan_uuid IS DISTINCT FROM OLD.scan_uuid
           OR NEW.verdict IS DISTINCT FROM OLD.verdict
           OR NEW.verdict_expires_at IS DISTINCT FROM OLD.verdict_expires_at
           OR NEW.attempts IS DISTINCT FROM OLD.attempts
           OR NEW.next_attempt_at IS DISTINCT FROM OLD.next_attempt_at
           OR NEW.lease_until IS DISTINCT FROM OLD.lease_until
           OR NEW.submit_attempt_started_at IS DISTINCT FROM OLD.submit_attempt_started_at
           OR NEW.submit_generation IS DISTINCT FROM OLD.submit_generation
           OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
            RETURN NULL;
        END IF;
        -- And it has to actually *be* reconciliation bookkeeping. An UPDATE that
        -- moves neither counter is some other statement that happened to match,
        -- and there is no reason to let it touch a terminal row — including to
        -- backdate updated_at, which is how a stale writer would otherwise leave
        -- a fingerprint on a row it has no business in.
        IF NEW.reconcile_attempts = OLD.reconcile_attempts
           AND NEW.next_reconcile_at IS NOT DISTINCT FROM OLD.next_reconcile_at THEN
            RETURN NULL;
        END IF;
        -- The recovery budget is finite and is never refilled. A decreasing
        -- counter is the one way that bound could be laundered away.
        IF NEW.reconcile_attempts < OLD.reconcile_attempts THEN
            RETURN NULL;
        END IF;
        RETURN NEW;
    END IF;

    -- The one deliberate exit. Anything that is not a fully-formed verdict for
    -- the very scan this row already carries is dropped, silently, exactly as
    -- every refused UPDATE was under 000008.
    IF NEW.state = 'done'
       AND NEW.scan_uuid IS NOT DISTINCT FROM OLD.scan_uuid
       AND NEW.scan_uuid IS NOT NULL
       AND NEW.verdict IS NOT NULL
       AND NEW.attempts >= OLD.attempts THEN
        RETURN NEW;
    END IF;

    -- Every reopening path, including every legacy worker's claim.
    RETURN NULL;
END
$$;

-- Reconciliation scheduling, so a bounded recovery can be attempted and cannot
-- become a loop. There is no column here that schedules a submission: recovery is
-- search-then-read at the provider, and the absence of a resubmit schedule is
-- structural.
ALTER TABLE files.link_scans
    ADD COLUMN reconcile_attempts SMALLINT NOT NULL DEFAULT 0;

ALTER TABLE files.link_scans
    ADD COLUMN next_reconcile_at TIMESTAMPTZ;

-- The access path for the reconciliation pass. Partial, so it holds nothing at
-- all in a deployment where no scan has ever come back empty.
CREATE INDEX idx_files_link_scans_reconcile_due
    ON files.link_scans (next_reconcile_at NULLS FIRST, created_at)
    WHERE state = 'inconclusive';

COMMIT;
