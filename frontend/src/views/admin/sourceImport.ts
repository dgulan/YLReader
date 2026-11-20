import { computed, onScopeDispose, ref, watch } from 'vue'
import { api } from '@/api/reader'
import { errorText, getSession, type Model } from '@/api/client'

export function useSourceImport(reload: () => Promise<void>) {
  const text = ref(''), sourceUrl = ref(''), fileLabel = ref(''), error = ref(''), busy = ref(false)
  const dryRun = ref<Model<'ImportResult'>>(), result = ref<Model<'ImportResult'>>()
  const dryRunMode = ref<'text' | 'url' | ''>('')
  let payload = '', revision = 0, activity = 0, disposed = false
  const canPreview = computed(() => Boolean(fileLabel.value || text.value.trim()) && !busy.value)
  const canPreviewUrl = computed(() => /^https?:\/\/\S+$/i.test(sourceUrl.value.trim()) && !busy.value)
  const canCommitText = computed(() => Boolean(dryRun.value && dryRunMode.value === 'text') && !busy.value)
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
    if (!owner || busy.value || (!preview && (!dryRun.value || dryRunMode.value !== 'text'))) return
    const token = ++activity
    const valid = () => !disposed && revision === at && getSession()?.user.id === owner
    busy.value = true; error.value = ''
    try {
      JSON.parse(input)
      const report = await api.importSources(input, preview)
      if (!valid()) return
      if (preview) { dryRun.value = report; dryRunMode.value = 'text' }
      else { result.value = report; dryRun.value = undefined; dryRunMode.value = ''; await reload() }
    } catch (e) { if (valid()) error.value = e instanceof SyntaxError ? 'JSON 格式不正确，请检查文件内容' : errorText(e) }
    finally { if (!disposed && token === activity) busy.value = false }
  }
  async function importUrl(preview: boolean) {
    const owner = getSession()?.user.id, target = sourceUrl.value.trim(), at = revision
    if (!owner || busy.value || !/^https?:\/\/\S+$/i.test(target) || (!preview && (!dryRun.value || dryRunMode.value !== 'url'))) return
    const token = ++activity
    const valid = () => !disposed && revision === at && getSession()?.user.id === owner
    busy.value = true; error.value = ''
    try {
      const report = await api.importSourcesFromUrl(target, preview)
      if (!valid()) return
      if (preview) { dryRun.value = report; dryRunMode.value = 'url' }
      else { result.value = report; dryRun.value = undefined; dryRunMode.value = ''; await reload() }
    } catch (e) { if (valid()) error.value = errorText(e) }
    finally { if (!disposed && token === activity) busy.value = false }
  }
  return { text, sourceUrl, fileLabel, error, busy, dryRun, result, canPreview, canPreviewUrl, canCommitText, canCommitUrl, readFile, importText, importUrl }
}
