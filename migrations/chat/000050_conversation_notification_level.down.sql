BEGIN;

-- Returns chat.conversation_notification_prefs to 000037's one-dimensional
-- shape, where the existence of a row means "silenced" and nothing else.
--
-- # The rows that have no old representation, and why they are deleted
--
-- A row holding `mentions_replies` with muted_at NULL says "alert me only for
-- mentions and replies, and do not silence me". The old model has no way to
-- write that down: its only vocabulary is silenced/not silenced.
--
-- Of the two possible conversions, only one is safe. Keeping the row would make
-- the old build read it as a mute and silence a conversation the user had
-- deliberately left active — the rollback turning alerts off behind their back.
-- Deleting it reads as "not silenced", which loses the granularity and is
-- exactly the documented, lossy conversion: the user sees every message again,
-- which is the product default, and can re-express the finer choice once the
-- feature returns.
--
-- So granularity is discarded and silence is never invented. Every row that the
-- old model *can* represent — any row carrying a muted_at — is left untouched,
-- so no existing mute is lost either.
DELETE FROM chat.conversation_notification_prefs
WHERE muted_at IS NULL;

-- With no NULLs left, the original NOT NULL can be restored without a rewrite
-- of meaning. The DEFAULT now() 000037 declared was never removed, so an insert
-- by the old build keeps working exactly as it did.
ALTER TABLE chat.conversation_notification_prefs
    ALTER COLUMN muted_at SET NOT NULL;

-- The constraints go before the column: dropping the column would take them
-- along, but naming them keeps this readable against a database where a partial
-- apply left one without the others.
ALTER TABLE chat.conversation_notification_prefs
    DROP CONSTRAINT IF EXISTS conversation_notification_prefs_sparse_default_check;

ALTER TABLE chat.conversation_notification_prefs
    DROP CONSTRAINT IF EXISTS conversation_notification_prefs_level_check;

ALTER TABLE chat.conversation_notification_prefs
    DROP COLUMN IF EXISTS notification_level;

COMMIT;
