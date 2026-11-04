-- Issue #1129 / delivery A. Run with psql -X -qAt -v ON_ERROR_STOP=1.
-- No claims, row locks, EXPLAIN ANALYZE, user identifiers or raw failure codes.
\set ON_ERROR_STOP on
BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY;
SET LOCAL statement_timeout = '2s';
SET LOCAL lock_timeout = '250ms';
SET LOCAL idle_in_transaction_session_timeout = '5s';
SET LOCAL work_mem = '4MB';
SET LOCAL max_parallel_workers_per_gather = 0;
SET LOCAL search_path = pg_catalog;

SELECT json_build_object(
    'section', 'snapshot', 'server_at', now(),
    'read_only', current_setting('transaction_read_only'),
    'sample_cap', 10000,
    'processing_vs_backoff', 'unavailable',
    'failure_categories', 'not_persisted',
    'latency_percentiles', 'unavailable'
);

-- Catalog estimates and the known indexes, never an exact table count.
SELECT json_build_object(
    'section', 'catalog',
    'estimated_table_rows', c.reltuples,
    'indexes', COALESCE((
        SELECT json_agg(json_build_object(
            'name', i.relname, 'valid', x.indisvalid,
            'definition', pg_get_indexdef(i.oid)
        ) ORDER BY i.relname)
        FROM pg_index x JOIN pg_class i ON i.oid = x.indexrelid
        WHERE x.indrelid = c.oid AND i.relname IN (
            'idx_attachments_scan_pending', 'idx_attachments_pending',
            'idx_attachments_channel', 'idx_attachments_conversation'
        )
    ), '[]'::json)
)
FROM pg_class c WHERE c.oid = 'files.attachments'::regclass;

-- Physical prefix, NOT a random/representative sample or a global status count.
-- LIMIT is inside MATERIALIZED, before aggregation or filtering. The extra row
-- detects truncation. Timeouts also bound sparse/dead-tuple traversal.
WITH bounded AS MATERIALIZED (
    SELECT status, deleted_at FROM files.attachments LIMIT 10001
), sample AS MATERIALIZED (
    SELECT * FROM bounded LIMIT 10000
)
SELECT json_build_object(
    'section', 'table_sample',
    'sampled_rows', count(*),
    'truncated', (SELECT count(*) > 10000 FROM bounded),
    'pending', count(*) FILTER (WHERE status = 'pending_scan' AND deleted_at IS NULL),
    'clean', count(*) FILTER (WHERE status = 'clean' AND deleted_at IS NULL),
    'rejected', count(*) FILTER (WHERE status = 'rejected' AND deleted_at IS NULL),
    'other_or_deleted', count(*) FILTER (
        WHERE deleted_at IS NOT NULL OR status NOT IN ('pending_scan', 'clean', 'rejected')
    )
) FROM sample;

-- table_sample already holds AccessShareLock until ROLLBACK, preventing a
-- concurrent DROP/ALTER of this contract between verification and queue read.
-- Strict server-deparsed form, tested on PG16/17: equivalent unrecognized
-- definitions fail closed rather than guessing at CHECK implication.
DO $contract$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint c
        WHERE c.conrelid = 'files.attachments'::regclass
          AND c.conname = 'attachments_dek_binding_complete_check'
          AND c.contype = 'c' AND c.convalidated AND c.conislocal
          AND pg_get_constraintdef(c.oid, false) =
              'CHECK (((status = ANY (ARRAY[''pending_upload''::text, ''failed''::text])) OR ((wrapped_dek IS NOT NULL) AND (kek_key_id IS NOT NULL))))'
          AND 3 = (
              SELECT count(*) FROM pg_attribute a
              WHERE a.attrelid = c.conrelid AND NOT a.attisdropped
                AND (a.attname, a.atttypid) IN (
                    ('status', 'text'::regtype), ('wrapped_dek', 'bytea'::regtype),
                    ('kek_key_id', 'text'::regtype)
                )
          )
    ) THEN
        RAISE EXCEPTION 'diagnostic schema contract unavailable' USING ERRCODE = '55000';
    END IF;
END
$contract$;

-- Same scheduling order as the worker. Key completeness is inferred from the
-- validated CHECK above; no SELECT privilege on either key column is needed.
WITH bounded AS MATERIALIZED (
    SELECT scan_attempts, scan_next_attempt_at, created_at, uploaded_at
    FROM files.attachments
    WHERE status = 'pending_scan' AND deleted_at IS NULL
    ORDER BY scan_next_attempt_at ASC NULLS FIRST, created_at ASC
    LIMIT 10001
), sample AS MATERIALIZED (
    SELECT * FROM bounded LIMIT 10000
)
SELECT json_build_object(
    'section', 'queue_sample',
    'sampled_pending', count(*),
    'truncated', (SELECT count(*) > 10000 FROM bounded),
    'key_completeness_basis', 'validated_schema_constraint',
    'eligible', count(*) FILTER (
        WHERE scan_next_attempt_at IS NULL OR scan_next_attempt_at <= now()
    ),
    'deferred_lease_or_backoff', count(*) FILTER (
        WHERE scan_next_attempt_at > now()
    ),
    'blocked_missing_keys', 0,
    'attempts_sum', COALESCE(sum(scan_attempts), 0),
    'attempt_counter_saturated', count(*) FILTER (WHERE scan_attempts >= 32000),
    'retried_or_abandoned_due', count(*) FILTER (
        WHERE scan_attempts > 0
          AND (scan_next_attempt_at IS NULL OR scan_next_attempt_at <= now())
    ),
    'oldest_created_age_seconds', max(EXTRACT(EPOCH FROM now() - created_at))
        FILTER (WHERE created_at <= now()),
    'oldest_uploaded_age_seconds', max(EXTRACT(EPOCH FROM now() - uploaded_at))
        FILTER (WHERE uploaded_at <= now()),
    'upload_age_under_1m', count(*) FILTER (
        WHERE uploaded_at BETWEEN now() - interval '1 minute' AND now()
    ),
    'upload_age_1m_to_10m', count(*) FILTER (
        WHERE uploaded_at < now() - interval '1 minute'
          AND uploaded_at >= now() - interval '10 minutes'
    ),
    'upload_age_10m_to_1h', count(*) FILTER (
        WHERE uploaded_at < now() - interval '10 minutes'
          AND uploaded_at >= now() - interval '1 hour'
    ),
    'upload_age_over_1h', count(*) FILTER (WHERE uploaded_at < now() - interval '1 hour'),
    'missing_uploaded_at', count(*) FILTER (WHERE uploaded_at IS NULL),
    'future_uploaded_at', count(*) FILTER (WHERE uploaded_at > now())
) FROM sample;

-- A missing completion marker or nonzero psql exit invalidates the collection.
SELECT json_build_object('section', 'complete', 'read_only', current_setting('transaction_read_only'));
ROLLBACK;
