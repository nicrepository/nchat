BEGIN;

DROP TRIGGER workspace_members_populate_public_channels ON chat.workspace_members;
DROP FUNCTION chat.populate_workspace_member_public_channels();

DROP TRIGGER channels_populate_public_members ON chat.channels;
DROP FUNCTION chat.populate_public_channel_members();

COMMIT;
