BEGIN;

-- Bound chat.channels.display_name at the database, so no writer — present or
-- future — can persist an unbounded name. char_length() counts characters,
-- matching domain.MaxChannelDisplayNameCodePoints and Go's
-- utf8.RuneCountInString; a byte cap would give an accented or emoji name a
-- fraction of the allowance.
--
-- btrim() mirrors the service's strings.TrimSpace, so the two agree on which
-- value is being measured.
--
-- NOT VALID is the point of this migration, not an oversight: it makes
-- PostgreSQL enforce the CHECK on every INSERT and UPDATE from here on while
-- skipping the scan of existing rows. Adding it validated would take a lock for
-- a full-table scan and would abort the deploy outright if any legacy row were
-- over the limit — the failure mode that matters least (old data) blocking the
-- fix that matters most (new writes). Legacy rows stay untouched and readable;
-- the moment one of them is updated it must come into range.
--
-- Auditing and repairing any out-of-range rows, then VALIDATE CONSTRAINT, is
-- deliberately left to a follow-up migration.
ALTER TABLE chat.channels
    ADD CONSTRAINT channels_display_name_length_check
    CHECK (char_length(btrim(display_name)) BETWEEN 1 AND 100)
    NOT VALID;

COMMIT;
