BEGIN;

-- Structured payload for server-generated system messages (issue #527).
--
-- chat.messages already carries kind IN ('user','system') and nothing has ever
-- written a system row. These two columns are what makes such a row describable
-- without persisting a pre-formatted sentence: the event names what happened and
-- the payload carries the facts, so the client renders localized text and a
-- later locale change does not need a data migration.
--
-- event_payload holds only the facts a renderer needs — old/new name, the target
-- — and never the actor's display name: the actor is chat.messages.sender_id,
-- resolved through the same authorized projection every other message uses, so
-- a name can never be spoofed into the payload.
ALTER TABLE chat.messages
    ADD COLUMN event_type    TEXT,
    ADD COLUMN event_payload JSONB;

-- The two columns belong to system messages and only to them. A user message
-- carrying an event, or a system message carrying none, is a shape this domain
-- does not produce — and a user message that could set event_type would be a
-- forgery vector, so the database refuses it outright.
--
-- NOT VALID: existing rows are all kind='user' with both columns NULL and
-- therefore already satisfy it, but skipping the full-table scan keeps the
-- deploy non-blocking. Migration 000039 validates it.
ALTER TABLE chat.messages
    ADD CONSTRAINT messages_system_event_check CHECK (
        (kind = 'system' AND event_type IS NOT NULL AND event_payload IS NOT NULL)
        OR (kind <> 'system' AND event_type IS NULL AND event_payload IS NULL)
    ) NOT VALID;

COMMIT;
