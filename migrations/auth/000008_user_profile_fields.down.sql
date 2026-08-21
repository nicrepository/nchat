-- migrations/auth/000008_user_profile_fields.down.sql
-- Drops only objects introduced by 000008.

BEGIN;

SET LOCAL search_path = auth, public;

ALTER TABLE auth.users
    DROP CONSTRAINT IF EXISTS users_bio_length_check,
    DROP CONSTRAINT IF EXISTS users_custom_status_length_check,
    DROP CONSTRAINT IF EXISTS users_job_title_length_check,
    DROP COLUMN IF EXISTS custom_status,
    DROP COLUMN IF EXISTS timezone,
    DROP COLUMN IF EXISTS bio,
    DROP COLUMN IF EXISTS job_title;

COMMIT;
