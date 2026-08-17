-- 000026_link_scan_inconclusive.up.sql
-- RF-21 (issue #135): a terminal Cloudflare URL Scanner result with no usable
-- verdict was indistinguishable from a transient provider failure.
--
-- Observed in nchat-dev: a scan row sat in status='pending' with scan_uuid set,
-- submit_attempt_started_at NULL and attempts climbing past 150. Queried
-- directly, the provider answered HTTP 200 with task.status='finished',
-- task.success=false, verdicts.overall.hasVerdicts=false — a scan that ran and
-- genuinely produced nothing. The worker read that as ErrUnavailable and polled
-- the same finished scan every five minutes forever; the two messages sharing
-- the URL never left pending_link_scan.
--
-- This migration adds the terminal state that answer maps to now:
-- 'inconclusive'. It is not a clearance — LoadLinkVerdicts still only ever
-- returns 'safe' or 'malicious' — but it is terminal for the scan uuid that
-- produced it: ClaimDueLinkScans's WHERE status = 'pending' already excludes
-- it, same as 'safe'/'malicious' today.
--
-- Deliberately absent: any path back from 'inconclusive' to 'pending'.
-- ReopenExpiredVerdicts and EnsureLinkScans are scoped in code to
-- status IN ('safe', 'malicious') for this reason — an inconclusive row does
-- not expire and does not get resubmitted on a timer. That is the conservative
-- choice this fix makes deliberately: no automatic retry loop, fail-closed
-- until a future, separate, deliberate mechanism decides otherwise.
--
-- A message whose links resolve to inconclusive (and none malicious) is
-- refused the same way a malicious one is: status moves to 'deleted', never
-- 'active'. message_publish_outbox gains block_reason so the one message.blocked
-- event both refusals share can tell its author which one happened, without
-- ever conflating "we could not verify this link" with "this link is
-- malicious".

BEGIN;

ALTER TABLE chat.link_scans
    DROP CONSTRAINT link_scans_status_check;

ALTER TABLE chat.link_scans
    ADD CONSTRAINT link_scans_status_check
    CHECK (status IN ('pending', 'safe', 'malicious', 'inconclusive'));

-- This index is the VerdictTTL access path. Inconclusive has no verdict to
-- expire, so it must not enter the index merely because it is non-pending.
DROP INDEX chat.idx_link_scans_decided_at;

CREATE INDEX idx_link_scans_decided_at
    ON chat.link_scans (decided_at)
    WHERE status IN ('safe', 'malicious');

ALTER TABLE chat.message_publish_outbox
    ADD COLUMN block_reason TEXT;

-- Every message.blocked row written before this migration was, by construction,
-- a malicious-link refusal — inconclusive did not exist yet. Backfilled before
-- the presence constraint below so an in-flight or undelivered row from the
-- previous version does not violate it.
UPDATE chat.message_publish_outbox
   SET block_reason = 'malicious_link'
 WHERE event_type = 'message.blocked' AND block_reason IS NULL;

ALTER TABLE chat.message_publish_outbox
    ADD CONSTRAINT message_publish_outbox_block_reason_check
    CHECK (block_reason IS NULL OR block_reason IN ('malicious_link', 'link_check_inconclusive'));

-- A block_reason is required exactly when the event is a refusal, and forbidden
-- otherwise — the two columns cannot drift into a promotion carrying a reason or
-- a refusal carrying none.
ALTER TABLE chat.message_publish_outbox
    ADD CONSTRAINT message_publish_outbox_block_reason_presence_check
    CHECK ((event_type = 'message.blocked') = (block_reason IS NOT NULL));

COMMIT;
