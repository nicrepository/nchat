BEGIN;
CREATE INDEX reactions_message_idx ON chat.reactions (message_id);
COMMIT;
