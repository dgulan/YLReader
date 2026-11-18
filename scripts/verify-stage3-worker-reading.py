"""One authorized source, current Linux code, offline worker, isolated disposable data.

No image pull, deployment, upstream login, implicit rule rewriting or bulk content download.
Optional migration applies one caller-supplied adapter only to this run's temporary account.
Requires local runtime/worker/postgres/redis images and fresh linux/amd64 binaries.
"""
from pathlib import Path
import argparse
import base64
import hashlib
import re
from collections import Counter
import json
import secrets
import subprocess
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

p = argparse.ArgumentParser(description=__doc__)
p.add_argument('--server', type=Path, required=True)
p.add_argument('--worker', type=Path, required=True)
p.add_argument('--sources-dir', type=Path, required=True)
p.add_argument('--source-name', default='猫眼看书')
p.add_argument('--keyword', default='三国演义')
p.add_argument('--expected-title', help='Exact title permitted for a single-chapter check; defaults to keyword')
p.add_argument('--expected-author', help='Optional exact author filter for the selected book')
p.add_argument('--search-only', action='store_true', help='Check search metadata without adding a book or reading content')
p.add_argument('--network-control', action='store_true', help='One guarded HTTPS example.com check; never counts as source success')
p.add_argument('--directory-probe', action='store_true', help='Inspect paging controls and directory metadata only; do not fetch content')
p.add_argument('--directory-probe-page', help='One explicit additional metadata page, resolved relative to the matched book URL')
p.add_argument('--directory-label-probe-page', action='append', default=[], help='Explicit metadata-only page for a bounded label rule, at most four')
p.add_argument('--directory-label-probe-rule', help='Explicit XPath for selected directory labels only')
p.add_argument('--source-variant', choices=['original', 'explicit_rule_patch'], default='original')
p.add_argument('--expected-directory-count', type=int, help='Caller supplied structural directory expectation, not product logic')
p.add_argument('--expected-numbered-chapters', type=int, help='Require numbered chapter coverage 1..N in ascending order')
p.add_argument('--migrate-to-source', type=Path, help='Explicit same-source adapter JSON; disposable metadata-only migration')
p.add_argument('--migration-expected-old-count', type=int)
p.add_argument('--migration-bookmark-index', type=int, action='append', default=[])
p.add_argument('--probe-json-shape', action='store_true', help='Optional extra GET for a plain JSON search URL only')
p.add_argument('--runtime-image', default='yreader-pre-release:race')
p.add_argument('--worker-image', default='ylreader-worker-check:amd64-20261114')
a = p.parse_args()
if a.directory_label_probe_page and (not a.directory_probe or not a.directory_label_probe_rule or len(a.directory_label_probe_page) > 4 or not a.directory_label_probe_rule.startswith(('//', '('))):
    p.error('label probes require directory-probe, an explicit XPath and at most four explicit pages')
if a.migrate_to_source and (not a.directory_probe or a.migration_expected_old_count is None or a.expected_directory_count is None or not a.migration_bookmark_index):
    p.error('migration requires directory-probe, explicit old/new counts and bookmark indices')
root = Path(__file__).resolve().parents[1]
ident = uuid.uuid4().hex[:12]
label_key = 'ylreader.stage3-worker'
label = label_key + '=' + ident
prefix = 'ylreader-stage3-worker-' + ident
out = root / '.toolchain' / ('stage3-worker-reading-' + ident)
out.mkdir()
objects = []
result = {'run_id': ident, 'infrastructure_passed': False, 'full_reading_passed': False,
          'single_chapter_passed': False, 'directory_integrity_verified': False,
          'search_passed': False, 'keyword': a.keyword, 'expected_title': a.expected_title or a.keyword,
          'expected_author': a.expected_author,
          'verification_mode': 'search_only' if a.search_only else 'directory_metadata' if a.directory_probe else 'single_chapter',
          'execution_scope': 'local_disposable_docker_loopback_api',
          'stages': [], 'cleanup': {}, 'current_binary_overlay': True}
token = None


def docker(*args, data=None, allowed=False):
    r = subprocess.run(['docker', *args], input=data, capture_output=True, timeout=120)
    if r.returncode and not allowed:
        raise RuntimeError('docker operation failed: ' + r.stderr.decode(errors='replace')[-500:])
    return r


