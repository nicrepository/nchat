-- 000017_channel_category_constraints.up.sql
-- RF-17: make chat.channel_categories safe to write.
--
-- The table itself, chat.channels.category_id, and the composite FK that keeps a
-- channel's category inside the channel's own workspace all already exist
-- (000001 and 000002). What was missing is everything that bounds the *content*
-- of a category row, because until now nothing could write one: the only writer,
-- PGXChannelStore.CreateCategory, was never wired to a service or a handler.
--
-- That is also why every constraint here is added VALIDATED rather than
-- NOT VALID, unlike channels_display_name_length_check in 000016. 000016 had to
-- protect new writes to a table full of live rows. This table has never had a
-- reachable writer, so there is no legacy row to scan around — and the unique
-- index below could not have been deferred anyway, since NOT VALID does not
-- exist for indexes. A partially validated migration would only have looked
-- safer.
--
-- Changes:
--   1. name bounds: length in characters, no surrounding whitespace, no control
--      characters, and "Geral" reserved for the virtual uncategorized group.
--   2. position bounds: non-negative and not absurd.
--   3. UNIQUE (workspace_id, lower(btrim(name))): one category name per
--      workspace, case-insensitively; the same name in two workspaces is fine.
--   4. (workspace_id, position) index replaces the workspace-only index from
--      000001, which it fully covers.
--   5. (workspace_id, category_id) index on chat.channels: the referencing side
--      of channels_workspace_category_fk had no usable index, so deleting a
--      category made the FK's ON DELETE SET NULL sequentially scan every
--      channel. It also serves the grouped sidebar read.

BEGIN;

-- ---------------------------------------------------------------------------
-- 1. name bounds
-- ---------------------------------------------------------------------------

-- char_length() counts characters, matching domain.MaxChannelCategoryNameCodePoints
-- and Go's utf8.RuneCountInString; a byte cap would give an accented or emoji
-- name a fraction of the allowance. Same reasoning as 000016 for channels.
ALTER TABLE chat.channel_categories
    ADD CONSTRAINT channel_categories_name_length_check
    CHECK (char_length(btrim(name)) BETWEEN 1 AND 60);

-- The service trims before writing; this makes "  Projetos" and "Projetos"
-- impossible to store as two different rows that render identically.
ALTER TABLE chat.channel_categories
    ADD CONSTRAINT channel_categories_name_trimmed_check
    CHECK (name = btrim(name));

-- [[:cntrl:]] is the POSIX class, so this does not depend on how the regex
-- engine treats backslash escapes. Control characters in a sidebar label are
-- either a rendering bug or an attempt to forge one.
ALTER TABLE chat.channel_categories
    ADD CONSTRAINT channel_categories_name_no_control_check
    CHECK (name !~ '[[:cntrl:]]');

-- "Geral" is the virtual group that holds channels with category_id IS NULL. A
-- persisted category by that name would make the API's two group kinds
-- indistinguishable to a reader, so the name is reserved at the database rather
-- than only in the service.
ALTER TABLE chat.channel_categories
    ADD CONSTRAINT channel_categories_name_not_reserved_check
    CHECK (lower(btrim(name)) <> 'geral');

-- ---------------------------------------------------------------------------
-- 2. position bounds
-- ---------------------------------------------------------------------------

-- position is server-derived on every path (MAX+1 on create, ordinal on
-- reorder), so this is a backstop against a future writer, not an input check.
ALTER TABLE chat.channel_categories
    ADD CONSTRAINT channel_categories_position_range_check
    CHECK (position >= 0 AND position <= 100000);

-- ---------------------------------------------------------------------------
-- 3. one name per workspace, case-insensitively
-- ---------------------------------------------------------------------------

CREATE UNIQUE INDEX channel_categories_workspace_name_uidx
    ON chat.channel_categories (workspace_id, lower(btrim(name)));

-- ---------------------------------------------------------------------------
-- 4. ordered listing index
-- ---------------------------------------------------------------------------

CREATE INDEX channel_categories_workspace_position_idx
    ON chat.channel_categories (workspace_id, position);

-- Redundant once the composite index above exists: any lookup by workspace_id
-- alone uses its leading column. Dropped rather than left to cost writes.
DROP INDEX IF EXISTS chat.idx_channel_categories_workspace;

-- ---------------------------------------------------------------------------
-- 5. referencing-side index for the composite category FK
-- ---------------------------------------------------------------------------

CREATE INDEX idx_channels_workspace_category
    ON chat.channels (workspace_id, category_id)
    WHERE category_id IS NOT NULL;

COMMIT;
