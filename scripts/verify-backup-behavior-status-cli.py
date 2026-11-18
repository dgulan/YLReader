"""Verify both read-only status commands in a network-none owned Linux fixture."""
import hashlib
import json
from pathlib import Path
import re
import subprocess
import uuid

ROOT = Path(__file__).resolve().parents[1]
TEST = ROOT / '.toolchain/backup-behavior-status-cli-linux.test'
CLI = ROOT / '.toolchain/backup-behavior-status-linux'
RUN = uuid.uuid4().hex[:12]
NAME = 'ylreader-behavior-status-cli-' + RUN
OUT = ROOT / '.toolchain' / ('backup-behavior-status-cli-' + RUN)


def docker(*args, allow=False):
    p = subprocess.run(['docker', *args], capture_output=True, timeout=90)
    if p.returncode and not allow:
        raise RuntimeError('Owned status CLI verification command failed')
    return p


def main():
    assert all(p.is_file() and not p.is_symlink() for p in [TEST, CLI])
    image = docker('image', 'inspect', 'redis:7-alpine', '--format', '{{.Id}}').stdout.decode().strip()
    assert re.fullmatch('sha256:[0-9a-f]{64}', image)
    OUT.mkdir(parents=True, exist_ok=False)
    result = {'passed': False, 'cleanup': False, 'run_id': RUN, 'runtime_image': image,
              'test_binary_sha256': hashlib.sha256(TEST.read_bytes()).hexdigest(),
              'cli_binary_sha256': hashlib.sha256(CLI.read_bytes()).hexdigest()}
    try:
        p = docker('run', '--pull=never', '--name', NAME, '--network', 'none', '--read-only',
                   '--label', 'ylreader.behavior-status-cli=' + RUN, '--user', '10001:10001',
                   '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges',
                   '--memory', '256m', '--cpus', '1', '--pids-limit', '128',
                   '--tmpfs', '/tmp:rw,size=33554432,mode=1777',
                   '--mount', 'type=bind,source=' + str(TEST) + ',target=/verify/status.test,readonly',
                   '--mount', 'type=bind,source=' + str(CLI) + ',target=/verify/backup-cli,readonly',
                   '--mount', 'type=bind,source=' + str(ROOT / 'backend') + ',target=/src,readonly',
                   '--mount', 'type=bind,source=' + str(ROOT / 'docs') + ',target=/docs,readonly',
                   '--workdir', '/src/internal/backup', '-e', 'BACKUP_STATUS_CLI=/verify/backup-cli',
                   '-e', 'BACKUP_BEHAVIOR_STATUS_CLI=/verify/backup-cli',
                   '--entrypoint', '/verify/status.test', image, '-test.v',
                   '-test.run', '^TestBackup(Status|BehaviorStatus)CLIIsolated$', '-test.timeout=45s', allow=True)
        (OUT / 'tests.txt').write_bytes(p.stdout + p.stderr)
        assert p.returncode == 0, 'Status CLI checks failed; see tests.txt'
        info = json.loads(docker('inspect', NAME).stdout)[0]
        assert info['HostConfig']['NetworkMode'] == 'none' and info['HostConfig']['ReadonlyRootfs']
        assert not info['HostConfig'].get('PortBindings') and info['Config']['User'] == '10001:10001'
        assert not info['State']['OOMKilled'] and info['RestartCount'] == 0
        lines = p.stdout.decode().splitlines()
        assert not any('--- FAIL:' in s or '--- SKIP:' in s for s in lines)
        passes = [s for s in lines if s.lstrip().startswith('--- PASS:')]
        assert len(passes) == 17
        behavior = [json.loads(m.group(1)) for s in lines if (m := re.search(r'BACKUP_BEHAVIOR_STATUS_EVIDENCE (\{.*\})$', s))]
        prior = [json.loads(m.group(1)) for s in lines if (m := re.search(r'BACKUP_STATUS_EVIDENCE (\{.*\})$', s))]
        assert len(behavior) == len(prior) == 1
        assert behavior[0]['scope'] == 'isolated-behavior-status-cli-fixture' and behavior[0]['checks'] == 8
        assert behavior[0]['content_and_modes_unchanged'] is True
        assert all(behavior[0][k] is False for k in ['dispatch_authorized', 'retry_authorized', 'deployment_authorized'])
        assert prior[0]['scope'] == 'isolated-status-cli-fixture' and prior[0]['checks'] == 7
        assert prior[0]['content_and_modes_unchanged'] is True
        assert prior[0]['ready_for_cutover'] is False and prior[0]['deployment_authorized'] is False
        (OUT / 'evidence.json').write_text(json.dumps({'behavior_status': behavior[0], 'status': prior[0]}, indent=2) + '\n', encoding='utf-8')
        result.update(passed=True, passing_events=len(passes), behavior_checks=8, prior_status_checks=7)
    finally:
        p = docker('inspect', NAME, allow=True)
        if p.returncode:
            result['cleanup'] = b'No such' in p.stderr
        else:
            info = json.loads(p.stdout)[0]
            if info['Config'].get('Labels', {}).get('ylreader.behavior-status-cli') == RUN:
                result['cleanup'] = docker('rm', '-f', '-v', NAME, allow=True).returncode == 0
        (OUT / 'result.json').write_text(json.dumps(result, indent=2) + '\n', encoding='utf-8')
        print('Report:', OUT / 'result.json', flush=True)
        if not result['cleanup']:
            raise RuntimeError('Owned status CLI fixture cleanup unconfirmed')


if __name__ == '__main__':
    main()
