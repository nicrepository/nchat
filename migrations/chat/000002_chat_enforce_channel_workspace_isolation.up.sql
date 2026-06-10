-- 000002_chat_enforce_channel_workspace_isolation.up.sql
-- Enforce cross-workspace category integrity and general channel invariants.
--
-- Changes:
--   1. UNIQUE(workspace_id, id) on channel_categories enables composite FK.
--   2. Replace plain category_id FK with composite (workspace_id, category_id) FK
--      to prevent channels in workspace A from referencing categories in workspace B.
--      NULL category_id rows are exempt from FK checks (standard PostgreSQL behaviour).
--   3. CHECK: is_general channels must be type='public' AND status='active'.

BEGIN;

-- 1. Unique composite key needed to be the target of the composite FK.
ALTER TABLE chat.channel_categories
    ADD CONSTRAINT channel_categories_workspace_id_id_unique UNIQUE (workspace_id, id);

-- 2a. Drop the existing plain FK (auto-generated name from migration 000001).
ALTER TABLE chat.channels
    DROP CONSTRAINT IF EXISTS channels_category_id_fkey;

-- 2b. Add composite FK: prevents cross-workspace category references.
ALTER TABLE chat.channels
    ADD CONSTRAINT channels_workspace_category_fk
        FOREIGN KEY (workspace_id, category_id)
        REFERENCES chat.channel_categories (workspace_id, id)
        ON DELETE SET NULL;

-- 3. Enforce general channel invariants at the database level.
ALTER TABLE chat.channels
    ADD CONSTRAINT channels_general_must_be_public_active
        CHECK (NOT is_general OR (type = 'public' AND status = 'active'));

COMMIT;
