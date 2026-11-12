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


def migrate_env(url, **extra):
    env = {"DATABASE_URL": url}
    env.update(extra)
    return env


def setup_env(maint, test, **extra):
    env = {"DATABASE_URL": maint, "TEST_DATABASE_URL": test}
    env.update(extra)
    return env


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

    # --- SAFE-01 v1.2.1 hardening: target-splitting environment variables ---

    def test_env_pghostaddr_denied_even_when_empty(self):
        self.deny(guard.check_migrate, migrate_env(LOCAL + "yreader_test", PGHOSTADDR=""), "CONFLICTING_ENV_PGHOSTADDR")

    def test_env_pghostaddr_value_denied(self):
        self.deny(guard.check_migrate, migrate_env(LOCAL + "yreader_test", PGHOSTADDR="10.0.0.5"), "CONFLICTING_ENV_PGHOSTADDR")

    def test_env_pgservice_denied(self):
        self.deny(guard.check_migrate, migrate_env(LOCAL + "yreader_test", PGSERVICE="prod"), "CONFLICTING_ENV_PGSERVICE")

    def test_env_pgservicefile_denied(self):
        self.deny(
            guard.check_migrate,
            migrate_env(LOCAL + "yreader_test", PGSERVICEFILE="/tmp/pg_service.conf"),
            "CONFLICTING_ENV_PGSERVICEFILE",
        )

    def test_env_pgport_denied(self):
        self.deny(guard.check_migrate, migrate_env(LOCAL + "yreader_test", PGPORT="6000"), "CONFLICTING_ENV_PGPORT")

    def test_env_pghost_denied(self):
        self.deny(guard.check_migrate, migrate_env(LOCAL + "yreader_test", PGHOST="10.0.0.5"), "CONFLICTING_ENV_PGHOST")

    def test_env_pgdatabase_denied(self):
        self.deny(guard.check_migrate, migrate_env(LOCAL + "yreader_test", PGDATABASE="yreader"), "CONFLICTING_ENV_PGDATABASE")

    # --- SAFE-01 v1.2.1 hardening: stricter URI validation ---

    def test_missing_port_denied(self):
        self.deny(guard.check_migrate, migrate_env("postgres://u:p@127.0.0.1/yreader_test"), "MISSING_PORT_DATABASE_URL")

    def test_duplicate_query_param_denied(self):
        self.deny(
            guard.check_migrate,
            migrate_env(LOCAL + "yreader_test?sslmode=disable&sslmode=require"),
            "DUPLICATE_PARAM_DATABASE_URL",
        )

    def test_invalid_sslmode_denied(self):
        self.deny(guard.check_migrate, migrate_env(LOCAL + "yreader_test?sslmode=bogus"), "INVALID_SSLMODE_DATABASE_URL")

    def test_negative_connect_timeout_denied(self):
        self.deny(
            guard.check_migrate,
            migrate_env(LOCAL + "yreader_test?connect_timeout=-1"),
            "INVALID_CONNECT_TIMEOUT_DATABASE_URL",
        )

    def test_non_numeric_connect_timeout_denied(self):
        self.deny(
            guard.check_migrate,
            migrate_env(LOCAL + "yreader_test?connect_timeout=abc"),
            "INVALID_CONNECT_TIMEOUT_DATABASE_URL",
        )

    def test_zero_connect_timeout_denied(self):
        self.deny(
            guard.check_migrate,
            migrate_env(LOCAL + "yreader_test?connect_timeout=0"),
            "INVALID_CONNECT_TIMEOUT_DATABASE_URL",
        )

    def test_application_name_control_char_denied(self):
        self.deny(
            guard.check_migrate,
            migrate_env(LOCAL + "yreader_test?application_name=%0Aevil"),
            "INVALID_APPLICATION_NAME_DATABASE_URL",
        )

    def test_multiple_at_signs_denied(self):
        self.deny(
            guard.check_migrate,
            migrate_env("postgres://a@b@127.0.0.1:5432/yreader_test"),
            "AMBIGUOUS_USERINFO_DATABASE_URL",
        )

    def test_empty_username_denied(self):
        self.deny(
            guard.check_migrate,
            migrate_env("postgres://@127.0.0.1:5432/yreader_test"),
            "AMBIGUOUS_USERINFO_DATABASE_URL",
        )

    def test_password_without_username_denied(self):
        self.deny(
            guard.check_migrate,
            migrate_env("postgres://:secret@127.0.0.1:5432/yreader_test"),
            "AMBIGUOUS_USERINFO_DATABASE_URL",
        )

    def test_uppercase_scheme_denied(self):
        self.deny(
            guard.check_migrate,
            migrate_env("POSTGRES://127.0.0.1:5432/yreader_test"),
            "INVALID_SCHEME_DATABASE_URL",
        )

    def test_malformed_uri_denied(self):
        self.deny(guard.check_migrate, migrate_env("postgres://[::1:5432/yreader_test"), "INVALID_URI_DATABASE_URL")


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

    def test_application_name_allowed(self):
        target = guard.check_migrate(migrate_env(LOCAL + "yreader_test?application_name=yreader-migrate"))
        self.assertEqual(target["label"], "DATABASE_URL")

    def test_ipv6_loopback_allowed(self):
        target = guard.check_migrate(migrate_env("postgres://u:p@[::1]:5432/yreader_test"))
        self.assertEqual((target["host"], target["port"]), ("::1", 5432))

    def test_no_userinfo_allowed(self):
        target = guard.check_migrate(migrate_env("postgres://127.0.0.1:5432/yreader_test"))
        self.assertEqual(target["dbname"], "yreader_test")

    def test_setup_env_without_pg_vars_allowed(self):
        guard.check_setup_test_db(
            setup_env("postgres://u:p@localhost:5432/postgres", "postgres://u:p@localhost:5432/yreader_test")
        )


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

    def test_t14c_env_conflict_reason_leaks_nothing(self):
        env = {
            "DATABASE_URL": f"postgres://owner:{self.SECRET}@127.0.0.1:5432/yreader_test",
            "PGHOSTADDR": "10.9.9.9",
        }
        buf = io.StringIO()
        with mock.patch.dict(os.environ, env, clear=True), contextlib.redirect_stdout(buf):
            code = guard.main(["--mode", "migrate"])
        out = buf.getvalue()
        self.assertEqual(code, 1)
        self.assertIn("CONFLICTING_ENV_PGHOSTADDR", out)
        self.assertNotIn(self.SECRET, out)


