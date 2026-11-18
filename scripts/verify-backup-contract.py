"""Validate Draft schemas/examples and negative cases. Never reads secret files."""
import copy
from datetime import datetime, timedelta
import hashlib
import json
import ntpath
from pathlib import Path
import posixpath
import re
import sys

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / '.toolchain/backup-schema-deps'))
from jsonschema import Draft202012Validator, FormatChecker, ValidationError

frozen_hashes_verified = 0
for record, expected_count in [('docs/contract-freeze-v1.5.md', 10), ('docs/backup-contract-freeze-v1.md', 6)]:
    entries = re.findall(r'^\| (docs/[^|]+) \| ([a-f0-9]{64}) \|$', (ROOT / record).read_text(encoding='utf-8'), re.M)
    if len(entries) != expected_count or len({p for p, _ in entries}) != expected_count:
        raise ValueError('missing or duplicate frozen entries: ' + record)
    for path, digest in entries:
        if hashlib.sha256((ROOT / path).read_bytes()).hexdigest() != digest:
            raise ValueError('frozen artifact drift: ' + path)
        frozen_hashes_verified += 1


def pairs(values):
    result = {}
    for key, value in values:
        if key in result:
            raise ValueError('duplicate JSON key')
        result[key] = value
    return result


def parse(text):
    return json.loads(text, object_pairs_hook=pairs,
                      parse_constant=lambda _: (_ for _ in ()).throw(ValueError('non-finite number')))


schema = parse((ROOT / 'docs/backup-contract.schema.json').read_text(encoding='utf-8'))
Draft202012Validator.check_schema(schema)
validator = Draft202012Validator(schema, format_checker=FormatChecker())


def utc(value):
    if not re.fullmatch(r'\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,6})?Z', value):
        raise ValueError('UTC timestamp required')
    return datetime.fromisoformat(value.replace('Z', '+00:00'))


def normalized(path):
    windows = bool(re.match(r'^[A-Za-z]:', path))
    path = path.replace('\\', '/')
    if any(part in ('.', '..') for part in path.split('/')):
        raise ValueError('dot path segment')
    return (ntpath.normpath(path).replace('\\', '/').casefold() if windows
            else posixpath.normpath(path))


def validate(value):
    validator.validate(value)
    if 'format' in value:
        if utc(value['snapshot_at']) > utc(value['created_at']):
            raise ValueError('snapshot after creation')
        objects = value['objects']
        if len({x['key'] for x in objects}) != len(objects) or len({x['opaque_id'] for x in objects}) != len(objects):
            raise ValueError('duplicate object key or ID')
        if value['object_count'] != len(objects):
            raise ValueError('object count mismatch')
        if value['total_plaintext_bytes'] != value['database']['bytes'] + sum(x['bytes'] for x in objects):
            raise ValueError('plaintext byte sum mismatch')
        if value['required_header_kids'] != sorted(value['required_header_kids']):
            raise ValueError('kid order')
    elif 'plan_format' in value:
        if value['source_instance_id'] == value['target_instance_id']:
            raise ValueError('source equals target')
        lifetime = (utc(value['expires_at']) - utc(value['created_at'])).total_seconds()
        if lifetime != 900:
            raise ValueError('plan lifetime must be 15 minutes')
        if len({value[k] for k in ['target_storage_fingerprint', 'control_fingerprint', 'temporary_fingerprint']}) != 3:
            raise ValueError('same location fingerprints')
        payload = {k: v for k, v in value.items() if k != 'plan_digest'}
        if any(ord(c) > 127 for c in json.dumps(payload, ensure_ascii=False)):
            raise ValueError('plan contains non-ASCII values')
        canonical = json.dumps(payload, sort_keys=True, ensure_ascii=True, separators=(',', ':'), allow_nan=False).encode('ascii')
        if hashlib.sha256(canonical).hexdigest() != value['plan_digest']:
            raise ValueError('plan digest mismatch')
    elif 'operation_format' in value:
        terminal = value['phase'] in ('completed', 'failed', 'canceled')
        if terminal != (value['finished_at'] is not None):
            raise ValueError('terminal timestamp mismatch')
        if (value['phase'] == 'failed') != (value['failure_code'] is not None):
            raise ValueError('failure code mismatch')
        if value['phase'] == 'canceled' and not value['cancel_requested']:
            raise ValueError('canceled without confirmed cancel')
        if value['phase'] == 'completed' and value['cancel_requested']:
            raise ValueError('completed despite pending cancellation')
        if not utc(value['created_at']) <= utc(value['heartbeat_at']) <= utc(value['updated_at']):
            raise ValueError('operation timestamps reversed')
        if terminal and utc(value['finished_at']) != utc(value['updated_at']):
            raise ValueError('finish time differs from terminal update')
    else:
        if value['source']['instance_id'] == value['target']['instance_id']:
            raise ValueError('source equals target')
        stores = [value['source']['storage'], value['target']['storage'], value['backup'], value['control']]
        locations = [normalized(s['root'].rstrip('/\\') + '/' + s['namespace']) for s in stores]
        locations.append(normalized(value['temporary']['root']))
        for i, first in enumerate(locations):
            for second in locations[i+1:]:
                if first == second or first.startswith(second.rstrip('/') + '/') or second.startswith(first.rstrip('/') + '/'):
                    raise ValueError('overlapping namespaces')
        for endpoint in ['source', 'target']:
            normalized(value[endpoint]['database_dsn_file'])
        for path in [*value['encryption'].values(), value['quiescence_file']]:
            normalized(path)


