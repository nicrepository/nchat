BEGIN;
LOCK TABLE files.scan_queue_policy IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM files.scan_queue_policy WHERE id=1 AND NOT enabled AND NOT activated) THEN
  RAISE EXCEPTION 'activated scan capacity cannot be removed' USING ERRCODE='NC004';
 END IF;
END $$;
DROP TRIGGER attachments_terminal_scan_capacity ON files.attachments;
DROP FUNCTION files.release_terminal_scan_capacity();
CREATE OR REPLACE FUNCTION files.claim_scans(p_limit integer,p_lease_seconds double precision)
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

DROP TABLE files.scan_capacity_reservations;
DROP TABLE files.scan_capacity_policy;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='nchat_app') THEN
  GRANT EXECUTE ON FUNCTION files.claim_scans(integer,double precision) TO nchat_app;
 END IF;
END $$;
COMMIT;
