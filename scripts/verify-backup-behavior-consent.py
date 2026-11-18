"""Exercise consent input with safe files in an owned network-none Linux fixture."""
import hashlib
import json
from pathlib import Path
import re
import subprocess
import uuid

ROOT = Path(__file__).resolve().parents[1]
TEST = ROOT / '.toolchain/backup-consent-linux.test'
RUN = uuid.uuid4().hex[:12]
NAME = 'ylreader-consent-' + RUN
OUT = ROOT / '.toolchain' / ('backup-consent-' + RUN)


def docker(*args, allow=False):
    p = subprocess.run(['docker', *args], capture_output=True, timeout=60)
    if p.returncode and not allow:
        raise RuntimeError('Owned consent input fixture command failed')
    return p


def main():
    assert TEST.is_file() and not TEST.is_symlink()
    image = docker('image', 'inspect', 'redis:7-alpine', '--format', '{{.Id}}').stdout.decode().strip()
    assert re.fullmatch('sha256:[0-9a-f]{64}', image)
    OUT.mkdir(parents=True, exist_ok=False)
    result = {'passed': False, 'cleanup': False, 'run_id': RUN, 'runtime_image': image,
              'test_binary_sha256': hashlib.sha256(TEST.read_bytes()).hexdigest()}
    try:
        p = docker('run', '--pull=never', '--name', NAME, '--network', 'none', '--read-only',
                   '--label', 'ylreader.consent-input=' + RUN, '--user', '10001:10001',
                   '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges',
                   '--memory', '256m', '--cpus', '1', '--pids-limit', '128',
                   '--tmpfs', '/tmp:rw,size=33554432,mode=1777',
                   '--mount', 'type=bind,source=' + str(TEST) + ',target=/verify/consent.test,readonly',
                   '--mount', 'type=bind,source=' + str(ROOT / 'backend') + ',target=/src,readonly',
                   '--mount', 'type=bind,source=' + str(ROOT / 'docs') + ',target=/docs,readonly',
                   '--workdir', '/src/internal/backup', '-e', 'BACKUP_CONSENT_FIXTURE=isolated-private-files',
                   '--entrypoint', '/verify/consent.test', image, '-test.v',
                   '-test.run', '^TestBehaviorConsentSecureFiles$', '-test.timeout=20s', allow=True)
        (OUT / 'tests.txt').write_bytes(p.stdout + p.stderr)
        assert p.returncode == 0, 'Consent verification failed; see tests.txt'
        info = json.loads(docker('inspect', NAME).stdout)[0]
        assert info['HostConfig']['NetworkMode'] == 'none' and info['HostConfig']['ReadonlyRootfs']
        assert not info['HostConfig'].get('PortBindings') and info['Config']['User'] == '10001:10001'
        assert not info['State']['OOMKilled'] and info['RestartCount'] == 0
        lines = p.stdout.decode().splitlines()
        passes = [s for s in lines if s.lstrip().startswith('--- PASS:')]
        assert len(passes) == 4 and not any('--- FAIL:' in s or '--- SKIP:' in s for s in lines)
        evidence = [json.loads(m.group(1)) for s in lines if (m := re.search(r'BACKUP_CONSENT_EVIDENCE (\{.*\})$', s))]
        assert len(evidence) == 1 and evidence[0]['content_and_modes_unchanged'] is True
        assert evidence[0]['network_used'] is False and evidence[0]['execution_authorized'] is False
        (OUT / 'evidence.json').write_text(json.dumps(evidence[0], indent=2) + '\n', encoding='utf-8')
        result.update(passed=True, passing_events=len(passes))
    finally:
        p = docker('inspect', NAME, allow=True)
        if p.returncode:
            result['cleanup'] = b'No such' in p.stderr
        else:
            info = json.loads(p.stdout)[0]
            if info['Config'].get('Labels', {}).get('ylreader.consent-input') == RUN:
                result['cleanup'] = docker('rm', '-f', '-v', NAME, allow=True).returncode == 0
        (OUT / 'result.json').write_text(json.dumps(result, indent=2) + '\n', encoding='utf-8')
        print('Report:', OUT / 'result.json', flush=True)
        if not result['cleanup']:
            raise RuntimeError('Owned consent fixture cleanup unconfirmed')


if __name__ == '__main__':
    main()
