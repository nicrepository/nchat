BEGIN;

ALTER TABLE chat.messages
    DROP CONSTRAINT IF EXISTS messages_parent_message_id_fkey;

ALTER TABLE chat.messages
    ADD CONSTRAINT messages_parent_message_id_fkey
    FOREIGN KEY (parent_message_id) REFERENCES chat.messages (id);

COMMIT;
