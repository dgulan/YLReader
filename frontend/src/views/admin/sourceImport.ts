import { computed, onScopeDispose, ref, watch } from 'vue'
import { api } from '@/api/reader'
import { errorText, getSession, type Model } from '@/api/client'

export function useSourceImport(reload: () => Promise<void>) {
  const text = ref(''), fileLabel = ref(''), error = ref(''), busy = ref(false)
  const dryRun = ref<Model<'ImportResult'>>(), result = ref<Model<'ImportResult'>>()
  let payload = '', revision = 0, activity = 0, disposed = false
  const canPreview = computed(() => Boolean(fileLabel.value || text.value.trim()) && !busy.value)
  watch(text, () => { revision++; payload = ''; fileLabel.value = ''; dryRun.value = undefined; result.value = undefined }, { flush: 'sync' })
  onScopeDispose(() => { disposed = true; revision++; activity++; payload = '' })
  async function readFile(file: Pick<File, 'name' | 'size' | 'text'>) {
    const token = ++activity
    revision++
    busy.value = false
    dryRun.value = undefined; result.value = undefined; payload = ''; fileLabel.value = ''; text.value = ''; error.value = ''
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
    if (!owner || busy.value || (!preview && !dryRun.value)) return
    const token = ++activity
    const valid = () => !disposed && revision === at && getSession()?.user.id === owner
    busy.value = true; error.value = ''
    try {
      JSON.parse(input)
      const report = await api.importSources(input, preview)
      if (!valid()) return
      if (preview) dryRun.value = report
      else { result.value = report; dryRun.value = undefined; await reload() }
    } catch (e) { if (valid()) error.value = e instanceof SyntaxError ? 'JSON 格式不正确，请检查文件内容' : errorText(e) }
    finally { if (!disposed && token === activity) busy.value = false }
  }
  return { text, fileLabel, error, busy, dryRun, result, canPreview, readFile, importText }
}
