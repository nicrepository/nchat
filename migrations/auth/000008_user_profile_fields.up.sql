-- migrations/auth/000008_user_profile_fields.up.sql
-- Adds the optional self-service profile fields shown on the "Editar perfil"
-- screen (job title, bio, timezone and custom status) alongside
-- the existing display_name. All four are nullable — a user may leave any of
-- them unset.

BEGIN;

SET LOCAL search_path = auth, public;

ALTER TABLE auth.users
    ADD COLUMN job_title    TEXT,
    ADD COLUMN bio          TEXT,
    ADD COLUMN timezone      TEXT,
    ADD COLUMN custom_status TEXT;

-- job_title and custom_status are short, single-line labels — same length
-- budget as display_name (see selfDisplayNameMaxLen in user_service.go).
ALTER TABLE auth.users
    ADD CONSTRAINT users_job_title_length_check
        CHECK (job_title IS NULL OR char_length(job_title) <= 80),
    ADD CONSTRAINT users_custom_status_length_check
        CHECK (custom_status IS NULL OR char_length(custom_status) <= 80);

-- bio is free-form paragraph text; 500 chars is a judgment call (no existing
-- product requirement or sibling field to reuse — see selfBioMaxLen).
ALTER TABLE auth.users
    ADD CONSTRAINT users_bio_length_check
        CHECK (bio IS NULL OR char_length(bio) <= 500);

-- timezone is not length-bounded here: it is validated at the application
-- layer against the real IANA time zone database (time.LoadLocation), which
-- is a stronger and more meaningful check than a character count — an IANA
-- name that passes a length check can still be nonsense, and a real one is
-- already short (the longest is under 40 characters). A CHECK constraint
-- cannot run that validation, so it is not duplicated here as a weaker proxy.

COMMIT;
