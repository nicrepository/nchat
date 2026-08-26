BEGIN;
ALTER TYPE chat.message_status ADD VALUE IF NOT EXISTS 'archived';
COMMIT;
