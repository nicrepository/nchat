BEGIN;

-- Bind every saved body to the link associations of the version it snapshots.
-- Existing history has no trustworthy version-to-link binding. Leave it NULL:
-- the reader treats NULL as withheld, while every new edit records the exact
-- fingerprint it snapshots.
ALTER TABLE chat.message_edit_history
    ADD COLUMN link_safety_fingerprint TEXT;

-- Version 0 means the current body was last written by a pre-000029 writer and
-- its fingerprint/associations cannot be trusted. New writers advance the
-- version with every body replacement.
ALTER TABLE chat.messages
    ADD COLUMN link_safety_projection_version BIGINT NOT NULL DEFAULT 0;

-- During a rolling deploy an old pod can still update body_text without
-- advancing the projection. Mark that body fail-closed; a later new edit will
-- snapshot it as legacy (NULL fingerprint) instead of certifying stale facts.
CREATE FUNCTION chat.invalidate_legacy_message_link_projection()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.body_text IS DISTINCT FROM OLD.body_text
       AND NEW.link_safety_projection_version <= OLD.link_safety_projection_version THEN
        NEW.link_safety_projection_version := 0;
        NEW.link_safety_fingerprint := NULL;
        NEW.link_safety_state := CASE
            WHEN OLD.link_safety_state = 'malicious' THEN 'malicious'
            ELSE ''
        END;
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER messages_invalidate_legacy_link_projection
BEFORE UPDATE OF body_text ON chat.messages
FOR EACH ROW
EXECUTE FUNCTION chat.invalidate_legacy_message_link_projection();

-- Keep prior-version associations outside the hot current-body table. This
-- avoids rebuilding message_link_scans' primary key under an exclusive lock.
CREATE TABLE chat.message_edit_history_link_scans (
    history_id UUID NOT NULL,
    canonical_url TEXT NOT NULL,
    PRIMARY KEY (history_id, canonical_url),
    CONSTRAINT message_edit_history_link_scans_history_fk
        FOREIGN KEY (history_id) REFERENCES chat.message_edit_history (id) ON DELETE CASCADE
);

COMMIT;
