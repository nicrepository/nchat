BEGIN;

-- Expand only: N workers remain authoritative until an explicitly approved cutover.
CREATE TABLE files.scan_queue_policy (
 id smallint PRIMARY KEY CHECK (id=1),
 enabled boolean NOT NULL DEFAULT false,
 activated boolean NOT NULL DEFAULT false,
 approval_reference text,
 CHECK (NOT enabled OR (activated AND length(trim(approval_reference))>0 AND approval_reference IS NOT NULL))
);
INSERT INTO files.scan_queue_policy(id) VALUES (1);
ALTER TABLE files.attachments
 ADD COLUMN scan_claim_token uuid,
 ADD COLUMN scan_lease_until timestamptz,
 ADD COLUMN scan_last_error text,
 ADD COLUMN scan_failed_at timestamptz;

CREATE FUNCTION files.scan_queue_policy_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog,files,pg_temp AS $$
BEGIN
 IF OLD.activated AND (NOT NEW.enabled OR NOT NEW.activated) THEN
  RAISE EXCEPTION 'scan fencing cutover cannot be reversed' USING ERRCODE='NC004';
 END IF;
 IF NEW.enabled THEN NEW.activated := true; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER scan_queue_policy_guard BEFORE UPDATE ON files.scan_queue_policy
 FOR EACH ROW EXECUTE FUNCTION files.scan_queue_policy_guard();

CREATE FUNCTION files.require_scan_fencing() RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,files,pg_temp AS $$
DECLARE active boolean;
DECLARE was_activated boolean;
BEGIN
 SELECT enabled,activated INTO active,was_activated FROM files.scan_queue_policy WHERE id=1 FOR SHARE;
 IF active IS false AND was_activated IS false THEN
  RAISE EXCEPTION 'scan queue preparation standby' USING ERRCODE='NC006';
 END IF;
 IF active IS DISTINCT FROM true THEN
  RAISE EXCEPTION 'scan fencing inactive' USING ERRCODE='NC004';
 END IF;
 RETURN true;
END $$;

CREATE FUNCTION files.scan_fencing_enabled() RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,files,pg_temp AS $$
DECLARE active boolean;
BEGIN
 SELECT enabled INTO active FROM files.scan_queue_policy WHERE id=1 FOR SHARE;
 IF active IS NULL THEN RAISE EXCEPTION 'scan fencing policy missing' USING ERRCODE='NC004'; END IF;
 RETURN active;
END $$;

