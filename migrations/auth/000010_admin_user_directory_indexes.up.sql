-- 000010_admin_user_directory_indexes.up.sql
-- Admin Console management (issue #579): the indexes the platform user
-- directory pages on.
--
-- No table and no column is added. The Admin Console reads the identity model
-- that already exists; what it needs that did not exist before is an ordering
-- the database can serve from an index instead of by sorting the whole table
-- for every page.
--
-- Why (created_at DESC, id DESC):
--   the directory is presented newest-first, and it pages by keyset — the next
--   page asks for rows strictly before the last row shown, as a row-value
--   comparison on exactly these two columns. Making the index match the sort
--   and the resumption predicate is what keeps a page costing its own rows
--   rather than the whole tenant (the same reasoning that moved auth-service's
--   workspace listing onto its membership primary key).
--
--   id is the tiebreak, not decoration: created_at is not unique, and without a
--   unique trailing column a keyset page can repeat or skip rows.
--
-- Why partial on deleted_at IS NULL:
--   soft-deleted accounts never appear in the directory, so they do not belong
--   in the index either. The predicate is part of every listing query.
--
-- Lock note: CREATE INDEX takes a SHARE lock, blocking writes to auth.users for
-- the duration. CONCURRENTLY is not used because it cannot run inside the
-- transaction this repository's migration runner requires, and auth.users is
-- small enough for the build to be brief. No table is rewritten.

BEGIN;

SET LOCAL search_path = auth, public;

CREATE INDEX idx_users_directory_page
    ON auth.users (created_at DESC, id DESC)
    WHERE deleted_at IS NULL;

-- Resolving "who administers the platform" for a page of the directory is a
-- lookup per row on auth.admin_principal_roles by user_id, which the table's
-- primary key (user_id, role_slug) already serves. The reverse question — "who
-- holds this role" — is what the last-superuser invariant asks on every role
-- revocation, and nothing indexed it: the primary key's leading column is the
-- user, not the role.
CREATE INDEX idx_admin_principal_roles_role
    ON auth.admin_principal_roles (role_slug);

COMMIT;