def image(name):
    metadata = json.loads(docker('image', 'inspect', name).stdout)[0]
    if metadata['Os'] != 'linux' or metadata['Architecture'] != 'amd64':
        raise RuntimeError('native linux/amd64 images required')
    return metadata['Id']


def owned(kind, name):
    objects.append((kind, name))
    return name


def container(name, *args):
    owned('container', name)
    return docker('run', '-d', '--pull=never', '--name', name, '--label', label, *args)


def api(method, path, body=None, extra_headers=None):
    headers = {'Content-Type': 'application/json'}
    if token:
        headers['Authorization'] = 'Bearer ' + token
    if extra_headers:
        headers.update(extra_headers)
    req = urllib.request.Request(base + path, data=json.dumps(body).encode() if body is not None else None,
                                 headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=35) as response:
            return response.status, json.load(response)
    except urllib.error.HTTPError as error:
        try:
            return error.code, json.load(error)
        except ValueError:
            return error.code, {}


def stage(name, method, path, body=None):
    start = time.monotonic()
    status, payload = api(method, path, body)
    row = {'stage': name, 'status': status, 'seconds': round(time.monotonic() - start, 3)}
    if status >= 400:
        row['error_code'] = payload.get('error', {}).get('code')
    result['stages'].append(row)
    return status, payload, row


def directory_label_diagnostics(readable, expected):
    numbered = []
    def summary(chapter):
        return {'chapter_index': chapter['chapter_index'], 'title': chapter.get('title'),
                'url_sha256': hashlib.sha256(str(chapter.get('url') or '').encode()).hexdigest()}
    unnumbered = []
    for chapter in readable:
        match = re.match(r'^第\s*(\d+)\s*章', chapter.get('title', ''))
        if match:
            numbered.append((int(match.group(1)), chapter))
        else:
            unnumbered.append(summary(chapter))
    counts = Counter(number for number, _ in numbered)
    missing = [number for number in range(1, expected + 1) if number not in counts] if expected is not None else []
    neighborhoods = []
    for number in missing[:16]:
        lower = next((ch for n, ch in reversed(numbered) if n < number), None)
        upper = next((ch for n, ch in numbered if n > number), None)
        neighborhoods.append({'missing_label': number, 'before': summary(lower) if lower else None,
                              'after': summary(upper) if upper else None})
    return {'non_numbered_samples': unnumbered[:16], 'missing_label_samples': missing[:16],
            'missing_neighborhoods': neighborhoods,
            'duplicate_samples': [dict(summary(ch), number=n) for n, ch in numbered if counts[n] > 1][:32],
            'text_missing_proven': False}


