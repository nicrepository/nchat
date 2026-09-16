BEGIN;

-- Per-conversation notification level, beside the mute that already existed
-- (issue #136, evolving #527).
--
-- # Two dimensions, not one enum
--
-- 000037 gave this table exactly one thing to say: the row exists, therefore
-- this user silenced this conversation. The product now has three states to
-- offer — every message, mentions and replies, silenced — and the obvious
-- shortcut would be one destructive enum holding all three.
--
-- It is refused here, because it loses information the user gave us. Silencing
-- #infraestrutura from the sidebar's three-dots menu must not forget that the
-- same user had chosen "mentions and replies" in their profile; turning the
-- notifications back on has to restore that choice and not the default. An enum
-- whose third value overwrites the first two cannot do that at all.
--
-- So the two facts are stored as two columns, each meaning one thing:
--
--     notification_level   which events the user wants alerts for
--     muted_at             whether alerts are silenced right now, and since when
--
-- The state the UI shows is derived, never stored: muted_at wins, otherwise the
-- level speaks. Mute and unmute touch muted_at and nothing else, which is what
-- makes the restore free rather than a second column remembering a previous
-- value.
--
-- # Compatibility with what 000037 already persisted
--
-- Every existing row is a mute and carries a muted_at, because that column was
-- NOT NULL DEFAULT now() from the start. The new column arrives NOT NULL
-- DEFAULT 'all', so an existing row reads as "all messages, currently
-- silenced" — semantically identical to what it meant yesterday. Nobody loses
-- a preference and no backfill is needed.
--
-- PostgreSQL 11+ stores ADD COLUMN ... DEFAULT as a catalogue default and
-- materialises it on the next write of each row, so this is a metadata change
-- rather than a rewrite.
ALTER TABLE chat.conversation_notification_prefs
    ADD COLUMN notification_level TEXT NOT NULL DEFAULT 'all';

-- muted_at becomes nullable, which is what makes the second dimension
-- expressible: "mentions and replies, not silenced" is a row that has to exist
-- and must not claim a mute.
--
-- Relaxing NOT NULL is compatible with the release slot still running the
-- previous build: its inserts name no muted_at and still receive the DEFAULT
-- now() this statement leaves in place, and its reads treat any row as a mute
-- — which stays true for every row that build itself can write.
--
-- The row that build would read *wrongly* is an unmuted `mentions_replies`, and
-- this migration is deliberately not what prevents it. Nothing can be written
-- into that state while the application's rollout gate is shut:
-- CHAT_CONVERSATION_NOTIFICATION_LEVELS_ENABLED defaults to false, and
-- SidebarService.SetConversationNotificationPreference refuses that mode before
-- reaching any store. So this migration may be applied while the previous slot
-- is still serving; the writer is opened later, once no such reader is left.
-- See docs/architecture/notification-policy.md, "Rollout".
ALTER TABLE chat.conversation_notification_prefs
    ALTER COLUMN muted_at DROP NOT NULL;

-- The closed set of levels, in the database, so an unknown value is unreachable
-- through any path at all — a repair script or a psql session included — and
-- not only through the endpoint that validates today.
--
-- NOT VALID for the same reason 000047 used it: every existing row carries the
-- default above and therefore already satisfies this, and validating here would
-- scan the table under ACCESS EXCLUSIVE. Migration 000051 performs that scan
-- under SHARE UPDATE EXCLUSIVE, which blocks nothing an application does.
-- Enforcement of new rows starts the instant this commits.
--
-- TEXT with a CHECK rather than an enum type, matching every other closed set
-- in this schema: adding a level later is an ordinary migration, while
-- ALTER TYPE ... ADD VALUE cannot run inside the transaction every migration
-- here is required to be wrapped in.
ALTER TABLE chat.conversation_notification_prefs
    ADD CONSTRAINT conversation_notification_prefs_level_check
    CHECK (notification_level IN ('all', 'mentions_replies'))
    NOT VALID;

-- The sparse representation, in the database.
--
-- "Every message, not silenced" is the *absence* of a row — that is what
-- 000037 established and what every reader still relies on. A row saying it
-- carries no preference anybody expressed, and during a blue/green window it is
-- worse than useless: a release slot from before issue #136 reads any row as a
-- mute, so `all` + NULL would silence somebody who had silenced nothing.
--
-- The application cannot write that state — Unmute is one statement whose
-- UPDATE half excludes `all` rows outright — and this is the independent second
-- defence, so a repair script, a psql session or a future writer cannot either.
--
-- Written as an implication rather than a NOT: for `mentions_replies` the row is
-- unconstrained, which is required — "mentions and replies, not silenced" is
-- exactly the state the issue exists to make expressible.
--
-- Legacy rows satisfy it by construction. Every row that predates this
-- migration carries muted_at, because 000037 declared the column NOT NULL
-- DEFAULT now(), so none of them can be `all` + NULL.
--
-- NOT VALID for the same reason as the constraint above; 000051 validates both
-- under a lock that blocks nothing an application does.
ALTER TABLE chat.conversation_notification_prefs
    ADD CONSTRAINT conversation_notification_prefs_sparse_default_check
    CHECK (notification_level <> 'all' OR muted_at IS NOT NULL)
    NOT VALID;

COMMIT;
