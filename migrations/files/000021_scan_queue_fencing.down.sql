BEGIN;
LOCK TABLE files.scan_queue_policy IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM files.scan_queue_policy WHERE id=1 AND NOT enabled AND NOT activated) THEN
  RAISE EXCEPTION 'activated scan fencing cannot be removed';
 END IF;
END $$;
DROP TRIGGER attachments_scan_queue_insert ON files.attachments;
DROP TRIGGER attachments_scan_queue_update ON files.attachments;
DROP FUNCTION files.guard_scan_queue_write();
DROP FUNCTION files.scan_fencing_enabled();
DROP FUNCTION files.retry_scan;
DROP FUNCTION files.persist_scan_v2;
DROP FUNCTION files.lock_scan_attempt;
DROP FUNCTION files.claim_scans;
DROP FUNCTION files.require_scan_fencing;
CREATE OR REPLACE FUNCTION files.persist_proven_scan(
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
DROP FUNCTION files.persist_proven_scan_internal;
DROP TABLE files.scan_queue_policy;
DROP FUNCTION files.scan_queue_policy_guard;
ALTER TABLE files.attachments DROP COLUMN scan_claim_token,DROP COLUMN scan_lease_until,
 DROP COLUMN scan_last_error,DROP COLUMN scan_failed_at;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='nchat_app') THEN
  GRANT EXECUTE ON FUNCTION files.persist_proven_scan TO nchat_app;
 END IF;
END $$;
COMMIT;
