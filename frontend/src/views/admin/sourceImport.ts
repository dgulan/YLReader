import { computed, onScopeDispose, ref, watch } from 'vue'
import { api } from '@/api/reader'
import { errorText, getSession, type Model } from '@/api/client'

export function useSourceImport(reload: () => Promise<void>, confirmOverwrite: (report: Model<'ImportResult'>) => Promise<boolean> = async () => false) {
  const text = ref(''), sourceUrl = ref(''), fileLabel = ref(''), error = ref(''), busy = ref(false)
  const dryRun = ref<Model<'ImportResult'>>(), result = ref<Model<'ImportResult'>>()
  const dryRunMode = ref<'text' | 'url' | ''>('')
  let payload = '', revision = 0, activity = 0, disposed = false
  const canPreview = computed(() => Boolean(fileLabel.value || text.value.trim()) && !busy.value)
  const canPreviewUrl = computed(() => /^https?:\/\/\S+$/i.test(sourceUrl.value.trim()) && !busy.value)
  const canCommitText = computed(() => canPreview.value && Boolean(dryRun.value) && dryRunMode.value === 'text')
  const canCommitUrl = computed(() => Boolean(dryRun.value && dryRunMode.value === 'url') && !busy.value)
  watch(text, () => { revision++; payload = ''; fileLabel.value = ''; dryRun.value = undefined; dryRunMode.value = ''; result.value = undefined }, { flush: 'sync' })
  watch(sourceUrl, () => { revision++; dryRun.value = undefined; dryRunMode.value = ''; result.value = undefined }, { flush: 'sync' })
  onScopeDispose(() => { disposed = true; revision++; activity++; payload = '' })
  async function readFile(file: Pick<File, 'name' | 'size' | 'text'>) {
    const token = ++activity
    revision++
    busy.value = false
    dryRun.value = undefined; dryRunMode.value = ''; result.value = undefined; payload = ''; fileLabel.value = ''; text.value = ''; error.value = ''
    // Clearing text can advance the revision through its synchronous watcher.
    const at = revision
    if (file.size > 32 * 1024 * 1024) { error.value = '书源文件不能超过 32 MiB'; return }
    busy.value = true
    try {
      const value = await file.text()
      if (disposed || at !== revision) return
      if (file.size > 512 * 1024 || value.length > 512 * 1024) { payload = value; fileLabel.value = `${file.name}（${(file.size / 1024 / 1024).toFixed(2)} MiB）` }
      else text.value = value
    } catch { if (!disposed && at === revision) error.value = '无法读取书源文件，请重新选择' }
    finally { if (!disposed && token === activity) busy.value = false }
  }
  async function importText(preview: boolean) {
    const owner = getSession()?.user.id, input = fileLabel.value ? payload : text.value, at = revision
    if (!owner || busy.value || !input.trim() || (!preview && !canCommitText.value)) return
    const token = ++activity
    const valid = () => !disposed && revision === at && getSession()?.user.id === owner
    busy.value = true; error.value = ''
    try {
      const original: unknown = JSON.parse(input)
      let report = await api.importSources(input, preview, 'skip')
      if (!valid()) return
      if (preview) { dryRun.value = report; dryRunMode.value = 'text'; return }
      result.value = report; dryRun.value = undefined; dryRunMode.value = ''
      const duplicates = report.duplicates ?? []
      if (report.duplicate_count && duplicates.length && await confirmOverwrite(report) && valid()) {
        const urls = new Set(duplicates.map(item => item.book_source_url))
        const rows: unknown[] = Array.isArray(original) ? original : [original]
        const selected = rows.map((row, index) => ({ row, index })).filter(({ row }) => {
          if (!row || typeof row !== 'object' || !('bookSourceUrl' in row) || typeof row.bookSourceUrl !== 'string') return false
          return urls.delete(row.bookSourceUrl)
        })
        if (selected.length) {
          const updated = await api.importSources(JSON.stringify(selected.map(entry => entry.row)), false, 'overwrite')
          if (!valid()) return
          report = {
            ...updated, imported: report.imported + updated.imported,
            skipped: report.skipped - selected.length + updated.skipped,
            failed: [...report.failed, ...updated.failed.map(failure => ({ ...failure, index: selected[failure.index]?.index ?? failure.index }))],
            imported_count: (report.imported_count ?? report.imported) + (updated.imported_count ?? updated.imported),
          }
          result.value = report
        }
      }
    } catch (e) { if (valid()) error.value = e instanceof SyntaxError ? 'JSON 格式不正确，请检查文件内容' : errorText(e) }
    finally {
      try { if (!preview && valid() && result.value) await reload() }
      finally { if (!disposed && token === activity) busy.value = false }
    }
  }
  async function importUrl(preview: boolean) {
    const owner = getSession()?.user.id, target = sourceUrl.value.trim(), at = revision
    if (!owner || busy.value || !/^https?:\/\/\S+$/i.test(target) || (!preview && (!dryRun.value || dryRunMode.value !== 'url'))) return
    const token = ++activity
    const valid = () => !disposed && revision === at && getSession()?.user.id === owner
    busy.value = true; error.value = ''
    try {
      if (preview) {
        const report = await api.importSourcesFromUrl(target, true, 'skip')
        if (valid()) { dryRun.value = report; dryRunMode.value = 'url' }
        return
      }
      const existing = dryRun.value
      const overwrite = existing && (existing.duplicate_count ?? existing.skipped) > 0 && await confirmOverwrite(existing)
      if (!valid()) return
      const report = await api.importSourcesFromUrl(target, false, overwrite ? 'overwrite' : 'skip')
      if (!valid()) return
      result.value = report; dryRun.value = undefined; dryRunMode.value = ''
      await reload()
    } catch (e) { if (valid()) error.value = errorText(e) }
    finally { if (!disposed && token === activity) busy.value = false }
  }
  return { text, sourceUrl, fileLabel, error, busy, dryRun, result, canPreview, canPreviewUrl, canCommitText, canCommitUrl, readFile, importText, importUrl }
}
