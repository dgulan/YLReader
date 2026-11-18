"""Verify an embedded frontend image against owned disposable Linux services.

No frontend config/dist overlay, production data, image pull or deployment.
"""
from pathlib import Path
import argparse
import base64
import hashlib
import json
import os
import platform
import re
import secrets
import subprocess
import time
import urllib.error
import urllib.request
import uuid
from prototypes.reading_samples import create_reading_samples
from prototypes.reader_soak_result import soak_checks, valid_resource_samples

p = argparse.ArgumentParser(description=__doc__)
p.add_argument('--image', required=True)
p.add_argument('--server', type=Path, required=True)
p.add_argument('--browser', choices=['webkit', 'chromium'], default='webkit')
p.add_argument('--reading-seconds', type=int, default=60)
p.add_argument('--http-only', action='store_true')
p.add_argument('--auth-probe', action='store_true', help='Measure isolated API authentication before browser work')
p.add_argument('--login-only', action='store_true', help='Check two login navigations only, without claiming reading acceptance')
p.add_argument('--lifecycle-only', action='store_true', help='Run the 19 controlled reader lifecycle cases, not real reading samples')
p.add_argument('--login-url-check', choices=['exact', 'predicate'], default='exact', help='Controlled URL waiter comparison')
p.add_argument('--linux-browser-image', help='Cached matching official Linux Playwright image; remote browser only')
p.add_argument('--soak-seconds', type=int, help='Continuous reading soak, 1800 to 3600 seconds')
p.add_argument('--soak-probe', action='store_true', help='60-second fixture check; does not qualify as long reading')
a = p.parse_args()
if not 60 <= a.reading_seconds <= 600:
    p.error('reading-seconds must be between 60 and 600')
if sum([a.http_only, a.login_only, a.lifecycle_only, a.soak_probe, a.soak_seconds is not None]) > 1:
    p.error('choose one verification mode')
if a.soak_seconds is not None and not 1800 <= a.soak_seconds <= 3600:
    p.error('soak-seconds must be between 1800 and 3600')
soak_duration = 60 if a.soak_probe else a.soak_seconds
if soak_duration and not a.linux_browser_image:
    p.error('soak verification requires an explicit Linux browser image')
if a.login_url_check == 'predicate' and not a.login_only:
    p.error('predicate comparison requires login-only diagnostic mode')
if a.linux_browser_image and a.http_only:
    p.error('linux-browser-image requires a browser verification mode')
root = Path(__file__).resolve().parents[1]
ident = uuid.uuid4().hex[:12]
label_key, label = 'ylreader.frontend-image', 'ylreader.frontend-image=' + ident
prefix = 'ylreader-frontend-image-' + ident
out = root / '.toolchain' / prefix
out.mkdir()
objects = []
result = {'run_id': ident, 'passed': False, 'cleanup': {}, 'physical_device_tested': False,
          'frontend_overlay': False, 'browser': a.browser, 'http_only': a.http_only, 'http_checks': {}}
result['verification_mode'] = 'reader_soak_probe' if a.soak_probe else 'reader_soak' if a.soak_seconds is not None else 'http_only' if a.http_only else 'login_navigation' if a.login_only else 'reader_lifecycle' if a.lifecycle_only else 'reading'
result['login_url_check'] = a.login_url_check
result['browser_instrumentation'] = bool(a.login_only and not a.http_only)
result['browser_platform'] = 'linux' if a.linux_browser_image else platform.system().lower()
result['client_platform'] = platform.system().lower()


def docker(*args, data=None, allowed=False):
    r = subprocess.run(['docker', *args], input=data, capture_output=True, timeout=120)
    if r.returncode and not allowed:
        raise RuntimeError('docker operation failed: ' + r.stderr.decode(errors='replace')[-1200:])
    return r


def own(kind, name):
    objects.append((kind, name))
    return name


def image(name):
    info = json.loads(docker('image', 'inspect', name).stdout)[0]
    if info['Os'] != 'linux' or info['Architecture'] != 'amd64':
        raise RuntimeError('requires cached native Linux amd64 images')
    return info['Id']


