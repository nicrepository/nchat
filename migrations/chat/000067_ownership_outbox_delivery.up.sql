BEGIN;
SET LOCAL lock_timeout = '5s';

-- Generated metadata also covers writers from the previous release. The
-- original reason remains the operation's provenance, including invalidation.
ALTER TABLE chat.ownership_audit
    ADD COLUMN conversation_type TEXT GENERATED ALWAYS AS
        (CASE conversation_kind WHEN 'dm' THEN 'group' ELSE 'private_channel' END) STORED,
    ADD COLUMN source TEXT GENERATED ALWAYS AS
        (CASE reason WHEN 'succession' THEN 'automatic_successor'
            WHEN 'invalidation' THEN 'workspace_invalidation'
            WHEN 'backfill' THEN 'backfill' ELSE 'manual' END) STORED,
    ADD COLUMN operation TEXT GENERATED ALWAYS AS
        (CASE reason WHEN 'transfer' THEN 'transfer'
            WHEN 'succession' THEN 'promote_successor'
            WHEN 'invalidation' THEN
                CASE WHEN new_role = 'owner' THEN 'promote_successor' ELSE 'invalidate_access' END
            WHEN 'backfill' THEN 'backfill' ELSE 'change_role' END) STORED,
    ADD COLUMN result TEXT NOT NULL DEFAULT 'success' CHECK (result = 'success');

ALTER TABLE chat.ownership_outbox
    ADD COLUMN attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    ADD COLUMN last_attempt_at TIMESTAMPTZ,
    ADD COLUMN next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ADD COLUMN last_failure TEXT CHECK (last_failure IN ('publish_failed', 'publish_timeout'));
CREATE INDEX ownership_outbox_ready ON chat.ownership_outbox(next_attempt_at,id)
    WHERE published_at IS NULL;
-- Audit represents a completed mutation even during activation backfill.
-- Enqueue atomically without depending on the rollout flag; membership-only
-- invalidations retain their existing rollout gate.
CREATE OR REPLACE FUNCTION chat.enqueue_ownership_change() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO chat.ownership_outbox(workspace_id,conversation_kind,conversation_id)
    VALUES (NEW.workspace_id,NEW.conversation_kind,NEW.conversation_id)
    ON CONFLICT (transaction_id,conversation_kind,conversation_id) DO NOTHING;
    RETURN NULL;
END;
$$;
COMMIT;
