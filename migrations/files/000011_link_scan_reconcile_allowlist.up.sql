-- 000011_link_scan_reconcile_allowlist.up.sql
-- RF-21 (issue #135, CQ-006): the `inconclusive -> done` door was too wide.
--
-- 000009 opened one deliberate exit from the terminal state and guarded it with
-- three conditions: the new state is 'done', the scan uuid is unchanged, a
-- verdict is present, and attempts do not decrease. Everything *else* about the
-- row was free to move in the same UPDATE — canonical_url, url_digest, created_at,
-- the whole submit lifecycle. A statement that satisfied those three conditions
-- could rewrite the row's identity along the way.
--
-- That is the wrong shape for a guard whose entire job is to survive code this
-- database cannot see. 000008 and 000009 are applied and are not edited; this
-- replaces the trigger function with one that decides per column.
--
-- # The rule
--
-- Instead of "if the state became done, accept", every column is compared OLD to
-- NEW and only the ones reconciliation legitimately writes may differ:
--
--   state                 'inconclusive' -> 'done'   (the transition itself)
--   verdict               NULL -> a verdict          (required, non-null)
--   verdict_expires_at    set by the caller          (dated from the evidence)
--   lease_until           cleared
--   next_attempt_at       cleared
--   reconcile_attempts    non-decreasing
--   next_reconcile_at     cleared or rescheduled
--   updated_at            bookkeeping
--
-- Everything else must be byte-identical:
--
--   url_digest, canonical_url    the row's identity
--   scan_uuid                    the scan the verdict is attributable to
--   attempts                     the poll history, not reconciliation's to rewrite
--   submit_attempt_started_at    submit lifecycle
--   submit_generation            submit lifecycle
--   created_at                   when this URL entered the queue
--
-- # A new column is NOT frozen by default — it must be reviewed
--
-- This guard names its columns. It compares the identity columns explicitly and
-- accepts the transition on the strength of `state`, `scan_uuid` and `verdict`;
-- it does not compare OLD and NEW wholesale. So a column added to
-- files.link_scans *after* this migration is not covered by anything here, and
-- this transition may write it freely.
--
-- That is the opposite of a safe default, and it is the reason this paragraph
-- exists: adding a column to files.link_scans requires deciding, in review,
-- which of the two lists above it belongs to, and a follow-up migration that
-- puts it there. Nothing in the database will raise the question for you.
--
-- # Rolling deployment
--
-- Strictly narrower than 000009, so it can only ever refuse more. An old pod that
-- was relying on 000009's laxity does not exist — the only writer of this
-- transition is the reconciliation path added in the same change — and every
-- reopening path stays refused exactly as before. The legacy `state <> 'done'`
-- claim continues to match zero rows.

BEGIN;

CREATE OR REPLACE FUNCTION files.reject_inconclusive_link_scan_update()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    -- Identity is never writable from this state, whatever the transition. Checked
    -- once, before the branches, because no legitimate path changes any of it.
    IF NEW.url_digest IS DISTINCT FROM OLD.url_digest
       OR NEW.canonical_url IS DISTINCT FROM OLD.canonical_url
       OR NEW.scan_uuid IS DISTINCT FROM OLD.scan_uuid
       OR NEW.created_at IS DISTINCT FROM OLD.created_at
       OR NEW.submit_attempt_started_at IS DISTINCT FROM OLD.submit_attempt_started_at
       OR NEW.submit_generation IS DISTINCT FROM OLD.submit_generation
       OR NEW.attempts IS DISTINCT FROM OLD.attempts THEN
        RETURN NULL;
    END IF;

    -- The recovery budget is finite and never refilled; a decreasing counter is
    -- the one way that bound could be laundered away.
    IF NEW.reconcile_attempts < OLD.reconcile_attempts THEN
        RETURN NULL;
    END IF;

    -- Bookkeeping that leaves the row inconclusive: the reconciliation attempt
    -- counter and its backoff, and nothing else at all.
    --
    -- The legacy claim's UPDATE does not set `state` — it sets attempts,
    -- lease_until and next_attempt_at on whatever row it selected — so a branch
    -- that merely checked "the state did not change" would hand an inconclusive
    -- row straight back to an origin/develop worker. attempts and lease_until are
    -- both refused above and below, so that claim falls through to the final
    -- refusal.
    IF NEW.state = 'inconclusive' THEN
        IF NEW.verdict IS DISTINCT FROM OLD.verdict
           OR NEW.verdict_expires_at IS DISTINCT FROM OLD.verdict_expires_at
           OR NEW.next_attempt_at IS DISTINCT FROM OLD.next_attempt_at
           OR NEW.lease_until IS DISTINCT FROM OLD.lease_until THEN
            RETURN NULL;
        END IF;
        -- And it has to actually *be* reconciliation bookkeeping. An UPDATE that
        -- moves neither counter is some other statement that happened to match, and
        -- there is no reason to let it touch a terminal row — including to backdate
        -- updated_at, which is how a stale writer would otherwise leave a
        -- fingerprint on a row it has no business in.
        IF NEW.reconcile_attempts = OLD.reconcile_attempts
           AND NEW.next_reconcile_at IS NOT DISTINCT FROM OLD.next_reconcile_at THEN
            RETURN NULL;
        END IF;
        RETURN NEW;
    END IF;

    -- The one deliberate exit. A verdict has to be present and attributable, and
    -- the identity checks above have already established that it is attributable
    -- to the scan this row already owns.
    IF NEW.state = 'done'
       AND NEW.scan_uuid IS NOT NULL
       AND NEW.verdict IS NOT NULL THEN
        RETURN NEW;
    END IF;

    -- Every reopening path, including every legacy worker's claim.
    RETURN NULL;
END
$$;

COMMIT;
