BEGIN;

ALTER TABLE chat.messages
    DROP CONSTRAINT IF EXISTS messages_body_format_check,
    DROP COLUMN IF EXISTS body_format;

COMMIT;
