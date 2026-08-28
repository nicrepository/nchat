BEGIN;

DROP INDEX IF EXISTS chat.messages_forward_idempotency_uidx;

ALTER TABLE chat.messages
    DROP COLUMN IF EXISTS forward_idempotency_key;

COMMIT;