-- Invoker trigger: only the migration owner executes the narrow definer APIs.
-- No caller-controlled GUC can stand in for authority. Legacy proof API is gated below.
CREATE FUNCTION files.guard_scan_queue_write() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog,files,pg_temp AS $$
DECLARE active boolean;
DECLARE trusted boolean;
BEGIN
 active := files.scan_fencing_enabled();
 SELECT current_user::regrole = proowner INTO trusted FROM pg_catalog.pg_proc
  WHERE oid='files.require_scan_fencing()'::regprocedure;
 IF active IS DISTINCT FROM false AND NOT trusted THEN
  IF TG_OP='INSERT' AND ROW(NEW.scan_claim_token,NEW.scan_lease_until,NEW.scan_attempts,
      NEW.scan_last_error,NEW.scan_failed_at) IS DISTINCT FROM
      ROW(NULL::uuid,NULL::timestamptz,0::smallint,NULL::text,NULL::timestamptz) THEN
   RAISE EXCEPTION 'fenced scheduling API required' USING ERRCODE='NC004';
  END IF;
  IF TG_OP='UPDATE' THEN
   IF ROW(NEW.id,NEW.workspace_id,NEW.scan_claim_token,NEW.scan_lease_until,NEW.scan_attempts,
          NEW.scan_last_error,NEW.scan_failed_at)
      IS DISTINCT FROM ROW(OLD.id,OLD.workspace_id,OLD.scan_claim_token,OLD.scan_lease_until,OLD.scan_attempts,
          OLD.scan_last_error,OLD.scan_failed_at)
      OR (OLD.deleted_at IS NOT NULL AND NEW.deleted_at IS DISTINCT FROM OLD.deleted_at)
      OR (NEW.status IS DISTINCT FROM OLD.status AND NOT
          (OLD.status='pending_upload' AND NEW.status IN ('pending_scan','failed'))) THEN
    RAISE EXCEPTION 'fenced lifecycle API required' USING ERRCODE='NC004';
   END IF;
   -- Cancellation/expiry may clear the schedule while hiding the row. They
   -- cannot change identity, status, lease or token, or resurrect a deletion.
   IF NEW.deleted_at IS NOT NULL AND NEW.status IS NOT DISTINCT FROM OLD.status
      AND (NEW.scan_next_attempt_at IS NOT DISTINCT FROM OLD.scan_next_attempt_at
           OR NEW.scan_next_attempt_at IS NULL) THEN RETURN NEW; END IF;
  END IF;
  IF NEW.status IN ('clean','rejected') OR NEW.scan_claim_token IS NOT NULL THEN
   RAISE EXCEPTION 'fenced scan API required' USING ERRCODE='NC004';
  END IF;
  IF TG_OP='UPDATE' AND OLD.status='pending_scan' AND
    ROW(NEW.scan_attempts,NEW.scan_next_attempt_at,NEW.scan_lease_until,NEW.scan_claim_token,
        NEW.scan_last_error,NEW.scan_failed_at)
    IS DISTINCT FROM ROW(OLD.scan_attempts,OLD.scan_next_attempt_at,OLD.scan_lease_until,OLD.scan_claim_token,
        OLD.scan_last_error,OLD.scan_failed_at) THEN
   RAISE EXCEPTION 'fenced scheduling API required' USING ERRCODE='NC004';
  END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER attachments_scan_queue_insert BEFORE INSERT ON files.attachments
 FOR EACH ROW EXECUTE FUNCTION files.guard_scan_queue_write();
CREATE TRIGGER attachments_scan_queue_update BEFORE UPDATE OF id,workspace_id,deleted_at,status,scan_attempts,
 scan_next_attempt_at,scan_claim_token,scan_lease_until,scan_last_error,scan_failed_at,
 scan_signature_generation,scan_engine_version,scan_signature_version,scan_database_time,
 scan_loaded_at,scan_completed_at ON files.attachments
 FOR EACH ROW EXECUTE FUNCTION files.guard_scan_queue_write();

-- Keep the B implementation intact, private, behind two explicitly versioned contracts.
CREATE FUNCTION files.persist_proven_scan_internal(
 p_id uuid, p_workspace uuid, p_generation text, p_engine text, p_version bigint,
 p_database_time timestamptz, p_loaded_at timestamptz, p_completed_at timestamptz,
 p_format_version integer
) RETURNS TABLE(result_status text, result_preview text, result_outcome text)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, files, pg_temp AS $$
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
REVOKE ALL ON FUNCTION files.persist_proven_scan_internal FROM PUBLIC;
CREATE OR REPLACE FUNCTION files.persist_proven_scan(
 p_id uuid,p_workspace uuid,p_generation text,p_engine text,p_version bigint,
 p_database_time timestamptz,p_loaded_at timestamptz,p_completed_at timestamptz,p_format_version integer
) RETURNS TABLE(result_status text,result_preview text,result_outcome text)
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,files,pg_temp AS $$
DECLARE active boolean;
BEGIN
 SELECT enabled INTO active FROM files.scan_queue_policy WHERE id=1 FOR SHARE;
 IF active IS DISTINCT FROM false THEN
  RAISE EXCEPTION 'fenced scan API required' USING ERRCODE='NC004';
 END IF;
 RETURN QUERY SELECT * FROM files.persist_proven_scan_internal(p_id,p_workspace,p_generation,p_engine,
  p_version,p_database_time,p_loaded_at,p_completed_at,p_format_version);
