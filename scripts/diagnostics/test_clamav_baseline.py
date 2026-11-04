#!/usr/bin/env python3
"""SQL integration checks in an owned, disposable, network-isolated PostgreSQL.

Requires Docker and the pinned postgres image locally; never pulls an image,
uses host database credentials, publishes ports or contacts Kubernetes.
Run: python3 scripts/diagnostics/test_clamav_baseline.py -v
"""

import copy
import datetime
import json
import os
import pathlib
import re
import shutil
import stat
import subprocess
import sys
import tempfile
import time
import unittest
import uuid


IMAGES = {
    "17": "postgres@sha256:31ba9c94a03ada9988f2bb493a22a75eef3a185884533e5cdbcd498ea835b08f",
    "16": "postgres@sha256:1a6ab3f5345eb6dbe04a1349529caabdb0ab09293a09590fad07b2246bfa4b54",
}
IMAGE = IMAGES[os.environ.get("CLAMAV_BASELINE_TEST_POSTGRES", "17")]
SQL = pathlib.Path(__file__).with_name("clamav-baseline.sql").read_text()
CAP = 10001
SCAN_TYPES = {"Seq Scan", "Index Scan", "Index Only Scan"}
AGE_BUCKETS = ("upload_age_under_1m", "upload_age_1m_to_10m", "upload_age_10m_to_1h",
               "upload_age_over_1h", "missing_uploaded_at", "future_uploaded_at")


def plan_nodes(node):
    yield node
    for child in node.get("Plans", []):
        yield from plan_nodes(child)


def bounded_nodes(plan):
    matches = [n for n in plan_nodes(plan) if n.get("Subplan Name") == "CTE bounded"]
    return list(plan_nodes(matches[0])) if len(matches) == 1 else []


def executed_rows(node):
    if not {"Actual Rows", "Actual Loops"} <= node.keys():
        return None
    if node["Actual Loops"] <= 0:
        return None
    return node["Actual Rows"] * node["Actual Loops"]


def scan_budget_status(limit, scan, required_index):
    returned, emitted = executed_rows(limit), executed_rows(scan)
    if None in (returned, emitted):
        return "indeterminate"
    removed = scan.get("Rows Removed by Filter", 0) + scan.get("Rows Removed by Index Recheck", 0)
    processed = emitted + removed * scan["Actual Loops"]
    if max(returned, processed) > CAP:
        return "inefficient"
    if required_index is not None and (
        scan.get("Index Name") != required_index or scan["Node Type"] == "Seq Scan"
    ):
        return "indeterminate"
    if (returned, emitted) != (CAP, CAP):
        return "indeterminate"
    return "efficient"


def bounded_plan_status(plan, required_index=None):
    """Validate this fixture's read path; not a general EXPLAIN cost estimator."""
    nodes = bounded_nodes(plan)
    kinds = {n.get("Node Type") for n in nodes}
    if kinds & {"Sort", "Incremental Sort", "Bitmap Heap Scan", "Bitmap Index Scan"}:
        return "inefficient"
    if kinds - (SCAN_TYPES | {"Limit", "Result", "Materialize"}):
        return "indeterminate"
    limits = [n for n in nodes if n.get("Node Type") == "Limit"]
    scans = [n for n in nodes if n.get("Node Type") in SCAN_TYPES]
    if len(limits) != 1 or len(scans) != 1:
        return "indeterminate"
    return scan_budget_status(limits[0], scans[0], required_index)


