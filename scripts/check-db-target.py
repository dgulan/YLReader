#!/usr/bin/env python3
"""Static database-target guard for the Makefile database entry points.

ADR-013 SAFE-01 (option S3). Reads ``DATABASE_URL`` / ``TEST_DATABASE_URL``
from the process environment, parses them with the Python standard library
only, and decides whether the requested operation targets an explicitly
allowed local test database.

Hard boundaries of this script:

* no database client, no network connection, no DNS resolution, no SQL;
* no ``eval`` / ``exec`` / shell evaluation, no command substitution;
* never prints the raw connection string, credentials or ``password`` values.

Policy is default-deny:

* target-affecting libpq environment variables are refused by *presence*
  (not by emptiness): libpq fills parameters the URI omits from the
  environment, so an inherited ``PGHOSTADDR`` can redirect the TCP target
  while the URI still looks local, and ``PGSERVICE`` can pull in a service
  file that supplies parameters the URI never mentions;
* the URI must start with an explicit lowercase ``postgres://`` or
  ``postgresql://``, carry an explicit port, a loopback host and an
  allow-listed database name;
* query parameters are limited to ``sslmode`` / ``connect_timeout`` /
  ``application_name`` with validated values and no duplicates.

Known limitation (recorded, not verified online): Python's ``urlsplit`` and
libpq do not necessarily agree on malformed input (for example an unescaped
``@`` inside userinfo), therefore such inputs are rejected rather than
interpreted.

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
ALLOWED_PARAMS = frozenset({"sslmode", "connect_timeout", "application_name"})
ALLOWED_SSLMODES = frozenset(
    {"disable", "allow", "prefer", "require", "verify-ca", "verify-full"}
)
ALLOWED_SCHEMES = ("postgres://", "postgresql://")
# libpq parameters that can change the effective target. Presence alone is
# enough to deny: environment values are consulted for every parameter the
# connection string does not specify.
TARGET_ENV_VARS = (
    "PGHOST",
    "PGHOSTADDR",
    "PGPORT",
    "PGDATABASE",
    "PGSERVICE",
    "PGSERVICEFILE",
)
MIGRATE_DB_NAMES = frozenset({"yreader_test"})
ISOLATED_DB_RE = re.compile(r"^yreader_a1_iso_[a-z0-9_]{1,24}$")
# Maintenance connection is only used to DROP/CREATE the test database.
MAINTENANCE_DB_NAMES = frozenset({"postgres"})
MAX_CONNECT_TIMEOUT = 600


class Deny(Exception):
    """Static policy denial carrying a fixed, credential-free reason code."""

    def __init__(self, reason: str) -> None:
        super().__init__(reason)
        self.reason = reason


def _deny_on_target_env(env) -> None:
    """Refuse inherited libpq target parameters (presence-based, default deny)."""
    for name in TARGET_ENV_VARS:
        if name in env:
            raise Deny("CONFLICTING_ENV_" + name)


def _check_query(parts, label: str) -> None:
    """Validate the allow-listed query parameters, rejecting duplicates."""
    seen: set[str] = set()
    for key, value in parse_qsl(parts.query, keep_blank_values=True):
        lowered = key.lower()
        if lowered not in ALLOWED_PARAMS:
            raise Deny("CONFLICTING_PARAM_" + label)
        if lowered in seen:
            raise Deny("DUPLICATE_PARAM_" + label)
        seen.add(lowered)
        if lowered == "sslmode" and value.lower() not in ALLOWED_SSLMODES:
            raise Deny("INVALID_SSLMODE_" + label)
        if lowered == "connect_timeout":
            if not (value.isdigit() and 1 <= int(value) <= MAX_CONNECT_TIMEOUT):
                raise Deny("INVALID_CONNECT_TIMEOUT_" + label)
        if lowered == "application_name" and any(ord(ch) < 0x20 for ch in value):
            raise Deny("INVALID_APPLICATION_NAME_" + label)


def _parse_target(raw, label: str, env) -> dict:
    """Parse and validate one PostgreSQL URI; raise :class:`Deny` on any doubt."""
    if raw is None or raw.strip() == "":
        raise Deny("MISSING_" + label)
    _deny_on_target_env(env)
    text = raw.strip()
    # Case-sensitive on purpose: libpq only documents these two spellings and
    # an ambiguous spelling must fail closed rather than be normalised.
    if not text.startswith(ALLOWED_SCHEMES):
        raise Deny("INVALID_SCHEME_" + label)
    try:
        parts = urlsplit(text)
    except ValueError:
        raise Deny("INVALID_URI_" + label)
    if parts.scheme not in ("postgres", "postgresql"):
        raise Deny("INVALID_SCHEME_" + label)
    if parts.fragment:
        raise Deny("INVALID_URI_" + label)
    if parts.netloc.count(",") > 0:
        raise Deny("MULTI_HOST_" + label)
    if parts.netloc.count("@") > 1:
        # Python and libpq may split userinfo differently on unescaped '@'.
        raise Deny("AMBIGUOUS_USERINFO_" + label)
    if "@" in parts.netloc:
        username = parts.netloc.rpartition("@")[0].split(":", 1)[0]
        if username == "":
            raise Deny("AMBIGUOUS_USERINFO_" + label)
    host = parts.hostname
    if not host:
        raise Deny("MISSING_HOST_" + label)
    try:
        port = parts.port
    except ValueError:
        raise Deny("INVALID_PORT_" + label)
    if port is None:
        # Without an explicit port libpq would fall back to PGPORT; the guard
        # cannot prove the effective port, so an explicit port is required.
        raise Deny("MISSING_PORT_" + label)
    if port < 1 or port > 65535:
        raise Deny("INVALID_PORT_" + label)
    path = parts.path
    dbname = path[1:] if path.startswith("/") else path
    if dbname == "" or "/" in dbname or "%" in dbname:
        raise Deny("INVALID_DBNAME_" + label)
    _check_query(parts, label)
    normalized_host = host.lower()
    if normalized_host not in ALLOWED_HOSTS:
        raise Deny("HOST_NOT_ALLOWED_" + label)
    return {"host": normalized_host, "port": port, "dbname": dbname, "label": label}


def check_migrate(env) -> dict:
    """``make migrate`` may only apply the schema to an allowed test database."""
    target = _parse_target(env.get("DATABASE_URL"), "DATABASE_URL", env)
    if target["dbname"] in MIGRATE_DB_NAMES or ISOLATED_DB_RE.match(target["dbname"]):
        return target
    raise Deny("DB_NOT_ALLOWED")


def check_setup_test_db(env) -> tuple[dict, dict]:
    """``make setup-test-db`` drops/creates ``yreader_test`` on one local instance."""
    maint = _parse_target(env.get("DATABASE_URL"), "DATABASE_URL", env)
    test = _parse_target(env.get("TEST_DATABASE_URL"), "TEST_DATABASE_URL", env)
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
