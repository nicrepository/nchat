BEGIN;

SET LOCAL search_path = files, public;

ALTER TABLE files.attachments
    DROP CONSTRAINT IF EXISTS attachments_audio_kind_check,
    DROP CONSTRAINT IF EXISTS attachments_declared_duration_ms_check;

ALTER TABLE files.attachments
    DROP COLUMN IF EXISTS audio_kind,
    DROP COLUMN IF EXISTS declared_duration_ms;

COMMIT;
