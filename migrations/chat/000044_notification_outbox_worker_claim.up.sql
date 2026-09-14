-- 000044_notification_outbox_worker_claim.up.sql
-- Issue #742 (parent #678, depends on #741): the three facts a claim protocol
-- needs, and the index that makes claiming them cheap.
--
-- 000042 deliberately stopped short of these. Its own words: "No attempt
-- counter, no lease, no next_retry_at. Those belong to the worker that defines
-- the claim protocol, and inventing them now would fix a retry design nothing
-- has exercised." The worker now exists, so the design is no longer guessed.
--
-- # Why a lease is three columns and not five
--
-- The claim protocol is the one this repository already runs twice —
-- chat.link_scans and chat.message_publish_outbox — where the UPDATE that takes
-- the row *is* the claim and next_attempt_at *is* the lease:
--
--   * two workers cannot hold one row, because taking it is a write and
--     FOR UPDATE SKIP LOCKED hands each of them disjoint rows;
--   * a worker that died holds nothing. Its rows sit in 'processing' with a
--     next_attempt_at in the past, which is precisely the definition of an
--     abandoned claim, and the same claim query picks them up again;
--   * a provider that keeps failing costs geometrically fewer attempts, because
--     the failure writes the next attempt further out.
--
-- So there is no lease_owner and no lease_token here. An owner column would only
-- be able to answer "did somebody else take this row from me?", and the answer
-- changes nothing: a worker whose lease expired mid-send has already delivered
-- or already failed, and the compare-and-set on status = 'processing' keeps the
-- table consistent either way. A column nothing would branch on is a column that
-- rots.
--
-- # next_attempt_at is availability, not "retry time"
--
-- The name reads like a retry schedule and it is more than that: it is the
-- instant the row next becomes available to a worker, in every state a worker
-- may claim.
--
--   pending     NULL          not processable yet; a policy has not looked at it
--   eligible    now()         when a policy made it deliverable
--   retrying    now()+backoff when its next attempt is due
--   processing  now()+lease   when its lease lapses and it may be reclaimed
--   terminal    irrelevant    suppressed, sent and failed are never claimed
--
-- One column, one question: "when may this row next be considered?". The claim
-- orders by it, which is what makes the queue fair by availability rather than
-- by when the underlying message happened — those are different facts, and
-- ordering by the second starves anything that had to wait.
--
-- # attempts
--
-- SMALLINT, matching files.link_scans.attempts, and counted by the claim rather
-- than by the failure. Counting at claim time is what bounds a poison event: a
-- notification that kills the worker before it can record anything still burns
-- an attempt every time it is reclaimed, so it reaches the ceiling and stops
-- instead of cycling forever.
--
-- # last_error
--
-- VARCHAR(64), and the length is the security control, not tidiness. This column
-- is written on every failed delivery and nothing truncates it, so unbounded it
-- would be the one place in a table designed to carry no content where a caller
-- could park a provider's error body — which is exactly the text that carries
-- recipient addresses, subscription endpoints and token fragments. The worker
-- writes a category from a closed set ('delivery_transient', 'delivery_timeout',
-- 'delivery_permanent', 'attempts_exhausted'); 64 is comfortably more than the
-- longest of those and far too little to smuggle a payload through.
--
-- The bound is in the type rather than in a CHECK on purpose. The column is new,
-- so every stored row is NULL and a type-level bound needs no validating scan at
-- all — where a CHECK would have had to be added NOT VALID and validated by a
-- second migration, as 000042/000043 had to for the columns they retrofitted.
-- VARCHAR(n) is the same choice 000015 and 000024 made for bounded keys.
--
-- # The index
--
-- idx_notification_outbox_open, from 000042, orders the backlog by occurred_at
-- for a worker draining pending work; it says nothing about when a row is next
-- due and does not cover 'processing', so it cannot serve the claim. This one
-- can: it leads on next_attempt_at, which every claimable row now carries and
-- which is the instant the row became available. That is the claim's ORDER BY
-- exactly, so the LIMIT stops the scan after one batch instead of sorting the
-- backlog, and occurred_at follows only as the tie-break the query uses.
--
-- No expression index. An earlier draft indexed COALESCE(next_attempt_at,
-- occurred_at) to paper over eligible rows that carried no availability
-- timestamp; stamping it in MarkEvaluated and backfilling it above removes the
-- reason for the expression, and a plain column index is cheaper to maintain
-- and easier for the planner to reason about.
--
-- Including 'processing' in the predicate is what makes recovering an abandoned
-- claim as cheap as taking a fresh one.
--
-- Both indexes are partial over non-terminal states, so neither grows with the
-- history of delivered notifications — only with the work actually outstanding.
--
-- Blue/Green: additive only. Three nullable-or-defaulted columns and one index;
-- nothing is dropped, narrowed or renamed, and no slot running the previous
-- release reads or writes any of it — the previous release has no notification
-- worker at all.

BEGIN;

ALTER TABLE chat.notification_outbox
    ADD COLUMN attempts        SMALLINT    NOT NULL DEFAULT 0,
    ADD COLUMN next_attempt_at TIMESTAMPTZ,
    ADD COLUMN last_error      VARCHAR(64);

-- ---------------------------------------------------------------------------
-- Backfill: no claimable row may exist without an availability instant
-- ---------------------------------------------------------------------------
--
-- The column arrives NULL for every stored row, and the claim treats NULL as
-- not-due. For pending that is correct and permanent — it is not claimable. For
-- the three claimable states it would be a row that never gets picked up, so
-- they are stamped here.
--
-- In practice this updates nothing today: both producers INSERT 'pending' and
-- nothing in either service moves a row out of it yet, so no eligible, retrying
-- or processing row can exist. It is written anyway, because a repair script or
-- a psql session is enough to make that false, and a migration that leaves a
-- claimable row unclaimable is a notification nobody is ever told about.
--
-- updated_at is the timestamp used, not occurred_at. 000042 added it and the
-- storage layer writes now() into it on every transition, so for a row that has
-- moved it is exactly "when it entered this state" — the closest thing the
-- schema has to the availability instant this column is being given. It is NOT
-- NULL, so it always has a value and needs no fallback of its own; for a row
-- that never transitioned it equals created_at, which is the right answer too.
--
-- occurred_at is deliberately not used. It is when the message happened, which
-- for an imported or long-pending event can be arbitrarily far in the past, and
-- letting that stand in for availability is precisely the confusion this column
-- exists to end. As a one-off migration fallback it would be defensible; as the
-- rule for new rows it is not, and after this migration no new row depends on
-- it: MarkEvaluated stamps now() on promotion to eligible.
UPDATE chat.notification_outbox
   SET next_attempt_at = updated_at
 WHERE next_attempt_at IS NULL
   AND status IN ('eligible', 'retrying', 'processing');

CREATE INDEX idx_notification_outbox_claimable
    ON chat.notification_outbox (next_attempt_at ASC, occurred_at ASC, id ASC)
    WHERE status IN ('eligible', 'retrying', 'processing');

COMMIT;
