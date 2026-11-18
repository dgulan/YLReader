"""Exercise the read-only target inspector in an owned, network-isolated cluster."""
from pathlib import Path
import json
import subprocess
import time
import uuid

ROOT = Path(__file__).resolve().parents[1]
RUN = uuid.uuid4().hex[:12]
PG = 'ylreader-plan-pg-' + RUN
GO = 'ylreader-plan-test-' + RUN
OUT = ROOT / '.toolchain' / ('backup-plan-db-' + RUN)
OUT.mkdir(parents=True)
result = {'run_id': RUN, 'passed': False, 'cleanup': {}}


def run(*args, data=None, allowed=False, timeout=180):
    process = subprocess.run(['docker', *args], input=data, capture_output=True, timeout=timeout)
    if process.returncode and not allowed:
        raise RuntimeError(process.stderr.decode('utf-8', errors='replace'))
    return process


try:
    image = run('image', 'inspect', 'postgres:16', '--format', '{{.Id}}').stdout.decode().strip()
    result['postgres_image'] = image
    run('run', '-d', '--pull=never', '--name', PG, '--network', 'none',
        '--label', 'ylreader.plan-prototype=' + RUN,
        '--tmpfs', '/var/lib/postgresql/data:rw', '-e', 'POSTGRES_HOST_AUTH_METHOD=trust', image)
    for _ in range(45):
        probe = run('exec', PG, 'psql', '-XqAt', '-h', '127.0.0.1', '-U', 'postgres', '-d', 'postgres', '-c', 'SELECT 1', allowed=True)
        if probe.returncode == 0:
            break
        time.sleep(1)
    else:
        raise RuntimeError('isolated PostgreSQL TCP did not become ready')
    info = json.loads(run('inspect', PG).stdout)[0]
    assert info['HostConfig']['NetworkMode'] == 'none' and not info['HostConfig'].get('PortBindings') and not info['HostConfig'].get('Binds')
    assert '/var/lib/postgresql/data' in info['HostConfig']['Tmpfs']
    fixture = b'CREATE ROLE reader LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION;\nCREATE DATABASE yreader_a1_iso_plan OWNER reader;\n'
    run('exec', '-i', PG, 'psql', '-Xq', '-v', 'ON_ERROR_STOP=1', '-U', 'postgres', data=fixture)
    result['postgres_version'] = run('exec', PG, 'psql', '-XqAt', '-U', 'postgres', '-c', 'SHOW server_version').stdout.decode().strip()
    process = run('run', '--rm', '--pull=never', '--name', GO,
                  '--label', 'ylreader.plan-prototype=' + RUN,
                  '--network', 'container:' + PG, '--read-only', '--user', '10001:10001',
                  '--tmpfs', '/tmp:rw,exec,mode=1777', '-e', 'GOCACHE=/cache', '-e', 'GOTMPDIR=/tmp',
                  '-e', 'GOTOOLCHAIN=local', '-e', 'GOPROXY=off',
                  '-e', 'BACKUP_INSPECT_DSN=postgres://reader:@127.0.0.1:5432/yreader_a1_iso_plan?sslmode=disable',
                  '--mount', f'type=bind,source={ROOT / ".toolchain/backup-linux-gocache"},target=/cache',
                  '--mount', f'type=bind,source={ROOT / "backend"},target=/src,readonly',
                  '--mount', f'type=bind,source={ROOT / "docs"},target=/docs,readonly',
                  '--workdir', '/src', '--entrypoint', 'go', 'yreader-pre-release:race',
                  'test', '-race', '-tags', 'integration', '-run', '^TestPostgresTargetInspectorIsolated$',
                  '-json', './internal/backup', '-count=1', allowed=True)
    (OUT / 'tests.jsonl').write_bytes(process.stdout)
    (OUT / 'tests.stderr').write_bytes(process.stderr)
    if process.returncode:
        raise RuntimeError('isolated inspector test failed; see tests.jsonl/stderr')
    events = [json.loads(line) for line in process.stdout.decode().splitlines() if line.startswith('{')]
    assert not any(e.get('Action') in ('fail', 'skip') for e in events)
    passes = [e for e in events if e.get('Test') and e.get('Action') == 'pass']
    assert len(passes) == 5
    result.update(passed=True, passing_events=len(passes))
    print('PASS real PostgreSQL inspector: empty target, competing connection, table/function/type/schema, privileged owner and no persistent inspector writes')
finally:
    for name in [GO, PG]:
        label = run('inspect', name, '--format', '{{index .Config.Labels "ylreader.plan-prototype"}}', allowed=True)
        if label.returncode == 0 and label.stdout.decode().strip() == RUN:
            result['cleanup'][name] = run('rm', '-f', '-v', name, allowed=True).returncode == 0
        else:
            result['cleanup'][name] = label.returncode != 0
    (OUT / 'result.json').write_text(json.dumps(result, indent=2) + '\n', encoding='utf-8')
    print('Report:', OUT / 'result.json')
    if not all(result['cleanup'].values()):
        raise RuntimeError('owned container cleanup failed')
