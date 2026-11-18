import ast
from collections import Counter
import hashlib
from pathlib import Path
import re
import unittest


class DirectoryLabelsTest(unittest.TestCase):
    def diagnose(self, titles, expected):
        source = Path(__file__).resolve().parents[1] / 'verify-stage3-worker-reading.py'
        tree = ast.parse(source.read_text(encoding='utf-8'))
        function = next(n for n in tree.body if isinstance(n, ast.FunctionDef) and n.name == 'directory_label_diagnostics')
        ns = {'re': re, 'Counter': Counter, 'hashlib': hashlib}
        exec(compile(ast.Module(body=[function], type_ignores=[]), str(source), 'exec'), ns)
        return ns[function.name]([{'chapter_index': i, 'title': title, 'url': f'https://example.com/{i}'} for i, title in enumerate(titles)], expected)

    def test_duplicate_titles_keep_distinct_urls(self):
        result = self.diagnose(['第1章 相同', '第1章 相同', '完本感言'], 2)
        self.assertEqual(result['missing_label_samples'], [2])
        self.assertEqual(len(result['duplicate_samples']), 2)
        self.assertNotEqual(*[c['url_sha256'] for c in result['duplicate_samples']])
        self.assertEqual(result['non_numbered_samples'][0]['title'], '完本感言')
        self.assertFalse(result['text_missing_proven'])
        self.assertNotIn('https://', str(result))

    def test_missing_labels_include_neighbors(self):
        result = self.diagnose(['第1章 开始', '第二章 中文数字', '第3章 后续'], 3)
        gap = result['missing_neighborhoods'][0]
        self.assertEqual(gap['missing_label'], 2)
        self.assertEqual(gap['before']['chapter_index'], 0)
        self.assertEqual(gap['after']['chapter_index'], 2)
        self.assertFalse(result['text_missing_proven'])

    def test_valid_numeric_labels(self):
        result = self.diagnose(['第1章 一', '第2章 二'], 2)
        self.assertEqual(result['missing_label_samples'], [])
        self.assertEqual(result['duplicate_samples'], [])
        self.assertEqual(result['non_numbered_samples'], [])


if __name__ == '__main__':
    unittest.main()
