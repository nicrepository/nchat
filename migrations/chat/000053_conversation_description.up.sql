BEGIN;

-- Conversation description (issue #894).
--
-- The "Sobre" block of the channel and group panels states four facts about a
-- conversation: what it is for, when it was created, by whom, and how many
-- people are in it. Three of them were already persisted — created_at,
-- created_by, and the membership the counts read. The first was not, so the
-- panel had nothing to render and said so; docs/api/chat-channel-details.md
-- recorded the absence as a schema fact rather than as a missing feature.
--
-- The description belongs to the conversation aggregate, not to the person who
-- opened it and not to a profile, so it is a column on the two tables that are
-- conversations here: chat.channels and chat.dm_conversations. A 1:1 row in
-- dm_conversations inherits the column because it is the same table; nothing
-- reads it for a 'direct' conversation, whose panel is a person's profile and
-- has no description to show.
--
-- Nullable with no default, and no backfill. Every channel and group that
-- exists today was created without one, and "this conversation has never had a
-- description" is a true state that stays true — inventing text for historical
-- rows would put words in their creators' mouths. NULL and '' both read as
-- absent downstream, so a later writer that trims to empty needs no migration.
--
-- Plain text. There is no rich-text format anywhere in this domain for
-- conversation metadata, and the read path renders the value as a React text
-- node, never as markup.
ALTER TABLE chat.channels
    ADD COLUMN description TEXT;

ALTER TABLE chat.dm_conversations
    ADD COLUMN description TEXT;

-- The cap is domain.MaxConversationDescriptionCodePoints (500), the same number
-- the service will enforce the day a write path exists. The database is the
-- last line, not the only one: a repair script, an importer or a psql session
-- reaches the column without passing through any service.
--
-- 500 sits where the existing text caps put it: above a title (120) and a
-- channel display name (100), because this is prose rather than a label, and
-- well below a scraped link-preview description (1000), because a person types
-- this one. char_length counts code points, matching Go's
-- utf8.RuneCountInString, so the two enforcements agree on what "500" means for
-- non-ASCII text.
--
-- NOT VALID: every existing row has NULL here and therefore already satisfies
-- the constraint, but validating in this statement would scan both tables under
-- ACCESS EXCLUSIVE. Migration 000054 performs that scan under SHARE UPDATE
-- EXCLUSIVE, which blocks nothing the application does — the same two-step
-- 000047/000048 and 000050/000051 use. New rows are enforced the instant this
-- commits; only the proof about old ones is deferred.
ALTER TABLE chat.channels
    ADD CONSTRAINT channels_description_length_check
    CHECK (description IS NULL OR char_length(description) <= 500)
    NOT VALID;

ALTER TABLE chat.dm_conversations
    ADD CONSTRAINT dm_conversations_description_length_check
    CHECK (description IS NULL OR char_length(description) <= 500)
    NOT VALID;

COMMIT;
