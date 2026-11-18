"""Disposable PostgreSQL restore experiment; never accepts a database URL.

Uses only an already available postgres:16 image, network=none, no host ports,
and tmpfs data. Not a production backup/restore implementation.
"""
import hashlib
import json
import re
from pathlib import Path
import subprocess
import time
import uuid

ROOT = Path(__file__).resolve().parents[1]
RUN = uuid.uuid4().hex[:12]
NAME = 'ylreader-backup-prototype-' + RUN
OUT = ROOT / '.toolchain' / ('backup-db-' + RUN)
OUT.mkdir(parents=True)
RESULT = {'run_id': RUN, 'checks': [], 'cleanup': False}


def command(args, data=None, allowed_failure=False):
    p = subprocess.run(args, input=data, capture_output=True, timeout=120)
    if p.returncode and not allowed_failure:
        raise RuntimeError(p.stderr.decode('utf-8', errors='replace'))
    return p


def dock(*args, data=None, allowed_failure=False):
    return command(['docker', *args], data, allowed_failure)


def sql(db, text, role='postgres', fail=False):
    return dock('exec', '-i', NAME, 'psql', '-X', '-qAt', '-v',
                'ON_ERROR_STOP=1', '-U', role, '-d', db,
                data=text.encode(), allowed_failure=fail)


def check(name, condition):
    RESULT['checks'].append({'name': name, 'passed': bool(condition)})
    if not condition:
        raise AssertionError(name)
    print('PASS', name, flush=True)


def snapshot(db):
    tables = sql(db, "SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY 1").stdout.decode().splitlines()
    result = {}
    for table in tables:
        assert table.replace('_', '').isalnum()
        rows = sql(db, f'SELECT row_to_json(t)::text FROM public."{table}" t ORDER BY row_to_json(t)::text').stdout
        result[table] = {'count': len(rows.splitlines()), 'sha256': hashlib.sha256(rows).hexdigest()}
    return result


def restore(db, archive, extra=()):
    # Reordered custom archives require seeking. Production design must stage
    # authenticated plaintext before invoking pg_restore, rather than pipe it.
    seekable = any(x.startswith('--use-list=') for x in extra)
    if seekable:
        dock('exec', '-i', NAME, 'tee', '/tmp/restore-input.dump', data=archive)
    p = dock('exec', '-i', NAME, 'pg_restore', '-U', 'restorer', '-d', db,
             '--no-owner', '--no-acl', '--exit-on-error', '--single-transaction',
             *extra, *(['/tmp/restore-input.dump'] if seekable else []),
             data=None if seekable else archive, allowed_failure=True)
    (OUT / (db + '-restore.stderr')).write_bytes(p.stderr)
    return p