def container(name, *args):
    own('container', name)
    docker('run', '-d', '--pull=never', '--name', name, '--label', label, *args)


def published(name, port=None):
    info = json.loads(docker('inspect', name).stdout)[0]
    port = port or (8080 if name.endswith('-app') else 80)
    binding = info['NetworkSettings']['Ports'][f'{port}/tcp'][0]
    if binding['HostIp'] != '127.0.0.1':
        raise RuntimeError('owned control ports must remain loopback-only')
    return 'http://127.0.0.1:' + binding['HostPort']


def http(base, path, data=None, headers=None, timeout=15):
    req = urllib.request.Request(base + path, data=data, headers=headers or {})
    start = time.monotonic()
    try:
        response = urllib.request.urlopen(req, timeout=timeout)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        return response.status, {k.lower(): v for k, v in response.headers.items()}, response.read(), round(time.monotonic() - start, 3)


def safe_headers(headers):
    return {k: headers[k] for k in ['content-type', 'content-security-policy', 'x-content-type-options',
            'x-frame-options', 'referrer-policy', 'cache-control', 'etag', 'strict-transport-security'] if k in headers}


def run_soak(command, env, duration):
    samples = []
    names = [name for kind, name in objects if kind == 'container']
    began, next_sample = time.monotonic(), 0
    with (out / 'browser.stdout').open('wb') as stdout, (out / 'browser.stderr').open('wb') as stderr:
        process = subprocess.Popen(command, cwd=root / 'frontend', env=env, stdout=stdout, stderr=stderr)
        try:
            while process.poll() is None:
                now = time.monotonic()
                if now - began > duration + 600:
                    result['browser_timeout'] = True
                    raise TimeoutError('owned soak exceeded its total budget')
                if now >= next_sample:
                    stats = docker('stats', '--no-stream', '--format', '{{json .}}', *names, allowed=True)
                    rows = [json.loads(line) for line in stats.stdout.splitlines() if line.strip()] if stats.returncode == 0 else []
                    samples.append({'elapsed_seconds': round(now - began, 2), 'containers': [
                        {key: row.get(key) for key in ['Name', 'MemUsage', 'CPUPerc', 'PIDs']} for row in rows]})
                    (out / 'memory-samples.json').write_text(json.dumps(samples, indent=2) + '\n', encoding='utf-8')
                    result['resource_sample_count'] = len(samples)
                    next_sample = now + 60
                time.sleep(1)
        finally:
            if process.poll() is None:
                if os.name == 'nt':
                    subprocess.run(['taskkill', '/PID', str(process.pid), '/T', '/F'], capture_output=True, timeout=15)
                else:
                    process.kill()
                process.wait(timeout=30)
    return subprocess.CompletedProcess(command, process.returncode, (out / 'browser.stdout').read_bytes(), (out / 'browser.stderr').read_bytes())


