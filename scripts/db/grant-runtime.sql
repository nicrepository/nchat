\set ON_ERROR_STOP on

REVOKE ALL ON SCHEMA auth, chat, files FROM nchat_app;
GRANT USAGE ON SCHEMA auth, chat, files TO nchat_app;

GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA auth, chat, files TO nchat_app;
GRANT USAGE, SELECT, UPDATE ON ALL SEQUENCES IN SCHEMA auth, chat, files TO nchat_app;

ALTER DEFAULT PRIVILEGES FOR ROLE nchat_migrator IN SCHEMA auth
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO nchat_app;
ALTER DEFAULT PRIVILEGES FOR ROLE nchat_migrator IN SCHEMA chat
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO nchat_app;
ALTER DEFAULT PRIVILEGES FOR ROLE nchat_migrator IN SCHEMA files
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO nchat_app;
ALTER DEFAULT PRIVILEGES FOR ROLE nchat_migrator IN SCHEMA auth
  GRANT USAGE, SELECT, UPDATE ON SEQUENCES TO nchat_app;
ALTER DEFAULT PRIVILEGES FOR ROLE nchat_migrator IN SCHEMA chat
  GRANT USAGE, SELECT, UPDATE ON SEQUENCES TO nchat_app;
ALTER DEFAULT PRIVILEGES FOR ROLE nchat_migrator IN SCHEMA files
  GRANT USAGE, SELECT, UPDATE ON SEQUENCES TO nchat_app;

-- The signature gate is operator-owned, even after broad runtime grants above.
-- Compatible with databases that have not expanded to files/000020 yet.
DO $$ BEGIN
 IF to_regclass('files.signature_policy') IS NOT NULL THEN
  REVOKE ALL ON files.signature_policy FROM nchat_app;
  GRANT SELECT ON files.signature_policy TO nchat_app;
  GRANT EXECUTE ON FUNCTION files.require_active_signature_policy() TO nchat_app;
 END IF;
END $$;


-- Table-level grants override column restrictions, so revoke those first.
DO $$
DECLARE writable text;
BEGIN
 IF to_regclass('files.signature_policy') IS NOT NULL THEN
  SELECT string_agg(format('%I',attname),',' ORDER BY attnum) INTO writable
  FROM pg_attribute WHERE attrelid='files.attachments'::regclass AND attnum>0 AND NOT attisdropped
   AND attname NOT IN ('scan_signature_generation','scan_engine_version','scan_signature_version',
                      'scan_database_time','scan_loaded_at','scan_completed_at');
  REVOKE INSERT,UPDATE ON files.attachments FROM nchat_app;
  EXECUTE format('GRANT INSERT (%s), UPDATE (%s) ON files.attachments TO nchat_app',writable,writable);
  GRANT EXECUTE ON FUNCTION files.persist_proven_scan TO nchat_app;
 END IF;
END $$;
