<script setup lang="ts">
import { computed, ref, watch, onScopeDispose } from 'vue'
import { api } from '@/api/reader'
import { errorText, getSession, type Model } from '@/api/client'
import VirtualList from '@/components/VirtualList.vue'
const props = defineProps<{ bookId: string; activeSource?: string | null; bookName?: string; bookAuthor?: string }>()
const emit = defineEmits<{ switched: [] }>()
interface DiscoveredCandidate {
  source_id: string
  source_name: string
  book_url: string
  source_host?: string
  elapsed_ms: number
  latest_chapter?: string | null
}
const dialog = ref<HTMLDialogElement>(), mappings = ref<Model<'BookSourceMapping'>[]>([]), discovered = ref<DiscoveredCandidate[]>([]), selected = ref(''), preview = ref<Model<'SwitchSourceResult'>>(), error = ref(''), busy = ref(false), refreshing = ref(false)
let version = 0, generation = 0, controller = new AbortController()
let openedOwner = ''
const activeSourceName = ref('')
const currentName = computed(() => activeSourceName.value || mappings.value.find(row => row.source_id === props.activeSource)?.source_name || '未知书源')
function candidateLabel(name: string | null | undefined, bookUrl: string, sourceHost?: string, latest?: string | null, found?: DiscoveredCandidate) {
  let host = ''
  try { host = new URL(bookUrl).hostname } catch { /* Imported mappings may have no valid URL. */ }
  return [name || '未命名书源', sourceHost || host, found ? found.elapsed_ms + 'ms' : '', found?.latest_chapter || latest].filter(Boolean).join(' · ')
}
const candidates = computed(() => {
  const mappedIds = new Set<string>()
  const out: { id: string; label: string; disabled: boolean }[] = []
  for (const m of mappings.value) {
    if (m.source_id === props.activeSource) continue
    mappedIds.add(m.source_id)
    const found = discovered.value.find(d => d.source_id === m.source_id)
    out.push({ id: m.source_id, label: candidateLabel(m.source_name, m.book_url, m.source_host, m.latest_chapter_title, found), disabled: busy.value })
  }
  for (const d of discovered.value) {
    if (d.source_id === props.activeSource || mappedIds.has(d.source_id)) continue
    out.push({ id: d.source_id, label: candidateLabel(d.source_name, d.book_url, d.source_host, d.latest_chapter, d), disabled: busy.value })
  }
  return out
})
function reset() {
  generation++; controller.abort(); controller = new AbortController()
  openedOwner = ''
  activeSourceName.value = ''
  mappings.value = []; discovered.value = []; selected.value = ''; preview.value = undefined; error.value = ''; busy.value = false; refreshing.value = false
}
function close() { reset(); dialog.value?.close() }
watch(() => props.bookId, close, { flush: 'sync' })
onScopeDispose(close)
watch(selected, () => { preview.value = undefined; error.value = '' })
function snapshot() {
  const token = generation, book = props.bookId, owner = openedOwner, signal = controller.signal
  return { book, signal, valid: () => Boolean(owner) && owner === getSession()?.user.id && token === generation && book === props.bookId && !signal.aborted }
}
async function open() {
  reset(); openedOwner = getSession()?.user.id ?? ''; const at = snapshot(); if (!at.valid()) { dialog.value?.close(); return }
  dialog.value?.showModal(); busy.value = true
  try {
    const [list, progress] = await Promise.all([api.mappings(at.book, at.signal), api.progress(at.book, at.signal)])
    if (!at.valid()) return
    mappings.value = list
    version = progress.version
    const sourceId = props.activeSource
    if (sourceId && !list.some(row => row.source_id === sourceId && row.source_name)) {
      const source = await api.source(sourceId, at.signal).catch(() => undefined)
      if (at.valid() && sourceId === props.activeSource && source) activeSourceName.value = source.book_source_name
    }
  }
  catch (e) { if (at.valid()) error.value = errorText(e) }
  finally { if (at.valid()) busy.value = false }
}
async function refreshSources() {
  const at = snapshot(), kw = (props.bookName ?? '').trim()
  if (!at.valid() || !kw || busy.value || refreshing.value) return
  refreshing.value = true; error.value = ''
  try {
    const res = await api.search(kw, at.signal)
    if (!at.valid()) return
    const items: DiscoveredCandidate[] = []
    for (const g of res.groups ?? []) {
      const exact = g.items.find(it => it.name.trim() === kw) ?? g.items[0]
      if (exact) {
        items.push({
          source_id: g.source_id,
          source_name: g.source_name,
          source_host: g.source_host,
          book_url: exact.book_url,
          elapsed_ms: g.elapsed_ms ?? 0,
          latest_chapter: exact.latest_chapter,
        })
      }
    }
    discovered.value = items
  } catch (e) { if (at.valid()) error.value = errorText(e) }
  finally { if (at.valid()) refreshing.value = false }
}
async function prepare() {
  const at = snapshot(), target = selected.value
  if (!at.valid() || !target || busy.value) return
  busy.value = true; error.value = ''; preview.value = undefined
  try {
    const alreadyMapped = mappings.value.some(m => m.source_id === target)
    if (!alreadyMapped) {
      const found = discovered.value.find(d => d.source_id === target)
      if (found) {
        const created = await api.addMapping(at.book, { source_id: target, book_url: found.book_url })
        if (!at.valid() || selected.value !== target) return
        mappings.value = [...mappings.value, created]
      }
    }
    await api.chapters(at.book, false, target, at.signal)
    if (!at.valid() || selected.value !== target) return
    const result = await api.switchSource(at.book, { source_id: target, dry_run: true }, at.signal)
    if (at.valid() && selected.value === target) preview.value = result
  } catch (e) { if (at.valid()) error.value = errorText(e) }
  finally { if (at.valid()) busy.value = false }
}
async function commit() {
  const at = snapshot(), prepared = preview.value
  if (!at.valid() || busy.value || !prepared || prepared.source_id !== selected.value) return
  busy.value = true; error.value = ''
  try {
    const progress = await api.progress(at.book, at.signal)
    if (!at.valid()) return
    if (progress.version !== version) { preview.value = undefined; error.value = '阅读进度已更新，请关闭后重新预览'; return }
    // Consume the preview before the mutation: an uncertain response cannot authorize replay.
    preview.value = undefined
    await api.switchSource(at.book, { source_id: prepared.source_id, dry_run: false }, at.signal)
    if (at.valid()) { close(); emit('switched') }
  } catch (e) { if (at.valid()) error.value = errorText(e) }
  finally { if (at.valid()) busy.value = false }
}
defineExpose({ open })
</script>
<template>
  <dialog ref="dialog" class="switch-dialog" aria-labelledby="switch-title" @cancel="busy ? $event.preventDefault() : close()" @close="!dialog?.open && reset()"><header><h2 id="switch-title">切换书源 ({{ candidates.length }})</h2><div class="header-actions"><button v-if="bookName" type="button" :disabled="busy || refreshing" @click="refreshSources">{{ refreshing ? '正在搜索…' : '刷新书源' }}</button><button autofocus :disabled="busy" @click="close">关闭</button></div></header><p class="current-source">当前源：{{ currentName }}</p><p>选择备用源，或点击“刷新书源”自动搜索全站同名书源。切换会清除本书的单章源覆盖。</p><p v-if="error" role="alert">{{ error }}</p><p v-if="busy || refreshing" role="status">正在处理…</p><p v-if="!busy && !refreshing && !candidates.length">暂无备用源。可点击右上角“刷新书源”搜索可用书源，或在找书页面加入同名书籍。</p><VirtualList :items="candidates" :selected="selected" @select="selected = String($event)" /><button :disabled="!selected || busy" @click="prepare">预览切换</button><section v-if="preview" class="switch-preview"><p>预计第 {{ preview.chapter_index + 1 }} 章 · 置信度 {{ Math.round(preview.confidence * 100) }}%</p><p v-if="preview.confidence === 0" role="alert">服务端尚未完成可靠重定位，切换后请打开目录核对章节。</p><p v-else-if="preview.strategy === 'ratio'" role="alert">按整书位置比例回退，章节可能不精确，请核对目录。</p><p v-else>定位策略：{{ preview.strategy }}。确认前仍使用原书源。</p><button :disabled="busy" @click="commit">确认切换并清除单章覆盖</button></section></dialog>
</template>
<style scoped>
.switch-dialog { width: min(550px,95vw); max-height: 90dvh; overflow: auto; background: var(--reader-bg); color: var(--reader-ink); border: 1px solid #8885; border-radius: 10px; padding: 20px; }.switch-dialog::backdrop { background: #10282077; }header { display: flex; justify-content: space-between; align-items: center; gap: 10px; }.header-actions { display: flex; gap: 8px; }h2 { font-size: 18px; }p { font-size: 12px; line-height: 1.8; }button { border: 1px solid #8885; border-radius: 5px; padding: 9px 12px; cursor: pointer; background: transparent; color: inherit; }button:disabled { opacity: .4; cursor: default; }[role=alert] { color: #b06437; }.current-source { font-size: 15px; font-weight: 600; overflow-wrap: anywhere; }.switch-preview { border-top: 1px solid #8883; margin-top: 16px; }
</style>
