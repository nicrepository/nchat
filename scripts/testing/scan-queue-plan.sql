-- Only invoked inside the runner's owned disposable database. Data rolls back.
BEGIN;
INSERT INTO files.attachments(id,workspace_id,uploader_id,destination_kind,channel_id,
 original_filename,declared_mime,storage_provider,storage_object_key,envelope_version,
 dek_wrap_version,wrapped_dek,kek_key_id,status,scan_next_attempt_at,scan_lease_until)
SELECT gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),'channel',gen_random_uuid(),
 'synthetic.txt','text/plain','seaweedfs','plan/'||g,1,2,'\x01','test-only','pending_scan',
 now()-g*interval '1 second',CASE WHEN g%10=0 THEN now()+interval '330 seconds' END
FROM generate_series(1,10000)g;
ANALYZE files.attachments;
EXPLAIN(ANALYZE,BUFFERS)
SELECT a.id FROM files.attachments a
WHERE a.status='pending_scan' AND a.deleted_at IS NULL
 AND a.wrapped_dek IS NOT NULL AND a.kek_key_id IS NOT NULL
 AND (a.scan_next_attempt_at IS NULL OR a.scan_next_attempt_at<=now())
 AND (a.scan_lease_until IS NULL OR a.scan_lease_until<=clock_timestamp())
ORDER BY a.scan_next_attempt_at NULLS FIRST,a.created_at
LIMIT 1 FOR UPDATE SKIP LOCKED;
ROLLBACK;
