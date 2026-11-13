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
* URI form uses explicit lowercase ``postgres://`` or ``postgresql://``;
  a strict keyword/value conninfo subset is also accepted. Both forms require
  explicit port, loopback host and an allow-listed database name;
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
    if "=" in text and not re.match(r"^[A-Za-z][A-Za-z0-9+.-]*://", text):
        return _parse_conninfo(text, label)
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


def _parse_conninfo(text: str, label: str) -> dict:
    """Strict libpq keyword/value subset: explicit target, no services/indirection.

    Quoted values and backslash escaping follow libpq conninfo syntax. Repeated
    keys and every unrecognised parameter are rejected instead of interpreted.
    """
    allowed = {"host", "port", "dbname", "user", "password"} | set(ALLOWED_PARAMS)
    whitespace = " \t\n\r\v\f"
    values = {}
    pos, size = 0, len(text)
    while pos < size:
        while pos < size and text[pos] in whitespace:
            pos += 1
        if pos == size:
            break
        start = pos
        while pos < size and text[pos] not in whitespace + "=":
            pos += 1
        key = text[start:pos]
        if key not in allowed:
            raise Deny("CONFLICTING_PARAM_" + label)
        if key in values:
            raise Deny("DUPLICATE_PARAM_" + label)
        while pos < size and text[pos] in whitespace:
            pos += 1
        if pos == size or text[pos] != "=":
            raise Deny("INVALID_CONNINFO_" + label)
        pos += 1
        while pos < size and text[pos] in whitespace:
            pos += 1
        quoted = pos < size and text[pos] == "'"
        if quoted:
            pos += 1
        result = []
        closed = not quoted
        while pos < size:
            char = text[pos]
            if char == "\\":
                pos += 1
                if pos == size:
                    raise Deny("INVALID_CONNINFO_" + label)
                result.append(text[pos]); pos += 1
            elif quoted and char == "'":
                pos += 1; closed = True; break
            elif not quoted and char in whitespace:
                break
            else:
                result.append(char); pos += 1
        if not closed or (quoted and pos < size and text[pos] not in whitespace):
            raise Deny("INVALID_CONNINFO_" + label)
        value = "".join(result)
        if "\x00" in value:
            raise Deny("INVALID_CONNINFO_" + label)
        values[key] = value
    host = values.get("host", "").lower()
    if not host:
        raise Deny("MISSING_HOST_" + label)
    if host not in ALLOWED_HOSTS:
        raise Deny("HOST_NOT_ALLOWED_" + label)
    port = values.get("port", "")
    if not port:
        raise Deny("MISSING_PORT_" + label)
    if not re.fullmatch(r"[0-9]+", port) or not 1 <= int(port) <= 65535:
        raise Deny("INVALID_PORT_" + label)
    dbname = values.get("dbname", "")
    if not re.fullmatch(r"[a-z0-9_]+", dbname):
        raise Deny("INVALID_DBNAME_" + label)
    if "sslmode" in values and values["sslmode"] not in ALLOWED_SSLMODES:
        raise Deny("INVALID_SSLMODE_" + label)
    if "connect_timeout" in values:
        timeout = values["connect_timeout"]
        if not re.fullmatch(r"[0-9]+", timeout) or not 1 <= int(timeout) <= MAX_CONNECT_TIMEOUT:
            raise Deny("INVALID_CONNECT_TIMEOUT_" + label)
    if any(ord(char) < 0x20 for char in values.get("application_name", "")):
        raise Deny("INVALID_APPLICATION_NAME_" + label)
    return {"host": host, "port": int(port), "dbname": dbname, "label": label}


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