END $$;

CREATE FUNCTION files.claim_scans(p_limit integer,p_lease_seconds double precision)
RETURNS SETOF files.attachments
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,files,pg_temp AS $$
BEGIN
 PERFORM files.require_scan_fencing();
 IF p_limit IS DISTINCT FROM 1 OR p_lease_seconds IS NULL OR p_lease_seconds<1 OR p_lease_seconds>3630 THEN
  RAISE EXCEPTION 'invalid claim parameters' USING ERRCODE='22023';
 END IF;
 RETURN QUERY
 WITH due AS (
  SELECT a.id FROM files.attachments a
  WHERE a.status='pending_scan' AND a.deleted_at IS NULL
   AND a.wrapped_dek IS NOT NULL AND a.kek_key_id IS NOT NULL
   AND (a.scan_next_attempt_at IS NULL OR a.scan_next_attempt_at<=now())
   AND (a.scan_lease_until IS NULL OR a.scan_lease_until<=clock_timestamp())
  ORDER BY a.scan_next_attempt_at NULLS FIRST,a.created_at
  LIMIT p_limit FOR UPDATE SKIP LOCKED
 )
 UPDATE files.attachments a SET scan_attempts=LEAST(a.scan_attempts+1,32000), scan_claim_token = gen_random_uuid(),
  scan_lease_until=clock_timestamp()+p_lease_seconds*interval '1 second',updated_at=now()
 FROM due WHERE a.id=due.id RETURNING a.*;
END $$;

-- Linearization is the authority check after acquiring the row lock. A reclaimer
-- cannot obtain authority until this transaction commits or rolls back.
CREATE FUNCTION files.lock_scan_attempt(p_id uuid,p_workspace uuid,p_token uuid)
RETURNS files.attachments
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,files,pg_temp AS $$
DECLARE target files.attachments%ROWTYPE;
BEGIN
 PERFORM files.require_scan_fencing();
 SELECT * INTO target FROM files.attachments WHERE id=p_id AND workspace_id=p_workspace
  AND deleted_at IS NULL FOR UPDATE;
 IF target.id IS NULL OR p_token IS NULL OR target.scan_claim_token IS DISTINCT FROM p_token
    OR target.scan_lease_until IS NULL OR target.scan_lease_until<=clock_timestamp() THEN
  RAISE EXCEPTION 'scan attempt superseded' USING ERRCODE='NC005';
 END IF;
 RETURN target;
END $$;

