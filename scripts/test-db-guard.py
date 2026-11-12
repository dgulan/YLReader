#!/usr/bin/env python3
"""Offline tests for scripts/check-db-target.py (ADR-013 SAFE-01, option S3).

Everything here is static: no database client, no network, no SQL, no shell
evaluation. The guard module is loaded by path because its filename is kebab-case.
"""

from __future__ import annotations

import contextlib
import importlib.util
import io
import os
import re
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parent.parent
GUARD_PATH = ROOT / "scripts" / "check-db-target.py"
MAKEFILE = ROOT / "Makefile"

_spec = importlib.util.spec_from_file_location("check_db_target", GUARD_PATH)
guard = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(guard)

LOCAL = "postgres://u:p@127.0.0.1:5432/"


def migrate_env(url):
    return {"DATABASE_URL": url}


def setup_env(maint, test):
    return {"DATABASE_URL": maint, "TEST_DATABASE_URL": test}


class TestDeny(unittest.TestCase):
    def deny(self, fn, env, reason):
        with self.assertRaises(guard.Deny) as ctx:
            fn(env)
        self.assertEqual(ctx.exception.reason, reason)

    def test_t1_missing_database_url(self):
        self.deny(guard.check_migrate, {}, "MISSING_DATABASE_URL")

    def test_t2_non_postgres_scheme(self):
        self.deny(guard.check_migrate, migrate_env("mysql://127.0.0.1:5432/yreader_test"), "INVALID_SCHEME_DATABASE_URL")

    def test_t3_missing_dbname(self):
        self.deny(guard.check_migrate, migrate_env("postgres://127.0.0.1:5432/"), "INVALID_DBNAME_DATABASE_URL")

    def test_t4_invalid_port(self):
        self.deny(guard.check_migrate, migrate_env("postgres://127.0.0.1:99999/yreader_test"), "INVALID_PORT_DATABASE_URL")

    def test_t5_migrate_production_db(self):
        self.deny(guard.check_migrate, migrate_env(LOCAL + "yreader"), "DB_NOT_ALLOWED")

    def test_t6_migrate_remote_ip(self):
        self.deny(guard.check_migrate, migrate_env("postgres://10.0.0.5:5432/yreader_test"), "HOST_NOT_ALLOWED_DATABASE_URL")

    def test_t7_test_db_name_not_allowed(self):
        self.deny(
            guard.check_setup_test_db,
            setup_env("postgres://u:p@127.0.0.1:5432/postgres", "postgres://u:p@127.0.0.1:5432/yreader_other"),
            "TEST_DB_NOT_ALLOWED",
        )

    def test_t8_host_mismatch(self):
        self.deny(
            guard.check_setup_test_db,
            setup_env("postgres://u:p@127.0.0.1:5432/postgres", "postgres://u:p@localhost:5432/yreader_test"),
            "HOST_PORT_MISMATCH",
        )

    def test_t9_port_mismatch(self):
        self.deny(
            guard.check_setup_test_db,
            setup_env("postgres://u:p@127.0.0.1:5432/postgres", "postgres://u:p@127.0.0.1:5433/yreader_test"),
            "HOST_PORT_MISMATCH",
        )

    def test_t10_setup_remote_host(self):
        self.deny(
            guard.check_setup_test_db,
            setup_env("postgres://u:p@127.0.0.1:5432/postgres", "postgres://u:p@192.168.1.10:5432/yreader_test"),
            "HOST_NOT_ALLOWED_TEST_DATABASE_URL",
        )

    def test_t11_maintenance_points_at_test_db(self):
        self.deny(
            guard.check_setup_test_db,
            setup_env("postgres://u:p@127.0.0.1:5432/yreader_test", "postgres://u:p@127.0.0.1:5432/yreader_test"),
            "MAINT_DB_NOT_ALLOWED",
        )

    def test_t12_conflicting_query_param(self):
        self.deny(
            guard.check_migrate,
            migrate_env("postgres://127.0.0.1:5432/yreader_test?host=/var/run/postgresql"),
            "CONFLICTING_PARAM_DATABASE_URL",
        )

    def test_t12b_service_param(self):
        self.deny(
            guard.check_migrate,
            migrate_env("postgres://127.0.0.1:5432/yreader_test?service=prod"),
            "CONFLICTING_PARAM_DATABASE_URL",
        )

    def test_t13_multi_host(self):
        self.deny(
            guard.check_migrate,
            migrate_env("postgres://127.0.0.1:5432,127.0.0.1:5433/yreader_test"),
            "MULTI_HOST_DATABASE_URL",
        )

    def test_t13b_unix_socket_form(self):
        self.deny(guard.check_migrate, migrate_env("postgres:///yreader_test"), "MISSING_HOST_DATABASE_URL")

    def test_uppercase_isolated_name_denied(self):
        self.deny(guard.check_migrate, migrate_env(LOCAL + "yreader_a1_iso_ABC"), "DB_NOT_ALLOWED")

    def test_percent_encoded_dbname_denied(self):
        self.deny(guard.check_migrate, migrate_env(LOCAL + "yreader%5Ftest"), "INVALID_DBNAME_DATABASE_URL")


