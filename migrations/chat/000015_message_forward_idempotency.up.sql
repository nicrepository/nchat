BEGIN;

ALTER TABLE chat.messages
    ADD COLUMN forward_idempotency_key VARCHAR(128);

CREATE UNIQUE INDEX messages_forward_idempotency_uidx
    ON chat.messages (workspace_id, sender_id, channel_id, forward_idempotency_key)
    WHERE forward_idempotency_key IS NOT NULL;

COMMIT;
