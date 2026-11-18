"""Create local, source-only audit batches from the CURRENT working tree.

Does not upload, commit, read .env, or traverse ignored dependency/data directories.
Run from any directory: python scripts/package-gemini-audit.py
"""
from pathlib import Path
from datetime import datetime, timezone, timedelta
import hashlib
import json
import re
import subprocess
import uuid
import zipfile

ROOT = Path(__file__).resolve().parents[1]
OUT = ROOT / '.toolchain' / ('gemini-audit-' + datetime.now(timezone(timedelta(hours=8))).strftime('%Y%m%d-%H%M%S') + '-' + uuid.uuid4().hex[:6])


def git(*args):
    return subprocess.check_output(['git', '-c', 'safe.directory=' + ROOT.as_posix(), *args], cwd=ROOT)


candidates = sorted(set(git('ls-files', '--cached', '--others', '--exclude-standard', '-z').decode('utf-8').split('\0')) - {''})
common = {
    'AGENTS.md', 'README.md', '.env.example', '.gitattributes', '.gitignore', 'Makefile', 'docker-compose.yml',
    'backend/go.mod', 'backend/Dockerfile', 'backend/.dockerignore',
    'frontend/package.json', 'frontend/Dockerfile', 'frontend/.dockerignore', 'frontend/nginx.conf',
    'docs/architecture.md', 'docs/schema.sql', 'docs/openapi.yaml', 'docs/contract-freeze-v1.5.md',
    'docs/research/summary.md', 'docs/deployment.md',
    'scripts/check-db-target.py', 'scripts/test-db-guard.py', 'scripts/compose-check.env',
    'scripts/package-gemini-audit.py', 'docs/gemini-audit-upload-guide-2026-11-14.md',
    'docs/gemini-audit-recheck-2026-11-14.md',
    'docs/gemini-remediation-design-2026-11-14.md',
    'docs/gemini-remediation-verification-2026-11-14.md',
    'docs/gemini-followup-design-2026-11-14.md',
    'docs/gemini-followup-recheck-2026-11-14.md',
    'docs/gemini-followup-verification-2026-11-14.md',
    'docs/release-readiness-recheck-2026-11-15.md',
    'docs/reader-linux-webkit-verification-2026-11-16.md',
    'docs/reader-soak-verification-2026-11-16.md',
    'docs/gemini-audit-upload-guide-2026-11-16.md',
    'docs/backup-next-stage-plan-2026-11-16.md',
    'docs/backup-preview-cli-verification-2026-11-16.md',
}
groups = {name: {} for name in [
    '00-context', '01-engine-security', '02-backend-business', '03-frontend', '04-backup-restore',
    '05-optional-generated-locks', '06-optional-history-research',
]}
excluded = []
secrets = []
patterns = [
    re.compile(rb'-----BEGIN (?:RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----'),
    re.compile(rb'\bgh[pousr]_[A-Za-z0-9]{30,}\b'),
    re.compile(rb'\bgithub_pat_[A-Za-z0-9_]{40,}\b'),
    re.compile(rb'\bAKIA[0-9A-Z]{16}\b'),
]

