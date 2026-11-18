"""Run only synthetic backup transaction tests in an owned, isolated cluster.

Requires a Linux test binary built with -tags=integration and the previously
generated synthetic database.dump. No business DSN or external network is used.
"""
from pathlib import Path
import argparse
import hashlib
import json
import re
import subprocess
import time
import uuid

ROOT = Path(__file__).resolve().parents[1]
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--fixture', type=Path, default=ROOT / '.toolchain/backup-db-01437dc0b289/database.dump')
parser.add_argument('--binary', type=Path, default=ROOT / '.toolchain/backup-restore.test')
parser.add_argument('--handover-data', action='store_true', help='also verify the guarded eight-check handover review')
parser.add_argument('--handover-behavior', action='store_true', help='bind same-operation authorized fixture HTTP behavior to the full review')
parser.add_argument('--handover-register', action='store_true', help='also CAS-register the confirmed isolated review without advancing its phase')
parser.add_argument('--behavior-accounts', action='store_true', help='verify generic restored accounts under the live guard before fixture HTTP login')
parser.add_argument('--behavior-dispatch', action='store_true', help='exercise one-shot dispatch with a test-only AuthService adapter in fresh restored targets')
parser.add_argument('--behavior-ledger', action='store_true', help='observe response chains and verify known-session revocation using read-only transactions')
parser.add_argument('--behavior-http', action='store_true', help='verify pinned loopback HTTPS with real API login/logout and no POST replay')
parser.add_argument('--behavior-review', action='store_true', help='collect/save a separate partial account review from the confirmed live HTTPS operation')
args = parser.parse_args()
if args.handover_behavior and not args.handover_data:
    parser.error('--handover-behavior requires --handover-data')
if args.handover_register and not args.handover_behavior:
    parser.error('--handover-register requires --handover-behavior')
if args.behavior_ledger and not args.behavior_dispatch:
    parser.error('--behavior-ledger requires --behavior-dispatch')
if args.behavior_http and not args.behavior_dispatch:
    parser.error('--behavior-http requires --behavior-dispatch')
if args.behavior_review and not args.behavior_http:
    parser.error('--behavior-review requires --behavior-http')
RUN = uuid.uuid4().hex[:12]
PG = 'ylreader-restore-pg-' + RUN
TEST = 'ylreader-restore-test-' + RUN
REDIS_SOURCE = 'ylreader-restore-redis-source-' + RUN
REDIS_TARGET = 'ylreader-restore-redis-target-' + RUN
OUT = ROOT / '.toolchain' / ('backup-restore-db-' + RUN)
OUT.mkdir(parents=True)
result = {'run_id': RUN, 'passed': False, 'cleanup': {}}
FIXTURE = args.fixture.resolve()
BINARY = args.binary.resolve()


def run(*args, data=None, allowed=False, timeout=240):
    p = subprocess.run(['docker', *args], input=data, capture_output=True, timeout=timeout)
    if p.returncode and not allowed:
        raise RuntimeError(p.stderr.decode('utf-8', errors='replace'))
    return p


def sql(db, command):
    return run('exec', '-i', PG, 'psql', '-XqAt', '-v', 'ON_ERROR_STOP=1',
               '-U', 'postgres', '-d', db, data=command.encode())


