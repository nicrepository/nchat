BEGIN;

-- Dormant until a reviewed policy is enabled through GitOps/migration governance.
-- Old binaries remain usable with the gate off; with it on, missing proof fails.
CREATE TABLE files.signature_policy (
 id smallint PRIMARY KEY CHECK (id = 1),
 enabled boolean NOT NULL DEFAULT false,
 max_age_seconds integer CHECK (max_age_seconds > 0 AND max_age_seconds <= 31536000),
 approval_reference text,
 CHECK (NOT enabled OR (max_age_seconds IS NOT NULL AND approval_reference IS NOT NULL AND length(trim(approval_reference)) > 0))
);
INSERT INTO files.signature_policy (id) VALUES (1);
ALTER TABLE files.attachments
 ADD COLUMN scan_signature_generation text,
 ADD COLUMN scan_engine_version text,
 ADD COLUMN scan_signature_version bigint,
 ADD COLUMN scan_database_time timestamptz,
 ADD COLUMN scan_loaded_at timestamptz,
 ADD COLUMN scan_completed_at timestamptz;

-- New workers call this in their verdict statement; old workers never call it.
CREATE FUNCTION files.require_active_signature_policy() RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, files AS $$
DECLARE policy files.signature_policy%ROWTYPE;
BEGIN
 SELECT * INTO policy FROM files.signature_policy WHERE id = 1 FOR SHARE;
 IF NOT FOUND THEN
  RAISE EXCEPTION 'signature policy missing' USING ERRCODE = 'NC002';
 END IF;
 IF NOT policy.enabled THEN
  RAISE EXCEPTION 'signature policy inactive' USING ERRCODE = 'NC001';
 END IF;
 IF policy.max_age_seconds IS NULL OR policy.max_age_seconds <= 0
    OR policy.max_age_seconds > 31536000 OR policy.approval_reference IS NULL
    OR length(trim(policy.approval_reference)) = 0 THEN
  RAISE EXCEPTION 'signature policy invalid' USING ERRCODE = 'NC002';
 END IF;
 RETURN true;
END;
$$;
REVOKE ALL ON files.signature_policy FROM PUBLIC;
REVOKE ALL ON FUNCTION files.require_active_signature_policy() FROM PUBLIC;
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'nchat_app') THEN
  REVOKE ALL ON files.signature_policy FROM nchat_app;
  GRANT SELECT ON files.signature_policy TO nchat_app;
  GRANT EXECUTE ON FUNCTION files.require_active_signature_policy() TO nchat_app;
 END IF;
END $$;

CREATE FUNCTION files.require_fresh_scan() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, files AS $$
DECLARE policy files.signature_policy%ROWTYPE;
DECLARE checked_at timestamptz;
DECLARE proof_changed boolean := true;
BEGIN
 IF TG_OP = 'UPDATE' THEN
  proof_changed := ROW(NEW.scan_signature_generation, NEW.scan_engine_version,
   NEW.scan_signature_version, NEW.scan_database_time, NEW.scan_loaded_at, NEW.scan_completed_at)
   IS DISTINCT FROM ROW(OLD.scan_signature_generation, OLD.scan_engine_version,
   OLD.scan_signature_version, OLD.scan_database_time, OLD.scan_loaded_at, OLD.scan_completed_at);
  IF TG_WHEN = 'AFTER' AND OLD.status = 'clean' AND NOT proof_changed THEN RETURN NEW; END IF;
 END IF;
 IF NEW.status <> 'clean' THEN
  IF TG_OP = 'UPDATE' AND proof_changed THEN
   RAISE EXCEPTION 'evidence requires a completed clean scan' USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
 END IF;
 -- Serializes policy changes with the approval transaction, including N/N+1.
 SELECT * INTO policy FROM files.signature_policy WHERE id = 1 FOR SHARE;
 IF NOT FOUND THEN RAISE EXCEPTION 'signature policy missing' USING ERRCODE = 'NC002'; END IF;
 IF NOT policy.enabled THEN
  -- A partial rollback must not make existing forensic evidence mutable.
  IF TG_OP = 'UPDATE' AND OLD.status = 'clean' AND proof_changed THEN
   RAISE EXCEPTION 'signature policy inactive' USING ERRCODE = 'NC001';
  END IF;
  RETURN NEW;
 END IF;
 PERFORM files.require_active_signature_policy();
 checked_at := clock_timestamp();
 IF NEW.scan_signature_generation IS NULL
    OR NEW.scan_signature_generation !~ '^[0-9a-f]{64}$'
    OR NEW.scan_engine_version IS DISTINCT FROM '1.4.6'
    OR NEW.scan_signature_version IS NULL OR NEW.scan_signature_version <= 0
    OR NEW.scan_database_time IS NULL OR NEW.scan_database_time > checked_at
    OR NEW.scan_loaded_at IS NULL OR NEW.scan_loaded_at < NEW.scan_database_time
    OR NEW.scan_completed_at IS NULL OR NEW.scan_completed_at < NEW.scan_loaded_at
    OR NEW.scan_completed_at > checked_at
    OR checked_at >= NEW.scan_database_time + policy.max_age_seconds * interval '1 second'
 THEN RAISE EXCEPTION 'fresh scan evidence required' USING ERRCODE = '23514'; END IF;
 IF TG_WHEN = 'BEFORE' AND TG_OP = 'UPDATE' AND OLD.status = 'clean'
    AND (NEW.scan_completed_at IS NULL OR NEW.scan_completed_at <= OLD.scan_completed_at)
 THEN RAISE EXCEPTION 'new scan evidence required' USING ERRCODE = 'NC003'; END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER attachments_fresh_scan
 BEFORE INSERT OR UPDATE OF status, scan_signature_generation, scan_engine_version,
 scan_signature_version, scan_database_time, scan_loaded_at, scan_completed_at ON files.attachments
 FOR EACH ROW EXECUTE FUNCTION files.require_fresh_scan();

