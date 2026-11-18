from pathlib import Path
import re
import tempfile
import unittest
import xml.etree.ElementTree as ET
import zipfile
from scripts.prototypes.reading_samples import create_reading_samples


class ReadingSamplesTest(unittest.TestCase):
    def test_reproducible_epub_has_complete_spine_and_unique_members(self):
        with tempfile.TemporaryDirectory() as directory:
            a, b = Path(directory) / 'a', Path(directory) / 'b'
            epub, _, manifest = create_reading_samples(a)
            other, _, repeated = create_reading_samples(b)
            self.assertEqual(epub.read_bytes(), other.read_bytes())
            self.assertEqual(manifest, repeated)
            with zipfile.ZipFile(epub) as archive:
                self.assertEqual(archive.namelist()[0], 'mimetype')
                self.assertEqual(archive.getinfo('mimetype').compress_type, zipfile.ZIP_STORED)
                self.assertEqual(len(archive.namelist()), len(set(archive.namelist())))
                opf = ET.fromstring(archive.read('book.opf'))
                ns = {'o': 'http://www.idpf.org/2007/opf'}
                items = {item.get('id'): item.get('href') for item in opf.findall('o:manifest/o:item', ns)}
                refs = opf.findall('o:spine/o:itemref', ns)
                self.assertEqual(len(refs), 3)
                for ref in refs:
                    ET.fromstring(archive.read(items[ref.get('idref')]))

    def test_txt_has_exact_numbering_and_distinct_content_markers(self):
        with tempfile.TemporaryDirectory() as directory:
            _, txt, _ = create_reading_samples(Path(directory))
            text = txt.read_text(encoding='utf-8')
            self.assertEqual([int(n) for n in re.findall(r'^第(\d+)章', text, re.M)], list(range(1, 121)))
            self.assertIn('原创编号001', text)
            self.assertIn('原创编号120', text)
            self.assertNotIn(b'\r', txt.read_bytes())


if __name__ == '__main__':
    unittest.main()
