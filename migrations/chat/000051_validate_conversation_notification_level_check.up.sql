BEGIN;

-- The scanning half of 000050's CHECK constraint (issue #136).
--
-- 000050 adds it NOT VALID, which is a catalogue write: it enforces every new
-- row immediately but does not read the existing ones. This pass performs that
-- read and marks the constraint validated, so the planner may rely on it.
--
-- Lock taken here: SHARE UPDATE EXCLUSIVE. It conflicts with DDL and with
-- VACUUM FULL, and with nothing an application does — reading, muting,
-- unmuting and changing a level all proceed while it runs.
--
-- The scan cannot fail: the column is NOT NULL DEFAULT 'all', so every row that
-- predates 000050 reads as 'all', and every row written since has passed this
-- very constraint.
ALTER TABLE chat.conversation_notification_prefs
    VALIDATE CONSTRAINT conversation_notification_prefs_level_check;

-- The sparse-default invariant, validated on the same pass and for the same
-- reason. It also cannot fail: every row that predates 000050 carries a
-- muted_at, because the column was NOT NULL DEFAULT now() before it, and
-- nothing written since could have violated a constraint that was already
-- enforcing new rows.
ALTER TABLE chat.conversation_notification_prefs
    VALIDATE CONSTRAINT conversation_notification_prefs_sparse_default_check;

COMMIT;
