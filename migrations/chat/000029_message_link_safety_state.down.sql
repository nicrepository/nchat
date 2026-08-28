-- 000029_message_link_safety_state.down.sql
-- Reverses 000027.
--
-- Rolling back drops the per-message link-safety axis, which means a message
-- published *because* its links were only inconclusive loses the marker that
-- said so. The previous version treats such a message as an ordinary active one:
-- it stays visible, which is the same content the recipients already have, and no
-- warning is drawn.
--
-- What the rollback does not do — and must not — is republish or re-block
-- anything. It touches no message status. The one real consequence is stated
-- rather than hidden: a message whose link was later proven malicious by
-- reconciliation goes back to rendering as normal, because the column carrying
-- that fact is gone. An operator rolling this back should expect that.

BEGIN;

DROP INDEX chat.idx_link_scans_reconcile_due;

ALTER TABLE chat.link_scans
    DROP COLUMN IF EXISTS next_reconcile_at;

ALTER TABLE chat.link_scans
    DROP COLUMN IF EXISTS reconcile_attempts;

ALTER TABLE chat.messages
    DROP CONSTRAINT IF EXISTS messages_link_safety_state_check;

ALTER TABLE chat.messages
    DROP COLUMN IF EXISTS link_safety_state;

COMMIT;
