"""Run only the disposable database identity fixture; accepts no database URL.

Requires an already cached postgres:16 and prebuilt Linux test binary. Creates
no host port, persistent volume, business service or production connection.
"""
import hashlib
import json
from pathlib import Path
import re
import subprocess
import time
import uuid

ROOT = Path(__file__).resolve().parents[1]
BINARY = ROOT / '.toolchain/backup-identity-linux.test'
RUN = uuid.uuid4().hex[:12]
PG = 'ylreader-identity-pg-' + RUN
TEST = 'ylreader-identity-test-' + RUN
OUT = ROOT / '.toolchain' / ('backup-database-identity-' + RUN)


def docker(*args, data=None, allow=False, timeout=90):
    p = subprocess.run(['docker', *args], input=data, capture_output=True, timeout=timeout)
    if p.returncode and not allow:
        raise RuntimeError('Docker fixture command failed; no business target was used')
    return p


def main():
    if not BINARY.is_file() or BINARY.is_symlink():
        raise RuntimeError('Build .toolchain/backup-identity-linux.test first')
    image = docker('image', 'inspect', 'postgres:16', '--format', '{{.Id}}').stdout.decode().strip()
    if not re.fullmatch(r'sha256:[0-9a-f]{64}', image):
        raise RuntimeError('Invalid cached image identity')
    OUT.mkdir(parents=True, exist_ok=False)
    result = {'run_id': RUN, 'passed': False, 'cleanup': {}, 'postgres_image': image,
              'test_binary_sha256': hashlib.sha256(BINARY.read_bytes()).hexdigest()}
    attempted = set()
    try:
        attempted.add(PG)
        docker('run', '-d', '--pull=never', '--name', PG, '--network', 'none',
               '--label', 'ylreader.database-identity=' + RUN,
               '--cpus', '1', '--memory', '512m', '--pids-limit', '256',
               '--tmpfs', '/var/lib/postgresql/data:rw,size=268435456',
               '-e', 'POSTGRES_HOST_AUTH_METHOD=trust', image)
        for _ in range(40):
            p = docker('exec', PG, 'psql', '-XqAt', '-h', '127.0.0.1', '-U', 'postgres',
                       '-c', 'SELECT 1', allow=True)
            if p.returncode == 0:
                break
            time.sleep(1)
        else:
            raise RuntimeError('Disposable PostgreSQL not ready')
        info = json.loads(docker('inspect', PG).stdout)[0]
        assert info['HostConfig']['NetworkMode'] == 'none'
        assert not info['HostConfig'].get('PortBindings') and not info['HostConfig'].get('Binds')
        assert '/var/lib/postgresql/data' in info['HostConfig']['Tmpfs']
        setup = b'CREATE ROLE reader LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;\nCREATE DATABASE yreader_a1_iso_identity OWNER reader;\n'
        docker('exec', '-i', PG, 'psql', '-XqAt', '-v', 'ON_ERROR_STOP=1', '-U', 'postgres', data=setup)
        result['postgres_version'] = docker('exec', PG, 'psql', '-XqAt', '-U', 'postgres',
                                             '-c', 'SHOW server_version').stdout.decode().strip()
        attempted.add(TEST)
        p = docker('run', '--pull=never', '--name', TEST,
                   '--label', 'ylreader.database-identity=' + RUN,
                   '--network', 'container:' + PG, '--read-only', '--user', '10001:10001',
                   '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges',
                   '--cpus', '1', '--memory', '256m', '--pids-limit', '128',
                   '--tmpfs', '/tmp:rw,size=16777216',
                   '--mount', 'type=bind,source=' + str(BINARY) + ',target=/verify/backup.test,readonly',
                   '-e', 'BACKUP_IDENTITY_ISOLATED=1', '--entrypoint', '/verify/backup.test', image,
                   '-test.v', '-test.run', '^TestHandoverDatabaseIdentityIsolated$', '-test.timeout=60s',
                   allow=True)
        (OUT / 'tests.txt').write_bytes(p.stdout + p.stderr)
        assert p.returncode == 0, 'Identity integration failed; see tests.txt'
        lines = p.stdout.decode('utf-8').splitlines()
        passes = [s for s in lines if s.lstrip().startswith('--- PASS:')]
        assert len(passes) == 13 and not any('--- SKIP:' in s or '--- FAIL:' in s for s in lines)
        reports = [json.loads(m.group(1)) for s in lines
                   if (m := re.search(r'DATABASE_IDENTITY_EVIDENCE (\{.*\})$', s))]
        assert len(reports) == 1
        report = reports[0]
        assert set(report) == {'scope', 'version', 'database_identity_sha256',
                               'readonly_fixture_unchanged', 'deployment_authorized', 'ready_for_cutover'}
        assert report['scope'] == 'isolated-database-identity-fixture' and report['version'] == 1
        assert re.fullmatch('[0-9a-f]{64}', report['database_identity_sha256'])
        assert report['readonly_fixture_unchanged'] is True
        assert report['deployment_authorized'] is False and report['ready_for_cutover'] is False
        (OUT / 'evidence.json').write_text(json.dumps(report, indent=2) + '\n', encoding='utf-8')
        state = json.loads(docker('inspect', PG).stdout)[0]
        assert state['State']['Running'] and not state['State']['OOMKilled'] and state['RestartCount'] == 0
        result.update(passed=True, passing_events=len(passes))
    finally:
        for name in [TEST, PG]:
            if name not in attempted:
                result['cleanup'][name] = True
                continue
            info = docker('inspect', name, allow=True)
            if info.returncode:
                result['cleanup'][name] = b'No such' in info.stderr
                continue
            record = json.loads(info.stdout)[0]
            if record['Config'].get('Labels', {}).get('ylreader.database-identity') == RUN:
                result['cleanup'][name] = docker('rm', '-f', '-v', name, allow=True).returncode == 0
            else:
                result['cleanup'][name] = False
        (OUT / 'result.json').write_text(json.dumps(result, indent=2) + '\n', encoding='utf-8')
        print('Report:', OUT / 'result.json', flush=True)
        if not all(result['cleanup'].values()):
            raise RuntimeError('Owned fixture cleanup unconfirmed')


if __name__ == '__main__':
    main()