for name in candidates:
    path = ROOT / name
    # No links, reparse points, credentials, artifacts, or large external samples.
    if path.is_symlink() or path.resolve().parent != (ROOT / name).absolute().parent or not path.is_file():
        excluded.append({'path': name, 'reason': 'not a regular source file'})
        continue
    if name.startswith(('references/', '.toolchain/', '.git/')) or ('/testdata/legado_samples/' in name):
        excluded.append({'path': name, 'reason': 'excluded data/reference/cache; not audit source'})
        continue
    if path.name.startswith('.env') and name != '.env.example':
        excluded.append({'path': name, 'reason': 'private environment file'})
        continue
    if path.suffix.lower() in {'.pem', '.key', '.p12', '.pfx', '.dump', '.epub', '.txt', '.zip', '.exe', '.png', '.jpg', '.pdf'}:
        # Prototype requirements are plain public build notes, not a book/data file.
        if name not in {'scripts/prototypes/backup-age/schema-requirements.txt', 'backend/internal/backup/archive_columns_v1.txt', 'backend/internal/backup/archive_inventory_v1.txt'}:
            excluded.append({'path': name, 'reason': 'data/binary/private or unclassified text artifact'})
            continue
    target = []
    if name in common or name.startswith('.github/workflows/'):
        target.append('00-context')
    if name.startswith('backend/internal/engine/') or (name.startswith('backend/internal/pkg/') and not path.name.startswith('backup_')) or name == 'backend/Dockerfile.sandboxworker' or (name.startswith('backend/internal/service/auth') and name.endswith('.go')) or name == 'backend/internal/api/auth.go' or name.startswith(('backend/cmd/server/', 'backend/cmd/sandboxprobe/', 'backend/cmd/sandboxworker/', 'backend/cmd/sourceprobe/')):
        target.append('01-engine-security')
    if name.startswith(('backend/cmd/', 'backend/internal/api/', 'backend/internal/service/', 'backend/internal/repository/', 'backend/internal/migration/')) and name != 'backend/internal/api/generated.gen.go':
        target.append('02-backend-business')
    if name.startswith('frontend/') and name != 'frontend/package-lock.json':
        target.append('03-frontend')
    if name.startswith(('backend/cmd/backup/', 'backend/internal/backup/', 'backend/internal/migration/', 'backend/internal/pkg/crypto/', 'backend/internal/pkg/storage/', 'scripts/prototypes/backup-age/', 'docs/examples/')) or name.startswith('scripts/verify-backup-') or name.startswith('docs/backup-'):
        target.append('04-backup-restore')
    if name == 'backend/internal/api/generated.gen.go' or path.name in {'go.sum', 'package-lock.json'}:
        target.append('05-optional-generated-locks')
    if name.startswith('docs/') and (path.name.startswith(('modern-web-', 'reader-', 'stage3-reading-'))):
        target.append('03-frontend')
    if name in {'scripts/verify-stage3-reading.py', 'scripts/verify-web-proxy.py', 'scripts/verify-frontend-image.py', 'scripts/prototypes/reading_samples.py', 'scripts/tests/test_reading_samples.py', 'scripts/prototypes/webkit-network-repro.mjs', 'scripts/prototypes/reader_soak_result.py', 'scripts/tests/test_reader_soak_result.py'}:
        target.append('03-frontend')
    if name.startswith('docs/') and path.name.startswith(('dependency-security-', 'audit-remediation-', 'domain-admission-', 'source-admission-', 'sandbox-resource-', 'sandbox-worker-', 'browser-assistance-', 'stage3-worker-', 'source-network-')):
        target.append('01-engine-security')
    if name in {'scripts/verify-sandbox-isolation.py', 'scripts/verify-sandbox-worker.py', 'scripts/verify-sandbox-worker-performance.py', 'scripts/verify-browser-relay-topology.py', 'scripts/verify-stage3-worker-reading.py', 'scripts/tests/test_toc_migration.py', 'scripts/tests/test_directory_labels.py', 'scripts/verify-source-network.py'}:
        target.append('01-engine-security')
    if name.startswith('docs/') and path.name.startswith(('migration-', 'import-budget-', 'search-request-', 'reader-screenshot-', 'reader-toc-')):
        target.append('02-backend-business')
    if name.startswith('docs/') and not target:
        target.append('06-optional-history-research')
    if not target:
        excluded.append({'path': name, 'reason': 'not needed for scoped source audit'})
        continue
    data = path.read_bytes()
    try:
        text = data.decode('utf-8-sig')
    except UnicodeDecodeError:
        excluded.append({'path': name, 'reason': 'not UTF-8 source'})
        continue
    if b'\0' in data or len(data) > 1_000_000:
        raise RuntimeError('Unexpected binary/oversized selected source: ' + name)
    if any(pattern.search(data) for pattern in patterns):
        secrets.append(name)
    for group in set(target):
        groups[group][name] = (data, len(text.splitlines()))

if secrets:
    raise RuntimeError('Review potential credentials before packaging (values suppressed): ' + ', '.join(sorted(set(secrets))))

