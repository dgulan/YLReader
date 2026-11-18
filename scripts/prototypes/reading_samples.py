"""Deterministic original reading fixtures; no third-party text or network I/O."""
import hashlib
from pathlib import Path
import zipfile


def create_reading_samples(out: Path):
    out.mkdir(parents=True, exist_ok=True)
    epub, txt = out / 'original.epub', out / '原创编号验收.txt'
    titles = ['山间的晨光', '河岸的午后', '星光下的书页']
    members = {
        'mimetype': 'application/epub+zip',
        'META-INF/container.xml': '<container xmlns="urn:oasis:names:tc:opendocument:xmlns:container" version="1.0"><rootfiles><rootfile full-path="book.opf" media-type="application/oebps-package+xml"/></rootfiles></container>',
        'book.opf': '<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="id"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:identifier id="id">original-stage3</dc:identifier><dc:title>原创中文验收</dc:title><dc:language>zh-CN</dc:language><dc:creator>YLReader原创测试</dc:creator><meta property="dcterms:modified">2026-11-15T00:00:00Z</meta></metadata><manifest><item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>' + ''.join(f'<item id="c{i}" href="c{i}.xhtml" media-type="application/xhtml+xml"/>' for i in range(3)) + '</manifest><spine>' + ''.join(f'<itemref idref="c{i}"/>' for i in range(3)) + '</spine></package>',
        'nav.xhtml': '<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops"><head><title>目录</title></head><body><nav epub:type="toc"><ol>' + ''.join(f'<li><a href="c{i}.xhtml">第{i+1}章</a></li>' for i in range(3)) + '</ol></nav></body></html>',
    }
    for i, title in enumerate(titles):
        members[f'c{i}.xhtml'] = '<html xmlns="http://www.w3.org/1999/xhtml"><head><title>' + title + '</title></head><body><h1>' + title + '</h1>' + ''.join(f'<p>第{n}段。{title}，我们在这里继续原创的故事。😀</p>' for n in range(100)) + '</body></html>'
    with zipfile.ZipFile(epub, 'w') as archive:
        for name, text in members.items():
            info = zipfile.ZipInfo(name, date_time=(2026, 11, 15, 0, 0, 0))
            info.create_system = 3
            info.external_attr = 0o600 << 16
            archive.writestr(info, text.encode('utf-8'))
    txt.write_text(''.join(f'第{i}章 原创编号验收\n' + ''.join(f'原创编号{i:03d}。这是独立编写的验收段落{n}，用于核对章节身份和中文排版。😀\n' for n in range(40)) + '\n' for i in range(1, 121)), encoding='utf-8', newline='\n')
    manifest = {name: {'sha256': hashlib.sha256(file.read_bytes()).hexdigest(), 'expected_chapters': count}
                for name, file, count in [('epub', epub, 3), ('numbered_txt', txt, 120)]}
    return epub, txt, manifest
