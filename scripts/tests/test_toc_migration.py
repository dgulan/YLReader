"""Offline negative tests for the real-source migration acceptance gate."""
import ast
import copy
import hashlib
import json
from pathlib import Path
from types import SimpleNamespace
import time
import unittest


class MigrationGateTest(unittest.TestCase):
    def run_gate(self, fault=None):
        original = {'bookSourceName': 'original', 'bookSourceUrl': 'https://books.example',
                    'header': {}, 'enabled': True, 'ruleToc': {'chapterList': 'li', 'nextTocUrl': ''}}
        adapted = copy.deepcopy(original)
        adapted['bookSourceName'] = 'adapter'
        adapted['ruleToc'].update(chapterList='dd', nextTocUrl='a.next@href')
        if fault == 'unrelated_rule':
            adapted['searchUrl'] = '/changed'
        data = json.dumps([adapted]).encode()
        args = SimpleNamespace(migration_expected_old_count=2, expected_directory_count=3,
                               migration_bookmark_index=[0, 1], migrate_to_source=SimpleNamespace(
                                   read_text=lambda **_: data.decode(), read_bytes=lambda: data))
        old = {'total': 2, 'source_id': 'source', 'items': [
            {'chapter_index': 0, 'title': 'last', 'url': 'https://books.example/last'},
            {'chapter_index': 1, 'title': 'first', 'url': 'https://books.example/first'}]}
        new = {'total': 3, 'source_id': 'source', 'items': [old['items'][1].copy(),
            {'chapter_index': 1, 'title': 'middle', 'url': 'https://books.example/middle'}, old['items'][0].copy()]}
        for index, row in enumerate(new['items']):
            row['chapter_index'] = index
        state = {'progress': {'version': 1}, 'marks': [], 'migrated': False}

        def api(method, path, body=None, extra_headers=None):
            if path == '/sources/source':
                return 200, {'id': 'source', 'book_source_name': 'adapter'}
            if path.endswith('/progress'):
                if method == 'GET':
                    return 200, copy.deepcopy(state['progress'])
                if state['progress']['version'] == 1:
                    state['progress'] = dict(body, version=2)
                    return 200, copy.deepcopy(state['progress'])
                return (200 if fault == 'stale_accepted' else 409), {}
            if path.endswith('/bookmarks'):
                if method == 'GET':
                    return 200, copy.deepcopy(state['marks'])
                mark = dict(body, id=str(len(state['marks'])), created_at='2026-11-15T00:00:00Z')
                state['marks'].append(mark)
                return 201, copy.deepcopy(mark)
            if '/chapters?' in path:
                return 200, copy.deepcopy(new if state['migrated'] else old)
            if path == '/books/book':
                return 200, {'id': 'book', 'version': 2 if state['migrated'] else 1,
                             'active_source_id': 'wrong' if fault == 'active_source' and state['migrated'] else 'source'}
            raise AssertionError('unexpected API request')

        def stage(name, method, path, body=None):
            if name == 'migration_import_adapter':
                return 200, {}, {}
            if fault == 'refresh_failed':
                return 504, {}, {}
            state['migrated'] = True
            state['progress'].update(version=3, chapter_index=2)
            for mark in state['marks']:
                mark['chapter_index'] = {0: 2, 1: 0}[mark['chapter_index']]
            if fault == 'wrong_progress':
                state['progress']['chapter_index'] = 0
            if fault == 'lost_note':
                state['marks'][0].pop('note')
            if fault == 'wrong_bookmark_id':
                state['marks'][0]['id'] = 'replacement'
            return 200, copy.deepcopy(new), {}

        source = Path(__file__).resolve().parents[1] / 'verify-stage3-worker-reading.py'
        tree = ast.parse(source.read_text(encoding='utf-8'))
        function = next(node for node in tree.body if isinstance(node, ast.FunctionDef) and node.name == 'migrate_directory')
        result = {}
        namespace = {'a': args, 'api': api, 'stage': stage, 'result': result,
                     'json': json, 'hashlib': hashlib, 'time': time}
        exec(compile(ast.Module(body=[function], type_ignores=[]), str(source), 'exec'), namespace)
        namespace['migrate_directory'](original, 'source', 'book', old)
        return result

    def test_correct_migration(self):
        self.assertTrue(self.run_gate()['migration_passed'])

    def test_false_successes_rejected(self):
        for fault in ['wrong_progress', 'lost_note', 'wrong_bookmark_id', 'stale_accepted', 'active_source']:
            with self.subTest(fault=fault):
                self.assertFalse(self.run_gate(fault)['migration_passed'])

    def test_failed_refresh_preserves_state(self):
        report = self.run_gate('refresh_failed')['migration']
        self.assertFalse(report['passed'])
        self.assertTrue(report['failure_preserved_old_state'])

    def test_unrelated_adapter_change_rejected(self):
        with self.assertRaisesRegex(RuntimeError, 'outside declared'):
            self.run_gate('unrelated_rule')


if __name__ == '__main__':
    unittest.main()