class TestAllow(unittest.TestCase):
    def test_t15_migrate_local_test_db(self):
        target = guard.check_migrate(migrate_env(LOCAL + "yreader_test"))
        self.assertEqual((target["host"], target["port"], target["dbname"]), ("127.0.0.1", 5432, "yreader_test"))

    def test_t16_migrate_isolated_db(self):
        target = guard.check_migrate(migrate_env(LOCAL + "yreader_a1_iso_abc123"))
        self.assertEqual(target["dbname"], "yreader_a1_iso_abc123")

    def test_t17_setup_test_db_same_instance(self):
        maint, test = guard.check_setup_test_db(
            setup_env("postgres://u:p@127.0.0.1:5432/postgres", "postgres://u:p@127.0.0.1:5432/yreader_test")
        )
        self.assertEqual(maint["dbname"], "postgres")
        self.assertEqual(test["dbname"], "yreader_test")

    def test_safe_params_allowed(self):
        target = guard.check_migrate(migrate_env(LOCAL + "yreader_test?sslmode=disable&connect_timeout=5"))
        self.assertEqual(target["dbname"], "yreader_test")


class TestSanitization(unittest.TestCase):
    SECRET = "s3cr3t_pw_9f"

    def test_t14_no_credential_leak(self):
        env = {"DATABASE_URL": f"postgres://owner:{self.SECRET}@10.9.9.9:5432/yreader"}
        buf = io.StringIO()
        with mock.patch.dict(os.environ, env, clear=True), contextlib.redirect_stdout(buf):
            code = guard.main(["--mode", "migrate"])
        out = buf.getvalue()
        self.assertEqual(code, 1)
        self.assertIn("DB_GUARD=DENY", out)
        self.assertNotIn(self.SECRET, out)
        self.assertNotIn("postgres://", out)
        self.assertNotIn("10.9.9.9", out)

    def test_t14b_allow_output_has_no_credentials(self):
        env = {"DATABASE_URL": f"postgres://owner:{self.SECRET}@127.0.0.1:5432/yreader_test"}
        buf = io.StringIO()
        with mock.patch.dict(os.environ, env, clear=True), contextlib.redirect_stdout(buf):
            code = guard.main(["--mode", "migrate"])
        out = buf.getvalue()
        self.assertEqual(code, 0)
        self.assertIn("DB_GUARD=ALLOW", out)
        self.assertNotIn(self.SECRET, out)


class TestMakefileWiring(unittest.TestCase):
    """Static proof that the guard is a prerequisite (runs before any psql)."""

    def setUp(self):
        self.text = MAKEFILE.read_text(encoding="utf-8")
        self.targets = {}
        current = None
        for line in self.text.splitlines():
            if re.match(r"^[A-Za-z0-9_.-]+:", line):
                current = line.split(":", 1)[0]
                self.targets.setdefault(current, []).append(line)
            elif current and (line.startswith("\t") or line.strip() == ""):
                self.targets[current].append(line)

    def test_migrate_requires_guard(self):
        self.assertTrue(any("db-guard-migrate" in ln for ln in self.targets["migrate"]))

    def test_setup_test_db_requires_guard(self):
        self.assertTrue(any("db-guard-setup-test-db" in ln for ln in self.targets["setup-test-db"]))

    def test_guard_target_invokes_python_checker(self):
        self.assertIn("DB_GUARD := $(PYTHON) scripts/check-db-target.py", self.text)
        body = "\n".join(self.targets["db-guard-migrate"])
        self.assertIn("$(DB_GUARD)", body)
        self.assertIn("--mode migrate", body)
        body = "\n".join(self.targets["db-guard-setup-test-db"])
        self.assertIn("--mode setup-test-db", body)

    def test_no_eval_or_dynamic_execution(self):
        self.assertNotIn("eval ", self.text)
        self.assertNotIn("$$(", self.text.replace("$$($(GOFMT) -l .)", ""))

    def test_psql_only_after_guard_prerequisite(self):
        migrate = "\n".join(self.targets["migrate"])
        self.assertIn("$(PSQL)", migrate)
        guard_body = "\n".join(self.targets["db-guard-migrate"])
        self.assertNotIn("$(PSQL)", guard_body)

    def test_schema_has_no_executable_chapter_cache_drop(self):
        sql = (ROOT / "docs" / "schema.sql").read_text(encoding="utf-8")
        executable = [
            ln for ln in sql.splitlines()
            if re.match(r"^\s*DROP\s+TABLE\s+(IF\s+EXISTS\s+)?(public\.)?chapter_cache\s*;", ln, re.IGNORECASE)
        ]
        self.assertEqual(executable, [])
        self.assertIn("Physical removal requires a separately authorized A2 migration.", sql)


class TestGuardExitCodes(unittest.TestCase):
    def test_deny_exit_code(self):
        buf = io.StringIO()
        with mock.patch.dict(os.environ, {"DATABASE_URL": "postgres://127.0.0.1:5432/yreader"}, clear=True), contextlib.redirect_stdout(buf):
            self.assertEqual(guard.main(["--mode", "migrate"]), 1)
        self.assertIn("REASON=DB_NOT_ALLOWED", buf.getvalue())

    def test_allow_exit_code(self):
        buf = io.StringIO()
        with mock.patch.dict(os.environ, {"DATABASE_URL": "postgres://127.0.0.1:5432/yreader_test"}, clear=True), contextlib.redirect_stdout(buf):
            self.assertEqual(guard.main(["--mode", "migrate"]), 0)
        self.assertIn("MODE=migrate", buf.getvalue())


if __name__ == "__main__":
    unittest.main(verbosity=2)
