-- 000029_message_link_safety_state.up.sql
-- RF-21 (issue #135): separate "may this message be delivered" from "may this
-- server fetch the URL it carries".
--
-- 000026 made a terminal scan with no usable verdict its own state and stopped
-- the infinite poll. It also refused the message: an inconclusive link became
-- status='deleted' and a sender-only message.blocked. That is fail-closed for
-- both questions at once, and the second one is the wrong answer — Cloudflare's
-- real production reply was
--
--   "Refusing to scan: hostname was recently scanned or too many scans to
--    hostname in the last days."
--
-- which is an operational refusal by the provider and carries no evidence at all
-- about the link. Blocking a legitimate message on it is a product bug.
--
-- The policy this migration supports:
--
--   state          message      link click   server-side preview
--   pending        withheld     n/a          NO
--   safe           published    yes          YES
--   malicious      blocked      no           NO
--   inconclusive   published    yes          NO
--
-- Note the asymmetry, which is the whole point: inconclusive publishes the
-- message and still forbids every server-side fetch. "Inconclusive" means "no
-- clearance for automatic server-side execution", never "the URL is safe".
--
-- # Why a column and not a derived value
--
-- Deriving the aggregate at read time from message_link_scans/link_scans was the
-- first choice and it is not safe: EnsureLinkScans legitimately moves a decided
-- row back to 'pending' when its VerdictTTL lapses and somebody sends the URL
-- again, so a *published* message whose link had been proven malicious would
-- momentarily derive as unrestricted. A durable per-message column cannot be
-- un-set by an unrelated sender's timing.
--
-- It is deliberately not part of chat.messages' lifecycle: `status` still means
-- pending_link_scan / active / deleted and nothing here changes that. This column
-- is the link-safety axis, and the two are independent — an inconclusive message
-- is `active`, which is exactly why every recipient receives it.
--
-- # Prospective only
--
-- Messages the previous version turned into status='deleted' for an inconclusive
-- link stay deleted. There is deliberately no backfill: republishing historical
-- messages would make them appear, without warning and out of order, to
-- recipients who never saw them. The new semantics apply from here forward.

BEGIN;

-- '' is "this message has no link-safety opinion": either it carries no links at
-- all, or it was created before this column existed. NOT NULL with a '' default
-- so no read path has to handle a NULL, and so every pre-existing row is
-- unambiguously "nothing to say" rather than "unknown".
--
-- Since PostgreSQL 11 a NOT NULL column with a non-volatile DEFAULT is added by
-- writing the default into the catalogue rather than rewriting every row, so this
-- is a fast metadata-only operation however long the history is. No row-by-row
-- backfill is needed or wanted.
ALTER TABLE chat.messages
    ADD COLUMN link_safety_state TEXT NOT NULL DEFAULT '';

-- The CHECK is added NOT VALID here; 000028 validates it.
--
-- `ADD CONSTRAINT ... CHECK` in one shot takes ACCESS EXCLUSIVE on chat.messages
-- and holds it for a full sequential scan, so every read and write of every
-- message queues behind it for as long as the scan takes. On a busy workspace
-- that is an outage whose length grows with the message history.
--
-- NOT VALID takes the same lock but does no scan, so it is a catalogue write and
-- returns immediately. The scan then happens in 000028 under SHARE UPDATE
-- EXCLUSIVE, which does not block SELECT, INSERT, UPDATE or DELETE.
--
-- The split has to be across two migrations rather than two statements: this file
-- runs inside one transaction, and a lock is held until commit, so a VALIDATE
-- here would still be scanning under the ACCESS EXCLUSIVE this statement took.
--
-- NOT VALID does not weaken anything from here on: every row written or updated
-- after this point is checked. It only means pre-existing rows are not re-read at
-- this instant — and they cannot violate it, because the column was just added
-- with DEFAULT ''.
ALTER TABLE chat.messages
    ADD CONSTRAINT messages_link_safety_state_check
    CHECK (link_safety_state IN ('', 'safe', 'inconclusive', 'malicious'))
    NOT VALID;

-- Reconciliation scheduling for an inconclusive scan.
--
-- Both columns exist so a *bounded* recovery can be attempted for a scan that
-- finished without a verdict, and so that recovery cannot become a loop. There is
-- no column here that could schedule a submission: reconciliation is
-- search-then-read at the provider, and the absence of any resubmit schedule is
-- structural rather than a matter of what the code happens to do.
ALTER TABLE chat.link_scans
    ADD COLUMN reconcile_attempts SMALLINT NOT NULL DEFAULT 0;

ALTER TABLE chat.link_scans
    ADD COLUMN next_reconcile_at TIMESTAMPTZ;

-- The access path for the background reconciliation pass, and empty whenever
-- nothing is inconclusive — which is the normal state of a healthy deployment,
-- so an idle pass costs an empty index scan.
CREATE INDEX idx_link_scans_reconcile_due
    ON chat.link_scans (next_reconcile_at NULLS FIRST, created_at)
    WHERE status = 'inconclusive';

-- No new index is needed for "which messages named this URL": 000024 already
-- created idx_message_link_scans_url for exactly that direction, and the
-- link-safety recompute below reads it.

COMMIT;
