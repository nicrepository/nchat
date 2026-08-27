-- 000015_voice_messages.up.sql
-- Issue #670: explicit voice-message semantics for an attachment.
--
-- audio_kind is the whole contract: a message-side voice bubble is never
-- inferred from filename, extension or content type (both of those stay
-- exactly what they always were). It is set once, at upload time, from the
-- multipart "purpose" field the client sends, exactly like draft_expires_at
-- already is (000014) — never guessed at afterwards and never derived from
-- the sniffed bytes.
--
-- declared_duration_ms mirrors declared_mime: recorded for display, never
-- trusted for anything security- or capacity-relevant. The server's own
-- upload size cap is the only enforced limit; this column never gates it.
--
-- Both columns are nullable with no default, so every row this build has
-- already written stays valid, and a file-service instance that predates
-- this migration keeps inserting and reading rows unchanged — it simply
-- never populates or reads the two new columns.

BEGIN;

SET LOCAL search_path = files, public;

ALTER TABLE files.attachments
    ADD COLUMN audio_kind            TEXT,
    ADD COLUMN declared_duration_ms  INTEGER;

ALTER TABLE files.attachments
    ADD CONSTRAINT attachments_audio_kind_check CHECK (
        audio_kind IS NULL OR audio_kind = 'voice'
    ),
    ADD CONSTRAINT attachments_declared_duration_ms_check CHECK (
        declared_duration_ms IS NULL OR declared_duration_ms > 0
    );

COMMIT;