class PlanValidationTest(unittest.TestCase):
    def setUp(self):
        self.scan = {"Node Type": "Index Scan", "Index Name": "idx_attachments_scan_pending",
                     "Actual Rows": CAP, "Actual Loops": 1}
        self.bounded = {"Node Type": "Limit", "Subplan Name": "CTE bounded",
                        "Actual Rows": CAP, "Actual Loops": 1, "Plans": [self.scan]}

    def test_rejects_sort_bitmap_full_backlog_despite_limit(self):
        bad = copy.deepcopy(self.bounded)
        bad["Plans"] = [{"Node Type": "Sort", "Actual Rows": CAP, "Actual Loops": 1,
                         "Plans": [{"Node Type": "Bitmap Heap Scan", "Actual Rows": 20000,
                                    "Actual Loops": 1, "Plans": [{"Node Type": "Bitmap Index Scan",
                                    "Index Name": "idx_attachments_scan_pending",
                                    "Actual Rows": 20000, "Actual Loops": 1}]}]}]
        # The previous validator accepted exactly these two facts.
        self.assertEqual((bad["Node Type"], bad["Actual Rows"]), ("Limit", CAP))
        self.assertEqual(bounded_plan_status(bad, "idx_attachments_scan_pending"), "inefficient")

    def test_recurses_past_wrappers_without_equating_all_operator_counts(self):
        self.bounded["Plans"] = [{"Node Type": "Materialize", "Actual Rows": CAP,
                                  "Actual Loops": 1, "Plans": [self.scan]}]
        plan = {"Node Type": "Aggregate", "Actual Rows": 1, "Actual Loops": 1,
                "Plans": [{"Node Type": "Result", "Plans": [self.bounded]}]}
        self.assertEqual(bounded_plan_status(plan, "idx_attachments_scan_pending"), "efficient")

    def test_rejects_excess_read_work_from_loops_filters_or_full_scan(self):
        for change in ({"Actual Loops": 2}, {"Rows Removed by Filter": 9999},
                       {"Rows Removed by Index Recheck": 9999},
                       {"Node Type": "Seq Scan", "Actual Rows": 20000}):
            with self.subTest(change=change):
                plan = copy.deepcopy(self.bounded)
                plan["Plans"][0].update(change)
                self.assertEqual(bounded_plan_status(plan), "inefficient")

    def test_unproven_paths_are_indeterminate(self):
        missing_execution = copy.deepcopy(self.bounded)
        del missing_execution["Plans"][0]["Actual Loops"]
        unknown = copy.deepcopy(self.bounded)
        unknown["Plans"][0]["Node Type"] = "Custom Scan"
        missing_limit = copy.deepcopy(self.bounded)
        missing_limit["Node Type"] = "Result"
        short_limit = copy.deepcopy(self.bounded)
        short_limit["Actual Rows"] = CAP - 1
        duplicate_cte = {"Node Type": "Result", "Plans": [self.bounded, self.bounded]}
        for plan in ({}, missing_execution, unknown, missing_limit, short_limit, duplicate_cte):
            with self.subTest(plan=plan):
                self.assertEqual(bounded_plan_status(plan), "indeterminate")
        self.assertEqual(bounded_plan_status(self.bounded, "different_index"), "indeterminate")


