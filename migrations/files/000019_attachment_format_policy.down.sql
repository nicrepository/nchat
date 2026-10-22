BEGIN;

LOCK TABLE files.attachments IN ACCESS EXCLUSIVE MODE;

-- Keep the fence across application rollback. Removing it while new uploads
-- exist would let an old worker approve content without policy revalidation.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM files.attachments WHERE format_policy_version > 0) THEN
        RAISE EXCEPTION 'format policy metadata is in use; retain the expanded schema';
    END IF;
END
$$;

ALTER TABLE files.attachments
    DROP CONSTRAINT attachments_format_version_check,
    DROP COLUMN format_id,
    DROP COLUMN format_scan_version,
    DROP COLUMN format_policy_version;

COMMIT;
