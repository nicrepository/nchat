-- Reexecutable operational backfill. The integer is the number of owners assigned.
\set ON_ERROR_STOP on
BEGIN;
SET TRANSACTION ISOLATION LEVEL READ COMMITTED;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';
LOCK TABLE chat.workspaces, chat.dm_conversations, chat.channels,
    chat.dm_members, chat.channel_members, chat.workspace_members, auth.users
    IN SHARE ROW EXCLUSIVE MODE;
SELECT count(*) AS audit_before FROM chat.ownership_audit \gset
SELECT chat.backfill_conversation_ownership() AS owners_assigned;
SELECT count(*) - :audit_before AS audited_role_changes FROM chat.ownership_audit;
COMMIT;
