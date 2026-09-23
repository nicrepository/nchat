-- 000055_link_scan_evidence_and_secondary.up.sql
--
-- Two facts issue #928 made the row unable to hold, both added as nullable
-- columns with no default, so this is expand-only in the Blue/Green sense: a
-- slot running the previous release writes and reads `chat.link_scans` without
-- knowing either column, and every row it writes stays valid.
--
-- 1. evidence_expires_at — when the *provider* says its evidence stops being
--    current.
--
--    Until now a verdict's lifetime was entirely local: decided_at plus the
--    shared VerdictTTL. Google Web Risk states an expireTime with a threat
--    match, and a condemnation may not outlive the evidence it rests on. The
--    column is the ceiling, never an extension — freshVerdictSQL requires
--    *both* the local window and this one, so a verdict expires at whichever
--    comes first and a NULL simply leaves VerdictTTL in charge, which is
--    exactly the behaviour every existing row already has.
--
--    Expiry is not a verdict. A row whose evidence lapsed is reopened and asked
--    again; nothing here promotes it to safe.
--
-- 2. secondary_due_at / secondary_scan_uuid — the background second opinion.
--
--    Web Risk answers synchronously, so a cleared link becomes clickable in the
--    pass that checked it. Cloudflare URL Scanner cannot answer in that window —
--    it is submit-then-poll — but its answer still matters: an explicit
--    condemnation from it must move a target safe -> malicious and revoke the
--    href and the preview.
--
--    That is a second *lane* on the same row, not a second queue and not a
--    second state machine. The row stays `safe`, keeps its verdict, keeps
--    serving hrefs, and carries in addition "a secondary verification is
--    outstanding, next due at T, currently tracking scan U". The same worker
--    drains it in the same pass, with the same lease-by-update discipline as
--    the primary claim.
--
--    It is deliberately bounded by the clearance it verifies rather than by an
--    attempt counter: the claim predicate requires the row to still be a fresh
--    safe verdict, so when the clearance expires the lane ends with it and the
--    ordinary reopen path takes over. There is no state in which a secondary
--    verification outlives the thing it was verifying.

BEGIN;

ALTER TABLE chat.link_scans
    ADD COLUMN evidence_expires_at TIMESTAMPTZ;

ALTER TABLE chat.link_scans
    ADD COLUMN secondary_due_at TIMESTAMPTZ;

ALTER TABLE chat.link_scans
    ADD COLUMN secondary_scan_uuid TEXT;

-- A secondary scan id is only meaningful while a verification is outstanding.
-- Stated as a constraint rather than a convention because the pair is written
-- by three different statements (scheduled, submitted, settled) and a leftover
-- id with no lane would be polled by nobody and cleared by nothing.
ALTER TABLE chat.link_scans
    ADD CONSTRAINT link_scans_secondary_pair_check
    CHECK (secondary_scan_uuid IS NULL OR secondary_due_at IS NOT NULL);

-- The secondary claim's predicate and nothing else. Partial, so the index is
-- empty whenever no verification is outstanding — the same property the primary
-- claim's index has, and the reason idle polling on this lane costs nothing.
CREATE INDEX idx_link_scans_secondary_due
    ON chat.link_scans (secondary_due_at)
    WHERE secondary_due_at IS NOT NULL;

-- Expiry sweeps read this alongside decided_at. Partial for the same reason:
-- only a provider that stated a limit has a row here.
CREATE INDEX idx_link_scans_evidence_expires_at
    ON chat.link_scans (evidence_expires_at)
    WHERE evidence_expires_at IS NOT NULL;

COMMIT;
