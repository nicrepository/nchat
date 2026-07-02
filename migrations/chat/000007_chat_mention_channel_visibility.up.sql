BEGIN;

-- Shared predicate for "is this channel visible to this user for mention
-- purposes": the channel is the workspace general channel, is public, or the
-- user is an explicit member. Extracted from the duplicated SQL previously
-- inlined in CreateMessage's invalid_mentions CTE and in
-- ResolveAuthorizedMentionLabels. Pure refactor: identical boolean result.
CREATE FUNCTION chat.channel_visible_to_user(p_channel_id UUID, p_user_id UUID)
RETURNS boolean
LANGUAGE sql
STABLE
AS $$
    SELECT EXISTS (
        SELECT 1
        FROM chat.channels c
        LEFT JOIN chat.channel_members cm
          ON cm.channel_id = c.id AND cm.user_id = p_user_id
        WHERE c.id = p_channel_id
          AND (c.is_general = true OR c.type = 'public' OR cm.user_id IS NOT NULL)
    );
$$;

COMMIT;