OUT.mkdir(parents=True, exist_ok=False)
snapshot = {
    'captured_at': datetime.now(timezone(timedelta(hours=8))).isoformat(),
    'branch': git('branch', '--show-current').decode().strip(),
    'head': git('rev-parse', 'HEAD').decode().strip(),
    'source': 'current working tree including modified and untracked non-ignored files',
    'not_a_clean_commit_export': True,
    'secrets_check': 'limited private-key/GitHub/AWS patterns only; not a comprehensive credential audit',
    'groups': {},
}
status = git('status', '--short').decode('utf-8')
index = []
for group, files in groups.items():
    manifest = [{'path': name, 'bytes': len(data), 'lines': lines, 'sha256': hashlib.sha256(data).hexdigest()} for name, (data, lines) in sorted(files.items())]
    instruction = f'''# {group}

This is one part of a CURRENT, DIRTY working-tree snapshot, not only HEAD.
Read 00-context first. Other batches supply dependencies; these ZIPs are not standalone builds.
Preserve original relative paths. Some shared source files repeat across batches with identical hashes.
Tests and verification documents are evidence to inspect, not proof that you executed tests.
Omitted large Legado samples mean related sample-driven tests cannot run from these packages alone.
Optional generated/lockfiles and historical research are separate; request them only as needed.
Report concrete defects with original path and verified line numbers, trigger, impact, and proposed fix.
Do not invent missing code, treat design intentions as implemented, or claim production readiness.
'''
    combined = [instruction]
    for name, (data, _) in sorted(files.items()):
        combined.append('\n\n===== FILE: ' + name + ' =====\n' + data.decode('utf-8-sig') + '\n===== END FILE =====\n')
    text_path = OUT / (group + '.txt')
    text_path.write_text(''.join(combined), encoding='utf-8', newline='\n')
    # Keep complete original files together; avoid the truncation seen in the
    # first review. A single larger generated file may exceed the soft target.
    parts = []
    batch = [instruction]
    size = len(instruction.encode('utf-8'))
    for block in combined[1:]:
        block_size = len(block.encode('utf-8'))
        if len(batch) > 1 and size + block_size > 120_000:
            parts.append(''.join(batch)); batch = [instruction]; size = len(instruction.encode('utf-8'))
        batch.append(block); size += block_size
    if len(batch) > 1:
        parts.append(''.join(batch))
    for number, part in enumerate(parts, 1):
        (OUT / f'{group}.part-{number:02d}.txt').write_text(part, encoding='utf-8', newline='\n')
    manifest_bytes = (json.dumps(manifest, ensure_ascii=False, indent=2) + '\n').encode('utf-8')
    with zipfile.ZipFile(OUT / (group + '.zip'), 'w', compression=zipfile.ZIP_DEFLATED, compresslevel=9) as archive:
        archive.writestr('_AUDIT_README.md', instruction)
        archive.writestr('_FILE_MANIFEST.json', manifest_bytes)
        archive.writestr('_SNAPSHOT.json', json.dumps({k: v for k, v in snapshot.items() if k != 'groups'}, ensure_ascii=False, indent=2))
        for name, (data, _) in sorted(files.items()):
            archive.writestr(name, data)
    stats = {'files': len(files), 'source_bytes': sum(r['bytes'] for r in manifest), 'lines': sum(r['lines'] for r in manifest), 'zip_bytes': (OUT / (group + '.zip')).stat().st_size, 'text_bytes': text_path.stat().st_size, 'text_parts': len(parts)}
    snapshot['groups'][group] = stats
    index.extend(dict(row, batch=group) for row in manifest)

(OUT / 'SNAPSHOT.json').write_text(json.dumps(snapshot, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
(OUT / 'FILE_INDEX.json').write_text(json.dumps(index, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
(OUT / 'EXCLUSIONS.json').write_text(json.dumps(excluded, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
(OUT / 'WORKTREE_STATUS.txt').write_text(status, encoding='utf-8')
readme = '''# YLReader 审计资料

先上传 00-context，再一次只审一个主题包：01-engine-security、02-backend-business、03-frontend、04-backup-restore。
05（生成代码/锁文件）与 06（历史研究/记录）按需补充，不建议首轮全部投入上下文。
同一主题的 ZIP 与 TXT 内容重叠，只上传其中一种；TXT 的 FILE 标记保留原路径，文件内容不加行号。
若整份 TXT 仍截断，可改用 part-XX.txt 分段，一次审一个子范围；分段不切开原始文件。不要同时上传整份与分段。
ZIP 保留原目录，适合能解压并逐文件读取的工具；若不能确认完整读取 ZIP，改用对应 TXT。
这是当前未提交工作树快照；仅访问 GitHub/HEAD 会漏掉大量待审修改。所有批次来自同一轮打包。
各包有依赖关系，不能单独编译；需要完整构建时需合并主要批次和 05，且大测试样本仍缺失。
没有上传任何资料、部署或修改业务数据。私密配置、.git、references、依赖、工具链、构建/测试产物均不在包内。
凭据检查只检查有限模式，不保证覆盖所有敏感信息；公开示例和合成测试常量不等于真实凭据。
复现打包：python scripts/package-gemini-audit.py。先看项目 docs/gemini-audit-upload-guide-2026-11-16.md；旧指南用于历史参考，当前能力以新指南为准。
'''
(OUT / 'START_HERE.md').write_text(readme, encoding='utf-8')
print(json.dumps({'output': str(OUT), 'snapshot': snapshot}, ensure_ascii=False, indent=2))