manifest = parse((ROOT / 'docs/examples/backup-manifest.json').read_text(encoding='utf-8'))
config = parse((ROOT / 'docs/examples/backup-config.json').read_text(encoding='utf-8'))
cases = []


def negative(name, original, change):
    value = copy.deepcopy(original)
    change(value)
    try:
        validate(value)
    except (ValueError, ValidationError) as error:
        # Test reports contain only case names; no config contents or secrets.
        cases.append({'name': name, 'passed': True, 'rejection': type(error).__name__})
        return
    raise AssertionError('accepted invalid case: ' + name)


for name, value in [('manifest-example', manifest), ('config-example', config)]:
    validate(value)
    cases.append({'name': name, 'passed': True})
negative('unknown-field', manifest, lambda x: x.update(secret='forbidden'))
negative('unknown-format', manifest, lambda x: x.update(format_version=2))
negative('wrong-schema-checksum', manifest, lambda x: x['migrations'][0].update(checksum='0'*64))
negative('invalid-uuid', manifest, lambda x: x.update(backup_id='not-a-uuid'))
negative('invalid-calendar-time', manifest, lambda x: x.update(created_at='2026-02-30T00:00:00Z'))
negative('time-order', manifest, lambda x: x.update(snapshot_at='2027-01-01T00:00:00Z'))
negative('wrong-byte-sum', manifest, lambda x: x.update(total_plaintext_bytes=105))
negative('wrong-object-count', manifest, lambda x: x.update(object_count=2))
negative('duplicate-object-key', manifest, lambda x: x['objects'].append({**x['objects'][0], 'opaque_id': '2'*32}))
negative('duplicate-object-id', manifest, lambda x: x['objects'].append({**x['objects'][0], 'key': 'other'}))
for key in ['../escape', '/absolute', 'a/../escape', 'a\\b', 'C:/escape', 'a//b', 'a/./b', 'a\x00b']:
    negative('unsafe-key-' + repr(key), manifest, lambda x, key=key: x['objects'][0].update(key=key))
negative('oversize-object', manifest, lambda x: x['objects'][0].update(bytes=10737418241))
negative('negative-size', manifest, lambda x: x['database'].update(bytes=-1))
negative('unsupported-s3-profile', config, lambda x: x['backup'].update(provider='s3'))
negative('inline-secret', config, lambda x: x['source'].update(password='not-allowed'))
negative('relative-secret-file', config, lambda x: x['encryption'].update(identity_file='identity.txt'))
negative('same-instance', config, lambda x: x['target'].update(instance_id=x['source']['instance_id']))
negative('same-namespace', config, lambda x: x.update(backup=x['source']['storage']))
negative('nested-namespace', config, lambda x: x['backup'].update(namespace='source/nested'))
negative('case-alias-namespace', config, lambda x: x['backup'].update(root='d:/ISOLATED-BACKUP-EXAMPLE', namespace='SOURCE'))
negative('temporary-overlap', config, lambda x: x['temporary'].update(root='D:/isolated-backup-example/source/tmp'))
negative('dot-path-alias', config, lambda x: x['backup'].update(root='D:/isolated-backup-example/../elsewhere'))
negative('no-encryption-key-file', config, lambda x: x.update(encryption={}))
for text in ['{"a":1,"a":2}', '{"x":NaN}', '{"x":Infinity}']:
    try:
        parse(text)
    except ValueError:
        cases.append({'name': 'strict-json-' + text, 'passed': True})
    else:
        raise AssertionError('accepted invalid JSON')

plan = parse((ROOT / 'docs/examples/restore-plan.json').read_text(encoding='utf-8'))
operation = parse((ROOT / 'docs/examples/restore-operation.json').read_text(encoding='utf-8'))
for name, value in [('restore-plan-example', plan), ('restore-operation-example', operation),
                    ('plan-digest-independent-of-input-key-order', dict(reversed(list(plan.items()))))]:
    validate(value)
    cases.append({'name': name, 'passed': True})
negative('plan-tampered-target', plan, lambda x: x['target_database'].update(port=55440))
negative('plan-wrong-digest', plan, lambda x: x.update(plan_digest='f'*64))
negative('plan-invalid-expiry', plan, lambda x: x.update(expires_at='2026-11-13T08:16:00Z'))
negative('plan-production-db-denied', plan, lambda x: x['target_database'].update(database='yreader'))
negative('plan-external-db-denied', plan, lambda x: x['target_database'].update(host='example.com'))
negative('plan-inline-dsn-denied', plan, lambda x: x.update(database_url='postgres://synthetic'))
negative('active-state-finished-time-denied', operation, lambda x: x.update(finished_at=x['updated_at']))
negative('active-state-failure-code-denied', operation, lambda x: x.update(failure_code='storage_failed'))
negative('terminal-state-without-finish-denied', operation, lambda x: x.update(phase='completed'))
negative('canceled-without-request-denied', operation, lambda x: x.update(phase='canceled', finished_at=x['updated_at']))
negative('reversed-heartbeat-denied', operation, lambda x: x.update(heartbeat_at='2027-01-01T00:00:00Z'))


