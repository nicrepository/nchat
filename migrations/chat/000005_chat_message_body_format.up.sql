BEGIN;

ALTER TABLE chat.messages
    ADD COLUMN body_format TEXT NOT NULL DEFAULT 'v1',
    ADD CONSTRAINT messages_body_format_check CHECK (body_format IN ('v1', 'v2'));

COMMIT;