try:
    assert FIXTURE.is_file() and BINARY.is_file(), 'build test binary and synthetic fixture first'
    result['test_binary_sha256'] = hashlib.sha256(BINARY.read_bytes()).hexdigest()
    result['source_fixture_sha256'] = hashlib.sha256(FIXTURE.read_bytes()).hexdigest()
    image = run('image', 'inspect', 'postgres:16', '--format', '{{.Id}}').stdout.decode().strip()
    result['postgres_image'] = image
    run('run', '-d', '--pull=never', '--name', PG, '--network', 'none',
        '--label', 'ylreader.restore-prototype=' + RUN,
        '--tmpfs', '/var/lib/postgresql/data:rw', '-e', 'POSTGRES_HOST_AUTH_METHOD=trust', image)
    for _ in range(45):
        probe = run('exec', PG, 'psql', '-XqAt', '-h', '127.0.0.1', '-U', 'postgres', '-c', 'SELECT 1', allowed=True)
        if probe.returncode == 0:
            break
        time.sleep(1)
    else:
        raise RuntimeError('isolated database did not become ready')
    info = json.loads(run('inspect', PG).stdout)[0]
    assert info['HostConfig']['NetworkMode'] == 'none' and not info['HostConfig'].get('PortBindings') and not info['HostConfig'].get('Binds')
    assert '/var/lib/postgresql/data' in info['HostConfig']['Tmpfs']
    network_container_id = info['Id']
    sql('postgres', 'CREATE ROLE reader LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION;')
    redis_image = run('image', 'inspect', 'redis:7-alpine', '--format', '{{.Id}}').stdout.decode().strip()
    result['redis_image'] = redis_image
    for name, port in [(REDIS_SOURCE, '16379'), (REDIS_TARGET, '16380')]:
        run('run', '-d', '--pull=never', '--name', name,
            '--label', 'ylreader.restore-prototype=' + RUN,
            '--network', 'container:' + PG, '--read-only', '--tmpfs', '/data:rw',
            redis_image, 'redis-server', '--bind', '127.0.0.1', '--port', port,
            '--save', '', '--appendonly', 'no', '--protected-mode', 'yes')
        for _ in range(30):
            probe = run('exec', name, 'redis-cli', '-p', port, 'PING', allowed=True)
            if probe.returncode == 0 and probe.stdout.strip() == b'PONG':
                break
            time.sleep(1)
        else:
            raise RuntimeError('isolated Redis did not become ready')
        info = json.loads(run('inspect', name).stdout)[0]
        assert info['HostConfig']['NetworkMode'] in {'container:' + PG, 'container:' + network_container_id}
        assert not info['HostConfig'].get('PortBindings') and not info['HostConfig'].get('Binds')
        assert '/data' in info['HostConfig']['Tmpfs'] and info['HostConfig']['ReadonlyRootfs']
    for name in ['restore', 'badfk', 'overflow', 'cancel', 'occupied', 'objects', 'missing', 'orphan', 'badref', 'crossuser', 'recordedgood', 'recordedrollback', 'recordedlost', 'recordedstate', 'recordedintent','headergood','headerwrong','headermissing','headeraad','headerinventory','headerplain','headerpubfail','headerpuback','headerpubintent','headerpubcopy','headerpubrename','headerhttp']:
        sql('postgres', f'CREATE DATABASE yreader_a1_iso_{name} OWNER reader;')
    if args.behavior_dispatch:
        for name in ['dispatch_good', 'dispatch_lost', 'dispatch_intent_ack', 'dispatch_response_ack']:
            sql('postgres', f'CREATE DATABASE yreader_a1_iso_{name} OWNER reader;')
    if args.behavior_http:
        for name in ['dispatch_http_good', 'dispatch_http_lost', 'dispatch_http_redirect', 'dispatch_http_wrong_pin']:
            sql('postgres', f'CREATE DATABASE yreader_a1_iso_{name} OWNER reader;')
    for name in ['fk_source', 'overflow_source', 'badref_source', 'crossuser_source','header_source']:
        sql('postgres', f'CREATE DATABASE yreader_a1_iso_{name};')
        run('exec', '-i', PG, 'pg_restore', '--no-owner', '--no-acl', '--exit-on-error',
            '-U', 'postgres', '-d', 'yreader_a1_iso_' + name, data=FIXTURE.read_bytes())
    # Synthetic corruption only: a late FK error after earlier tables loaded,
    # and an integer overflow after all tables and sequence values were loaded.
    sql('yreader_a1_iso_fk_source', "SET session_replication_role=replica; UPDATE jobs SET user_id='ffffffff-ffff-4fff-8fff-ffffffffffff';")
    sql('yreader_a1_iso_overflow_source', 'UPDATE users SET token_version=2147483647;')
    sql('yreader_a1_iso_badref_source', "UPDATE books SET active_book_url='local:wrong-book';")
    sql('yreader_a1_iso_crossuser_source', "UPDATE book_sources SET user_id='00000000-0000-4000-8000-000000000002' WHERE id='10000000-0000-4000-8000-000000000001';")
    for source, filename in [('fk_source', 'bad-fk.dump'), ('overflow_source', 'overflow.dump'), ('badref_source', 'bad-ref.dump'), ('crossuser_source', 'cross-user.dump')]:
        archive = run('exec', PG, 'pg_dump', '-Fc', '-U', 'postgres', '-d', 'yreader_a1_iso_' + source).stdout
        (OUT / filename).write_bytes(archive)
    result['postgres_version'] = sql('postgres', 'SHOW server_version').stdout.decode().strip()
    process = run('run', '--rm', '--pull=never', '--name', TEST,
                  '--label', 'ylreader.restore-prototype=' + RUN,
                  '--network', 'container:' + PG, '--read-only', '--user', '10001:10001',
                  '--tmpfs', '/tmp:rw,exec,mode=1777',
                  '-e', 'BACKUP_RESTORE_DSN=postgres://reader:@127.0.0.1:5432/yreader_a1_iso_restore?sslmode=disable',
                  '-e', 'BACKUP_PG_RESTORE=/usr/lib/postgresql/16/bin/pg_restore',
                  '-e', 'BACKUP_PG_DUMP=/usr/lib/postgresql/16/bin/pg_dump',
                  '-e', 'BACKUP_HEADER_SOURCE_DSN=postgres://postgres:@127.0.0.1:5432/yreader_a1_iso_header_source?sslmode=disable',
                  '-e', 'BACKUP_HTTP_SOURCE_REDIS_URL=redis://127.0.0.1:16379/0',
                  '-e', 'BACKUP_HTTP_TARGET_REDIS_URL=redis://127.0.0.1:16380/0',
                  '-e', 'BACKUP_HANDOVER_DATA=' + ('1' if args.handover_data else '0'),
                  '-e', 'BACKUP_HANDOVER_BEHAVIOR=' + ('1' if args.handover_behavior else '0'),
                  '-e', 'BACKUP_HANDOVER_REGISTER=' + ('1' if args.handover_register else '0'),
                  '-e', 'BACKUP_BEHAVIOR_ACCOUNTS=' + ('1' if args.behavior_accounts else '0'),
                  '-e', 'BACKUP_BEHAVIOR_DISPATCH=' + ('1' if args.behavior_dispatch else '0'),
                  '-e', 'BACKUP_BEHAVIOR_LEDGER=' + ('1' if args.behavior_ledger else '0'),
                  '-e', 'BACKUP_BEHAVIOR_HTTP=' + ('1' if args.behavior_http else '0'),
                  '-e', 'BACKUP_BEHAVIOR_REVIEW=' + ('1' if args.behavior_review else '0'),
                  '-e', 'BACKUP_SYNTHETIC_DUMP=/verify/database.dump',
                  '-e', 'BACKUP_BAD_FK_DUMP=/fixtures/bad-fk.dump',
                  '-e', 'BACKUP_OVERFLOW_DUMP=/fixtures/overflow.dump',
                  '-e', 'BACKUP_BAD_REF_DUMP=/fixtures/bad-ref.dump',
                  '-e', 'BACKUP_CROSS_USER_DUMP=/fixtures/cross-user.dump',
                  '--mount', f'type=bind,source={BINARY},target=/verify/backup.test,readonly',
                  '--mount', f'type=bind,source={FIXTURE},target=/verify/database.dump,readonly',
                  '--mount', f'type=bind,source={OUT},target=/fixtures,readonly',
                  '--mount', f'type=bind,source={ROOT / "backend"},target=/src,readonly',
                  '--mount', f'type=bind,source={ROOT / "docs"},target=/docs,readonly',
                  '--workdir', '/src/internal/backup', '--entrypoint', '/verify/backup.test', image,
                  '-test.run=^Test(IsolatedDatabaseRestore|ObjectDatabasePhase|RecordedDatabaseIntegration|RestoreHeaderIntegration' + ('|HandoverRegistrationFiles' if args.handover_register else '') + ')$', '-test.v=true', '-test.timeout=180s', allowed=True)
    (OUT / 'tests.txt').write_bytes(process.stdout + process.stderr)
    if process.returncode:
        raise RuntimeError('isolated restore tests failed; see tests.txt')
    lines = process.stdout.decode().splitlines()
    passes = [s for s in lines if s.lstrip().startswith('--- PASS:')]
    expected_passes = (53 if args.handover_register else 45 if args.handover_behavior else 39 if args.handover_data else 32) + (14 if args.behavior_accounts else 0) + (20 if args.behavior_dispatch else 0) + (12 if args.behavior_ledger else 0) + (20 if args.behavior_http else 0) + (8 if args.behavior_review else 0)
    assert len(passes) == expected_passes and not any('--- SKIP:' in s or '--- FAIL:' in s for s in lines)
    account_observations = [json.loads(m.group(1)) for s in lines if (m := re.search(r'RESTORED_ACCOUNTS_EVIDENCE (\{.*\})$', s))]
    assert len(account_observations) == (1 if args.behavior_accounts else 0)
    if args.behavior_accounts:
        observed = account_observations[0]
        assert observed['scope'] == 'guarded-synthetic-accounts' and observed['checks'] == 14
        assert observed['database_and_control_unchanged'] is True
        assert observed['login_dispatched'] is False and observed['execution_authorized'] is False
        (OUT / 'accounts-evidence.json').write_text(json.dumps(observed, indent=2) + '\n', encoding='utf-8')
        result['restored_accounts'] = observed
    dispatch_observations = [json.loads(m.group(1)) for s in lines if (m := re.search(r'BEHAVIOR_DISPATCH_EVIDENCE (\{.*\})$', s))]
    assert len(dispatch_observations) == (4 if args.behavior_dispatch else 0)
    if args.behavior_dispatch:
        expected_modes = {'good': (2, 2), 'lost': (1, 0), 'intent_ack': (0, 0), 'response_ack': (1, 1)}
        assert {o['mode'] for o in dispatch_observations} == set(expected_modes)
        for observed in dispatch_observations:
            assert observed['scope'] == 'isolated-service-dispatch'
            assert (observed['login_calls'], observed['logout_calls']) == expected_modes[observed['mode']]
            assert observed['unknown_response_retained'] == (observed['mode'] != 'good')
            assert observed['deployment_authorized'] is False
        (OUT / 'dispatch-evidence.json').write_text(json.dumps(dispatch_observations, indent=2) + '\n', encoding='utf-8')
        result['behavior_dispatch'] = dispatch_observations
    ledger_observations = [json.loads(m.group(1)) for s in lines if (m := re.search(r'BEHAVIOR_LEDGER_EVIDENCE (\{.*\})$', s))]
    assert len(ledger_observations) == (4 if args.behavior_ledger else 0)
    if args.behavior_ledger:
        wanted = {'good': ('responses_observed', 'known_sessions_revoked', 2, 2, 0),
                  'lost': ('outcome_unknown', 'unaccounted_active', 0, 0, 1),
                  'intent_ack': ('outcome_unknown', 'no_known_sessions', 0, 0, 0),
                  'response_ack': ('outcome_unknown', 'known_sessions_revoked', 1, 1, 0)}
        assert {o['mode'] for o in ledger_observations} == set(wanted)
        for o in ledger_observations:
            assert o['scope'] == 'isolated-ledger-session-observation' and o['readonly'] is True
            assert (o['ledger_status'], o['database_status'], o['known_sessions'], o['revoked_sessions'], o['unaccounted_active']) == wanted[o['mode']]
            assert o['dispatch_authorized'] is False and o['retry_authorized'] is False
        (OUT / 'ledger-evidence.json').write_text(json.dumps(ledger_observations, indent=2) + '\n', encoding='utf-8')
        result['behavior_ledger'] = ledger_observations
    http_observations = [json.loads(m.group(1)) for s in lines if (m := re.search(r'BEHAVIOR_HTTP_EVIDENCE (\{.*\})$', s))]
    assert len(http_observations) == (4 if args.behavior_http else 0)
    if args.behavior_http:
        wanted_http = {'good': (2, 2, 2, 0), 'lost': (1, 0, 1, 1), 'redirect': (1, 0, 0, 0), 'wrong_pin': (0, 0, 0, 0)}
        assert {o['mode'] for o in http_observations} == set(wanted_http)
        for o in http_observations:
            assert o['old_access_requests'] == (2 if o['mode'] == 'good' else 0)
            assert o['scope'] == 'isolated-https-dispatch'
            assert (o['login_requests'], o['logout_requests'], o['new_sessions'], o['active_after_dispatch']) == wanted_http[o['mode']]
            assert o['redirect_or_proxy_requests'] == 0 and o['deployment_authorized'] is False
        (OUT / 'https-evidence.json').write_text(json.dumps(http_observations, indent=2) + '\n', encoding='utf-8')
        result['behavior_http'] = http_observations
    bound_reviews = [json.loads(m.group(1)) for s in lines if (m := re.search(r'BOUND_BEHAVIOR_REVIEW (\{.*\})$', s))]
    assert len(bound_reviews) == (1 if args.behavior_review else 0)
    if args.behavior_review:
        review = bound_reviews[0]
        assert review['report_format'] == 'ylreader-bound-behavior-review' and review['report_version'] == 1
        assert review['profile'] == 'bound-https-account-verification-v1' and review['scope'] == 'localfs-bound-https-account-review'
        assert review['status'] == 'partial' and review['account_count'] == review['session_count'] == 2
        assert all(review[k] is False for k in ['execution_authorized', 'ready_for_cutover', 'deployment_authorized'])
        ids = ['schema_ledger', 'database_identity', 'database_role', 'published_objects', 'header_keyring', 'redis_isolation', 'sessions_revoked', 'tasks_disabled', 'credential_preflight', 'https_login_logout', 'old_token_http_rejection', 'reader_roundtrip']
        assert [c['id'] for c in review['checks']] == ids
        assert [c['status'] for c in review['checks']] == ['pass'] * 11 + ['unavailable']
        for key in ['tls_certificate_sha256', 'behavior_ledger_sha256', 'plan_digest', 'package_sha256', 'manifest_sha256']:
            assert re.fullmatch('[0-9a-f]{64}', review[key])
        (OUT / 'bound-behavior-review.json').write_text(json.dumps(review, indent=2) + '\n', encoding='utf-8')
        result['bound_behavior_review'] = {'profile': review['profile'], 'status': 'partial', 'verified_checks': 11, 'unavailable_checks': 1}
    data_reviews = [json.loads(s.removeprefix('RESTORE_DATA_REVIEW ')) for s in lines if s.startswith('RESTORE_DATA_REVIEW ')]
    assert len(data_reviews) == (1 if args.handover_data else 0)
    if args.handover_data:
        data_review = data_reviews[0]
        assert data_review['report_format'] == 'ylreader-handover-review' and data_review['report_version'] == 1
        assert data_review['scope'] == 'isolated-localfs-handover-review'
        assert data_review['deployment_authorized'] is False and data_review['ready_for_cutover'] is False
        checks = data_review['checks']
        expected = ['schema_ledger', 'database_identity', 'database_role', 'published_objects', 'header_keyring', 'jwt_rotation', 'redis_isolation', 'sessions_revoked', 'tasks_disabled', 'runtime_behavior']
        assert [c['id'] for c in checks] == expected
        assert all(set(c) == {'id','status'} and c['status'] == ('unavailable' if c['id'] in {'jwt_rotation','runtime_behavior'} else 'pass') for c in checks)
        (OUT / 'data-review.json').write_text(json.dumps(data_review, indent=2) + '\n', encoding='utf-8')
        result['data_review'] = {'verified_checks': 8, 'unavailable_checks': 2}
    reports = [json.loads(s.removeprefix('RESTORE_HTTP_EVIDENCE ')) for s in lines if s.startswith('RESTORE_HTTP_EVIDENCE ')]
    assert len(reports) == 1
    report = reports[0]
    assert set(report) == {'scope', 'version', 'operation_id', 'plan_digest', 'package_sha256',
                           'manifest_sha256', 'target_instance_id', 'target_storage_fingerprint',
                           'publication_tree_fingerprint', 'publication_state', 'ready_for_cutover',
                           'deployment_authorized', 'operation_phase', 'checks'}
    for field in ['operation_id', 'target_instance_id']:
        assert str(uuid.UUID(report[field])) == report[field]
    for field in ['plan_digest', 'package_sha256', 'manifest_sha256',
                  'target_storage_fingerprint', 'publication_tree_fingerprint']:
        assert re.fullmatch(r'[0-9a-f]{64}', report[field])
    expected_checks = {'health_readiness', 'fresh_login_refresh', 'old_tokens_rejected',
                       'token_version_revocation', 'jwt_key_rotated', 'redis_pair_isolated',
                       'bookshelf_chapters_content', 'progress_bookmarks', 'tenant_isolation',
                       'no_background_or_fetch'}
    assert report['scope'] == 'isolated-http-fixture' and report['version'] == 1
    assert report['ready_for_cutover'] is False and report['deployment_authorized'] is False
    assert report['operation_phase'] == 'validating' and report['publication_state'] == 'verified'
    assert set(report['checks']) == expected_checks and all(v is True for v in report['checks'].values())
    (OUT / 'http-evidence.json').write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
    behavior_reviews = [json.loads(s.removeprefix('RESTORE_BEHAVIOR_REVIEW ')) for s in lines if s.startswith('RESTORE_BEHAVIOR_REVIEW ')]
    assert len(behavior_reviews) == (1 if args.handover_behavior else 0)
    if args.handover_behavior:
        complete = behavior_reviews[0]
        assert complete['report_format'] == 'ylreader-handover-review' and complete['report_version'] == 1
        assert complete['scope'] == 'isolated-localfs-handover-review'
        for key in ['operation_id','plan_digest','package_sha256','manifest_sha256','target_instance_id','target_storage_fingerprint','publication_tree_fingerprint']:
            assert complete[key] == report[key], 'behavior evidence belongs to a different operation'
        assert complete['ready_for_cutover'] is False and complete['deployment_authorized'] is False
        assert [c['id'] for c in complete['checks']] == expected
        assert all(set(c) == {'id','status'} and c['status'] == 'pass' for c in complete['checks'])
        (OUT / 'behavior-review.json').write_text(json.dumps(complete, indent=2) + '\n', encoding='utf-8')
        result['behavior_review'] = {'verified_checks':10, 'unavailable_checks':0, 'profile':'isolated-http-fixture-v1'}
    registrations = [json.loads(s.removeprefix('RESTORE_REVIEW_REGISTRATION ')) for s in lines if s.startswith('RESTORE_REVIEW_REGISTRATION ')]
    assert len(registrations) == (1 if args.handover_register else 0)
    if args.handover_register:
        reg = registrations[0]
        assert set(reg) == set('registration_format registration_version scope operation_id backup_id owner_id plan_digest report_sha256 base_state_sha256 base_generation next_state_sha256 next_generation recorded_at state ready_for_cutover deployment_authorized'.split())
        assert reg['registration_format'] == 'ylreader-handover-registration' and reg['registration_version'] == 1
        assert reg['scope'] == 'isolated-review-registration' and reg['state'] == 'registered'
        assert reg['ready_for_cutover'] is False and reg['deployment_authorized'] is False
        for key in ['operation_id','backup_id','plan_digest']:
            assert reg[key] == complete[key]
        assert reg['base_generation'] == complete['observed_generation'] and reg['next_generation'] == reg['base_generation'] + 1
        assert reg['base_state_sha256'] == complete['operation_record_sha256']
        assert re.fullmatch('[0-9a-f]{64}', reg['next_state_sha256'])
        raw_report = next(s.removeprefix('RESTORE_BEHAVIOR_REVIEW ') for s in lines if s.startswith('RESTORE_BEHAVIOR_REVIEW '))
        assert reg['report_sha256'] == hashlib.sha256(raw_report.encode('utf-8')).hexdigest()
        (OUT / 'review-registration.json').write_text(json.dumps(reg, indent=2) + '\n', encoding='utf-8')
        result['registration'] = {'state':'registered','base_generation':reg['base_generation'],'next_generation':reg['next_generation'],'operation_phase':'validating'}
    result.update(passed=True, passing_events=len(passes))
    print('PASS isolated recorded restore: actual commits/rollbacks, durable intent, result/state acknowledgement failures, object retention and no replay')
finally:
    for name in [TEST, REDIS_TARGET, REDIS_SOURCE, PG]:
        label = run('inspect', name, '--format', '{{index .Config.Labels "ylreader.restore-prototype"}}', allowed=True)
        if label.returncode == 0 and label.stdout.decode().strip() == RUN:
            result['cleanup'][name] = run('rm', '-f', '-v', name, allowed=True).returncode == 0
        else:
            result['cleanup'][name] = label.returncode != 0
    (OUT / 'result.json').write_text(json.dumps(result, indent=2) + '\n', encoding='utf-8')
    print('Report:', OUT / 'result.json')
    if not all(result['cleanup'].values()):
        raise RuntimeError('owned container cleanup failed')
