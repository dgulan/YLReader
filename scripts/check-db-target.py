#!/usr/bin/env python3
"""Static database-target guard for the Makefile entry points (ADR-013 SAFE-01, option S3).

Reads ``DATABASE_URL`` / ``TEST_DATABASE_URL`` from the environment, parses them
with the Python standard library only, and decides whether the requested
operation targets an explicitly allowed local test database.

Hard boundaries of this script:

* no database client, no network connection, no DNS resolution, no SQL;
* no ``eval`` / ``exec`` / shell evaluation, no command substitution;
* never prints the raw connection string, credentials or ``password`` values.

Exit codes: 0 = allowed, 1 = denied. Output is a fixed, credential-free pair of
lines (``DB_GUARD=...`` / ``REASON=...`` or ``MODE=...``).
"""

from __future__ import annotations

import argparse
import os
import re
import sys
from urllib.parse import parse_qsl, urlsplit

ALLOWED_HOSTS = frozenset({"127.0.0.1", "::1", "localhost"})
# Query parameters that cannot change host/port/dbname/service of the target.
ALLOWED_PARAMS = frozenset({"sslmode", "connect_timeout", "application_name"})
MIGRATE_DB_NAMES = frozenset({"yreader_test"})
ISOLATED_DB_RE = re.compile(r"^yreader_a1_iso_[a-z0-9_]{1,24}$")
# Maintenance connection is only used to DROP/CREATE the test database.
MAINTENANCE_DB_NAMES = frozenset({"postgres"})
DEFAULT_PORT = 5432


class Deny(Exception):
    """Static policy denial carrying a fixed, credential-free reason code."""

    def __init__(self, reason: str) -> None:
        super().__init__(reason)
        self.reason = reason


def _parse_target(raw: str | None, label: str) -> dict:
    """Parse and validate one PostgreSQL URI; raise :class:`Deny` on any doubt."""
    if raw is None or raw.strip() == "":
        raise Deny("MISSING_" + label)
    try:
        parts = urlsplit(raw)
    except ValueError:
        raise Deny("INVALID_URI_" + label)
    if parts.scheme not in ("postgres", "postgresql"):
        raise Deny("INVALID_SCHEME_" + label)
    if parts.fragment:
        raise Deny("INVALID_URI_" + label)
    if parts.netloc.count(",") > 0:
        raise Deny("MULTI_HOST_" + label)
    host = parts.hostname
    if not host:
        raise Deny("MISSING_HOST_" + label)
    try:
        port = parts.port
    except ValueError:
        raise Deny("INVALID_PORT_" + label)
    if port is None:
        port = DEFAULT_PORT
    if port < 1 or port > 65535:
        raise Deny("INVALID_PORT_" + label)
    path = parts.path
    dbname = path[1:] if path.startswith("/") else path
    if dbname == "" or "/" in dbname or "%" in dbname:
        raise Deny("INVALID_DBNAME_" + label)
    for key, _value in parse_qsl(parts.query, keep_blank_values=True):
        if key.lower() not in ALLOWED_PARAMS:
            raise Deny("CONFLICTING_PARAM_" + label)
    normalized_host = host.lower()
    if normalized_host not in ALLOWED_HOSTS:
        raise Deny("HOST_NOT_ALLOWED_" + label)
    return {"host": normalized_host, "port": port, "dbname": dbname, "label": label}


def check_migrate(env) -> dict:
    """``make migrate`` may only apply the schema to an allowed test database."""
    target = _parse_target(env.get("DATABASE_URL"), "DATABASE_URL")
    if target["dbname"] in MIGRATE_DB_NAMES or ISOLATED_DB_RE.match(target["dbname"]):
        return target
    raise Deny("DB_NOT_ALLOWED")


def check_setup_test_db(env) -> tuple[dict, dict]:
    """``make setup-test-db`` drops/creates ``yreader_test`` on one local instance."""
    maint = _parse_target(env.get("DATABASE_URL"), "DATABASE_URL")
    test = _parse_target(env.get("TEST_DATABASE_URL"), "TEST_DATABASE_URL")
    if test["dbname"] != "yreader_test":
        raise Deny("TEST_DB_NOT_ALLOWED")
    if maint["dbname"] not in MAINTENANCE_DB_NAMES:
        raise Deny("MAINT_DB_NOT_ALLOWED")
    if (maint["host"], maint["port"]) != (test["host"], test["port"]):
        raise Deny("HOST_PORT_MISMATCH")
    return maint, test


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(
        description="Static Makefile database-target guard (no DB access)."
    )
    parser.add_argument("--mode", required=True, choices=("migrate", "setup-test-db"))
    args = parser.parse_args(argv)
    try:
        if args.mode == "migrate":
            check_migrate(os.environ)
        else:
            check_setup_test_db(os.environ)
    except Deny as denied:
        print("DB_GUARD=DENY")
        print("REASON=" + denied.reason)
        return 1
    print("DB_GUARD=ALLOW")
    print("MODE=" + args.mode)
    return 0


if __name__ == "__main__":
    sys.exit(main())
