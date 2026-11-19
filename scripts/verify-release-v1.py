#!/usr/bin/env python3
"""One-command v1.0.0 release gate verifier for YLReader.

Default mode validates all local release gates without touching production
databases or leaving workspace artifacts behind:
  1. Frozen contract SHA-256 hashes (16 files) & backup contract checks
  2. OpenAPI 43 operationId count check
  3. DB target static guard tests
  4. Docker Compose syntax checks (dev + prod)
  5. Go formatting, integration-tagged `go vet`, full unit + concurrency/goleak tests, and CLI builds
  6. Frontend OpenAPI type sync, `vue-tsc`, Vitest, and Vite production build + PWA SW check

Optional flags:
  --with-integration  Run `-tags=integration` Go tests against TEST_DATABASE_URL
  --with-e2e          Spin up ephemeral PG16/Redis7 containers, run integration + Playwright E2E, and clean up
  --skip-docker       Skip Docker Compose syntax validation
"""

from __future__ import annotations

import argparse
import base64
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[1]
BACKEND = ROOT / "backend"
FRONTEND = ROOT / "frontend"


def find_go_tool(name: str) -> str:
    found = shutil.which(name)
    if found:
        return found
    ext = ".exe" if os.name == "nt" else ""
    candidate = ROOT / ".toolchain" / "go" / "bin" / f"{name}{ext}"
    if candidate.is_file():
        return str(candidate)
    raise FileNotFoundError(f"Unable to locate Go tool: {name}")


def find_npm_cmd() -> list[str]:
    if os.name == "nt":
        return ["cmd", "/c", "npm"]
    return ["npm"]


def build_go_env(base_env: dict[str, str], tmp_sub: Path | None = None) -> dict[str, str]:
    env = dict(base_env)
    toolchain = ROOT / ".toolchain"
    if (toolchain / "gocache").is_dir() and not env.get("GOCACHE"):
        env["GOCACHE"] = str(toolchain / "gocache")
    if (toolchain / "gopath").is_dir() and not env.get("GOPATH"):
        env["GOPATH"] = str(toolchain / "gopath")
    if (toolchain / "gotmp").is_dir() and not env.get("GOTMPDIR"):
        env["GOTMPDIR"] = str(toolchain / "gotmp")
    if tmp_sub is not None:
        tmp_sub.mkdir(parents=True, exist_ok=True)
        env["TMP"] = str(tmp_sub)
        env["TEMP"] = str(tmp_sub)
    return env


def run_step(
    name: str,
    cmd: list[str],
    cwd: Path = ROOT,
    env: dict[str, str] | None = None,
    timeout: int = 180,
    capture_stdout: bool = False,
) -> subprocess.CompletedProcess[str]:
    print(f"[gate] {name} ...", flush=True)
    proc = subprocess.run(
        cmd,
        cwd=str(cwd),
        env=env,
        text=True,
        encoding="utf-8",
        errors="replace",
        capture_output=capture_stdout,
        timeout=timeout,
    )
    if proc.returncode != 0:
        detail = ""
        if capture_stdout:
            detail = f"\nstdout:\n{proc.stdout}\nstderr:\n{proc.stderr}"
        raise RuntimeError(f"Gate failed [{name}] (exit={proc.returncode}){detail}")
    print(f"[PASS] {name}", flush=True)
    return proc


def check_openapi_operation_ids() -> int:
    text = (ROOT / "docs" / "openapi.yaml").read_text(encoding="utf-8")
    ops = re.findall(r"^\s+operationId:\s*([A-Za-z0-9_]+)\s*$", text, re.MULTILINE)
    if len(ops) != 43 or len(set(ops)) != 43:
        raise RuntimeError(f"Expected 43 unique OpenAPI operationIds, got {len(ops)} ({len(set(ops))} unique)")
    print(f"[PASS] openapi-operation-ids (43 unique operationIds verified)", flush=True)
    return len(ops)


def check_compose_configs() -> None:
    docker = shutil.which("docker")
    if not docker:
        raise RuntimeError("docker CLI not found; pass --skip-docker to skip compose validation")
    for compose_file in ("docker-compose.yml", "docker-compose.prod.yml"):
        run_step(
            f"compose-config:{compose_file}",
            [docker, "compose", "-f", compose_file, "--env-file", "scripts/compose-check.env", "config", "--quiet"],
            cwd=ROOT,
            timeout=60,
        )


