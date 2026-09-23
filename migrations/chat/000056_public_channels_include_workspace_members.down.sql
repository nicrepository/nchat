BEGIN;

DELETE FROM chat.channel_members cm
USING chat.migration_000056_public_channel_memberships repaired,
      chat.channels c
WHERE cm.channel_id = repaired.channel_id
  AND cm.user_id = repaired.user_id
  AND c.id = cm.channel_id
  -- Migration 000055 remains applied after this rollback, so a channel's
  -- creator must remain a member.
  AND cm.user_id IS DISTINCT FROM c.created_by;

DROP TABLE chat.migration_000056_public_channel_memberships;

COMMIT;
