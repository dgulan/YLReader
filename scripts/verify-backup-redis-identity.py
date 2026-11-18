"""Disposable Redis identity pair; no business URL, network, or service startup."""
import hashlib
import json
from pathlib import Path
import re
import subprocess
import time
import uuid

ROOT = Path(__file__).resolve().parents[1]
BINARY = ROOT / '.toolchain/backup-redis-identity-linux.test'
RUN = uuid.uuid4().hex[:12]
SOURCE = 'ylreader-identity-redis-source-' + RUN
TARGET = 'ylreader-identity-redis-target-' + RUN
TEST = 'ylreader-identity-redis-test-' + RUN
OUT = ROOT / '.toolchain' / ('backup-redis-identity-' + RUN)


def docker(*args, allow=False):
    p = subprocess.run(['docker', *args], capture_output=True, timeout=60)
    if p.returncode and not allow:
        raise RuntimeError('Disposable Redis fixture command failed')
    return p


def main():
    if not BINARY.is_file() or BINARY.is_symlink():
        raise RuntimeError('Build .toolchain/backup-redis-identity-linux.test first')
    image = docker('image', 'inspect', 'redis:7-alpine', '--format', '{{.Id}}').stdout.decode().strip()
    assert re.fullmatch('sha256:[0-9a-f]{64}', image)
    OUT.mkdir(parents=True, exist_ok=False)
    result = {'run_id': RUN, 'passed': False, 'redis_image': image,
              'test_binary_sha256': hashlib.sha256(BINARY.read_bytes()).hexdigest(), 'cleanup': {}}
    attempted = set()
    try:
        pins = []
        source_id = ''
        for name, port in [(SOURCE, '16379'), (TARGET, '16380')]:
            attempted.add(name)
            mode = 'none' if name == SOURCE else 'container:' + SOURCE
            docker('run', '-d', '--pull=never', '--name', name, '--network', mode,
                   '--label', 'ylreader.redis-identity=' + RUN, '--read-only',
                   '--cpus', '1', '--memory', '128m', '--pids-limit', '128',
                   '--tmpfs', '/data:rw,size=16777216', image,
                   'redis-server', '--bind', '127.0.0.1', '--port', port,
                   '--save', '', '--appendonly', 'no', '--protected-mode', 'yes')
            for _ in range(30):
                p = docker('exec', name, 'redis-cli', '-p', port, 'PING', allow=True)
                if p.returncode == 0 and p.stdout.strip() == b'PONG':
                    break
                time.sleep(1)
            else:
                raise RuntimeError('Disposable Redis not ready')
            info = json.loads(docker('inspect', name).stdout)[0]
            if name == SOURCE:
                assert info['HostConfig']['NetworkMode'] == 'none'
                source_id = info['Id']
            else:
                assert info['HostConfig']['NetworkMode'] in {'container:' + SOURCE, 'container:' + source_id}
            assert not info['HostConfig'].get('PortBindings') and not info['HostConfig'].get('Binds')
            assert info['HostConfig']['ReadonlyRootfs'] and '/data' in info['HostConfig']['Tmpfs']
            server = docker('exec', name, 'redis-cli', '-p', port, '--raw', 'INFO', 'server').stdout.decode()
            found = re.findall(r'^run_id:([0-9a-f]{40})\r?$', server, re.M)
            assert len(found) == 1
            pins.append(found[0])
        assert pins[0] != pins[1]
        acl = docker('exec', TARGET, 'redis-cli', '-p', '16380', 'ACL', 'SETUSER',
                     'identity_ro', 'reset', 'on', '>synthetic-only', '~ylreader:handover:*', '+get', '+select')
        assert acl.stdout.strip() == b'OK'
        attempted.add(TEST)
        p = docker('run', '--pull=never', '--name', TEST,
                   '--label', 'ylreader.redis-identity=' + RUN,
                   '--network', 'container:' + SOURCE, '--read-only', '--user', '10001:10001',
                   '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges',
                   '--cpus', '1', '--memory', '256m', '--pids-limit', '128',
                   '--tmpfs', '/tmp:rw,size=16777216', '--tmpfs', '/data:rw,size=1048576',
                   '--mount', 'type=bind,source=' + str(BINARY) + ',target=/verify/backup.test,readonly',
                   '-e', 'BACKUP_REDIS_IDENTITY_ISOLATED=1',
                   '-e', 'IDENTITY_SOURCE_RUN_ID=' + pins[0], '-e', 'IDENTITY_TARGET_RUN_ID=' + pins[1],
                   '--entrypoint', '/verify/backup.test', image,
                   '-test.v', '-test.run', '^TestHandoverRedisIdentityIsolated$', '-test.timeout=30s', allow=True)
        (OUT / 'tests.txt').write_bytes(p.stdout + p.stderr)
        assert p.returncode == 0, 'Redis fixture failed; see tests.txt'
        lines = p.stdout.decode().splitlines()
        passes = [s for s in lines if s.lstrip().startswith('--- PASS:')]
        assert len(passes) == 12 and not any('--- SKIP:' in s or '--- FAIL:' in s for s in lines)
        reports = [json.loads(m.group(1)) for s in lines
                   if (m := re.search(r'REDIS_IDENTITY_EVIDENCE (\{.*\})$', s))]
        assert len(reports) == 1
        report = reports[0]
        assert set(report) == {'scope', 'version', 'redis_identity_sha256',
                               'witnesses_and_sentinels_retained', 'deployment_authorized', 'ready_for_cutover'}
        assert report['scope'] == 'isolated-redis-identity-fixture' and report['version'] == 1
        assert re.fullmatch('[0-9a-f]{64}', report['redis_identity_sha256'])
        assert report['witnesses_and_sentinels_retained'] is True
        assert report['deployment_authorized'] is False and report['ready_for_cutover'] is False
        (OUT / 'evidence.json').write_text(json.dumps(report, indent=2) + '\n', encoding='utf-8')
        for name in [SOURCE, TARGET]:
            state = json.loads(docker('inspect', name).stdout)[0]
            assert state['State']['Running'] and not state['State']['OOMKilled'] and state['RestartCount'] == 0
        result.update(passed=True, passing_events=len(passes))
    finally:
        for name in [TEST, TARGET, SOURCE]:
            if name not in attempted:
                result['cleanup'][name] = True
                continue
            p = docker('inspect', name, allow=True)
            if p.returncode:
                result['cleanup'][name] = b'No such' in p.stderr
                continue
            info = json.loads(p.stdout)[0]
            if info['Config'].get('Labels', {}).get('ylreader.redis-identity') == RUN:
                result['cleanup'][name] = docker('rm', '-f', '-v', name, allow=True).returncode == 0
            else:
                result['cleanup'][name] = False
        (OUT / 'result.json').write_text(json.dumps(result, indent=2) + '\n', encoding='utf-8')
        print('Report:', OUT / 'result.json', flush=True)
        if not all(result['cleanup'].values()):
            raise RuntimeError('Owned Redis fixture cleanup unconfirmed')


if __name__ == '__main__':
    main()
