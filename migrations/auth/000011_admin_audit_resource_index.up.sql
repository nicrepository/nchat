-- 000011_admin_audit_resource_index.up.sql
-- Admin Console management (issue #579): the index behind "show me this
-- person's administrative history".
--
-- GET /api/admin/audit/events?user_id=<uuid> narrows the trail to one resource
-- key — 'admin.user:<uuid>' — and then returns the most recent rows. Without an
-- index that combines the two, PostgreSQL walks
-- idx_admin_audit_events_occurred from the newest row and discards everything
-- that does not match, which costs the whole table for a person whose last
-- administrative event is old. That is precisely the case the feature exists
-- for: an event outside the platform-wide window is still the first one in its
-- own subject's history.
--
-- (resource, occurred_at DESC, id DESC) answers the equality, the ordering and
-- the limit in one range scan.
--
-- Not redundant with the two indexes migration 000008 created:
--   - idx_admin_audit_events_occurred leads with occurred_at and cannot serve
--     the equality;
--   - idx_admin_audit_events_actor answers "what did this person *do*", which
--     is a different question from "what was done *to* them" — the actor column
--     and the resource column are not the same fact.
--
-- Partial on resource IS NOT NULL: the column is nullable and a row without a
-- resource can never match an equality filter, so it does not belong in the
-- index. auth.admin_audit_events only grows, so keeping it out is worth stating.
--
-- Lock note: CREATE INDEX takes a SHARE lock for the duration of the build.
-- CONCURRENTLY cannot run inside the transaction this repository's migration
-- runner requires. No table is rewritten.

BEGIN;

SET LOCAL search_path = auth, public;

CREATE INDEX idx_admin_audit_events_resource
    ON auth.admin_audit_events (resource, occurred_at DESC, id DESC)
    WHERE resource IS NOT NULL;

COMMIT;
