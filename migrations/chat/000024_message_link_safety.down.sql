-- 000024_message_link_safety.down.sql
--
-- The join table goes first (it references both others), then the scan queue,
-- then the status constraint returns to its two-value form.
--
-- Any message still withheld when this runs has never been published, so it is
-- marked deleted rather than released: rolling back a security control must not
-- publish the messages that control was holding.

BEGIN;

ALTER TABLE chat.messages
    DROP COLUMN IF EXISTS create_request_fingerprint;

ALTER TABLE chat.messages
    DROP COLUMN IF EXISTS link_safety_fingerprint;

DROP INDEX IF EXISTS chat.messages_create_idempotency_unique;

ALTER TABLE chat.messages
    DROP COLUMN IF EXISTS create_idempotency_key;

DROP TABLE IF EXISTS chat.message_pending_mentions;

DROP TABLE IF EXISTS chat.message_publish_outbox;

DROP TABLE IF EXISTS chat.message_link_scans;

DROP INDEX IF EXISTS chat.idx_messages_pending_link_scan;

DROP TABLE IF EXISTS chat.link_scans;

UPDATE chat.messages
   SET status = 'deleted',
       deleted_at = COALESCE(deleted_at, now())
 WHERE status = 'pending_link_scan';

ALTER TABLE chat.messages
    DROP CONSTRAINT messages_status_check;

ALTER TABLE chat.messages
    ADD CONSTRAINT messages_status_check
    CHECK (status IN ('active', 'deleted'));

COMMIT;
