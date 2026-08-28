BEGIN;

-- Drops the constraint and nothing else: no data was changed on the way up, so
-- there is none to restore.
ALTER TABLE chat.channels
    DROP CONSTRAINT IF EXISTS channels_display_name_length_check;

COMMIT;
