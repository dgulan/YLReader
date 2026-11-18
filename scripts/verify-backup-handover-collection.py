"""Disposable PG/Redis collection fixture, partial review only; no business DSN."""
import hashlib
import json
from pathlib import Path
import re
import subprocess
import time
import uuid

ROOT = Path(__file__).resolve().parents[1]
BINARY = ROOT / '.toolchain/backup-handover-collection-linux.test'
RUN = uuid.uuid4().hex[:12]
PG = 'ylreader-collection-pg-' + RUN
SOURCE = 'ylreader-collection-source-' + RUN
TARGET = 'ylreader-collection-target-' + RUN
TEST = 'ylreader-collection-test-' + RUN
OUT = ROOT / '.toolchain' / ('backup-handover-collection-' + RUN)


def docker(*args, data=None, allow=False):
    p = subprocess.run(['docker', *args], input=data, capture_output=True, timeout=90)
    if p.returncode and not allow:
        raise RuntimeError('Disposable collection fixture command failed')
    return p


def main():
    if not BINARY.is_file() or BINARY.is_symlink():
        raise RuntimeError('Build .toolchain/backup-handover-collection-linux.test first')
    images = [docker('image', 'inspect', tag, '--format', '{{.Id}}').stdout.decode().strip()
              for tag in ['postgres:16', 'redis:7-alpine']]
    assert all(re.fullmatch('sha256:[0-9a-f]{64}', image) for image in images)
    OUT.mkdir(parents=True, exist_ok=False)
    result = {'run_id': RUN, 'passed': False, 'postgres_image': images[0], 'redis_image': images[1],
              'test_binary_sha256': hashlib.sha256(BINARY.read_bytes()).hexdigest(), 'cleanup': {}}
    attempted = set()
    try:
        attempted.add(PG)
        docker('run', '-d', '--pull=never', '--name', PG, '--network', 'none',
               '--label', 'ylreader.handover-collection=' + RUN,
               '--cpus', '1', '--memory', '512m', '--pids-limit', '256',
               '--tmpfs', '/var/lib/postgresql/data:rw,size=268435456',
               '-e', 'POSTGRES_HOST_AUTH_METHOD=trust', images[0])
        for _ in range(40):
            p = docker('exec', PG, 'psql', '-XqAt', '-h', '127.0.0.1', '-U', 'postgres', '-c', 'SELECT 1', allow=True)
            if p.returncode == 0:
                break
            time.sleep(1)
        else:
            raise RuntimeError('Disposable PG not ready')
        info = json.loads(docker('inspect', PG).stdout)[0]
        assert info['HostConfig']['NetworkMode'] == 'none'
        assert not info['HostConfig'].get('PortBindings') and not info['HostConfig'].get('Binds')
        assert '/var/lib/postgresql/data' in info['HostConfig']['Tmpfs']
        pg_id = info['Id']
        docker('exec', '-i', PG, 'psql', '-XqAt', '-v', 'ON_ERROR_STOP=1', '-U', 'postgres',
               data=b'CREATE ROLE reader LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;\nCREATE DATABASE yreader_a1_iso_collection OWNER reader;\n')
        pins = []
        for name, port in [(SOURCE, '16379'), (TARGET, '16380')]:
            attempted.add(name)
            docker('run', '-d', '--pull=never', '--name', name, '--network', 'container:' + PG,
                   '--label', 'ylreader.handover-collection=' + RUN, '--read-only',
                   '--cpus', '1', '--memory', '128m', '--pids-limit', '128', '--tmpfs', '/data:rw,size=16777216',
                   images[1], 'redis-server', '--bind', '127.0.0.1', '--port', port,
                   '--save', '', '--appendonly', 'no', '--protected-mode', 'yes')
            for _ in range(30):
                p = docker('exec', name, 'redis-cli', '-p', port, 'PING', allow=True)
                if p.returncode == 0 and p.stdout.strip() == b'PONG':
                    break
                time.sleep(1)
            else:
                raise RuntimeError('Disposable Redis not ready')
            state = json.loads(docker('inspect', name).stdout)[0]
            assert state['HostConfig']['NetworkMode'] in {'container:' + PG, 'container:' + pg_id}
            assert not state['HostConfig'].get('PortBindings') and not state['HostConfig'].get('Binds')
            assert state['HostConfig']['ReadonlyRootfs'] and '/data' in state['HostConfig']['Tmpfs']
            server = docker('exec', name, 'redis-cli', '-p', port, '--raw', 'INFO', 'server').stdout.decode()
            ids = re.findall(r'^run_id:([0-9a-f]{40})\r?$', server, re.M)
            assert len(ids) == 1
            pins.append(ids[0])
        assert pins[0] != pins[1]
        attempted.add(TEST)
        p = docker('run', '--pull=never', '--name', TEST,
                   '--label', 'ylreader.handover-collection=' + RUN, '--network', 'container:' + PG,
                   '--read-only', '--user', '10001:10001', '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges',
                   '--cpus', '1', '--memory', '256m', '--pids-limit', '128', '--tmpfs', '/tmp:rw,size=33554432,mode=1777',
                   '--mount', 'type=bind,source=' + str(BINARY) + ',target=/verify/backup.test,readonly',
                   '--mount', 'type=bind,source=' + str(ROOT / 'backend') + ',target=/src,readonly',
                   '--mount', 'type=bind,source=' + str(ROOT / 'docs') + ',target=/docs,readonly',
                   '--workdir', '/src/internal/backup', '-e', 'BACKUP_COLLECTION_ISOLATED=1',
                   '-e', 'IDENTITY_SOURCE_RUN_ID=' + pins[0], '-e', 'IDENTITY_TARGET_RUN_ID=' + pins[1],
                   '--entrypoint', '/verify/backup.test', images[0],
                   '-test.v', '-test.run', '^TestHandoverCollectionIsolated$', '-test.timeout=60s', allow=True)
        (OUT / 'tests.txt').write_bytes(p.stdout + p.stderr)
        assert p.returncode == 0, 'Collection fixture failed; see tests.txt'
        lines = p.stdout.decode().splitlines()
        passes = [s for s in lines if s.lstrip().startswith('--- PASS:')]
        assert len(passes) == 4 and not any('--- SKIP:' in s or '--- FAIL:' in s for s in lines)
        reports = [json.loads(m.group(1)) for s in lines
                   if (m := re.search(r'HANDOVER_COLLECTION_REPORT (\{.*\})$', s))]
        assert len(reports) == 1
        report = reports[0]
        assert set(report) == set('report_format report_version scope operation_id backup_id plan_digest package_sha256 manifest_sha256 target_instance_id target_storage_fingerprint publication_tree_fingerprint observed_generation operation_record_sha256 attempt_record_sha256 publication_record_sha256 target_identity started_at finished_at expires_at checks deployment_authorized ready_for_cutover'.split())
        assert report['report_format'] == 'ylreader-handover-review' and report['report_version'] == 1
        assert report['scope'] == 'isolated-localfs-handover-review'
        assert report['deployment_authorized'] is False and report['ready_for_cutover'] is False
        assert set(report['target_identity']) == {'database_identity_sha256', 'runtime_config_revision_sha256', 'redis_identity_sha256'}
        assert all(re.fullmatch('[0-9a-f]{64}', v) for v in report['target_identity'].values())
        ids = 'schema_ledger database_identity database_role published_objects header_keyring jwt_rotation redis_isolation sessions_revoked tasks_disabled runtime_behavior'.split()
        assert [c['id'] for c in report['checks']] == ids
        verified = {'schema_ledger', 'database_identity', 'database_role', 'redis_isolation'}
        assert all(set(c) == {'id', 'status'} and c['status'] == ('pass' if c['id'] in verified else 'unavailable') for c in report['checks'])
        (OUT / 'report.json').write_text(json.dumps(report, indent=2) + '\n', encoding='utf-8')
        for name in [PG, SOURCE, TARGET]:
            info = json.loads(docker('inspect', name).stdout)[0]
            assert info['State']['Running'] and not info['State']['OOMKilled'] and info['RestartCount'] == 0
        result.update(passed=True, passing_events=len(passes), verified_checks=4, unavailable_checks=6)
    finally:
        for name in [TEST, TARGET, SOURCE, PG]:
            if name not in attempted:
                result['cleanup'][name] = True
                continue
            info = docker('inspect', name, allow=True)
            if info.returncode:
                result['cleanup'][name] = b'No such' in info.stderr
                continue
            state = json.loads(info.stdout)[0]
            if state['Config'].get('Labels', {}).get('ylreader.handover-collection') == RUN:
                result['cleanup'][name] = docker('rm', '-f', '-v', name, allow=True).returncode == 0
            else:
                result['cleanup'][name] = False
        (OUT / 'result.json').write_text(json.dumps(result, indent=2) + '\n', encoding='utf-8')
        print('Report:', OUT / 'result.json', flush=True)
        if not all(result['cleanup'].values()):
            raise RuntimeError('Owned collection fixture cleanup unconfirmed')


if __name__ == '__main__':
    main()