-- Recheck after any transaction work or waits, immediately before commit.
CREATE CONSTRAINT TRIGGER attachments_fresh_scan_commit
 AFTER INSERT OR UPDATE ON files.attachments
 DEFERRABLE INITIALLY DEFERRED
 FOR EACH ROW EXECUTE FUNCTION files.require_fresh_scan();

-- The runtime cannot write forensic columns directly. This is the sole new
-- approval operation; old writers retain non-evidence column grants while off.
CREATE FUNCTION files.persist_proven_scan(
 p_id uuid, p_workspace uuid, p_generation text, p_engine text, p_version bigint,
 p_database_time timestamptz, p_loaded_at timestamptz, p_completed_at timestamptz,
 p_format_version integer
) RETURNS TABLE(result_status text, result_preview text, result_outcome text)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, files AS $$
BEGIN
 RETURN QUERY

 WITH policy AS MATERIALIZED (SELECT files.require_active_signature_policy() AS ok),
 target AS MATERIALIZED (
  SELECT * FROM files.attachments WHERE id=p_id AND workspace_id=p_workspace
   AND deleted_at IS NULL FOR UPDATE
 ), changed AS (
  UPDATE files.attachments a SET status='clean', format_scan_version=p_format_version,
   scan_next_attempt_at=NULL, updated_at=now(),
   scan_signature_generation=p_generation, scan_engine_version=p_engine, scan_signature_version=p_version,
   scan_database_time=p_database_time, scan_loaded_at=p_loaded_at, scan_completed_at=p_completed_at
  FROM target t, policy p WHERE p.ok AND a.id=t.id
   AND t.status IN ('pending_scan','clean') AND t.format_policy_version<=p_format_version
   AND (t.status <> 'clean' OR t.scan_completed_at IS DISTINCT FROM p_completed_at)
  RETURNING a.status, a.preview_status
 )
 SELECT changed.status, changed.preview_status,
 CASE WHEN changed.status IS NOT NULL THEN CASE WHEN target.status='clean' THEN 'rescanned' ELSE 'applied' END
  WHEN target.id IS NULL THEN 'missing'
  WHEN target.status NOT IN ('pending_scan','clean') THEN 'superseded'
  WHEN target.status='clean' AND ROW(target.scan_signature_generation,
   target.scan_engine_version,target.scan_signature_version,target.scan_database_time,
   target.scan_loaded_at,target.scan_completed_at) IS NOT DISTINCT FROM ROW(p_generation::text,
   p_engine::text,p_version::bigint,p_database_time::timestamptz,p_loaded_at::timestamptz,p_completed_at::timestamptz) THEN 'already_recorded'
  ELSE 'conflict' END
 FROM policy LEFT JOIN target ON true LEFT JOIN changed ON true WHERE policy.ok;
END;
$$;
REVOKE ALL ON FUNCTION files.persist_proven_scan FROM PUBLIC;

-- Table-level grants override column restrictions, so revoke those first.
DO $$
DECLARE writable text;
BEGIN
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'nchat_app') THEN
  SELECT string_agg(format('%I',attname),',' ORDER BY attnum) INTO writable
  FROM pg_attribute WHERE attrelid='files.attachments'::regclass AND attnum>0 AND NOT attisdropped
   AND attname NOT IN ('scan_signature_generation','scan_engine_version','scan_signature_version',
                      'scan_database_time','scan_loaded_at','scan_completed_at');
  REVOKE INSERT,UPDATE ON files.attachments FROM nchat_app;
  EXECUTE format('GRANT INSERT (%s), UPDATE (%s) ON files.attachments TO nchat_app',writable,writable);
  GRANT EXECUTE ON FUNCTION files.persist_proven_scan TO nchat_app;
 END IF;
END $$;

COMMIT;
