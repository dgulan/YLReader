"""Run standalone read-only status CLI in a network-none disposable fixture.

No database or Redis service is started. Uses only a cached Redis image as a
small Linux runtime for static Go binaries, plus a private tmpfs control root.
"""
import hashlib
import json
from pathlib import Path
import re
import subprocess
import uuid

ROOT = Path(__file__).resolve().parents[1]
TEST_BINARY = ROOT / '.toolchain/backup-status-cli-linux.test'
CLI_BINARY = ROOT / '.toolchain/backup-status-linux'
RUN = uuid.uuid4().hex[:12]
NAME = 'ylreader-status-cli-' + RUN
OUT = ROOT / '.toolchain' / ('backup-status-cli-' + RUN)


def docker(*args, allow=False):
    p = subprocess.run(['docker', *args], capture_output=True, timeout=90)
    if p.returncode and not allow:
        raise RuntimeError('Disposable status CLI fixture command failed')
    return p


def main():
    assert all(p.is_file() and not p.is_symlink() for p in [TEST_BINARY, CLI_BINARY]), 'Build test and CLI binaries first'
    image = docker('image', 'inspect', 'redis:7-alpine', '--format', '{{.Id}}').stdout.decode().strip()
    assert re.fullmatch('sha256:[0-9a-f]{64}', image)
    OUT.mkdir(parents=True, exist_ok=False)
    result = {'run_id': RUN, 'passed': False, 'cleanup': False, 'runtime_image': image,
              'test_binary_sha256': hashlib.sha256(TEST_BINARY.read_bytes()).hexdigest(),
              'cli_binary_sha256': hashlib.sha256(CLI_BINARY.read_bytes()).hexdigest()}
    try:
        p = docker('run', '--pull=never', '--name', NAME, '--network', 'none',
                   '--label', 'ylreader.backup-status-cli=' + RUN, '--read-only', '--user', '10001:10001',
                   '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges', '--cpus', '1', '--memory', '256m', '--pids-limit', '128',
                   '--tmpfs', '/tmp:rw,size=33554432,mode=1777', '--tmpfs', '/data:rw,size=1048576',
                   '--mount', 'type=bind,source=' + str(TEST_BINARY) + ',target=/verify/status.test,readonly',
                   '--mount', 'type=bind,source=' + str(CLI_BINARY) + ',target=/verify/backup-cli,readonly',
                   '--mount', 'type=bind,source=' + str(ROOT / 'backend') + ',target=/src,readonly',
                   '--mount', 'type=bind,source=' + str(ROOT / 'docs') + ',target=/docs,readonly',
                   '--workdir', '/src/internal/backup', '-e', 'BACKUP_STATUS_CLI=/verify/backup-cli',
                   '--entrypoint', '/verify/status.test', image,
                   '-test.v', '-test.run', '^TestBackupStatusCLIIsolated$', '-test.timeout=45s', allow=True)
        (OUT / 'tests.txt').write_bytes(p.stdout + p.stderr)
        assert p.returncode == 0, 'Standalone status CLI failed; see tests.txt'
        info = json.loads(docker('inspect', NAME).stdout)[0]
        assert info['HostConfig']['NetworkMode'] == 'none' and info['HostConfig']['ReadonlyRootfs']
        assert not info['HostConfig'].get('PortBindings')
        assert info['Config']['User'] == '10001:10001'
        assert not info['State']['OOMKilled'] and info['RestartCount'] == 0
        assert set(info['HostConfig']['Tmpfs']) == {'/tmp', '/data'}
        lines = p.stdout.decode().splitlines()
        passes = [s for s in lines if s.lstrip().startswith('--- PASS:')]
        assert len(passes) == 8 and not any('--- SKIP:' in s or '--- FAIL:' in s for s in lines)
        reports = [json.loads(m.group(1)) for s in lines if (m := re.search(r'BACKUP_STATUS_EVIDENCE (\{.*\})$', s))]
        assert len(reports) == 1
        report = reports[0]
        assert set(report) == {'scope', 'checks', 'content_and_modes_unchanged', 'ready_for_cutover', 'deployment_authorized'}
        assert report['scope'] == 'isolated-status-cli-fixture' and report['checks'] == 7
        assert report['content_and_modes_unchanged'] is True
        assert report['ready_for_cutover'] is False and report['deployment_authorized'] is False
        (OUT / 'evidence.json').write_text(json.dumps(report, indent=2) + '\n', encoding='utf-8')
        result.update(passed=True, passing_events=len(passes))
    finally:
        p = docker('inspect', NAME, allow=True)
        if p.returncode:
            result['cleanup'] = b'No such' in p.stderr
        else:
            info = json.loads(p.stdout)[0]
            if info['Config'].get('Labels', {}).get('ylreader.backup-status-cli') == RUN:
                result['cleanup'] = docker('rm', '-f', '-v', NAME, allow=True).returncode == 0
        (OUT / 'result.json').write_text(json.dumps(result, indent=2) + '\n', encoding='utf-8')
        print('Report:', OUT / 'result.json', flush=True)
        if not result['cleanup']:
            raise RuntimeError('Owned status CLI container cleanup unconfirmed')


if __name__ == '__main__':
    main()
