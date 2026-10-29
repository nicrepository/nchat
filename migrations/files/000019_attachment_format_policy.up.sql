BEGIN;

-- Expand: old writers retain version 0. New uploads record the classifier
-- version; a pre-policy worker cannot approve them by setting status alone.
ALTER TABLE files.attachments
    ADD COLUMN format_policy_version INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN format_scan_version INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN format_id TEXT NOT NULL DEFAULT '',
    ADD CONSTRAINT attachments_format_version_check CHECK (
        format_policy_version >= 0 AND format_scan_version >= 0
        AND length(format_id) <= 32
        AND (status <> 'clean' OR format_scan_version >= format_policy_version)
    );

COMMIT;