def migrate_directory(source, source_id, book_id, old_toc):
    """Operate only on the temporary account/book created by this run."""
    report = {'passed': False, 'old_total': old_toc.get('total'), 'checks': {},
              'applied_source_variant': 'explicit_rule_patch', 'rules_modified': True}
    result['migration'] = report
    if old_toc.get('total') != a.migration_expected_old_count:
        raise RuntimeError('unexpected original directory count; migration not started')
    candidates = json.loads(a.migrate_to_source.read_text(encoding='utf-8-sig'))
    if not isinstance(candidates, list) or len(candidates) != 1:
        raise RuntimeError('one explicit adapter required')
    adapted = dict(candidates[0])
    adapted['header'], adapted['enabled'] = {}, True
    compare = json.loads(json.dumps(adapted))
    compare['bookSourceName'] = source.get('bookSourceName')
    for key in ['chapterList', 'nextTocUrl']:
        if key in source.get('ruleToc', {}):
            compare.setdefault('ruleToc', {})[key] = source['ruleToc'][key]
        else:
            compare.setdefault('ruleToc', {}).pop(key, None)
    if compare != source:
        raise RuntimeError('adapter differs outside declared TOC/name fields')
    report['adapter_sha256'] = hashlib.sha256(a.migrate_to_source.read_bytes()).hexdigest()
    path = f'/books/{book_id}'
    status, progress = api('GET', path + '/progress')
    if status != 200:
        raise RuntimeError('initial migration progress unavailable')
    old_rows = {c['chapter_index']: c for c in old_toc['items'] if c.get('chapter_index') is not None}
    old_first = old_rows[0]
    incoming = {'chapter_index': 0, 'char_offset': 17, 'chapter_ratio': .125,
                'chapter_title': old_first['title'], 'client_updated_at': time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime()),
                'device_id': 'isolated-toc-migration'}
    status, before = api('PUT', path + '/progress', incoming, {'If-Match': '"' + str(progress['version']) + '"'})
    if status != 200:
        raise RuntimeError('synthetic migration progress not created')
    marks = []
    for index in a.migration_bookmark_index:
        row = old_rows[index]
        status, mark = api('POST', path + '/bookmarks', {'chapter_index': index, 'char_offset': 23 + index,
                           'chapter_title': row['title'], 'excerpt': '原创迁移验收摘录', 'note': '临时账号迁移书签'})
        if status != 201:
            raise RuntimeError('synthetic migration bookmark not created')
        marks.append(mark)
    status, old_book = api('GET', path)
    if status != 200:
        raise RuntimeError('migration book unavailable')
    status, _, _ = stage('migration_import_adapter', 'POST', '/sources/import',
                          {'format': 'legado_json', 'data': json.dumps([adapted]), 'overwrite': 'update', 'dry_run': False})
    if status != 200:
        raise RuntimeError('temporary adapter import failed')
    status, current_source = api('GET', f'/sources/{source_id}')
    report['checks']['same_source_id'] = status == 200 and current_source.get('id') == source_id and current_source.get('book_source_name') == adapted.get('bookSourceName')
    status, new_toc, row = stage('migration_directory', 'GET', path + '/chapters?refresh=true')
    report['refresh_status'] = status
    ps, after = api('GET', path + '/progress')
    ms, after_marks = api('GET', path + '/bookmarks')
    bs, new_book = api('GET', path)
    if status != 200:
        ts, retained = api('GET', path + '/chapters?refresh=false')
        report['failure_preserved_old_state'] = (ps == ms == bs == ts == 200 and retained == old_toc
            and after == before and new_book == old_book and sorted(after_marks, key=lambda m: m['id']) == sorted(marks, key=lambda m: m['id']))
        return status, new_toc, row
    new_rows = {c['chapter_index']: c for c in new_toc['items'] if c.get('chapter_index') is not None}
    urls = [c.get('url') for c in new_rows.values()]
    target = new_rows.get(after.get('chapter_index'), {})
    checks = report['checks']
    checks['readbacks_ok'] = ps == ms == bs == 200
    checks['expected_complete_count'] = (new_toc.get('total') == a.expected_directory_count == len(new_rows)
        and len(set(urls)) == len(urls) and all(urls) and sorted(new_rows) == list(range(len(new_rows))))
    checks['progress_url'] = target.get('url') == old_first['url'] and urls.count(old_first['url']) == 1
    checks['progress_fields'] = all(after.get(k) == before.get(k) for k in ['char_offset', 'chapter_ratio', 'client_updated_at', 'device_id'])
    checks['progress_version'] = after.get('version') == before['version'] + 1
    checks['book_version'] = new_book.get('version', 0) > old_book['version']
    checks['same_book_and_active_source'] = (new_book.get('id') == old_book.get('id') == book_id
        and new_book.get('active_source_id') == old_book.get('active_source_id') == source_id
        and new_toc.get('source_id') == source_id)
    checks['bookmarks_preserved'] = ms == 200 and len(after_marks) == len(marks)
    report['bookmark_moves'] = []
    for mark in marks:
        matches = [m for m in after_marks if m.get('id') == mark['id']]
        moved = matches[0] if len(matches) == 1 else {}
        url = old_rows[mark['chapter_index']]['url']
        preserved = (len(matches) == 1 and all(moved.get(k) == v for k, v in mark.items() if k != 'chapter_index')
            and new_rows.get(moved.get('chapter_index'), {}).get('url') == url and urls.count(url) == 1)
        checks['bookmarks_preserved'] &= preserved
        report['bookmark_moves'].append({'old_index': mark['chapter_index'], 'new_index': moved.get('chapter_index'),
                                        'url_sha256': hashlib.sha256(url.encode()).hexdigest(), 'preserved': preserved})
    stale_status, _ = api('PUT', path + '/progress', incoming, {'If-Match': '"' + str(before['version']) + '"'})
    ps, final_progress = api('GET', path + '/progress')
    checks['stale_progress_rejected'] = stale_status == 409 and ps == 200 and final_progress == after
    report.update(new_total=new_toc.get('total'), old_progress_index=before['chapter_index'],
                  new_progress_index=after.get('chapter_index'), old_progress_version=before['version'],
                  new_progress_version=after.get('version'), stale_put_status=stale_status)
    report['passed'] = all(checks.values())
    result['migration_passed'] = report['passed']
    return status, new_toc, row


