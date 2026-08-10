-- 000021_message_attachments.down.sql
-- Rolling back drops the message <-> attachment edge and nothing else.
--
-- The attachments themselves are untouched: they live in files.attachments,
-- which this migration never created and never wrote to, and they remain
-- listed by destination exactly as they were before RF-32 linked them to
-- messages. No object, storage key or wrapped DEK is affected.
--
-- The links are lost, which is what a rollback of this feature means: messages
-- go back to being text-only and a build that predates it would not read the
-- table anyway. Nothing else can be inferred from it, so there is nothing to
-- preserve elsewhere first.

BEGIN;

DROP TABLE IF EXISTS chat.message_attachments;

COMMIT;
