BEGIN;

-- RF-09 keeps the opaque source ID after a hard-deleted origin so readers get
-- the same generic unavailable state. Creation integrity remains enforced by
-- the atomic, workspace-scoped authorization query in chat-service.
ALTER TABLE chat.messages
    DROP CONSTRAINT IF EXISTS messages_referenced_message_id_fkey;

COMMIT;