try:
    selected = []
    for file in sorted(a.sources_dir.glob('*.json')):
        data = file.read_bytes()
        for source in json.loads(data.decode('utf-8-sig')):
            if source.get('bookSourceName') == a.source_name:
                selected.append((source, file.name, hashlib.sha256(data).hexdigest()))
    if len(selected) != 1:
        raise RuntimeError('source selection must be unique')
    source, file_name, source_hash = selected[0]
    source = dict(source)
    source['header'] = {}
    source['enabled'] = True
    result['source'] = {'name': a.source_name, 'file': file_name, 'sha256': source_hash,
                        'static_auth_headers_removed': True, 'rules_modified': a.source_variant != 'original'}
    binaries = {}
    for name, file in [('server', a.server), ('worker', a.worker)]:
        file = file.resolve()
        if not file.is_file() or file.read_bytes()[:4] != b'\x7fELF':
            raise RuntimeError('fresh Linux ELF binaries required')
        binaries[name] = file
        result[name + '_sha256'] = hashlib.sha256(file.read_bytes()).hexdigest()
    runtime, worker_image, pg_image, redis_image = [image(n) for n in [a.runtime_image, a.worker_image, 'postgres:16', 'redis:7-alpine']]
    result['images'] = {'runtime': runtime, 'worker': worker_image, 'postgres': pg_image, 'redis': redis_image}
    network = owned('network', prefix + '-net')
    docker('network', 'create', '--label', label, network)
    ipc = owned('volume', prefix + '-ipc')
    docker('volume', 'create', '--label', label, ipc)
    init = owned('container', prefix + '-init')
    docker('run', '--rm', '--pull=never', '--name', init, '--label', label, '--network', 'none',
           '--mount', f'type=volume,source={ipc},target=/ipc', '--entrypoint', 'sh', runtime,
           '-c', 'chown 10001:10001 /ipc && chmod 0700 /ipc')
    pg, redis, worker, server = [prefix + '-' + n for n in ['pg', 'redis', 'worker', 'api']]
    container(pg, '--network', network, '--network-alias', 'pg', '--tmpfs', '/var/lib/postgresql/data:rw',
              '-e', 'POSTGRES_DB=yreader_test', '-e', 'POSTGRES_HOST_AUTH_METHOD=trust', pg_image)
    container(redis, '--network', network, '--network-alias', 'redis', redis_image)
    for _ in range(45):
        # The image briefly starts a Unix-socket-only initialization server.
        # Probe TCP so schema setup waits for the final server after its restart.
        if docker('exec', pg, 'pg_isready', '-h', '127.0.0.1', '-U', 'postgres', '-d', 'yreader_test', allowed=True).returncode == 0:
            break
        time.sleep(1)
    else:
        raise RuntimeError('isolated database unavailable')
    docker('exec', '-i', pg, 'psql', '-h', '127.0.0.1', '-Xq', '-v', 'ON_ERROR_STOP=1', '-U', 'postgres', '-d', 'yreader_test',
           data=(root / 'docs/schema.sql').read_bytes())
    limits = ['--read-only', '--user', '10001:10001', '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges']
    container(worker, '--network', 'none', *limits, '--memory', '128m', '--memory-swap', '128m', '--cpus', '1',
              '--pids-limit', '64', '--mount', f'type=volume,source={ipc},target=/ipc',
              '--mount', f'type=bind,source={binaries["worker"]},target=/worker,readonly', worker_image)
    time.sleep(1)
    password = secrets.token_urlsafe(24)
    environment = {'DATABASE_URL': 'postgres://postgres:@pg:5432/yreader_test?sslmode=disable',
                   'REDIS_URL': 'redis://redis:6379/0', 'ENVIRONMENT': 'production', 'PORT': '8080',
                   'JWT_SECRET': secrets.token_hex(32), 'YREADER_HEADER_KEY_KID': 'primary',
                   'YREADER_HEADER_KEY_primary': base64.b64encode(secrets.token_bytes(32)).decode(),
                   'SEED_USERNAME': 'stage3-worker', 'SEED_PASSWORD': password,
                   'STORAGE_DIR': '/storage', 'YREADER_SANDBOX_SOCKETS': '/ipc/worker.sock'}
    env_args = [part for key, value in environment.items() for part in ['-e', key + '=' + value]]
    container(server, '--network', network, *limits, '--memory', '512m', '--memory-swap', '512m', '--cpus', '2',
              '--pids-limit', '128', '-p', '127.0.0.1::8080', '--tmpfs', '/tmp:rw,mode=1777',
              '--tmpfs', '/storage:rw,uid=10001,gid=10001,mode=0700',
              '--mount', f'type=volume,source={ipc},target=/ipc,readonly',
              '--mount', f'type=bind,source={binaries["server"]},target=/server,readonly',
              *env_args, '--entrypoint', '/server', runtime)
    info = json.loads(docker('inspect', server).stdout)[0]
    port = info['NetworkSettings']['Ports']['8080/tcp'][0]['HostPort']
    base = 'http://127.0.0.1:' + port + '/api/v1'
    for _ in range(45):
        try:
            with urllib.request.urlopen(base.replace('/api/v1', '/readyz'), timeout=2) as ready:
                if ready.status == 200:
                    break
        except (OSError, urllib.error.URLError):
            pass
        time.sleep(1)
    else:
        raise RuntimeError('isolated API/worker readiness unavailable')
    logs = docker('logs', server).stdout
    if b'isolated sandbox workers enabled' not in logs:
        raise RuntimeError('remote worker activation not confirmed')
    result['infrastructure_passed'] = True
    status, auth = api('POST', '/auth/login', {'username': 'stage3-worker', 'password': password})
    if status != 200 or not auth.get('access_token'):
        raise RuntimeError('isolated login failed')
    token = auth['access_token']
    status, imported, _ = stage('import', 'POST', '/sources/import',
                                {'format': 'legado_json', 'data': json.dumps([source]), 'overwrite': 'skip', 'dry_run': False})
    if status != 200:
        raise RuntimeError('source import failed')
    status, page = api('GET', '/sources?page_size=100')
    source_id = next(s['id'] for s in page.get('items', []) if s['book_source_url'] == source['bookSourceUrl'])
    if a.network_control:
        status, control, row = stage('public_https_control', 'POST', f'/sources/{source_id}/debug',
                                     {'rule_type': 'search', 'rule': '@js:JSON.stringify({hasExpectedText:result.indexOf("Example Domain")>=0,characters:result.length})', 'url': 'https://example.com/', 'max_preview_bytes': 256})
        try:
            shape = json.loads(control.get('result_preview') or '{}')
        except ValueError:
            shape = {}
        row['details'] = shape
        row['step_errors'] = [s.get('error_code') for s in control.get('steps', []) if s.get('error_code')]
        result['network_control_passed'] = status == 200 and shape.get('hasExpectedText') is True
        row['expected_text_matched'] = result['network_control_passed']
    status, search, row = stage('search', 'GET', '/search?' + urllib.parse.urlencode({'keyword': a.keyword, 'source_ids': source_id}))
    row.update(total=search.get('total', 0), failure_codes=[f.get('code') for f in search.get('failures', [])])
    row['failure_messages'] = [str(f.get('message', ''))[:256] for f in search.get('failures', [])]
    row['items'] = [{'name': str(item.get('name', ''))[:256], 'author': str(item.get('author') or '')[:128], 'has_book_url': bool(item.get('book_url'))}
                    for group in search.get('groups', []) for item in group.get('items', [])]
    matches = [item for group in search.get('groups', []) for item in group.get('items', []) if item.get('name') == result['expected_title'] and item.get('book_url') and (a.expected_author is None or item.get('author') == a.expected_author)]
    result['search_passed'] = status == 200 and bool(matches) and not search.get('failures')
    if not matches:
        result['stopped_at'] = 'search_no_exact_expected_title'
        # Optional structure-only probe; do not turn POST/JS/options into GET.
        search_url = source.get('searchUrl', '')
        json_rule = source.get('ruleSearch', {}).get('bookList', '').startswith(('$', '@Json:', '@json:'))
        if a.probe_json_shape and json_rule and not any(c in search_url for c in [',', '<', '@js:']):
            target = urllib.parse.urljoin(source['bookSourceUrl'], search_url.replace('{{key}}', urllib.parse.quote(a.keyword)).replace('{{page}}', '1'))
            shape_rule = '''@js:var v=JSON.parse(result); var d=v && v.data;
var keys=function(x){return x && typeof x==='object' ? Object.keys(x).slice(0,16).map(function(k){return k.slice(0,64)}) : []};
JSON.stringify({rootType:typeof v,rootKeys:keys(v),dataType:typeof d,dataIsArray:Array.isArray(d),dataKeys:keys(d),firstKeys:keys(Array.isArray(d)?d[0]:null)})'''
            status, debug, row = stage('search_structure_probe', 'POST', f'/sources/{source_id}/debug',
                                      {'rule_type': 'search', 'rule': shape_rule, 'url': target, 'max_preview_bytes': 2048})
            steps = debug.get('steps', [])
            if status == 200 and steps:
                try:
                    row['structure'] = json.loads(steps[-1].get('output_preview', ''))
                except ValueError:
                    row['structure_unavailable'] = True
    elif a.search_only:
        result['stopped_at'] = 'search_only_requested'
    else:
        item = matches[0]
        status, book, row = stage('add_book', 'POST', '/books', {'source_id': source_id, 'book_url': item['book_url'], 'name': item['name'], 'author': item.get('author') or ''})
        if status not in [200, 201]:
            result['stopped_at'] = 'add_book'
        else:
            book_id = book['id']
            if a.directory_probe:
                # Read-only metadata inspection. Returned values are never
                # followed automatically or counted as a corrected source rule.
                rule = '''//div[@id='list']//dt/text() | //select/@name | //select/@onchange | //select/option/@value | //select/option/text() | //a[contains(.,'下一页') or contains(.,'全部目录') or contains(.,'完整目录') or @rel='next']/@href'''
                pages = [('book_page', item['book_url'])]
                if a.directory_probe_page:
                    pages.append(('explicit_page', urllib.parse.urljoin(item['book_url'], a.directory_probe_page)))
                for page_label, page_url in pages:
                    status, debug, row = stage('directory_controls', 'POST', f'/sources/{source_id}/debug',
                                              {'rule_type': 'toc', 'rule': rule, 'url': page_url, 'max_preview_bytes': 4096})
                    row['page_label'] = page_label
                    row['metadata_preview'] = debug.get('result_preview') or ''
                    row['step_errors'] = [step.get('error_code') for step in debug.get('steps', []) if step.get('error_code')]
                for explicit_page in a.directory_label_probe_page:
                    target = urllib.parse.urljoin(item['book_url'], explicit_page)
                    if urllib.parse.urlsplit(target)[:2] != urllib.parse.urlsplit(item['book_url'])[:2]:
                        raise RuntimeError('label probes must remain on the matched book origin')
                    status, debug, row = stage('directory_label_probe', 'POST', f'/sources/{source_id}/debug',
                                              {'rule_type': 'toc', 'rule': a.directory_label_probe_rule, 'url': target, 'max_preview_bytes': 4096})
                    row['labels_preview'] = debug.get('result_preview') or ''
                    row['step_errors'] = [step.get('error_code') for step in debug.get('steps', []) if step.get('error_code')]
                    row['preview_within_limit'] = len(row['labels_preview'].encode('utf-8')) < 4096
            status, toc, row = stage('directory', 'GET', f'/books/{book_id}/chapters?refresh=true')
            row['total'] = toc.get('total', 0)
            if a.migrate_to_source and status == 200:
                status, toc, row = migrate_directory(source, source_id, book_id, toc)
            row['total'] = toc.get('total', 0)
            chapters = toc.get('items', [])
            readable = [chapter for chapter in chapters if chapter.get('chapter_index') is not None]
            row['label_diagnostics'] = directory_label_diagnostics(readable, a.expected_numbered_chapters)
            urls = [chapter.get('url') for chapter in readable]
            numbers = []
            for chapter in readable:
                match = re.match(r'^第\s*(\d+)\s*章', chapter.get('title', ''))
                if match:
                    numbers.append(int(match.group(1)))
            row['structure'] = {'returned_readable': len(readable), 'unique_urls': len(set(urls)),
                                'numbered_count': len(numbers), 'non_numbered_count': len(readable) - len(numbers),
                                'numbered_strictly_increasing': all(b > a for a, b in zip(numbers, numbers[1:]))}
            counts = Counter(numbers)
            duplicates = {number for number, count in counts.items() if count > 1}
            row['structure'].update(numbered_unique_count=len(counts),
                                    numbered_non_decreasing=all(b >= a for a, b in zip(numbers, numbers[1:])),
                                    duplicate_number_samples=sorted(duplicates)[:16])
            row['numbering_samples'] = [{'chapter_index': chapter['chapter_index'], 'title': chapter.get('title')}
                                       for chapter in readable if (match := re.match(r'^第\s*(\d+)\s*章', chapter.get('title', ''))) and int(match.group(1)) in duplicates][:24]
            if a.expected_numbered_chapters is not None:
                row['structure']['missing_number_count'] = sum(number not in counts for number in range(1, a.expected_numbered_chapters + 1))
            if a.expected_directory_count is not None and a.expected_numbered_chapters is not None:
                result['directory_integrity_verified'] = (status == 200 and toc.get('total') == a.expected_directory_count
                    and len(readable) == a.expected_directory_count and all(urls) and len(set(urls)) == len(urls)
                    and [chapter['chapter_index'] for chapter in readable] == list(range(len(readable)))
                    and numbers == list(range(1, a.expected_numbered_chapters + 1)))
                row['expected_total'] = a.expected_directory_count
                row['expected_numbered'] = a.expected_numbered_chapters
            if chapters:
                row['first_chapter'] = {key: chapters[0].get(key) for key in ['chapter_index', 'title']}
                row['last_chapter'] = {key: chapters[-1].get(key) for key in ['chapter_index', 'title']}
                if a.directory_probe:
                    row['head'] = [{key: chapter.get(key) for key in ['chapter_index', 'title']} for chapter in chapters[:14]]
            if status != 200 or not toc.get('total'):
                result['stopped_at'] = 'directory'
            elif a.directory_probe:
                result['stopped_at'] = 'directory_metadata_probe_only'
            else:
                status, content, row = stage('content', 'GET', f'/books/{book_id}/content?chapter_index=0')
                text = content.get('content', '')
                row.update(characters=len(text), content_sha256=hashlib.sha256(text.encode()).hexdigest())
                result['single_chapter_passed'] = status == 200 and bool(text.strip())
                if not result['single_chapter_passed']:
                    result['stopped_at'] = 'content'
    status, _ = api('GET', '/sources?page_size=1')
    result['api_healthy_after_source'] = status == 200
    wi = json.loads(docker('inspect', worker).stdout)[0]
    result['worker_after_source'] = {'running': wi['State']['Running'], 'oom_killed': wi['State']['OOMKilled'], 'restarts': wi['RestartCount']}