def transition(old, new, expected_generation, confirmed_dead=False):
    """Pure contract model; not an implementation of filesystem CAS or locking."""
    validate(old)
    validate(new)
    if old['generation'] != expected_generation or new['generation'] != old['generation'] + 1:
        raise ValueError('generation conflict')
    if old['phase'] in ('completed', 'failed', 'canceled'):
        raise ValueError('terminal state is immutable')
    for key in ['operation_id', 'backup_id', 'plan_digest', 'created_at', 'owner_id']:
        if new[key] != old[key]:
            raise ValueError('immutable binding changed')
    if utc(new['updated_at']) <= utc(old['updated_at']) or utc(new['heartbeat_at']) < utc(old['heartbeat_at']):
        raise ValueError('non-monotone transition time')
    forward = {'planned': 'staging', 'staging': 'validating', 'validating': 'ready_for_cutover', 'ready_for_cutover': 'completed'}
    if new['phase'] not in (old['phase'], forward[old['phase']], 'failed', 'canceled'):
        raise ValueError('illegal phase transition')
    if old['cancel_requested'] and not new['cancel_requested']:
        raise ValueError('cancel request reset')
    if old['cancel_requested'] and new['phase'] not in (old['phase'], 'failed', 'canceled'):
        raise ValueError('progress after cancellation')
    if new['failure_code'] == 'operation_interrupted' and not confirmed_dead:
        raise ValueError('timeout alone cannot confirm process death')


def next_state(old, phase):
    result = copy.deepcopy(old)
    stamp = (utc(old['updated_at']) + timedelta(seconds=1)).strftime('%Y-%m-%dT%H:%M:%SZ')
    result.update(phase=phase, generation=old['generation']+1, updated_at=stamp, heartbeat_at=stamp,
                  finished_at=stamp if phase in ('completed', 'failed', 'canceled') else None)
    return result


def reject_call(name, fn):
    try:
        fn()
    except ValueError:
        cases.append({'name': name, 'passed': True})
        return
    raise AssertionError('accepted invalid operation: ' + name)


previous = operation
for phase in ['staging', 'validating', 'ready_for_cutover', 'completed']:
    new = next_state(previous, phase)
    transition(previous, new, previous['generation'])
    cases.append({'name': 'transition-' + phase, 'passed': True})
    previous = new
reject_call('terminal-rewrite-denied', lambda: transition(previous, next_state(previous, 'completed'), previous['generation']))
reject_call('phase-skip-denied', lambda: transition(operation, next_state(operation, 'validating'), 1))
reject_call('stale-generation-denied', lambda: transition(operation, next_state(operation, 'staging'), 0))
request = next_state(operation, 'planned')
request['cancel_requested'] = True
transition(operation, request, 1)
cases.append({'name': 'cancel-request-persisted', 'passed': True})
cancel = next_state(request, 'canceled')
transition(request, cancel, request['generation'])
cases.append({'name': 'confirmed-cancel-terminal', 'passed': True})
reject_call('cancel-cannot-progress', lambda: transition(request, next_state(request, 'staging'), request['generation']))
dead = next_state(operation, 'failed')
dead['failure_code'] = 'operation_interrupted'
reject_call('stale-heartbeat-does-not-confirm-death', lambda: transition(operation, dead, 1))
transition(operation, dead, 1, confirmed_dead=True)
cases.append({'name': 'confirmed-dead-writer-may-fail-operation', 'passed': True})


def applicable(value, now, confirmed_instance):
    validate(value)
    if not utc(value['created_at']) <= utc(now) < utc(value['expires_at']):
        raise ValueError('plan not currently valid')
    if confirmed_instance != value['target_instance_id']:
        raise ValueError('target confirmation mismatch')


applicable(plan, '2026-11-13T08:14:59Z', plan['target_instance_id'])
cases.append({'name': 'unexpired-plan-confirmation', 'passed': True})
reject_call('expiry-boundary-denied', lambda: applicable(plan, plan['expires_at'], plan['target_instance_id']))
reject_call('future-plan-denied', lambda: applicable(plan, '2026-11-13T07:59:59Z', plan['target_instance_id']))
reject_call('wrong-target-confirmation-denied', lambda: applicable(plan, plan['created_at'], plan['source_instance_id']))
report = ROOT / '.toolchain/backup-contract-checks.json'
report.write_text(json.dumps({'passed': True, 'checks': cases, 'frozen_hashes_verified': frozen_hashes_verified}, indent=2) + '\n', encoding='utf-8')
print(f'{len(cases)} contract checks passed; {frozen_hashes_verified} frozen hashes match; report: {report}')