created = False
try:
    image = dock('image', 'inspect', 'postgres:16', '--format', '{{.Id}}').stdout.decode().strip()
    RESULT['image_id'] = image
    dock('run', '-d', '--pull=never', '--name', NAME, '--network', 'none',
         '--label', 'ylreader.backup-prototype=' + RUN,
         '--tmpfs', '/var/lib/postgresql/data:rw', '-e',
         'POSTGRES_HOST_AUTH_METHOD=trust', image)
    created = True
    for _ in range(45):
        p = dock('exec', NAME, 'pg_isready', '-U', 'postgres', allowed_failure=True)
        if p.returncode == 0:
            break
        time.sleep(1)
    else:
        raise RuntimeError('isolated PostgreSQL did not become ready')
    info = json.loads(dock('inspect', NAME).stdout)[0]
    check('no network, host ports or persistent volume',
          info['HostConfig']['NetworkMode'] == 'none' and
          not info['HostConfig'].get('PortBindings') and
          not info['HostConfig'].get('Binds') and
          '/var/lib/postgresql/data' in info['HostConfig']['Tmpfs'])
    RESULT['postgres_version'] = sql('postgres', 'SHOW server_version').stdout.decode().strip()
    sql('postgres', 'CREATE ROLE restorer LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION;')
    for db in ['source', 'full_target', 'baseline_target', 'broken_target']:
        sql('postgres', f'CREATE DATABASE {db} OWNER restorer;')
    schema = (ROOT / 'docs/schema.sql').read_bytes()
    dock('exec', '-i', NAME, 'psql', '-X', '-q', '-v', 'ON_ERROR_STOP=1', '-U', 'restorer', '-d', 'source', data=schema)
    for i in [1, 2]:
        user = f'00000000-0000-4000-8000-{i:012d}'
        source = f'10000000-0000-4000-8000-{i:012d}'
        book = f'20000000-0000-4000-8000-{i:012d}'
        sql('source', f"""
INSERT INTO users(id,username,password_hash,token_version) VALUES('{user}','prototype-{i}','not-a-real-hash',3);
INSERT INTO book_sources(id,user_id,book_source_url,book_source_name,book_source_type,header)
VALUES('{source}','{user}','local://{user}','local',3,'{{"__enc":"v1:prototype:synthetic-not-decryptable"}}');
INSERT INTO books(id,user_id,name,normalized_name,book_type,active_source_id,active_book_url,total_chapters,file_hash)
VALUES('{book}','{user}','Book {i}','book{i}','local','{source}','local:{book}',1,'hash-{i}');
INSERT INTO book_source_mappings(book_id,source_id,book_url,sync_status) VALUES('{book}','{source}','local:{book}','active');
INSERT INTO chapters(book_id,source_id,chapter_index,sort_order,title,normalized_title,url,url_hash)
VALUES('{book}','{source}',0,0,'Chapter 1','n:1','local:{book}:0','url-hash-{i}');
INSERT INTO reading_progress(user_id,book_id,chapter_index,char_offset,chapter_ratio,version,server_updated_at)
VALUES('{user}','{book}',0,17,0.5,7,'2026-01-02T03:04:05Z');
INSERT INTO bookmarks(user_id,book_id,chapter_index,char_offset,note) VALUES('{user}','{book}',0,17,'unicode: 中文 😀');
INSERT INTO app_settings(user_id,key,value) VALUES('{user}','reader.theme','"night"');
INSERT INTO auth_sessions(user_id,refresh_token_hash,expires_at) VALUES('{user}','synthetic-{i}','2030-01-01');
INSERT INTO jobs(user_id,type,status,payload,worker_id) VALUES('{user}','full_cache','running','{{}}','old-worker');
""")
    before = snapshot('source')
    RESULT['source_snapshot'] = before
    archive = dock('exec', NAME, 'pg_dump', '-U', 'restorer', '-Fc', '--no-owner', '--no-acl', 'source').stdout
    (OUT / 'database.dump').write_bytes(archive)
    RESULT['archive_sha256'] = hashlib.sha256(archive).hexdigest()
    check('full schema/data restore as database owner without superuser', restore('full_target', archive).returncode == 0)
    check('all table rows preserved byte-for-byte', before == snapshot('full_target'))
    check('pgcrypto and pg_trgm available', sql('full_target', "SELECT count(*) FROM pg_extension WHERE extname IN ('pgcrypto','pg_trgm')").stdout.strip() == b'2')
    check('identity sequence advanced after restore', sql('full_target', "SELECT nextval(pg_get_serial_sequence('chapters','id')) > (SELECT max(id) FROM chapters)", 'restorer').stdout.strip() == b't')
    check('role cannot create privileged roles', sql('full_target', 'CREATE ROLE forbidden_role SUPERUSER', 'restorer', True).returncode != 0)
    check('foreign keys remain enforced', sql('full_target', "INSERT INTO bookmarks(user_id,book_id,chapter_index) VALUES(gen_random_uuid(),gen_random_uuid(),0)", 'restorer', True).returncode != 0)
    check('internal FK triggers cannot be disabled', sql('full_target', 'ALTER TABLE bookmarks DISABLE TRIGGER ALL', 'restorer', True).returncode != 0)
    check('book version trigger remains active', sql('full_target', "UPDATE books SET intro='trigger check'; SELECT bool_and(version=2) FROM books", 'restorer').stdout.strip() == b't')
    # A trusted pre-created baseline contains a migration ledger seed: remove only
    # that row in this newly created disposable target before data-only restore.
    dock('exec', '-i', NAME, 'psql', '-X', '-q', '-v', 'ON_ERROR_STOP=1', '-U', 'restorer', '-d', 'baseline_target', data=schema)
    sql('baseline_target', 'DELETE FROM schema_migrations', 'restorer')
    naive = restore('baseline_target', archive, ('--data-only',))
    (OUT / 'baseline-unordered-expected-failure.stderr').write_bytes(naive.stderr)
    check('unordered data-only restore reproduces FK failure', naive.returncode != 0 and b'foreign key' in naive.stderr)
    check('unordered failed transaction leaves all baseline tables empty', all(v['count'] == 0 for v in snapshot('baseline_target').values()))
    # A frozen-schema dependency order; unknown tables fail closed. This list
    # selects only data/sequence entries, never executes archived schema SQL.
    order = ['users', 'book_sources', 'books', 'book_source_mappings', 'chapters',
             'reading_progress', 'chapter_source_overrides', 'bookmarks',
             'replace_rules', 'app_settings', 'auth_sessions', 'source_failures',
             'jobs', 'schema_migrations']
    check('explicit restore order covers every baseline table', set(order) == set(before))
    toc = dock('exec', '-i', NAME, 'pg_restore', '--list', data=archive).stdout.decode()
    selected = {}
    sequences = []
    for line in toc.splitlines():
        match = re.search(r' TABLE DATA public (\w+) ', line)
        if match:
            table = match[1]
            assert table in order and table not in selected
            selected[table] = line
        elif ' SEQUENCE SET public ' in line:
            sequences.append(line)
    check('archive data entries match frozen table set', set(selected) == set(order))
    use_list = '\n'.join([selected[t] for t in order] + sequences) + '\n'
    (OUT / 'ordered-data.list').write_text(use_list, encoding='utf-8')
    dock('exec', '-i', NAME, 'tee', '/tmp/ordered-data.list', data=use_list.encode())
    check('ordered data-only restore without disabling triggers', restore('baseline_target', archive, ('--data-only', '--use-list=/tmp/ordered-data.list')).returncode == 0)
    check('baseline restore preserves all row values including progress version/times', before == snapshot('baseline_target'))
    check('truncated archive rejected', restore('broken_target', archive[:len(archive)//2]).returncode != 0)
    check('failed single-transaction restore leaves no application tables', snapshot('broken_target') == {})
    sql('baseline_target', "BEGIN; UPDATE auth_sessions SET revoked_at=now(); UPDATE users SET token_version=token_version+1; UPDATE jobs SET status='canceled',worker_id=NULL,finished_at=now() WHERE status IN ('queued','running'); COMMIT;", 'restorer')
    check('restored sessions revoked and token generations advanced', sql('baseline_target', 'SELECT (SELECT bool_and(revoked_at IS NOT NULL) FROM auth_sessions) AND (SELECT bool_and(token_version=4) FROM users)').stdout.strip() == b't')
    check('in-flight jobs canceled and leases cleared', sql('baseline_target', "SELECT bool_and(status='canceled' AND worker_id IS NULL) FROM jobs").stdout.strip() == b't')
    check('source unchanged after success and failure experiments', before == snapshot('source'))
    RESULT['passed'] = True
except Exception as exc:
    RESULT['passed'] = False
    RESULT['error'] = str(exc)
    raise
finally:
    if created:
        label = dock('inspect', NAME, '--format', '{{index .Config.Labels "ylreader.backup-prototype"}}', allowed_failure=True)
        if label.returncode == 0 and label.stdout.decode().strip() == RUN:
            RESULT['cleanup'] = dock('rm', '-f', '-v', NAME, allowed_failure=True).returncode == 0
    (OUT / 'result.json').write_text(json.dumps(RESULT, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
    print('Report:', OUT / 'result.json', flush=True)
    if created and not RESULT['cleanup']:
        raise RuntimeError('owned prototype container cleanup failed; inspect report')