class CollectionRunbookTest(unittest.TestCase):
    def setUp(self):
        root = pathlib.Path(self.enterContext(tempfile.TemporaryDirectory(prefix="nchat-baseline-test-")))
        self.output_root = root / "output"
        self.output_root.mkdir()
        tools = root / "bin"
        tools.mkdir()
        book = pathlib.Path(__file__).resolve().parents[2] / "docs/runbooks/clamav-baseline-1129.md"
        self.snippet = re.search(r"```bash\n(# Coleta privada SR-001:.*?)\n```", book.read_text(), re.S).group(1)
        mktemp = f"""#!{sys.executable}
import os, sys
args = sys.argv[1:]
failure = os.environ.get('MOCK_MKTEMP_FAILURE')
if failure == 'directory' or (failure == 'file' and args[0] != '-d'):
    sys.exit(1)
if args[0] == '-d':
    args[-1] = os.path.join(os.environ['MOCK_BASELINE_ROOT'], os.path.basename(args[-1]))
os.execv({shutil.which('mktemp')!r}, ['mktemp'] + args)
"""
        psql = f"""#!{sys.executable}
import json, os, sys
mode = os.environ.get('MOCK_PSQL_MODE', 'ok')
if mode == 'invalid':
    print('PRIVATE_INVALID_JSON')
    sys.exit(0)
sections = ['snapshot', 'catalog', 'table_sample', 'queue_sample', 'complete']
if mode == 'partial':
    sections = sections[:-1]
if mode == 'marker_only':
    sections = ['complete']
for section in sections:
    row = {{'section': section}}
    if section in ('snapshot', 'complete'):
        row['read_only'] = 'on'
    print(json.dumps(row))
if mode == 'failure':
    sys.stderr.write('PRIVATE_OPERATIONAL_ERROR')
    sys.exit(2)
"""
        for name, content in (("mktemp", mktemp), ("psql", psql)):
            path = tools / name
            path.write_text(content)
            path.chmod(0o700)
        self.environment = dict(os.environ, PATH=str(tools) + os.pathsep + os.environ["PATH"],
                                MOCK_BASELINE_ROOT=str(self.output_root))

    def collect(self, mode="ok", mktemp_failure=""):
        env = dict(self.environment, MOCK_PSQL_MODE=mode, MOCK_MKTEMP_FAILURE=mktemp_failure)
        return subprocess.run(["bash", "-c", self.snippet], env=env,
                              text=True, capture_output=True, timeout=10)

    def test_exclusive_private_files_do_not_reuse_legacy_output(self):
        legacy = self.output_root / "nchat-clamav-baseline.jsonl"
        legacy.write_text("PRIVATE_LEGACY")
        legacy.chmod(0o666)
        outputs = []
        for _ in range(2):
            result = self.collect()
            self.assertEqual(result.returncode, 0, result.stderr)
            path = pathlib.Path(result.stdout.strip().removeprefix("baseline_output="))
            self.assertTrue(path.is_relative_to(self.output_root))
            self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)
            self.assertEqual(stat.S_IMODE(path.parent.stat().st_mode), 0o700)
            self.assertNotEqual(path, legacy)
            self.assertNotIn("PRIVATE", result.stdout + result.stderr)
            outputs.append(path)
        self.assertNotEqual(outputs[0], outputs[1])
        self.assertEqual(legacy.read_text(), "PRIVATE_LEGACY")

    def test_mktemp_failures_leave_no_result(self):
        for failure in ("directory", "file"):
            with self.subTest(failure=failure):
                result = self.collect(mktemp_failure=failure)
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn("baseline_output=", result.stdout)
                self.assertEqual(list(self.output_root.iterdir()), [])

    def test_psql_or_integrity_failure_discards_partial_result(self):
        for mode in ("failure", "partial", "invalid", "marker_only"):
            with self.subTest(mode=mode):
                result = self.collect(mode)
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn("PRIVATE", result.stdout + result.stderr)
                self.assertNotIn("baseline_output=", result.stdout)
                self.assertEqual(list(self.output_root.iterdir()), [])


class BaselineSQLTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.container = "nchat-clamav-baseline-test-" + uuid.uuid4().hex[:12]
        subprocess.run(["docker", "image", "inspect", IMAGE], check=True, capture_output=True)
        # Register cleanup before startup so failures cannot leave this container.
        cls.addClassCleanup(subprocess.run, ["docker", "rm", "-f", cls.container],
                            capture_output=True, timeout=20)
        subprocess.run([
            "docker", "run", "--detach", "--rm", "--pull=never", "--network=none",
            "--name", cls.container, "--memory=256m", "--cpus=1", "--pids-limit=128",
            "--tmpfs", "/var/lib/postgresql/data:rw,size=128m",
            "-e", "POSTGRES_HOST_AUTH_METHOD=trust", "-e", "POSTGRES_DB=baseline_test",
            IMAGE,
        ], check=True, capture_output=True, timeout=20)
        cls.wait_ready()
        # Minimal projection of migrations/files 000001/000002/000005. This
        # tests diagnostic SQL, not the application's complete migration chain.
        cls.psql("""
            CREATE SCHEMA files;
            CREATE TABLE files.attachments (
                status text NOT NULL DEFAULT 'pending_upload', deleted_at timestamptz,
                scan_attempts smallint NOT NULL DEFAULT 0,
                scan_next_attempt_at timestamptz,
                created_at timestamptz NOT NULL DEFAULT now(), uploaded_at timestamptz,
                wrapped_dek bytea, kek_key_id text,
                original_filename text DEFAULT 'PRIVATE_FILENAME',
                failure_code text DEFAULT 'PRIVATE_FAILURE',
                CONSTRAINT attachments_status_check CHECK (status IN
                    ('pending_upload', 'pending_scan', 'clean', 'rejected', 'failed', 'deleted')),
                CONSTRAINT attachments_scan_attempts_check CHECK (scan_attempts >= 0),
                CONSTRAINT attachments_kek_key_id_length_check CHECK (
                    kek_key_id IS NULL OR char_length(kek_key_id) BETWEEN 1 AND 64),
                CONSTRAINT attachments_dek_binding_complete_check CHECK (
                    status IN ('pending_upload', 'failed')
                    OR (wrapped_dek IS NOT NULL AND kek_key_id IS NOT NULL))
            );
            CREATE INDEX idx_attachments_scan_pending ON files.attachments
                (scan_next_attempt_at ASC NULLS FIRST, created_at ASC)
                WHERE status = 'pending_scan';
            CREATE ROLE baseline_reader;
            GRANT USAGE ON SCHEMA files TO baseline_reader;
            GRANT SELECT (status, deleted_at, scan_attempts, scan_next_attempt_at,
                created_at, uploaded_at)
                ON files.attachments TO baseline_reader;
            CREATE ROLE baseline_incomplete_reader;
            GRANT USAGE ON SCHEMA files TO baseline_incomplete_reader;
            GRANT SELECT (status, deleted_at, scan_attempts, scan_next_attempt_at,
                uploaded_at)
                ON files.attachments TO baseline_incomplete_reader;
        """)

    @classmethod
    def wait_ready(cls):
        deadline = time.monotonic() + 20
        while time.monotonic() < deadline:
            ready = subprocess.run([
                "docker", "exec", cls.container, "pg_isready", "-h", "127.0.0.1", "-U", "postgres",
                "-d", "baseline_test",
            ], capture_output=True, timeout=5)
            if ready.returncode == 0:
                return
            time.sleep(0.1)  # Bounded readiness polling, not test synchronization.
        raise RuntimeError("isolated PostgreSQL did not become ready")

    @classmethod
    def psql(cls, query, check=True):
        return subprocess.run([
            "docker", "exec", "-i", cls.container, "psql", "-X", "-qAt",
            "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", "baseline_test",
        ], input=query, text=True, capture_output=True, check=check, timeout=15)

    def setUp(self):
        self.psql("TRUNCATE files.attachments")

    def report(self, sql=SQL):
        output = self.psql("SET ROLE baseline_reader;\n" + sql).stdout
        self.assertNotIn("PRIVATE", output)
        sections = [json.loads(line) for line in output.splitlines()]
        self.assertEqual([s["section"] for s in sections],
                         ["snapshot", "catalog", "table_sample", "queue_sample", "complete"])
        self.assertEqual(sections[0]["read_only"], "on")
        self.assertEqual(sections[-1]["read_only"], "on")
        self.assertEqual(sections[3]["key_completeness_basis"], "validated_schema_constraint")
        return {s["section"]: s for s in sections}

    def test_empty_queue_is_not_zero_latency(self):
        r = self.report()
        self.assertEqual(r["queue_sample"]["sampled_pending"], 0)
        self.assertIsNone(r["queue_sample"]["oldest_uploaded_age_seconds"])
        self.assertIsNone(r["queue_sample"]["oldest_created_age_seconds"])
        self.assertEqual(r["snapshot"]["latency_percentiles"], "unavailable")
        self.assertFalse(r["queue_sample"]["truncated"])
        indexes = {i["name"]: i for i in r["catalog"]["indexes"]}
        self.assertTrue(indexes["idx_attachments_scan_pending"]["valid"])
        self.assertEqual(r["catalog"]["estimated_table_rows"], -1)

    def test_states_eligibility_ages_and_attempts(self):
        self.psql("""
            INSERT INTO files.attachments
                (status, scan_attempts, scan_next_attempt_at, uploaded_at, wrapped_dek, kek_key_id)
            VALUES
                ('pending_scan', 0, NULL, now() - interval '30 seconds', '\\x01', 'PRIVATE_KEY'),
                ('pending_scan', 2, now() - interval '1 minute', now() - interval '5 minutes', '\\x01', 'PRIVATE_KEY'),
                ('pending_scan', 32000, now() + interval '1 hour', now() - interval '2 hours', '\\x01', 'PRIVATE_KEY'),
                ('pending_scan', 0, NULL, now() - interval '30 minutes', '\\x01', 'PRIVATE_KEY'),
                ('pending_scan', 0, NULL, NULL, '\\x01', 'PRIVATE_KEY'),
                ('pending_scan', 0, NULL, now() + interval '1 hour', '\\x01', 'PRIVATE_KEY'),
                ('clean', 1, NULL, now(), '\\x01', 'PRIVATE_KEY'),
                ('rejected', 1, NULL, now(), '\\x01', 'PRIVATE_KEY');
            UPDATE files.attachments SET created_at = '2020-01-01T00:00:00Z'
                WHERE uploaded_at < now() - interval '1 hour';
            UPDATE files.attachments SET created_at = now() + interval '1 hour'
                WHERE uploaded_at IS NULL;
            INSERT INTO files.attachments(status) VALUES ('pending_upload'), ('failed');
            INSERT INTO files.attachments(status, deleted_at, wrapped_dek, kek_key_id)
                VALUES ('deleted', now(), '\\x01', 'PRIVATE_KEY'),
                       ('pending_scan', now(), '\\x01', 'PRIVATE_KEY');
        """)
        r = self.report()
        q = r["queue_sample"]
        for key, value in {"sampled_pending": 6, "eligible": 5,
                           "deferred_lease_or_backoff": 1, "blocked_missing_keys": 0,
                           "attempts_sum": 32002, "attempt_counter_saturated": 1,
                           "retried_or_abandoned_due": 1, "missing_uploaded_at": 1,
                           "future_uploaded_at": 1, "upload_age_under_1m": 1,
                           "upload_age_1m_to_10m": 1, "upload_age_10m_to_1h": 1,
                           "upload_age_over_1h": 1}.items():
            with self.subTest(key=key):
                self.assertEqual(q[key], value)
        self.assertGreaterEqual(q["oldest_uploaded_age_seconds"], 7200)
        server_at = datetime.datetime.fromisoformat(r["snapshot"]["server_at"])
        oldest = datetime.datetime(2020, 1, 1, tzinfo=datetime.timezone.utc)
        # Both timestamps are PostgreSQL-sourced; 1 ms covers JSON float rounding.
        self.assertAlmostEqual(q["oldest_created_age_seconds"],
                               (server_at - oldest).total_seconds(), delta=0.001)
        table = r["table_sample"]
        counts = {"pending": 6, "clean": 1, "rejected": 1, "other_or_deleted": 4}
        self.assertEqual({k: table[k] for k in counts}, counts)
        self.assertEqual(table["sampled_rows"], 12)
        self.assertEqual(sum(table[k] for k in counts), table["sampled_rows"])
        self.assertEqual(sum(q[k] for k in AGE_BUCKETS), q["sampled_pending"])

    def test_future_created_at_is_excluded_from_age(self):
        self.psql("""INSERT INTO files.attachments(status, created_at, wrapped_dek, kek_key_id)
                     VALUES ('pending_scan', now() + interval '1 hour', '\\x01', 'PRIVATE_KEY')""")
        q = self.report()["queue_sample"]
        self.assertEqual(q["sampled_pending"], 1)
        self.assertIsNone(q["oldest_created_age_seconds"])

    def test_upload_age_boundaries_partition_using_fixed_postgres_clock(self):
        # Substitute only the database clock expression, not the bucket predicates.
        clock = "TIMESTAMPTZ '2026-01-01T12:00:00Z'"
        self.psql(f"""INSERT INTO files.attachments(status, uploaded_at, wrapped_dek, kek_key_id)
            SELECT 'pending_scan', {clock} - offset_seconds * interval '1 second',
                   '\\x01', 'PRIVATE_KEY'
            FROM (VALUES (NULL), (-1), (0), (59), (60), (599), (600),
                         (3599), (3600), (3601)) AS ages(offset_seconds)""")
        q = self.report(SQL.replace("now()", clock))["queue_sample"]
        self.assertEqual([q[k] for k in AGE_BUCKETS], [3, 2, 2, 1, 1, 1])
        self.assertEqual(sum(q[k] for k in AGE_BUCKETS), q["sampled_pending"])

    def test_untrusted_binding_contract_aborts_before_queue_sample(self):
        self.addCleanup(self.psql, "DROP SCHEMA anomalous CASCADE")
        self.psql("""
            CREATE SCHEMA anomalous;
            GRANT USAGE ON SCHEMA anomalous TO baseline_reader;
        """)
        definition = "CHECK (status IN ('pending_upload', 'failed') OR (wrapped_dek IS NOT NULL AND kek_key_id IS NOT NULL))"
        cases = {
            "absent": "",
            "not_valid": f"ADD CONSTRAINT attachments_dek_binding_complete_check {definition} NOT VALID",
            "wrong_predicate": "ADD CONSTRAINT attachments_dek_binding_complete_check CHECK (wrapped_dek IS NOT NULL OR kek_key_id IS NOT NULL)",
            "wrong_kind": "ADD CONSTRAINT attachments_dek_binding_complete_check UNIQUE (uploaded_at)",
            "wrong_type": f"ALTER COLUMN status TYPE varchar, ADD CONSTRAINT attachments_dek_binding_complete_check {definition}",
        }
        for name, alteration in cases.items():
            with self.subTest(contract=name):
                self.psql("""CREATE TABLE anomalous.attachments (LIKE files.attachments INCLUDING ALL);
                    ALTER TABLE anomalous.attachments DROP CONSTRAINT attachments_dek_binding_complete_check;
                    GRANT SELECT (status, deleted_at, scan_attempts, scan_next_attempt_at,
                                  created_at, uploaded_at) ON anomalous.attachments TO baseline_reader;""")
                if alteration:
                    self.psql("ALTER TABLE anomalous.attachments " + alteration)
                else:
                    self.psql("""INSERT INTO anomalous.attachments(status, wrapped_dek, kek_key_id) VALUES
                        ('pending_scan', '\\x01', 'PRIVATE_KEY'), ('pending_scan', NULL, NULL),
                        ('pending_scan', '\\x01', NULL), ('pending_scan', NULL, 'PRIVATE_KEY')""")
                result = self.psql("\\set VERBOSITY sqlstate\nSET ROLE baseline_reader;\n" +
                                   SQL.replace("files.attachments", "anomalous.attachments"), check=False)
                self.assertNotEqual(result.returncode, 0)
                self.assertRegex(result.stderr, r"\b55000\b")
                self.assertEqual([json.loads(line)["section"] for line in result.stdout.splitlines()],
                                 ["snapshot", "catalog", "table_sample"])
                self.psql("DROP TABLE anomalous.attachments")

    def test_minimal_role_cannot_read_either_key_column(self):
        for column in ("wrapped_dek", "kek_key_id"):
            with self.subTest(column=column):
                privileges = self.psql("SET ROLE baseline_reader; SELECT has_column_privilege("
                                       f"current_user, 'files.attachments', '{column}', 'SELECT')").stdout
                self.assertEqual(privileges.strip(), "f")
                denied = self.psql("\\set VERBOSITY sqlstate\nSET ROLE baseline_reader;\n"
                                   f"SELECT {column} FROM files.attachments", check=False)
                self.assertNotEqual(denied.returncode, 0)
                self.assertRegex(denied.stderr, r"\b42501\b")
        self.report()

    def test_fixture_constraints_reject_invalid_normal_rows(self):
        for columns, values in (
            ("status, wrapped_dek, kek_key_id", "'unknown', '\\x01', 'PRIVATE_KEY'"),
            ("status, scan_attempts", "'pending_upload', -1"),
            ("status", "'pending_scan'"),
        ):
            with self.subTest(values=values):
                result = self.psql("\\set VERBOSITY sqlstate\n"
                                   f"INSERT INTO files.attachments({columns}) VALUES ({values})",
                                   check=False)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("23514", result.stderr)

    def test_insufficient_privilege_fails_without_completion_marker(self):
        result = self.psql("\\set VERBOSITY sqlstate\nSET ROLE baseline_incomplete_reader;\n" + SQL,
                           check=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertRegex(result.stderr, r"\b42501\b")
        sections = [json.loads(line)["section"] for line in result.stdout.splitlines()]
        # Metadata and the first sample succeed; SELECT created_at then fails.
        self.assertEqual(sections, ["snapshot", "catalog", "table_sample"])
        self.assertNotIn("complete", sections)

    def test_truncation_is_explicit(self):
        self.psql("""INSERT INTO files.attachments(status, wrapped_dek, kek_key_id)
                     SELECT 'pending_scan', '\\x01', 'PRIVATE_KEY' FROM generate_series(1, 10001)""")
        r = self.report()
        self.assertEqual(r["table_sample"]["sampled_rows"], 10000)
        self.assertTrue(r["table_sample"]["truncated"])
        self.assertEqual(r["queue_sample"]["sampled_pending"], 10000)
        self.assertTrue(r["queue_sample"]["truncated"])
        self.assertEqual(r["queue_sample"]["eligible"], 10000)
        self.psql("""TRUNCATE files.attachments;
                     INSERT INTO files.attachments(status, wrapped_dek, kek_key_id)
                     SELECT 'pending_scan', '\\x01', 'PRIVATE_KEY' FROM generate_series(1, 10000)""")
        exact = self.report()
        self.assertFalse(exact["table_sample"]["truncated"])
        self.assertFalse(exact["queue_sample"]["truncated"])
        self.assertEqual(exact["queue_sample"]["sampled_pending"], 10000)
        self.assertEqual(exact["queue_sample"]["eligible"], 10000)

    def test_transaction_rejects_mutation_even_as_superuser(self):
        attempted_write = SQL.replace("ROLLBACK;", "DELETE FROM files.attachments;\nROLLBACK;")
        result = self.psql("\\set VERBOSITY sqlstate\n" + attempted_write, check=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("25006", result.stderr)  # read_only_sql_transaction

    def test_query_plans_bound_rows_before_aggregation(self):
        self.psql("""INSERT INTO files.attachments(status, wrapped_dek, kek_key_id)
                     SELECT 'pending_scan', '\\x01', 'PRIVATE_KEY' FROM generate_series(1, 20000);
                     ANALYZE files.attachments;""")
        queries = re.findall(r"WITH bounded AS MATERIALIZED.*?FROM sample;", SQL, re.S)
        self.assertEqual(len(queries), 2)
        for query in queries:
            # ANALYZE is confined to this owned disposable database, never dev.
            output = self.psql("EXPLAIN (ANALYZE, FORMAT JSON) " + query).stdout
            plan = json.loads(output)[0]["Plan"]
            required_index = "idx_attachments_scan_pending" if "'queue_sample'" in query else None
            self.assertEqual(bounded_plan_status(plan, required_index), "efficient", plan)

    def test_missing_schema_fails_without_completion_marker(self):
        result = self.psql(SQL.replace("files.attachments", "files.missing_attachments"), check=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn('"section" : "complete"', result.stdout)

    def test_statement_timeout_invalidates_collection(self):
        slow = SQL.replace("-- Catalog estimates", "SELECT pg_sleep(3);\n-- Catalog estimates")
        result = self.psql("\\set VERBOSITY sqlstate\n" + slow, check=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("57014", result.stderr)
        self.assertNotIn('"section" : "complete"', result.stdout)


if __name__ == "__main__":
    unittest.main()