finally:
    for kind, name in reversed(objects):
        command = [kind, 'inspect', name] if kind != 'container' else ['inspect', name]
        inspected = docker(*command, allowed=True)
        if inspected.returncode:
            ok = True
        else:
            info = json.loads(inspected.stdout)[0]
            labels = info.get('Config', {}).get('Labels', {}) if kind == 'container' else info.get('Labels', {})
            ok = labels.get(label_key) == ident
            if ok:
                if kind == 'container':
                    # Logs do not contain request credentials or source response bodies.
                    logs = docker('logs', name, allowed=True)
                    (out / (name.rsplit('-', 1)[-1] + '.log')).write_bytes(logs.stdout + logs.stderr)
                    args = ['rm', '-f', '-v', name]
                else:
                    args = [kind, 'rm', name]
                ok = docker(*args, allowed=True).returncode == 0
        result['cleanup'][name] = ok
    result['cleanup_passed'] = all(result['cleanup'].values())
    selected_gate = result['search_passed'] if a.search_only else result['directory_integrity_verified'] if a.directory_probe else result['single_chapter_passed']
    result['passed'] = result['infrastructure_passed'] and selected_gate and result['cleanup_passed']
    if a.migrate_to_source:
        result['passed'] = result['passed'] and result.get('migration_passed', False)
    (out / 'result.json').write_text(json.dumps(result, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
    print(out / 'result.json', flush=True)
    if not result['cleanup_passed']:
        raise RuntimeError('owned resources were not fully cleaned')
if not result['passed']:
    raise SystemExit(2)
