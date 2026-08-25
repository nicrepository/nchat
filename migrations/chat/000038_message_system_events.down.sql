BEGIN;

ALTER TABLE chat.messages DROP CONSTRAINT IF EXISTS messages_system_event_check;

ALTER TABLE chat.messages
    DROP COLUMN IF EXISTS event_payload,
    DROP COLUMN IF EXISTS event_type;

COMMIT;
