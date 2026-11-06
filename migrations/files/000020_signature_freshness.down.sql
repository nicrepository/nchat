BEGIN;

-- Removing the enforcement gate while it is enabled would authorize old writers.
LOCK TABLE files.signature_policy IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF NOT EXISTS (SELECT 1 FROM files.signature_policy WHERE id=1 AND NOT enabled
                AND (max_age_seconds IS NULL OR max_age_seconds BETWEEN 1 AND 31536000)) THEN
  RAISE EXCEPTION 'explicit valid disabled signature policy required before removing its gate';
 END IF;
END $$;
DROP TRIGGER attachments_fresh_scan_commit ON files.attachments;
DROP TRIGGER attachments_fresh_scan ON files.attachments;
DROP FUNCTION files.require_fresh_scan();
DROP FUNCTION files.persist_proven_scan;
DROP FUNCTION files.require_active_signature_policy();
ALTER TABLE files.attachments
 DROP COLUMN scan_signature_generation,
 DROP COLUMN scan_engine_version,
 DROP COLUMN scan_signature_version,
 DROP COLUMN scan_database_time,
 DROP COLUMN scan_loaded_at,
 DROP COLUMN scan_completed_at;
DROP TABLE files.signature_policy;

DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'nchat_app') THEN
  GRANT INSERT,UPDATE ON files.attachments TO nchat_app;
 END IF;
END $$;

COMMIT;
