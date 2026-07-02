BEGIN;

DROP TABLE IF EXISTS chat.notification_outbox;

ALTER TABLE chat.messages
    DROP CONSTRAINT messages_body_format_check,
    ADD CONSTRAINT messages_body_format_check CHECK (body_format IN ('v1', 'v2'));

COMMIT;
