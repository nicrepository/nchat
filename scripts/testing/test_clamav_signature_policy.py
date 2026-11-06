#!/usr/bin/env python3
"""Exercise the actual migration in a disposable network-none PostgreSQL."""
import os
from pathlib import Path
import subprocess
import time
import unittest
import uuid

ROOT = Path(__file__).resolve().parents[2]
IMAGES = {
    "16": "postgres@sha256:1a6ab3f5345eb6dbe04a1349529caabdb0ab09293a09590fad07b2246bfa4b54",
    "17": "postgres@sha256:31ba9c94a03ada9988f2bb493a22a75eef3a185884533e5cdbcd498ea835b08f",
}


class SignaturePolicyTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.container = "nchat-signature-policy-test-" + uuid.uuid4().hex[:12]
        image = IMAGES[os.environ.get("CLAMAV_POLICY_TEST_POSTGRES", "17")]
        subprocess.run(["docker", "image", "inspect", image], check=True, capture_output=True)
        cls.addClassCleanup(subprocess.run, ["docker", "rm", "-f", cls.container],
                            check=False, capture_output=True)
        subprocess.run(["docker", "run", "--detach", "--pull=never", "--name", cls.container,
                        "--network", "none", "--memory", "512m", "--cpus", "1", "--pids-limit", "128",
                        "--tmpfs", "/var/lib/postgresql/data:rw,nosuid,size=256m",
                        "-e", "POSTGRES_HOST_AUTH_METHOD=trust", image], check=True, capture_output=True)
        deadline = time.monotonic() + 40
        while time.monotonic() < deadline:
            ready = subprocess.run(["docker", "exec", cls.container, "pg_isready", "-h", "127.0.0.1",
                                    "-U", "postgres"], capture_output=True)
            if ready.returncode == 0:
                subprocess.run(["docker", "exec", cls.container, "psql", "-X", "-U", "postgres", "-v", "ON_ERROR_STOP=1", "-c",
                                "CREATE ROLE nchat_app; CREATE ROLE nchat_migrator; CREATE SCHEMA auth; CREATE SCHEMA chat;"],
                               check=True, capture_output=True)
                return
            time.sleep(0.1)
        raise RuntimeError("isolated PostgreSQL did not become ready")

    def sql(self, sql, success=True):
        result = subprocess.run(["docker", "exec", "-i", self.container, "psql", "-X", "-qAt",
                                 "-U", "postgres", "-v", "ON_ERROR_STOP=1"],
                                input=sql, text=True, capture_output=True, timeout=20)
        if success:
            self.assertEqual(result.returncode, 0, result.stderr)
        else:
            self.assertNotEqual(result.returncode, 0, "invalid approval committed")
        return result

    def setUp(self):
        self.sql("DROP SCHEMA IF EXISTS files CASCADE; CREATE SCHEMA files; "
                 "ALTER DEFAULT PRIVILEGES IN SCHEMA files GRANT SELECT,INSERT,UPDATE,DELETE ON TABLES TO nchat_app; "
                 "CREATE TABLE files.attachments (id uuid PRIMARY KEY, status text, "
                 "workspace_id uuid NOT NULL DEFAULT '00000000-0000-0000-0000-0000000000f0', "
                 "preview_status text DEFAULT 'pending', deleted_at timestamptz, "
                 "format_policy_version integer DEFAULT 0, format_scan_version integer DEFAULT 0, "
                 "scan_next_attempt_at timestamptz, updated_at timestamptz DEFAULT now()); " +
                 (ROOT / "migrations/files/000020_signature_freshness.up.sql").read_text())
        self.sql("INSERT INTO files.attachments(id,status) VALUES ('00000000-0000-0000-0000-000000000001', 'pending_scan');")

    def enable(self):
        self.sql("UPDATE files.signature_policy SET max_age_seconds=7200, "
                 "approval_reference='isolated-test-only', enabled=true WHERE id=1;")

    def proof(self, database="clock_timestamp() - interval '1 hour'", generation="'" + "a" * 64 + "'",
              engine="'1.4.6'"):
        return ("UPDATE files.attachments SET status='clean', scan_signature_generation=" + generation +
                ", scan_engine_version=" + engine + ", scan_signature_version=28085, scan_database_time=" +
                database + ", scan_loaded_at=clock_timestamp()-interval '1 minute', "
                "scan_completed_at=clock_timestamp() WHERE id='00000000-0000-0000-0000-000000000001';")

    def test_dormant_policy_preserves_old_binary(self):
        self.sql("UPDATE files.attachments SET status='clean' WHERE id='00000000-0000-0000-0000-000000000001';")
        self.assertEqual(self.sql("SELECT status FROM files.attachments;").stdout.strip(), "clean")

    def test_missing_policy_approval_is_rejected(self):
        self.sql("UPDATE files.signature_policy SET enabled=true;", success=False)
        self.sql("UPDATE files.signature_policy SET max_age_seconds=0;", success=False)

    def test_old_writer_cannot_approve_with_gate_on(self):
        self.enable()
        self.sql("UPDATE files.attachments SET status='clean' WHERE id='00000000-0000-0000-0000-000000000001';", success=False)
        self.assertEqual(self.sql("SELECT status FROM files.attachments;").stdout.strip(), "pending_scan")
        self.sql("INSERT INTO files.attachments (id,status) VALUES ('00000000-0000-0000-0000-000000000002','clean');", success=False)

    def test_valid_proof_and_old_writer_replay(self):
        self.enable()
        self.sql(self.proof())
        self.sql("UPDATE files.attachments SET status='clean' WHERE id='00000000-0000-0000-0000-000000000001';", success=False)
        self.sql("UPDATE files.attachments SET status='rejected' WHERE id='00000000-0000-0000-0000-000000000001';")

    def test_invalid_expired_or_unknown_proofs(self):
        self.enable()
        for options in ({"database": "clock_timestamp()-interval '2 hours'"},
                        {"database": "clock_timestamp()+interval '1 second'"},
                        {"generation": "NULL"}, {"generation": "'../etc'"},
                        {"engine": "'1.3.0'"}):
            with self.subTest(options=options):
                self.sql(self.proof(**options), success=False)
        self.assertEqual(self.sql("SELECT status FROM files.attachments;").stdout.strip(), "pending_scan")

    def test_expiration_while_waiting_for_write(self):
        self.enable()
        self.sql("UPDATE files.signature_policy SET max_age_seconds=1;")
        # Proof was valid at scan completion, expires before the actual UPDATE.
        sql = ("BEGIN; CREATE TEMP TABLE proof_time AS SELECT clock_timestamp() AS completed; "
               "SELECT pg_sleep(1.05); UPDATE files.attachments SET status='clean', "
               "scan_signature_generation=repeat('a',64), scan_engine_version='1.4.6', "
               "scan_signature_version=28085, scan_database_time=(SELECT completed FROM proof_time), "
               "scan_loaded_at=(SELECT completed FROM proof_time), "
               "scan_completed_at=(SELECT completed FROM proof_time); COMMIT;")
        self.sql(sql, success=False)
        self.assertEqual(self.sql("SELECT status FROM files.attachments;").stdout.strip(), "pending_scan")

    def test_expiration_after_update_before_commit(self):
        self.enable()
        self.sql("UPDATE files.signature_policy SET max_age_seconds=1;")
        statement = self.proof(database="clock_timestamp() - interval '0.1 seconds'")
        # Use a load time consistent with the deliberately recent database.
        statement = statement.replace("clock_timestamp()-interval '1 minute'", "clock_timestamp()")
        self.sql("BEGIN; " + statement + " SELECT pg_sleep(1.05); COMMIT;", success=False)
        self.assertEqual(self.sql("SELECT status FROM files.attachments;").stdout.strip(), "pending_scan")

    def test_rollback_guard_and_reversible_additive_schema(self):
        down = (ROOT / "migrations/files/000020_signature_freshness.down.sql").read_text()
        self.enable()
        self.sql("BEGIN; " + down + " COMMIT;", success=False)
        self.sql("UPDATE files.signature_policy SET enabled=false;")
        self.sql("BEGIN; " + down + " COMMIT;")
        self.assertEqual(self.sql("SELECT status FROM files.attachments;").stdout.strip(), "pending_scan")

    def test_new_worker_policy_states_have_specific_sqlstate(self):
        for state in ("disabled", "enabled", "missing", "invalid"):
            with self.subTest(state=state):
                self.setUp()
                if state != "disabled":
                    self.enable()
                if state == "missing":
                    self.sql("DELETE FROM files.signature_policy;")
                if state == "invalid":
                    constraints = self.sql("SELECT conname FROM pg_constraint WHERE conrelid='files.signature_policy'::regclass AND contype='c';").stdout.splitlines()
                    for name in constraints:
                        self.sql('ALTER TABLE files.signature_policy DROP CONSTRAINT "' + name + '";')
                    self.sql("UPDATE files.signature_policy SET max_age_seconds=NULL;")
                result = self.sql("\\set VERBOSITY verbose\nSELECT files.require_active_signature_policy();", success=state == "enabled")
                if state != "enabled":
                    self.assertIn("NC001" if state == "disabled" else "NC002", result.stderr)

    def test_evidence_only_updates_cannot_rewrite_clean(self):
        self.enable()
        self.sql(self.proof())
        changes = {"scan_signature_generation": "repeat('b',64)", "scan_engine_version": "'1.4.6'",
                   "scan_signature_version": "28086", "scan_database_time": "clock_timestamp()-interval '30 minutes'",
                   "scan_loaded_at": "clock_timestamp()-interval '30 seconds'"}
        for column, value in changes.items():
            with self.subTest(column=column):
                # Even a no-op assignment of a protected field cannot pretend to be a new scan.
                self.sql("UPDATE files.attachments SET " + column + "=" + value + " WHERE id='00000000-0000-0000-0000-000000000001';", success=False)
        self.sql("UPDATE files.attachments SET scan_completed_at=NULL WHERE id='00000000-0000-0000-0000-000000000001';", success=False)
        self.sql("UPDATE files.attachments SET scan_completed_at=scan_completed_at-interval '1 second' WHERE id='00000000-0000-0000-0000-000000000001';", success=False)
        self.sql("ALTER TABLE files.attachments ADD COLUMN unrelated text;")
        self.sql("UPDATE files.attachments SET unrelated='metadata' WHERE id='00000000-0000-0000-0000-000000000001';")
        self.sql(self.proof())  # A new completed scan with fresh, fully validated evidence.

    def test_partial_rollback_does_not_unlock_forensic_evidence(self):
        self.enable()
        self.sql(self.proof())
        self.sql("UPDATE files.signature_policy SET enabled=false;")
        self.sql("UPDATE files.attachments SET scan_signature_generation=repeat('b',64) WHERE id='00000000-0000-0000-0000-000000000001';", success=False)
        self.sql("UPDATE files.attachments SET status='clean' WHERE id='00000000-0000-0000-0000-000000000001';")  # N writer unchanged evidence.
        self.enable()
        self.sql(self.proof())

    def test_runtime_grants_cannot_disable_policy(self):
        runtime = (ROOT / "scripts/db/grant-runtime.sql").read_text()
        self.sql(runtime)
        self.sql("SET ROLE nchat_app; UPDATE files.signature_policy SET enabled=false;", success=False)
        self.sql("SET ROLE nchat_app; DELETE FROM files.signature_policy;", success=False)
        self.sql("SET ROLE nchat_app; TRUNCATE files.signature_policy;", success=False)
        self.enable()
        self.sql("SET ROLE nchat_app; SELECT files.require_active_signature_policy();")
        self.sql("SET ROLE nchat_app; " + self.proof(), success=False)
        for column in ("scan_signature_generation", "scan_engine_version", "scan_signature_version",
                       "scan_database_time", "scan_loaded_at", "scan_completed_at"):
            self.sql("SET ROLE nchat_app; UPDATE files.attachments SET " + column + "=" + column + ";", success=False)
        self.sql("SET ROLE nchat_app; UPDATE files.attachments SET scan_completed_at=clock_timestamp();", success=False)
        self.sql("SET ROLE nchat_app; SELECT * FROM files.persist_proven_scan("
                 "'00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-0000000000f0',"
                 "repeat('a',64),'1.4.6',28085,clock_timestamp()-interval '1 hour',"
                 "clock_timestamp()-interval '1 minute',clock_timestamp(),1);")
        self.sql("SET ROLE nchat_app; UPDATE files.attachments SET updated_at=clock_timestamp();")
        self.sql("SET ROLE nchat_app; UPDATE files.attachments SET status='clean';", success=False)

    def test_expired_clean_metadata_is_still_updatable(self):
        self.enable()
        self.sql(self.proof())
        self.sql("UPDATE files.signature_policy SET max_age_seconds=1;")
        self.sql("ALTER TABLE files.attachments ADD COLUMN unrelated text;")
        self.sql("UPDATE files.attachments SET unrelated='metadata' WHERE id='00000000-0000-0000-0000-000000000001';")
        self.sql("UPDATE files.attachments SET scan_signature_version=28086 WHERE id='00000000-0000-0000-0000-000000000001';", success=False)

    def test_down_restores_legacy_writer_and_new_writer_fails(self):
        self.sql((ROOT / "scripts/db/grant-runtime.sql").read_text())
        self.sql((ROOT / "migrations/files/000020_signature_freshness.down.sql").read_text())
        self.sql("SET ROLE nchat_app; UPDATE files.attachments SET status='clean';")
        self.sql("SET ROLE nchat_app; SELECT * FROM files.persist_proven_scan("
                 "'00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-0000000000f0',"
                 "repeat('a',64),'1.4.6',28085,clock_timestamp(),clock_timestamp(),clock_timestamp(),1);", success=False)


    def test_full_rescan_and_exact_repeat_are_distinct(self):
        self.sql((ROOT / "scripts/db/grant-runtime.sql").read_text())
        self.enable()
        self.sql(self.proof())
        query = ("SET ROLE nchat_app; SELECT result_outcome FROM files.attachments a CROSS JOIN LATERAL "
                 "files.persist_proven_scan(a.id,a.workspace_id,a.scan_signature_generation,"
                 "a.scan_engine_version,a.scan_signature_version,a.scan_database_time,a.scan_loaded_at,"
                 "a.scan_completed_at,1) WHERE a.id='00000000-0000-0000-0000-000000000001';")
        self.assertEqual(self.sql(query).stdout.strip(), "already_recorded")
        rescan = query.replace("a.scan_completed_at,1)", "clock_timestamp(),1)")
        self.assertEqual(self.sql(rescan).stdout.strip(), "rescanned")


    def test_down_refuses_unknown_policy_state(self):
        down = (ROOT / "migrations/files/000020_signature_freshness.down.sql").read_text()
        self.sql("DELETE FROM files.signature_policy;")
        self.sql(down, success=False)
        self.sql("INSERT INTO files.signature_policy(id) VALUES(1);")
        self.sql(down)



if __name__ == "__main__":
    unittest.main()
