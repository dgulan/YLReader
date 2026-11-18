"""Verify authenticated read-only CLI against an owned network-none PostgreSQL fixture."""
import hashlib
import json
from pathlib import Path
import re
import subprocess
import time
import uuid

ROOT = Path(__file__).resolve().parents[1]
RUN = uuid.uuid4().hex[:12]
PG = 'ylreader-preview-pg-' + RUN
CLI = 'ylreader-preview-cli-' + RUN
OUT = ROOT / '.toolchain' / ('backup-preview-cli-' + RUN)
TEST = ROOT / '.toolchain/backup-preview-cli-linux.test'
BINARY = ROOT / '.toolchain/backup-preview-linux'


def docker(*args, data=None, allow=False):
    p = subprocess.run(['docker', *args], input=data, capture_output=True, timeout=90)
    if p.returncode and not allow:
        raise RuntimeError('Owned preview fixture command failed')
    return p


def main():
    assert all(p.is_file() and not p.is_symlink() for p in [TEST, BINARY])
    OUT.mkdir(parents=True, exist_ok=False)
    result = {'run_id': RUN, 'passed': False, 'cleanup': {},
              'test_binary_sha256': hashlib.sha256(TEST.read_bytes()).hexdigest(),
              'cli_binary_sha256': hashlib.sha256(BINARY.read_bytes()).hexdigest()}
    try:
        pg = docker('image', 'inspect', 'postgres:16', '--format', '{{.Id}}').stdout.decode().strip()
        runtime = docker('image', 'inspect', 'redis:7-alpine', '--format', '{{.Id}}').stdout.decode().strip()
        assert all(re.fullmatch('sha256:[0-9a-f]{64}', s) for s in [pg, runtime])
        result.update(postgres_image=pg, runtime_image=runtime)
        docker('run', '-d', '--pull=never', '--name', PG, '--network', 'none',
               '--label', 'ylreader.preview-cli=' + RUN,
               '--cpus', '1', '--memory', '256m', '--pids-limit', '128',
               '--tmpfs', '/var/lib/postgresql/data:rw,size=134217728',
               '-e', 'POSTGRES_HOST_AUTH_METHOD=trust', pg)
        for _ in range(40):
            p = docker('exec', PG, 'psql', '-XqAt', '-h', '127.0.0.1', '-U', 'postgres', '-c', 'SELECT 1', allow=True)
            if p.returncode == 0:
                break
            time.sleep(1)
        else:
            raise RuntimeError('Owned PostgreSQL fixture unavailable')
        docker('exec', '-i', PG, 'psql', '-Xq', '-v', 'ON_ERROR_STOP=1', '-U', 'postgres',
               data=b'CREATE ROLE reader LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION;\nCREATE DATABASE yreader_a1_iso_preview OWNER reader;\n')
        p = docker('run', '--pull=never', '--name', CLI, '--network', 'container:' + PG,
                   '--label', 'ylreader.preview-cli=' + RUN, '--read-only', '--user', '10001:10001',
                   '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges', '--cpus', '1', '--memory', '256m', '--pids-limit', '128',
                   '--tmpfs', '/tmp:rw,size=33554432,mode=1777', '--tmpfs', '/data:rw,size=1048576',
                   '--mount', 'type=bind,source=' + str(TEST) + ',target=/verify/preview.test,readonly',
                   '--mount', 'type=bind,source=' + str(BINARY) + ',target=/verify/backup-cli,readonly',
                   '--mount', 'type=bind,source=' + str(ROOT / 'backend') + ',target=/src,readonly',
                   '--mount', 'type=bind,source=' + str(ROOT / 'docs') + ',target=/docs,readonly',
                   '--workdir', '/src/internal/backup', '-e', 'BACKUP_PREVIEW_CLI=/verify/backup-cli',
                   '-e', 'BACKUP_PREVIEW_DSN=postgres://reader:@127.0.0.1:5432/yreader_a1_iso_preview?sslmode=disable',
                   '--entrypoint', '/verify/preview.test', runtime,
                   '-test.v', '-test.run', '^TestBackupPreviewCLIIsolated$', '-test.timeout=50s', allow=True)
        (OUT / 'tests.txt').write_bytes(p.stdout + p.stderr)
        assert p.returncode == 0, 'Preview CLI verification failed; see tests.txt'
        for name in [PG, CLI]:
            info = json.loads(docker('inspect', name).stdout)[0]
            assert not info['HostConfig'].get('PortBindings') and not info['State']['OOMKilled']
            assert info['RestartCount'] == 0
            if name == PG:
                assert info['HostConfig']['NetworkMode'] == 'none'
                assert not info['HostConfig'].get('Binds')
            else:
                assert info['HostConfig']['ReadonlyRootfs'] and info['Config']['User'] == '10001:10001'
        lines = p.stdout.decode().splitlines()
        passes = [s for s in lines if s.lstrip().startswith('--- PASS:')]
        assert len(passes) == 10 and not any('--- SKIP:' in s or '--- FAIL:' in s for s in lines)
        evidence = [json.loads(m.group(1)) for s in lines if (m := re.search(r'BACKUP_PREVIEW_EVIDENCE (\{.*\})$', s))]
        assert len(evidence) == 1 and evidence[0]['content_and_modes_unchanged'] is True
        assert evidence[0]['target_empty_after_plan'] is True
        assert evidence[0]['execution_authorized'] is False and evidence[0]['deployment_authorized'] is False
        (OUT / 'evidence.json').write_text(json.dumps(evidence[0], indent=2) + '\n', encoding='utf-8')
        result.update(passed=True, passing_events=len(passes))
    finally:
        for name in [CLI, PG]:
            p = docker('inspect', name, allow=True)
            if p.returncode:
                result['cleanup'][name] = b'No such' in p.stderr
            else:
                info = json.loads(p.stdout)[0]
                if info['Config'].get('Labels', {}).get('ylreader.preview-cli') == RUN:
                    result['cleanup'][name] = docker('rm', '-f', '-v', name, allow=True).returncode == 0
        (OUT / 'result.json').write_text(json.dumps(result, indent=2) + '\n', encoding='utf-8')
        print('Report:', OUT / 'result.json', flush=True)
        if not all(result['cleanup'].values()):
            raise RuntimeError('Owned preview fixture cleanup unconfirmed')


if __name__ == '__main__':
    main()
