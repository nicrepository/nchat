BEGIN;

-- Delete only rows the up migration actually inserted. Preexisting creator
-- memberships were never recorded here and therefore cannot be removed.
DELETE FROM chat.channel_members cm
USING chat.migration_000055_channel_creator_memberships repaired
WHERE cm.channel_id = repaired.channel_id
  AND cm.user_id = repaired.user_id;

DROP TABLE chat.migration_000055_channel_creator_memberships;

COMMIT;
