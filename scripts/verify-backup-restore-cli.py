"""Verify controlled restore CLI in an owned, network-none PostgreSQL + Redis container fixture."""
import hashlib
import json
from pathlib import Path
import re
import subprocess
import time
import uuid

ROOT = Path(__file__).resolve().parents[1]
RUN = uuid.uuid4().hex[:12]
PG = 'ylreader-restore-cli-pg-' + RUN
REDIS_SRC = 'ylreader-restore-cli-redis-src-' + RUN
REDIS_DST = 'ylreader-restore-cli-redis-dst-' + RUN
TEST = 'ylreader-restore-cli-test-' + RUN
OUT = ROOT / '.toolchain' / ('backup-restore-cli-' + RUN)
FIXTURE = ROOT / '.toolchain/backup-db-01437dc0b289/database.dump'
TEST_BINARY = ROOT / '.toolchain/backup-restore-cmd-linux.test'
CLI_BINARY = ROOT / '.toolchain/backup-restore-linux'


def docker(*args, data=None, allow=False, timeout=120):
    p = subprocess.run(['docker', *args], input=data, capture_output=True, timeout=timeout)
    if p.returncode and not allow:
        raise RuntimeError('Owned restore CLI fixture command failed')
    return p


def sql(db, command):
    return docker('exec', '-i', PG, 'psql', '-XqAt', '-v', 'ON_ERROR_STOP=1',
                  '-U', 'postgres', '-d', db, data=command.encode())


