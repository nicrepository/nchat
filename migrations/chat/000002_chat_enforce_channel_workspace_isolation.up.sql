-- 000002_chat_enforce_channel_workspace_isolation.up.sql
-- Enforce cross-workspace category integrity and general channel invariants.
--
-- Changes:
--   1. UNIQUE(workspace_id, id) on channel_categories enables composite FK.
--   2. Replace plain category_id FK with composite (workspace_id, category_id) FK
--      to prevent channels in workspace A from referencing categories in workspace B.
--      NULL category_id rows are exempt from FK checks (standard PostgreSQL behaviour).
--   3. CHECK: is_general channels must be type='public' AND status='active'.
--   4. Deferred constraint triggers require every committed workspace to have
--      an active public general channel while allowing workspace + channel creation
--      in the same transaction.

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
        ON DELETE SET NULL (category_id);

-- 3. Enforce general channel invariants at the database level.
ALTER TABLE chat.channels
    ADD CONSTRAINT channels_general_must_be_public_active
        CHECK (NOT is_general OR (type = 'public' AND status = 'active'));

-- 4. Enforce the at-least-one side of the general-channel invariant at commit.
CREATE FUNCTION chat.enforce_workspace_general_channel()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    workspace_ids UUID[];
    current_workspace_id UUID;
BEGIN
    IF TG_TABLE_NAME = 'workspaces' THEN
        workspace_ids := ARRAY[NEW.id];
    ELSIF TG_OP = 'INSERT' THEN
        workspace_ids := ARRAY[NEW.workspace_id];
    ELSIF TG_OP = 'DELETE' THEN
        workspace_ids := ARRAY[OLD.workspace_id];
    ELSE
        workspace_ids := ARRAY[OLD.workspace_id, NEW.workspace_id];
    END IF;

    FOREACH current_workspace_id IN ARRAY workspace_ids LOOP
        IF EXISTS (
            SELECT 1
            FROM chat.workspaces
            WHERE id = current_workspace_id
        ) AND NOT EXISTS (
            SELECT 1
            FROM chat.channels
            WHERE workspace_id = current_workspace_id
              AND is_general = true
              AND type = 'public'
              AND status = 'active'
        ) THEN
            RAISE EXCEPTION 'workspace must have exactly one active public general channel'
                USING ERRCODE = '23514',
                      CONSTRAINT = 'workspace_general_channel_required';
        END IF;
    END LOOP;

    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER workspaces_require_general_channel
    AFTER INSERT OR UPDATE ON chat.workspaces
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW
    EXECUTE FUNCTION chat.enforce_workspace_general_channel();

CREATE CONSTRAINT TRIGGER channels_require_general_channel
    AFTER INSERT OR UPDATE OR DELETE ON chat.channels
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW
    EXECUTE FUNCTION chat.enforce_workspace_general_channel();

COMMIT;