def check_pwa_bundle(dist_dir: Path) -> None:
    manifest_path = dist_dir / "manifest.webmanifest"
    sw_path = dist_dir / "sw.js"
    if not manifest_path.is_file() or not sw_path.is_file():
        raise RuntimeError("PWA build artifacts missing manifest.webmanifest or sw.js")
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    if manifest.get("name") != "YLReader" or manifest.get("start_url") != "/bookshelf":
        raise RuntimeError(f"Unexpected manifest contents: {manifest}")
    sw_text = sw_path.read_text(encoding="utf-8")
    if "__YLREADER_SW_VERSION__" in sw_text or "__YLREADER_PRECACHE_ASSETS__" in sw_text:
        raise RuntimeError("Service Worker template placeholders were not replaced during Vite build")
    if "url.pathname.startsWith('/api/')" not in sw_text:
        raise RuntimeError("Service Worker missing /api/ exclusion invariant")
    print("[PASS] frontend-pwa-bundle (manifest + injected sw.js verified)", flush=True)


def run_isolated_e2e(go_bin: str, go_env: dict[str, str], tmp_dir: Path) -> dict[str, object]:
    docker = shutil.which("docker")
    if not docker:
        raise RuntimeError("--with-e2e requires docker CLI")
    ident = uuid.uuid4().hex[:10]
    pg_name = f"ylreader-r6-pg-{ident}"
    redis_name = f"ylreader-r6-redis-{ident}"
    label = f"ylreader.r6={ident}"
    server_proc: subprocess.Popen[bytes] | None = None
    server_log = None
    results: dict[str, object] = {}

    def d(*args: str, data: bytes | None = None, allowed: bool = False) -> subprocess.CompletedProcess[bytes]:
        r = subprocess.run([docker, *args], input=data, capture_output=True, timeout=120)
        if r.returncode != 0 and not allowed:
            raise RuntimeError(f"docker {' '.join(args)} failed: {r.stderr.decode(errors='replace')[-600:]}")
        return r

    try:
        pg_img = d("image", "inspect", "postgres:16", "--format", "{{.Id}}").stdout.decode().strip()
        redis_img = d("image", "inspect", "redis:7-alpine", "--format", "{{.Id}}").stdout.decode().strip()
        d("run", "-d", "--pull=never", "--name", pg_name, "--label", label, "-p", "127.0.0.1::5432",
          "--tmpfs", "/var/lib/postgresql/data:rw", "-e", "POSTGRES_DB=yreader_test",
          "-e", "POSTGRES_HOST_AUTH_METHOD=trust", pg_img)
        d("run", "-d", "--pull=never", "--name", redis_name, "--label", label, "-p", "127.0.0.1::6379", redis_img)

        for _ in range(45):
            if d("exec", pg_name, "pg_isready", "-h", "127.0.0.1", "-U", "postgres", "-d", "yreader_test", allowed=True).returncode == 0:
                break
            time.sleep(1)
        else:
            raise RuntimeError("Isolated postgres:16 failed to become ready")

        d("exec", "-i", pg_name, "psql", "-Xq", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", "yreader_test",
          data=(ROOT / "docs" / "schema.sql").read_bytes())

        pg_port = json.loads(d("inspect", pg_name).stdout)[0]["NetworkSettings"]["Ports"]["5432/tcp"][0]["HostPort"]
        redis_port = json.loads(d("inspect", redis_name).stdout)[0]["NetworkSettings"]["Ports"]["6379/tcp"][0]["HostPort"]
        dsn = f"postgres://postgres:@127.0.0.1:{pg_port}/yreader_test?sslmode=disable"

        integ_env = dict(go_env)
        integ_env["TEST_DATABASE_URL"] = dsn
        integ_env["REDIS_ADDR"] = f"127.0.0.1:{redis_port}"
        run_step(
            "backend-integration-suite",
            [go_bin, "test", "-mod=readonly", "-buildvcs=false", "-tags=integration", "-p", "1", "-parallel", "1", "-timeout", "300s", "-count=1", "./..."],
            cwd=BACKEND,
            env=integ_env,
            timeout=360,
        )
        run_step(
            "backend-concurrency-100-goleak",
            [go_bin, "test", "-mod=readonly", "-buildvcs=false", "-tags=integration", "-timeout", "120s", "-v", "-run", "TestConcurrencyReadAndRelocate_NoGoroutineLeak", "./internal/api"],
            cwd=BACKEND,
            env=integ_env,
            timeout=150,
        )
        run_step(
            "backend-concurrency-100-bench",
            [go_bin, "test", "-mod=readonly", "-buildvcs=false", "-tags=integration", "-timeout", "120s", "-bench=BenchmarkConcurrencyContentRead100", "-benchtime=3x", "-run=^$", "./internal/api"],
            cwd=BACKEND,
            env=integ_env,
            timeout=150,
        )
        results["integration"] = "passed"

        ext = ".exe" if os.name == "nt" else ""
        server_bin = tmp_dir / f"yreader-r6-server{ext}"
        run_step(
            "build-e2e-server",
            [go_bin, "build", "-mod=readonly", "-buildvcs=false", "-o", str(server_bin), "./cmd/server"],
            cwd=BACKEND,
            env=go_env,
            timeout=120,
        )

        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            api_port = sock.getsockname()[1]

        e2e_pw = secrets.token_urlsafe(24)
        srv_env = dict(os.environ)
        srv_tmp = tmp_dir / "srv-tmp"
        srv_tmp.mkdir(parents=True, exist_ok=True)
        srv_env.update({
            "TMP": str(srv_tmp),
            "TEMP": str(srv_tmp),
            "DATABASE_URL": dsn,
            "REDIS_ADDR": f"127.0.0.1:{redis_port}",
            "REDIS_URL": f"redis://127.0.0.1:{redis_port}/0",
            "ENVIRONMENT": "production",
            "PORT": str(api_port),
            "JWT_SECRET": secrets.token_hex(32),
            "YREADER_HEADER_KEY_KID": "primary",
            "YREADER_HEADER_KEY_primary": base64.b64encode(secrets.token_bytes(32)).decode(),
            "SEED_USERNAME": "r6-release-ci",
            "SEED_PASSWORD": e2e_pw,
            "STORAGE_DIR": str(tmp_dir / "storage"),
            "YREADER_SANDBOX_SOCKETS": "",
        })
        (tmp_dir / "storage").mkdir(parents=True, exist_ok=True)
        server_log = (tmp_dir / "e2e-server.log").open("wb")
        flags = subprocess.CREATE_NO_WINDOW if os.name == "nt" else 0
        server_proc = subprocess.Popen([str(server_bin)], env=srv_env, stdout=server_log, stderr=server_log, creationflags=flags)

        for _ in range(60):
            if server_proc.poll() is not None:
                raise RuntimeError("Isolated E2E backend exited unexpectedly")
            try:
                if urllib.request.urlopen(f"http://127.0.0.1:{api_port}/readyz", timeout=1).status == 200:
                    break
            except Exception:
                pass
            time.sleep(1)
        else:
            raise RuntimeError("Isolated E2E backend /readyz timeout")

        npm = find_npm_cmd()
        run_step(
            "frontend-build-for-e2e-preview",
            [*npm, "run", "build"],
            cwd=FRONTEND,
            timeout=180,
        )
        e2e_env = dict(os.environ)
        e2e_env.pop("E2E_BASE_URL", None)
        e2e_env.update({
            "E2E_USERNAME": "r6-release-ci",
            "E2E_PASSWORD": e2e_pw,
            "E2E_DATABASE_URL": dsn,
            "E2E_API_TARGET": f"http://127.0.0.1:{api_port}",
            "E2E_DB_CONTAINER": pg_name,
            "E2E_DB_USER": "postgres",
            "E2E_DB_NAME": "yreader_test",
            "E2E_READING_PREVIEW": "1",
        })
        run_step(
            "frontend-playwright-e2e",
            [*npm, "run", "test:e2e"],
            cwd=FRONTEND,
            env=e2e_env,
            timeout=900,
        )
        results["e2e"] = "passed"
        return results
    finally:
        if server_proc is not None and server_proc.poll() is None:
            server_proc.terminate()
            try:
                server_proc.wait(timeout=15)
            except subprocess.TimeoutExpired:
                server_proc.kill()
        if server_log is not None:
            server_log.close()
        for cname in (pg_name, redis_name):
            d("rm", "-f", "-v", cname, allowed=True)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--with-integration", action="store_true", help="Run -tags=integration tests against TEST_DATABASE_URL")
    parser.add_argument("--with-e2e", action="store_true", help="Run ephemeral Docker PG/Redis + integration + Playwright E2E")
    parser.add_argument("--skip-docker", action="store_true", help="Skip Docker Compose configuration checks")
    args = parser.parse_args(argv)

    go_bin = find_go_tool("go")
    gofmt_bin = find_go_tool("gofmt")
    npm = find_npm_cmd()

    summary: dict[str, object] = {"passed": False, "steps": []}

    toolchain_dir = ROOT / ".toolchain"
    if toolchain_dir.is_dir():
        tmp_dir = toolchain_dir / f"r6-gate-{uuid.uuid4().hex[:8]}"
        tmp_dir.mkdir(parents=True, exist_ok=True)
        cleanup_tmp = lambda: shutil.rmtree(tmp_dir, ignore_errors=True)
        temp_ctx = None
    else:
        temp_ctx = tempfile.TemporaryDirectory(prefix="ylreader-r6-")
        tmp_dir = Path(temp_ctx.name)
        cleanup_tmp = temp_ctx.cleanup

    go_env = build_go_env(os.environ, tmp_dir / "go-exec-tmp")

    try:
        run_step("verify-backup-contract", [sys.executable, "scripts/verify-backup-contract.py"], cwd=ROOT, timeout=60)
        summary["steps"].append("verify-backup-contract")

        ops_count = check_openapi_operation_ids()
        summary["openapi_operation_ids"] = ops_count
        summary["steps"].append("openapi-operation-ids")

        run_step("test-db-guard", [sys.executable, "scripts/test-db-guard.py"], cwd=ROOT, timeout=60)
        summary["steps"].append("test-db-guard")

        if not args.skip_docker:
            check_compose_configs()
            summary["steps"].append("compose-config")

        fmt_res = run_step("backend-gofmt", [gofmt_bin, "-l", "."], cwd=BACKEND, env=go_env, timeout=60, capture_stdout=True)
        if fmt_res.stdout.strip():
            raise RuntimeError(f"Unformatted Go files:\n{fmt_res.stdout}")
        summary["steps"].append("backend-gofmt")

        run_step(
            "backend-vet",
            [go_bin, "vet", "-mod=readonly", "-buildvcs=false", "-tags=integration", "./..."],
            cwd=BACKEND,
            env=go_env,
            timeout=120,
        )
        summary["steps"].append("backend-vet")

        run_step(
            "backend-unit-and-concurrency-goleak",
            [go_bin, "test", "-mod=readonly", "-buildvcs=false", "-p", "1", "-timeout", "180s", "-count=1", "./..."],
            cwd=BACKEND,
            env=go_env,
            timeout=240,
        )
        summary["steps"].append("backend-unit-and-concurrency-goleak")

        ext = ".exe" if os.name == "nt" else ""
        for pkg in ("./cmd/server", "./cmd/migrator", "./cmd/sandboxworker"):
            out_bin = tmp_dir / (Path(pkg).name + ext)
            run_step(
                f"backend-build:{pkg}",
                [go_bin, "build", "-mod=readonly", "-buildvcs=false", "-o", str(out_bin), pkg],
                cwd=BACKEND,
                env=go_env,
                timeout=120,
            )
        summary["steps"].append("backend-builds")

        run_step("frontend-generate-api", [*npm, "run", "generate:api"], cwd=FRONTEND, timeout=120)
        git = shutil.which("git")
        if git:
            run_step(
                "frontend-api-schema-clean",
                [git, "-c", f"safe.directory={ROOT.as_posix()}", "diff", "--exit-code", "--", "frontend/src/api/schema.d.ts"],
                cwd=ROOT,
                timeout=30,
            )
        summary["steps"].append("frontend-api-sync")

        run_step("frontend-type-check", [*npm, "run", "type-check"], cwd=FRONTEND, timeout=120)
        summary["steps"].append("frontend-type-check")

        run_step("frontend-unit-tests", [*npm, "test"], cwd=FRONTEND, timeout=180)
        summary["steps"].append("frontend-unit-tests")

        dist_dir = tmp_dir / "frontend-dist"
        run_step(
            "frontend-build",
            [*npm, "exec", "--", "vite", "build", "--outDir", str(dist_dir), "--emptyOutDir"],
            cwd=FRONTEND,
            timeout=180,
        )
        check_pwa_bundle(dist_dir)
        summary["steps"].append("frontend-build-and-pwa")

        if args.with_integration or os.environ.get("TEST_DATABASE_URL"):
            run_step(
                "backend-integration",
                [go_bin, "test", "-mod=readonly", "-buildvcs=false", "-tags=integration", "-p", "1", "-parallel", "1", "-timeout", "300s", "-count=1", "./..."],
                cwd=BACKEND,
                env=go_env,
                timeout=360,
            )
            summary["steps"].append("backend-integration")

        if args.with_e2e:
            e2e_res = run_isolated_e2e(go_bin, go_env, tmp_dir)
            summary["isolated_e2e"] = e2e_res
            summary["steps"].append("isolated-e2e")

        summary["passed"] = True
    finally:
        cleanup_tmp()

    report_dir = ROOT / ".toolchain"
    if report_dir.is_dir():
        report_path = report_dir / "release-v1-gate-report.json"
        report_path.write_text(json.dumps(summary, indent=2) + "\n", encoding="utf-8")
        print(f"Release gate report written to: {report_path}")
    print("ALL RELEASE V1.0.0 GATES PASSED.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
