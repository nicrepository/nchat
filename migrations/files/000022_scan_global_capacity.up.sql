BEGIN;

-- Drain in-flight C claims before installing admission. Never activate C here.
LOCK TABLE files.scan_queue_policy IN ACCESS EXCLUSIVE MODE;
LOCK TABLE files.attachments IN SHARE ROW EXCLUSIVE MODE;
CREATE TABLE files.scan_capacity_policy (
 id smallint PRIMARY KEY CHECK (id=1),
 max_scans smallint NOT NULL DEFAULT 1 CHECK (max_scans BETWEEN 1 AND 4),
 admit_after timestamptz NOT NULL
);
-- Existing C workers may have lost their token through retry: no safe early reuse.
-- 3630 is C's maximum lease; 420 is clamd MaxScanTime; 30 is safety margin.
INSERT INTO files.scan_capacity_policy(id,admit_after)
 SELECT 1,clock_timestamp()+CASE WHEN activated THEN interval '4080 seconds' ELSE interval '0' END
 FROM files.scan_queue_policy WHERE id=1;
CREATE TABLE files.scan_capacity_reservations (
 attachment_id uuid PRIMARY KEY,
 attempt_id uuid NOT NULL UNIQUE,
 reserved_until timestamptz NOT NULL
);
-- No FK: deleting/canceling an attachment cannot stop an orphaned daemon scan.
CREATE OR REPLACE FUNCTION files.claim_scans(p_limit integer,p_lease_seconds double precision)
RETURNS SETOF files.attachments
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,files,pg_temp AS $$
DECLARE capacity integer;
DECLARE ready_at timestamptz;
DECLARE claimed files.attachments%ROWTYPE;
BEGIN
 PERFORM files.require_scan_fencing();
 IF p_limit IS DISTINCT FROM 1 OR p_lease_seconds IS NULL OR p_lease_seconds<1 OR p_lease_seconds>3630 THEN
  RAISE EXCEPTION 'invalid claim parameters' USING ERRCODE='22023';
 END IF;
 -- Serialize admission across every replica; scans never hold this lock.
 UPDATE files.scan_capacity_policy SET max_scans=max_scans WHERE id=1
 RETURNING max_scans,admit_after INTO capacity,ready_at;
 IF capacity IS NULL THEN
  RAISE EXCEPTION 'scan capacity policy missing' USING ERRCODE='NC004';
 END IF;
 IF ready_at>clock_timestamp() THEN RETURN; END IF;
 DELETE FROM files.scan_capacity_reservations WHERE reserved_until<=clock_timestamp();
 IF (SELECT count(*) FROM files.scan_capacity_reservations)>=capacity THEN RETURN; END IF;
 WITH due AS (
  SELECT a.id FROM files.attachments a
  WHERE a.status='pending_scan' AND a.deleted_at IS NULL
   AND a.wrapped_dek IS NOT NULL AND a.kek_key_id IS NOT NULL
   AND (a.scan_next_attempt_at IS NULL OR a.scan_next_attempt_at<=now())
   AND NOT EXISTS(SELECT 1 FROM files.scan_capacity_reservations r WHERE r.attachment_id=a.id)
   AND (a.scan_lease_until IS NULL OR a.scan_lease_until<=clock_timestamp())
  ORDER BY a.scan_next_attempt_at NULLS FIRST,a.created_at
  LIMIT p_limit FOR UPDATE SKIP LOCKED
 )
 UPDATE files.attachments a SET scan_attempts=LEAST(a.scan_attempts+1,32000), scan_claim_token = gen_random_uuid(),
  scan_lease_until=clock_timestamp()+p_lease_seconds*interval '1 second',updated_at=now()
 FROM due WHERE a.id=due.id RETURNING a.* INTO claimed;
 IF claimed.id IS NOT NULL THEN
  INSERT INTO files.scan_capacity_reservations(attachment_id,attempt_id,reserved_until)
  VALUES(claimed.id,claimed.scan_claim_token,claimed.scan_lease_until+interval '450 seconds');
  RETURN NEXT claimed;
 END IF;
END $$;


-- Only a fenced terminal transition proves work finished (or was rejected before
-- INSTREAM). Retry, deletion, cancellation and expired leases retain capacity.
CREATE FUNCTION files.release_terminal_scan_capacity() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,files,pg_temp AS $$
BEGIN
 DELETE FROM files.scan_capacity_reservations
 WHERE attachment_id=NEW.id AND attempt_id=NEW.scan_claim_token;
 RETURN NEW;
END $$;
CREATE TRIGGER attachments_terminal_scan_capacity AFTER UPDATE OF status ON files.attachments
 FOR EACH ROW WHEN (OLD.status='pending_scan' AND NEW.status IN ('clean','rejected'))
 EXECUTE FUNCTION files.release_terminal_scan_capacity();

REVOKE ALL ON files.scan_capacity_policy,files.scan_capacity_reservations FROM PUBLIC;
REVOKE ALL ON FUNCTION files.claim_scans(integer,double precision),files.release_terminal_scan_capacity() FROM PUBLIC;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='nchat_app') THEN
  REVOKE ALL ON files.scan_capacity_policy,files.scan_capacity_reservations FROM nchat_app;
  GRANT SELECT ON files.scan_capacity_policy,files.scan_capacity_reservations TO nchat_app;
  REVOKE ALL ON FUNCTION files.release_terminal_scan_capacity() FROM nchat_app;
  GRANT EXECUTE ON FUNCTION files.claim_scans(integer,double precision) TO nchat_app;
 END IF;
END $$;
COMMIT;