class TestMakefileWiring(unittest.TestCase):
    """Static proof that the guard is inlined in the same recipe as psql."""

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

    def recipe(self, target):
        return "\n".join(self.targets[target])

    def test_migrate_inlines_guard_before_psql(self):
        body = self.recipe("migrate")
        self.assertIn("$(DB_GUARD) --mode migrate", body)
        self.assertIn("$(PSQL)", body)
        self.assertLess(body.index("$(DB_GUARD)"), body.index("$(PSQL)"))
        self.assertIn("&&", body)

    def test_setup_test_db_inlines_guard_before_all_psql_calls(self):
        body = self.recipe("setup-test-db")
        self.assertIn("$(DB_GUARD) --mode setup-test-db", body)
        self.assertLess(body.index("$(DB_GUARD)"), body.index("$(PSQL)"))
        self.assertEqual(body.count("$(PSQL)"), 3)
        self.assertEqual(body.count("&&"), 3)

    def test_no_prerequisite_only_guards_left(self):
        self.assertNotIn("db-guard-migrate", self.text)
        self.assertNotIn("db-guard-setup-test-db", self.text)
        self.assertNotIn("migrate: db-guard", self.text)
        self.assertNotIn("setup-test-db: db-guard", self.text)

    def test_dangerous_commands_are_chained_not_separate_lines(self):
        # Every psql line in the two DB targets must also carry the guard on the
        # same logical recipe (continuation-joined) so `make -o` cannot skip it.
        for target in ("migrate", "setup-test-db"):
            body = self.recipe(target)
            for line in body.splitlines():
                if "$(PSQL)" in line:
                    self.assertIn("&&", body)

    def test_guard_definition_present(self):
        self.assertIn("DB_GUARD := $(PYTHON) scripts/check-db-target.py", self.text)

    def test_no_eval_or_dynamic_execution(self):
        self.assertNotIn("eval ", self.text)
        self.assertNotIn("$$(", self.text.replace("$$($(GOFMT) -l .)", ""))

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
