-- 000002_attachment_dek_binding.down.sql
-- Reverses 000002 — but only against an empty files.attachments.
--
-- Why the rollback refuses to run with data:
--
--   Dropping kek_key_id and dek_wrap_version is not a reversal of anything. The
--   columns are the only record of which key-encryption key sealed each row's
--   data key and under which binding format; the wrapped keys themselves stay
--   exactly as they are, sealed under a binding that authenticates the
--   workspace, the key id, both format versions and the plaintext length. The
--   previous build's format authenticated none of that and cannot reconstruct
--   the associated data, so it cannot open a single one of those keys.
--
--   A rollback with rows present therefore *succeeds at the schema level and
--   breaks every download*: the migration reports success, the objects are still
--   in SeaweedFS, the KEKs are still in the process environment, and nothing can
--   decrypt anything. Reverse rewrap — unwrapping under the current binding and
--   re-sealing under the previous one — is not implemented, and would need the
--   old format back to be implemented first.
--
--   Refusing is the only honest answer. The guard fails closed, before any DROP,
--   and touches no data: it never deletes, truncates, nulls out or converts a
--   row to make itself pass. An operator who hits it has attachments that a
--   rollback would strand, which is a data problem to escalate rather than an
--   obstacle to route around.
--
--   Every row counts, whatever its status. A pending_upload row is an upload in
--   flight whose finalisation would fail against the old schema; a clean row is
--   a file someone can currently download. Neither survives the rollback, so
--   neither is filtered out of the check.
--
-- Draining the new instances first is still the documented procedure — see
-- docs/runbooks/file-service-envelope-encryption.md. The lock below removes the
-- race between the check and the DROPs; it is not a substitute for stopping the
-- writers.

BEGIN;

-- First statement, before the check and before any schema change, and held for
-- the rest of the transaction. Without it a row could be inserted between the
-- emptiness check and the DROPs, and the rollback would strand it.
LOCK TABLE files.attachments IN ACCESS EXCLUSIVE MODE;

DO $$
DECLARE
    existing_rows BIGINT;
BEGIN
    SELECT count(*) INTO existing_rows FROM files.attachments;

    IF existing_rows > 0 THEN
        RAISE EXCEPTION
            'cannot roll back migration 000002 while files.attachments contains rows, found % row(s)',
            existing_rows
            USING HINT =
                'Dropping kek_key_id and dek_wrap_version would leave these rows undecryptable: '
                'the wrapped data keys stay sealed under the current binding and reverse DEK '
                'rewrap is not implemented. Do not TRUNCATE, DELETE or edit the columns to '
                'proceed. See docs/runbooks/file-service-envelope-encryption.md.';
    END IF;
END
$$;

ALTER TABLE files.attachments
    DROP CONSTRAINT IF EXISTS attachments_dek_binding_complete_check,
    DROP CONSTRAINT IF EXISTS attachments_dek_wrap_version_check,
    DROP CONSTRAINT IF EXISTS attachments_kek_key_id_length_check;

ALTER TABLE files.attachments
    DROP COLUMN IF EXISTS dek_wrap_version,
    DROP COLUMN IF EXISTS kek_key_id;

-- Restores the constraint 000001 declared. Unconditionally safe here: the guard
-- above proved there is no row to violate it.
ALTER TABLE files.attachments
    ALTER COLUMN wrapped_dek SET NOT NULL;

COMMIT;
