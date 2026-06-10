-- 000002_chat_enforce_channel_workspace_isolation.down.sql
BEGIN;

ALTER TABLE chat.channels
    DROP CONSTRAINT IF EXISTS channels_general_must_be_public_active;

ALTER TABLE chat.channels
    DROP CONSTRAINT IF EXISTS channels_workspace_category_fk;

-- Restore original plain FK (auto-generated name matches migration 000001).
ALTER TABLE chat.channels
    ADD CONSTRAINT channels_category_id_fkey
        FOREIGN KEY (category_id)
        REFERENCES chat.channel_categories (id)
        ON DELETE SET NULL;

ALTER TABLE chat.channel_categories
    DROP CONSTRAINT IF EXISTS channel_categories_workspace_id_id_unique;

COMMIT;