CREATE FUNCTION files.persist_scan_v2(
 p_id uuid,p_workspace uuid,p_token uuid,p_verdict text,p_format_version integer,
 p_generation text,p_engine text,p_version bigint,p_database_time timestamptz,
 p_loaded_at timestamptz,p_completed_at timestamptz
) RETURNS TABLE(result_status text,result_preview text,result_outcome text)
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,files,pg_temp AS $$
DECLARE target files.attachments%ROWTYPE;
BEGIN
 target := files.lock_scan_attempt(p_id,p_workspace,p_token);
 IF p_verdict IS NULL OR p_verdict NOT IN ('clean','rejected') THEN
  RAISE EXCEPTION 'invalid scan verdict' USING ERRCODE='22023';
 END IF;
 IF target.status=p_verdict THEN
  RETURN QUERY SELECT target.status,target.preview_status,'already_recorded'::text;
  RETURN;
 END IF;
 IF target.status<>'pending_scan' THEN
  RAISE EXCEPTION 'scan attempt superseded' USING ERRCODE='NC005';
 END IF;
 IF p_verdict='clean' AND p_generation IS NOT NULL THEN
  RETURN QUERY SELECT * FROM files.persist_proven_scan_internal(p_id,p_workspace,p_generation,p_engine,
   p_version,p_database_time,p_loaded_at,p_completed_at,p_format_version);
  RETURN;
 END IF;
 IF p_verdict='clean' THEN
  PERFORM 1 FROM files.signature_policy WHERE id=1 FOR SHARE;
  IF EXISTS(SELECT 1 FROM files.signature_policy WHERE id=1 AND enabled) THEN
   RAISE EXCEPTION 'fresh scan evidence required' USING ERRCODE='23514';
  END IF;
  RETURN QUERY UPDATE files.attachments a SET status='clean',format_scan_version=p_format_version,
   scan_next_attempt_at=NULL,updated_at=now()
   WHERE a.id=p_id AND a.format_policy_version<=p_format_version
   RETURNING a.status,a.preview_status,'applied'::text;
  IF NOT FOUND THEN
   RETURN QUERY SELECT target.status,target.preview_status,'conflict'::text;
  END IF;
  RETURN;
 END IF;
 RETURN QUERY
 WITH rejected AS (
  UPDATE files.attachments a SET status='rejected',
   preview_status=CASE WHEN a.preview_status='pending' THEN 'unsupported' ELSE a.preview_status END,
   preview_next_attempt_at=CASE WHEN a.preview_status='pending' THEN NULL ELSE a.preview_next_attempt_at END,
   scan_next_attempt_at=NULL,updated_at=now() WHERE a.id=p_id
  RETURNING a.*
 ), orphaned AS (
  INSERT INTO files.object_cleanup_jobs(object_key)
  SELECT 'nchat/previews/' || preview_object_id::text FROM rejected
   WHERE preview_object_id IS NOT NULL AND preview_status='ready'
  ON CONFLICT(object_key) DO NOTHING
 ) SELECT rejected.status,rejected.preview_status,'applied'::text FROM rejected;
END $$;

CREATE FUNCTION files.retry_scan(p_id uuid,p_workspace uuid,p_token uuid,p_delay_seconds double precision,p_error text)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,files,pg_temp AS $$
DECLARE target files.attachments%ROWTYPE;
BEGIN
 target := files.lock_scan_attempt(p_id,p_workspace,p_token);
 IF target.status<>'pending_scan' THEN RETURN false; END IF;
 IF p_delay_seconds IS NULL OR p_delay_seconds<10 OR p_delay_seconds>2640
    OR p_error IS NULL OR p_error NOT IN ('storage_read','decrypt','unsupported_policy','clamd_unavailable',
      'clamd_protocol','clamd_stream_too_large','timeout','canceled','database','signature_policy') THEN
  RAISE EXCEPTION 'invalid retry parameters' USING ERRCODE='22023';
 END IF;
 UPDATE files.attachments SET scan_next_attempt_at=clock_timestamp()+p_delay_seconds*interval '1 second',
  scan_lease_until=NULL,scan_claim_token = NULL,scan_last_error=p_error,scan_failed_at=clock_timestamp(),
  updated_at=now() WHERE id=p_id;
 RETURN true;
END $$;

REVOKE ALL ON files.scan_queue_policy FROM PUBLIC;
REVOKE ALL ON FUNCTION files.scan_queue_policy_guard(),files.guard_scan_queue_write(),
 files.require_scan_fencing(),files.scan_fencing_enabled(),files.claim_scans(integer,double precision),files.lock_scan_attempt(uuid,uuid,uuid),
 files.persist_proven_scan,files.persist_scan_v2,files.retry_scan FROM PUBLIC;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='nchat_app') THEN
  REVOKE ALL ON files.scan_queue_policy FROM nchat_app;
  GRANT SELECT ON files.scan_queue_policy TO nchat_app;
  REVOKE ALL ON FUNCTION files.persist_proven_scan_internal FROM nchat_app;
  GRANT EXECUTE ON FUNCTION files.scan_fencing_enabled(),files.persist_proven_scan,files.claim_scans(integer,double precision),
   files.persist_scan_v2,files.retry_scan TO nchat_app;
 END IF;
END $$;
COMMIT;
