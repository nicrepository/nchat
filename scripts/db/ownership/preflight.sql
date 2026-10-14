-- Diagnostic snapshot only; activation repeats this check under locks.
\set ON_ERROR_STOP on
BEGIN TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY;
SELECT kind, conversation_id, workspace_id, active_members
FROM chat.orphaned_private_conversations ORDER BY kind, conversation_id;
SELECT count(*) AS orphaned_conversations FROM chat.orphaned_private_conversations;
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM chat.orphaned_private_conversations) THEN
        RAISE EXCEPTION 'ownership preflight failed: active participants without an eligible owner';
    END IF;
END $$;
COMMIT;
