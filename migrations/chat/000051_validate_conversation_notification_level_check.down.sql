BEGIN;

-- Returns conversation_notification_prefs_level_check to the NOT VALID state
-- 000050 leaves it in.
--
-- PostgreSQL has no "invalidate constraint", so the constraint is dropped and
-- re-added NOT VALID. Both statements are in one transaction, so there is no
-- instant in which the column is unconstrained: a concurrent writer either sees
-- the old constraint or the new one, never neither.
--
-- The re-added constraint is byte-identical to 000050's, so re-running 000051
-- afterwards validates it again.
ALTER TABLE chat.conversation_notification_prefs
    DROP CONSTRAINT conversation_notification_prefs_level_check;

ALTER TABLE chat.conversation_notification_prefs
    ADD CONSTRAINT conversation_notification_prefs_level_check
    CHECK (notification_level IN ('all', 'mentions_replies'))
    NOT VALID;

ALTER TABLE chat.conversation_notification_prefs
    DROP CONSTRAINT conversation_notification_prefs_sparse_default_check;

ALTER TABLE chat.conversation_notification_prefs
    ADD CONSTRAINT conversation_notification_prefs_sparse_default_check
    CHECK (notification_level <> 'all' OR muted_at IS NOT NULL)
    NOT VALID;

COMMIT;