def main():
    assert all(p.is_file() and not p.is_symlink() for p in [FIXTURE, TEST_BINARY, CLI_BINARY]), 'Build restore CLI binaries and fixture first'
    OUT.mkdir(parents=True, exist_ok=False)
    result = {
        'run_id': RUN,
        'passed': False,
        'cleanup': {},
        'test_binary_sha256': hashlib.sha256(TEST_BINARY.read_bytes()).hexdigest(),
        'cli_binary_sha256': hashlib.sha256(CLI_BINARY.read_bytes()).hexdigest(),
        'source_fixture_sha256': hashlib.sha256(FIXTURE.read_bytes()).hexdigest(),
    }
    attempted = []
    try:
        pg = docker('image', 'inspect', 'postgres:16', '--format', '{{.Id}}').stdout.decode().strip()
        redis_img = docker('image', 'inspect', 'redis:7-alpine', '--format', '{{.Id}}').stdout.decode().strip()
        assert all(re.fullmatch('sha256:[0-9a-f]{64}', s) for s in [pg, redis_img])
        result.update(postgres_image=pg, redis_image=redis_img)

        attempted.append(PG)
        docker('run', '-d', '--pull=never', '--name', PG, '--network', 'none',
               '--label', 'ylreader.restore-cli=' + RUN,
               '--cpus', '1', '--memory', '512m', '--pids-limit', '256',
               '--tmpfs', '/var/lib/postgresql/data:rw,size=268435456',
               '-e', 'POSTGRES_HOST_AUTH_METHOD=trust', pg)
        for _ in range(45):
            p = docker('exec', PG, 'psql', '-XqAt', '-h', '127.0.0.1', '-U', 'postgres', '-c', 'SELECT 1', allow=True)
            if p.returncode == 0:
                break
            time.sleep(1)
        else:
            raise RuntimeError('Owned PostgreSQL fixture unavailable')

        sql('postgres', 'CREATE ROLE reader LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION;')
        sql('postgres', 'REVOKE EXECUTE ON FUNCTION pg_catalog.pg_control_system() FROM PUBLIC; GRANT EXECUTE ON FUNCTION pg_catalog.pg_control_system() TO reader;')
        sql('postgres', 'CREATE DATABASE yreader_a1_iso_restore_src;')
        docker('exec', '-i', PG, 'pg_restore', '--no-owner', '--no-acl', '--exit-on-error',
               '-U', 'postgres', '-d', 'yreader_a1_iso_restore_src', data=FIXTURE.read_bytes())
        for name in ['core', 'partial', 'fail']:
            sql('postgres', f'CREATE DATABASE yreader_a1_iso_restore_{name} OWNER reader;')

        pg_info = json.loads(docker('inspect', PG).stdout)[0]
        assert pg_info['HostConfig']['NetworkMode'] == 'none' and not pg_info['HostConfig'].get('PortBindings') and not pg_info['HostConfig'].get('Binds')
        pg_id = pg_info['Id']

        redis_ids = []
        for name, port in [(REDIS_SRC, '16379'), (REDIS_DST, '16380')]:
            attempted.append(name)
            docker('run', '-d', '--pull=never', '--name', name, '--network', 'container:' + PG,
                   '--label', 'ylreader.restore-cli=' + RUN, '--read-only',
                   '--cpus', '1', '--memory', '128m', '--pids-limit', '128',
                   '--tmpfs', '/data:rw,size=16777216',
                   redis_img, 'redis-server', '--bind', '127.0.0.1', '--port', port,
                   '--save', '', '--appendonly', 'no', '--protected-mode', 'yes')
            for _ in range(30):
                p = docker('exec', name, 'redis-cli', '-p', port, 'PING', allow=True)
                if p.returncode == 0 and p.stdout.strip() == b'PONG':
                    break
                time.sleep(1)
            else:
                raise RuntimeError('Owned Redis fixture unavailable')
            info = json.loads(docker('inspect', name).stdout)[0]
            assert info['HostConfig']['NetworkMode'] in {'container:' + PG, 'container:' + pg_id}
            assert info['HostConfig']['ReadonlyRootfs'] and not info['HostConfig'].get('PortBindings')
            srv = docker('exec', name, 'redis-cli', '-p', port, '--raw', 'INFO', 'server').stdout.decode()
            ids = re.findall(r'^run_id:([0-9a-f]{40})\r?$', srv, re.M)
            assert len(ids) == 1
            redis_ids.append(ids[0])
        assert redis_ids[0] != redis_ids[1]

        attempted.append(TEST)
        p = docker('run', '--pull=never', '--name', TEST, '--network', 'container:' + PG,
                   '--label', 'ylreader.restore-cli=' + RUN, '--read-only', '--user', '10001:10001',
                   '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges',
                   '--cpus', '1', '--memory', '512m', '--pids-limit', '256',
                   '--tmpfs', '/tmp:rw,size=134217728,mode=1777',
                   '--mount', 'type=bind,source=' + str(TEST_BINARY) + ',target=/verify/restore-cmd.test,readonly',
                   '--mount', 'type=bind,source=' + str(CLI_BINARY) + ',target=/verify/backup-cli,readonly',
                   '--mount', 'type=bind,source=' + str(ROOT / 'backend') + ',target=/src,readonly',
                   '--mount', 'type=bind,source=' + str(ROOT / 'docs') + ',target=/docs,readonly',
                   '--workdir', '/src/cmd/backup',
                   '-e', 'BACKUP_RESTORE_CLI=/verify/backup-cli',
                   '-e', 'BACKUP_RESTORE_PG_RESTORE=/usr/bin/pg_restore',
                   '-e', 'BACKUP_RESTORE_PG_DUMP=/usr/bin/pg_dump',
                   '-e', 'BACKUP_RESTORE_SOURCE_DSN=postgres://postgres:@127.0.0.1:5432/yreader_a1_iso_restore_src?sslmode=disable',
                   '-e', 'BACKUP_RESTORE_TARGET_BASE=postgres://reader:@127.0.0.1:5432',
                   '-e', 'BACKUP_RESTORE_SOURCE_REDIS=redis://127.0.0.1:16379/0',
                   '-e', 'BACKUP_RESTORE_TARGET_REDIS=redis://127.0.0.1:16380/0',
                   '-e', 'BACKUP_RESTORE_SOURCE_REDIS_ID=' + redis_ids[0],
                   '-e', 'BACKUP_RESTORE_TARGET_REDIS_ID=' + redis_ids[1],
                   '--entrypoint', '/verify/restore-cmd.test', pg,
                   '-test.v', '-test.run', '^TestBackupRestoreCLIIsolated$', '-test.timeout=90s', allow=True)
        (OUT / 'tests.txt').write_bytes(p.stdout + p.stderr)
        assert p.returncode == 0, 'Restore CLI verification failed; see tests.txt'

        info = json.loads(docker('inspect', TEST).stdout)[0]
        assert info['HostConfig']['ReadonlyRootfs'] and info['Config']['User'] == '10001:10001'
        assert not info['HostConfig'].get('PortBindings') and not info['State']['OOMKilled'] and info['RestartCount'] == 0

        lines = p.stdout.decode().splitlines()
        passes = [s for s in lines if s.lstrip().startswith('--- PASS:')]
        assert len(passes) == 8 and not any('--- SKIP:' in s or '--- FAIL:' in s for s in lines)
        evidence = [json.loads(m.group(1)) for s in lines if (m := re.search(r'BACKUP_RESTORE_CLI_EVIDENCE (\{.*\})$', s))]
        assert len(evidence) == 1
        assert evidence[0]['scope'] == 'isolated-controlled-restore-cli' and evidence[0]['checks'] == 7
        assert evidence[0]['phase'] == 'validating' and evidence[0]['partial_registration_denied'] is True
        assert evidence[0]['replay_denied'] is True
        assert evidence[0]['execution_authorized'] is False and evidence[0]['ready_for_cutover'] is False and evidence[0]['deployment_authorized'] is False
        (OUT / 'evidence.json').write_text(json.dumps(evidence[0], indent=2) + '\n', encoding='utf-8')
        result.update(passed=True, passing_events=len(passes))
    finally:
        for name in reversed(attempted):
            p = docker('inspect', name, allow=True)
            if p.returncode:
                result['cleanup'][name] = b'No such' in p.stderr
            else:
                info = json.loads(p.stdout)[0]
                if info['Config'].get('Labels', {}).get('ylreader.restore-cli') == RUN:
                    result['cleanup'][name] = docker('rm', '-f', '-v', name, allow=True).returncode == 0
        (OUT / 'result.json').write_text(json.dumps(result, indent=2) + '\n', encoding='utf-8')
        print('Report:', OUT / 'result.json', flush=True)
        if attempted and not all(result['cleanup'].values()):
            raise RuntimeError('Owned restore CLI fixture cleanup unconfirmed')


if __name__ == '__main__':
    main()