try:
    binary = a.server.resolve()
    if binary.read_bytes()[:4] != b'\x7fELF':
        raise RuntimeError('current Linux server ELF required')
    result['server_sha256'] = hashlib.sha256(binary.read_bytes()).hexdigest()
    front_image, runtime, pg_image, redis_image = [image(n) for n in [a.image, 'yreader-pre-release:race', 'postgres:16', 'redis:7-alpine']]
    result['images'] = {'frontend': front_image, 'api_runtime': runtime, 'postgres': pg_image, 'redis': redis_image}
    if a.linux_browser_image:
        linux_image = image(a.linux_browser_image)
        result['images']['browser'] = linux_image
        sdk = root / 'frontend/node_modules/playwright-core'
        sdk_version = json.loads((sdk / 'package.json').read_text(encoding='utf-8'))['version']
    network = own('network', prefix + '-net')
    docker('network', 'create', '--label', label, network)
    pg, redis, app, front = [prefix + '-' + suffix for suffix in ['pg', 'redis', 'app', 'front']]
    container(pg, '--network', network, '--network-alias', 'pg', '--memory', '512m', '--cpus', '1', '--pids-limit', '128',
              '--tmpfs', '/var/lib/postgresql/data:rw', '-e', 'POSTGRES_DB=yreader_test', '-e', 'POSTGRES_HOST_AUTH_METHOD=trust', pg_image)
    for _ in range(45):
        if docker('exec', pg, 'pg_isready', '-h', '127.0.0.1', '-U', 'postgres', '-d', 'yreader_test', allowed=True).returncode == 0:
            break
        time.sleep(1)
    else:
        raise RuntimeError('owned database unavailable')
    docker('exec', '-i', pg, 'psql', '-Xq', '-v', 'ON_ERROR_STOP=1', '-U', 'postgres', '-d', 'yreader_test', data=(root / 'docs/schema.sql').read_bytes())
    container(redis, '--network', network, '--network-alias', 'redis', '--read-only', '--memory', '128m', '--cpus', '1',
              '--pids-limit', '64', '--tmpfs', '/data:rw', redis_image, 'redis-server', '--save', '', '--appendonly', 'no')
    password = secrets.token_urlsafe(24)
    environment = {'DATABASE_URL': 'postgres://postgres:@pg:5432/yreader_test?sslmode=disable', 'REDIS_URL': 'redis://redis:6379/0',
                   'ENVIRONMENT': 'production', 'PORT': '8080', 'JWT_SECRET': secrets.token_hex(32),
                   'YREADER_HEADER_KEY_KID': 'primary', 'YREADER_HEADER_KEY_primary': base64.b64encode(secrets.token_bytes(32)).decode(),
                   'SEED_USERNAME': 'stage3-original', 'SEED_PASSWORD': password, 'STORAGE_DIR': '/storage', 'YREADER_SANDBOX_SOCKETS': ''}
    container(app, '--network', network, '--network-alias', 'app', '--read-only', '--user', '10001:10001',
              '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges', '--memory', '512m', '--cpus', '2', '--pids-limit', '128',
              '-p', '127.0.0.1::8080', '--tmpfs', '/tmp:rw,mode=1777', '--tmpfs', '/storage:rw,uid=10001,gid=10001,mode=0700',
              '--mount', f'type=bind,source={binary},target=/server,readonly',
              *[part for k, v in environment.items() for part in ['-e', k + '=' + v]], '--entrypoint', '/server', runtime)
    api_base = published(app)
    for _ in range(45):
        try:
            if http(api_base, '/readyz')[0] == 200:
                break
        except OSError:
            pass
        time.sleep(1)
    else:
        raise RuntimeError('owned API unavailable')
    container(front, '--network', network, '--read-only', '--security-opt', 'no-new-privileges', '--cap-drop', 'ALL',
              '--cap-add', 'CHOWN', '--cap-add', 'SETUID', '--cap-add', 'SETGID', '--cap-add', 'NET_BIND_SERVICE',
              '--memory', '256m', '--cpus', '1', '--pids-limit', '128', '-p', '127.0.0.1::80',
              *(['-p', '127.0.0.1::3000'] if a.linux_browser_image else []),
              '--tmpfs', '/var/cache/nginx:rw', '--tmpfs', '/var/run:rw', '--tmpfs', '/tmp:rw', front_image)
    base = published(front)
    for _ in range(30):
        try:
            if http(base, '/')[0] == 200:
                break
        except OSError:
            pass
        time.sleep(1)
    else:
        raise RuntimeError('embedded frontend unavailable')
    info = json.loads(docker('inspect', front).stdout)[0]
    result['frontend_mounts'] = [{'type': m['Type'], 'destination': m['Destination']} for m in info['Mounts']]
    if any(m['Type'] != 'tmpfs' for m in info['Mounts']):
        raise RuntimeError('frontend must not have overlays or persistent volumes')
    embedded = docker('exec', front, 'cat', '/etc/nginx/conf.d/default.conf').stdout
    result['embedded_nginx_sha256'] = hashlib.sha256(embedded).hexdigest()
    checks = result['http_checks']
    checks['embedded_config_matches_workspace'] = embedded == (root / 'frontend/nginx.conf').read_bytes()
    status, headers, html, elapsed = http(base, '/')
    result['root'] = {'status': status, 'headers': safe_headers(headers), 'seconds': elapsed}
    checks['html_revalidates'] = status == 200 and 'no-cache' in headers.get('cache-control', '')
    checks['http_has_no_hsts'] = 'strict-transport-security' not in headers
    asset = re.search(rb'(?:src|href)="(/assets/[^" ]+\.js)"', html)
    if not asset:
        raise RuntimeError('embedded HTML has no hashed JS entry')
    asset_path = asset.group(1).decode()
    result['index_sha256'] = hashlib.sha256(html).hexdigest()
    responses = [('root', status, headers)]
    status, headers, body, elapsed = http(base, '/reader/00000000-0000-4000-8000-000000000001')
    checks['spa_deep_link'] = status == 200 and body == html
    checks['deep_link_revalidates'] = 'no-cache' in headers.get('cache-control', '')
    responses.append(('deep_link', status, headers))
    status, headers, body, elapsed = http(base, asset_path)
    result['asset'] = {'status': status, 'headers': safe_headers(headers), 'bytes': len(body), 'seconds': elapsed, 'sha256': hashlib.sha256(body).hexdigest()}
    checks['asset_cache'] = status == 200 and 'max-age=31536000' in headers.get('cache-control', '')
    responses.append(('asset', status, headers))
    checks['asset_conditional_get'] = bool(headers.get('etag')) and http(base, asset_path, headers={'If-None-Match': headers['etag']})[0] == 304
    status, headers, body, _ = http(base, '/assets/missing-image-acceptance.js')
    checks['missing_asset_is_404'] = status == 404
    responses.append(('missing_asset', status, headers))
    for name, status, headers in responses:
        checks[name + '_security_headers'] = (headers.get('x-content-type-options') == 'nosniff'
            and headers.get('x-frame-options') == 'DENY' and headers.get('referrer-policy') == 'strict-origin-when-cross-origin'
            and all(part in headers.get('content-security-policy', '') for part in ["script-src 'self'", "connect-src 'self'", "object-src 'none'", "frame-ancestors 'none'"]))
    result['http_responses'] = [{'name': name, 'status': status, 'headers': safe_headers(headers)} for name, status, headers in responses]
    if a.auth_probe:
        auth = []
        for name, username, supplied, expected in [
            ('missing_cold', 'missing-probe-user', 'synthetic-wrong', 401),
            ('missing_warm', 'missing-probe-user', 'synthetic-wrong', 401),
            ('known_wrong', 'stage3-original', 'synthetic-wrong', 401),
            ('known_correct', 'stage3-original', password, 200),
        ]:
            status, _, _, elapsed = http(api_base, '/api/v1/auth/login',
                json.dumps({'username': username, 'password': supplied}).encode(), {'Content-Type': 'application/json'}, timeout=60)
            auth.append({'case': name, 'status': status, 'seconds': elapsed, 'expected_status': expected})
        result['authentication_probe'] = auth
        checks['authentication_statuses'] = all(row['status'] == row['expected_status'] for row in auth)
    if not a.http_only:
        epub, txt, result['original_samples'] = create_reading_samples(out)
        env = os.environ.copy()
        env.pop('PW_TEST_CONNECT_WS_ENDPOINT', None)
        env.update(E2E_BASE_URL=base, E2E_BROWSER=a.browser, E2E_USERNAME='stage3-original', E2E_PASSWORD=password,
                   E2E_STAGE3_EPUB=str(epub), E2E_STAGE3_TXT=str(txt), E2E_STAGE3_READ_SECONDS=str(a.reading_seconds),
                   E2E_LOGIN_DIAGNOSTICS='1' if a.login_only else '0', E2E_LOGIN_URL_CHECK=a.login_url_check)
        if soak_duration:
            env.update(E2E_SOAK_SECONDS=str(soak_duration), E2E_SOAK_PROBE='1' if a.soak_probe else '0', E2E_SOAK_HEARTBEAT=str(out / 'soak-heartbeat.json'))
        if a.linux_browser_image:
            browser_server = prefix + '-browser'
            container(browser_server, '--network', 'container:' + front, '--init', '--read-only', '--user', 'pwuser',
                      '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges', '--memory', '2g', '--cpus', '2',
                      '--pids-limit', '512', '--shm-size', '256m', '--tmpfs', '/tmp:rw,mode=1777', '-e', 'HOME=/tmp/pw-home',
                      '--mount', f'type=bind,source={sdk},target=/sdk,readonly', '--entrypoint', 'node', linux_image,
                      '/sdk/cli.js', 'run-server', '--host', '0.0.0.0', '--port', '3000', '--path', '/' + ident, '--max-clients', '1')
            for _ in range(30):
                logs = docker('logs', browser_server, allowed=True)
                if b'Listening on' in logs.stdout + logs.stderr:
                    break
                time.sleep(1)
            else:
                raise RuntimeError('owned Linux browser server unavailable')
            result['browser_runtime'] = json.loads(docker('exec', browser_server, 'node', '-p',
                "JSON.stringify({platform:process.platform,arch:process.arch,uid:process.getuid(),node:process.version,sdk:require('/sdk/package.json').version})").stdout)
            browser_metadata = json.loads(docker('exec', browser_server, 'node', '-p',
                "require('fs').readFileSync('/ms-playwright/.docker-info','utf8')").stdout)
            if browser_metadata.get('driverVersion') != sdk_version:
                raise RuntimeError('official browser image metadata must match the installed Playwright SDK')
            result['browser_image_release'] = browser_metadata['dockerImageName']
            browser_controls = json.loads(docker('inspect', browser_server).stdout)[0]['HostConfig']
            result['browser_controls'] = {key: browser_controls.get(key) for key in ['ReadonlyRootfs', 'CapDrop', 'Memory', 'NanoCpus', 'PidsLimit', 'ShmSize', 'SecurityOpt']}
            if result['browser_runtime']['uid'] == 0 or not browser_controls['ReadonlyRootfs']:
                raise RuntimeError('owned Linux browser must remain non-root with a read-only root filesystem')
            env['PW_TEST_CONNECT_WS_ENDPOINT'] = published(front, 3000).replace('http:', 'ws:', 1) + '/' + ident
            env['E2E_BASE_URL'] = 'http://127.0.0.1'
        spec = 'tests/reader-soak.spec.ts' if soak_duration else 'tests/login-navigation.spec.ts' if a.login_only else 'tests/reader-loading.spec.ts' if a.lifecycle_only else 'tests/stage3-reading.spec.ts'
        command = ['cmd', '/c', 'npm', 'run', 'test:e2e', '--', spec]
        run = run_soak(command, env, soak_duration) if soak_duration else subprocess.run(command, cwd=root / 'frontend', env=env, capture_output=True, timeout=max(900, a.reading_seconds + 600))
        (out / 'browser.stdout').write_bytes(run.stdout)
        (out / 'browser.stderr').write_bytes(run.stderr)
        result['browser_exit'] = run.returncode
        for i, file in enumerate((root / 'frontend/test-results').glob('**/reading-diagnostics.json')):
            (out / f'reading-diagnostics-{i}.json').write_bytes(file.read_bytes())
        for i, file in enumerate((root / 'frontend/test-results').glob('**/reader-loading-diagnostics.json')):
            (out / f'reader-loading-diagnostics-{i}.json').write_bytes(file.read_bytes())
        for i, file in enumerate((root / 'frontend/test-results').glob('**/reader-soak-diagnostics.json')):
            (out / f'reader-soak-diagnostics-{i}.json').write_bytes(file.read_bytes())
        metrics = []
        for file in out.glob('reading-diagnostics-*.json'):
            diagnostic = json.loads(file.read_text(encoding='utf-8'))
            if isinstance(diagnostic, list):
                metrics.extend(json.loads(event['detail']) for event in diagnostic if event.get('event') == 'cold-resources')
            elif diagnostic.get('cold_resources'):
                metrics.append(diagnostic['cold_resources'])
        result['browser_resource_metrics'] = metrics
        if soak_duration:
            reports = [json.loads(file.read_text(encoding='utf-8')) for file in out.glob('reader-soak-diagnostics-*.json')]
            checks['soak_case_executed'] = run.returncode == 0 and b'1 passed' in run.stdout and len(reports) == 1
            checks.update(soak_checks(reports[0] if len(reports) == 1 else None, soak_duration, a.soak_probe))
            states = {name: json.loads(docker('inspect', name).stdout)[0] for kind, name in objects if kind == 'container'}
            result['soak_container_states'] = {name: {'running': item['State']['Running'], 'oom_killed': item['State']['OOMKilled'], 'restart_count': item['RestartCount']} for name, item in states.items()}
            checks['soak_containers_healthy'] = all(item['running'] and not item['oom_killed'] and item['restart_count'] == 0 for item in result['soak_container_states'].values())
            sample_file = out / 'memory-samples.json'
            samples = json.loads(sample_file.read_text(encoding='utf-8')) if sample_file.exists() else []
            result['valid_resource_sample_count'] = valid_resource_samples(samples, list(states))
            checks['soak_resources_sampled'] = result['valid_resource_sample_count'] >= (1 if a.soak_probe else 25)
        elif a.lifecycle_only:
            result['lifecycle_diagnostic_count'] = len(list(out.glob('reader-loading-diagnostics-*.json')))
            checks['reader_lifecycle_executed'] = run.returncode == 0 and bool(re.search(rb'\b19 passed\b', run.stdout)) and result['lifecycle_diagnostic_count'] == 19
        elif a.login_only:
            reports = [json.loads(file.read_text(encoding='utf-8')) for file in out.glob('reading-diagnostics-*.json')]
            checks['both_browser_logins_executed'] = run.returncode == 0 and b'1 passed' in run.stdout and len(reports) == 1 and reports[0].get('mode') == 'login_navigation' and reports[0].get('completed_logins') == 2
        else:
            checks['both_browser_samples_executed'] = run.returncode == 0 and b'2 passed' in run.stdout and len(metrics) == 2 and all(m.get('asset_count', 0) > 0 for m in metrics)
    limited = []
    login_statuses = []
    for _ in range(12):
        status, headers, body, _ = http(base, '/api/v1/auth/login', b'{', {'Content-Type': 'application/json'})
        login_statuses.append(status)
        if status == 429:
            try:
                limited.append(json.loads(body).get('error', {}).get('code') == 'rate_limited')
            except ValueError:
                limited.append(False)
    logs = docker('logs', front).stdout + docker('logs', front).stderr
    checks['login_rate_limit_json'] = bool(limited) and all(limited) and b'limiting requests' in logs
    checks['login_invalid_json_rejected'] = 400 in login_statuses and all(status in (400, 429) for status in login_statuses)
    result['login_probe_statuses'] = login_statuses
    result['login_429_count'] = len(limited)
    result['passed'] = all(checks.values()) and (a.http_only or result.get('browser_exit') == 0)
finally:
    for kind, name in reversed(objects):
        r = docker('inspect' if kind == 'container' else 'network', *([] if kind == 'container' else ['inspect']), name, allowed=True)
        ok = r.returncode != 0
        if not ok:
            info = json.loads(r.stdout)[0]
            labels = info.get('Config', {}).get('Labels', {}) if kind == 'container' else info.get('Labels', {})
            if labels.get(label_key) == ident:
                if kind == 'container':
                    logs = docker('logs', name, allowed=True)
                    (out / (name.rsplit('-', 1)[-1] + '.log')).write_bytes(logs.stdout + logs.stderr)
                    ok = docker('rm', '-f', '-v', name, allowed=True).returncode == 0
                else:
                    ok = docker('network', 'rm', name, allowed=True).returncode == 0
        result['cleanup'][name] = ok
    result['passed'] = result['passed'] and all(result['cleanup'].values())
    (out / 'result.json').write_text(json.dumps(result, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
    print(out / 'result.json', flush=True)
    if not all(result['cleanup'].values()):
        raise RuntimeError('owned cleanup incomplete')
if not result['passed']:
    raise SystemExit(2)
