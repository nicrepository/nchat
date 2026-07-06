BEGIN;

CREATE INDEX IF NOT EXISTS message_reactions_message_id_idx
    ON chat.message_reactions (message_id, created_at);

COMMIT;
