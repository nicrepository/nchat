BEGIN;

-- NOT VALID restores enforcement for new writes without making rollback fail
-- on RF-09 tombstones whose source was already hard-deleted.
ALTER TABLE chat.messages
    DROP CONSTRAINT IF EXISTS messages_referenced_message_id_fkey;

ALTER TABLE chat.messages
    ADD CONSTRAINT messages_referenced_message_id_fkey
    FOREIGN KEY (referenced_message_id) REFERENCES chat.messages (id) NOT VALID;

COMMIT;
