<script setup lang="ts">
import { computed, nextTick, onScopeDispose, ref, watch } from 'vue'
import { api } from '@/api/reader'
import { errorText, getSession, sessionEvents, type Model } from '@/api/client'
import VirtualList from '@/components/VirtualList.vue'

const props = defineProps<{
  bookId: string
  activeSource?: string | null
  bookName?: string
  bookAuthor?: string
  activeLatestChapter?: string | null
  activeTotalChapters?: number
}>()
const emit = defineEmits<{ switched: [] }>()

interface SearchMatch {
  name: string
  host?: string
  bookUrl: string
  tocUrl?: string
  elapsedMs?: number
  latestChapter?: string | null
}
interface SourceRow {
  id: string
  label: string
  host: string
  elapsedMs?: number
  latestChapter?: string | null
  total?: number
  current: boolean
  disabled: boolean
}
const fastLimit = 1500
const sorts = [{ id: 'default', label: '默认' }, { id: 'latest', label: '最新' }, { id: 'fastest', label: '最快' }] as const
const rowHeight = 88
const sort = ref<(typeof sorts)[number]['id']>('default')
const dialog = ref<HTMLDialogElement>()
const confirmButton = ref<HTMLButtonElement>()
const mappings = ref<Model<'BookSourceMapping'>[]>([])
const discovered = ref(new Map<string, SearchMatch>())
const activeDetails = ref<Model<'Source'>>()
const selected = ref('')
const preview = ref<Model<'SwitchSourceResult'>>()
const error = ref('')
const busy = ref(false)
const refreshing = ref(false)
const searched = ref(0)
const version = ref<number>()
let controller = new AbortController()
let scan: AbortController | undefined
let openedOwner = ''

const activeMapping = computed(() => mappings.value.find(row => row.source_id === props.activeSource))
const currentName = computed(() => activeMapping.value?.source_name || activeDetails.value?.book_source_name
  || discovered.value.get(props.activeSource ?? '')?.name || '未知书源')

function hostname(url?: string | null) {
  try { return url ? new URL(url).hostname : '' } catch { return '' }
}

const sourceRows = computed(() => {
  const rows = new Map<string, SourceRow>()
  const addRow = (id: string, mapping?: Model<'BookSourceMapping'>) => {
    const found = discovered.value.get(id)
    const current = id === props.activeSource
    const total = mapping?.total_chapters || (current ? props.activeTotalChapters : undefined)
    rows.set(id, {
      id,
      label: current ? currentName.value : mapping?.source_name || found?.name || '未命名书源',
      host: mapping?.source_host || found?.host || hostname(current ? activeDetails.value?.book_source_url : undefined)
        || hostname(mapping?.book_url || found?.bookUrl),
      elapsedMs: found?.elapsedMs,
      latestChapter: found?.latestChapter || mapping?.latest_chapter_title || (current ? props.activeLatestChapter : undefined),
      total: total || undefined,
      current,
      disabled: current || busy.value || version.value === undefined,
    })
  }
  if (props.activeSource) addRow(props.activeSource, activeMapping.value)
  for (const mapping of mappings.value) if (!rows.has(mapping.source_id)) addRow(mapping.source_id, mapping)
  for (const id of discovered.value.keys()) if (!rows.has(id)) addRow(id)
  return [...rows.values()]
})
const alternatives = computed(() => sourceRows.value.filter(row => !row.current).length)
const fastCount = computed(() => sourceRows.value.filter(row => row.elapsedMs !== undefined && row.elapsedMs < fastLimit).length)
const slowCount = computed(() => sourceRows.value.filter(row => row.elapsedMs !== undefined && row.elapsedMs >= fastLimit).length)
const maximumTotal = computed(() => sourceRows.value.reduce((max, row) => Math.max(max, row.total ?? 0), 0))
const latestRanks = computed(() => {
  const chapterNumbers = sourceRows.value.map(row => Number(row.latestChapter?.normalize('NFKC').match(/^第\s*(\d+)\s*[章节回]/)?.[1] ?? 0))
  const useNumbers = chapterNumbers.some(Boolean)
  return new Map(sourceRows.value.map((row, index) => [row.id, useNumbers ? chapterNumbers[index]! : row.total ?? 0]))
})
const latestRank = computed(() => sourceRows.value.reduce((max, row) => Math.max(max, latestRanks.value.get(row.id) ?? 0), 0))
const rows = computed(() => {
  if (sort.value === 'fastest') {
    const latency = (row: SourceRow) => row.elapsedMs ?? Number.MAX_SAFE_INTEGER
    return [...sourceRows.value].sort((a, b) => latency(a) - latency(b))
  }
  if (sort.value === 'latest') return [...sourceRows.value].sort((a, b) => latestRanks.value.get(b.id)! - latestRanks.value.get(a.id)!)
  return sourceRows.value
})

function stopScan() {
  scan?.abort()
  scan = undefined
  refreshing.value = false
}
function reset() {
  stopScan()
  controller.abort()
  controller = new AbortController()
  openedOwner = ''
  version.value = undefined
  activeDetails.value = undefined
  mappings.value = []
  discovered.value.clear()
  selected.value = ''
  preview.value = undefined
  error.value = ''
  busy.value = false
  searched.value = 0
  sort.value = 'default'
}
function close() { reset(); dialog.value?.close() }
function sessionChanged() { if (openedOwner && openedOwner !== getSession()?.user.id) close() }
watch(() => props.bookId, close, { flush: 'sync' })
sessionEvents.addEventListener('change', sessionChanged)
onScopeDispose(() => { sessionEvents.removeEventListener('change', sessionChanged); close() })

function snapshot() {
  const book = props.bookId, owner = openedOwner, signal = controller.signal
  return { book, signal, valid: () => Boolean(owner) && owner === getSession()?.user.id && book === props.bookId && !signal.aborted }
}

async function open() {
  reset()
  openedOwner = getSession()?.user.id ?? ''
  const at = snapshot()
  if (!at.valid()) { dialog.value?.close(); return }
  if (!dialog.value?.open) dialog.value?.showModal()
  busy.value = true
  try {
    const [list, progress] = await Promise.all([api.mappings(at.book, at.signal), api.progress(at.book, at.signal)])
    if (!at.valid()) return
    mappings.value = list
    version.value = progress.version
    const sourceId = props.activeSource
    if (sourceId && !activeMapping.value?.source_name) {
      const source = await api.source(sourceId, at.signal).catch(() => undefined)
      if (at.valid() && sourceId === props.activeSource) activeDetails.value = source
    }
  } catch (e) {
    if (at.valid()) error.value = errorText(e)
  } finally {
    if (at.valid()) busy.value = false
  }
}

async function refreshSources() {
  const at = snapshot(), keyword = props.bookName?.trim(), author = props.bookAuthor?.trim()
  if (!at.valid() || !keyword || busy.value || refreshing.value) return
  const request = new AbortController()
  scan = request
  refreshing.value = true
  error.value = ''
  preview.value = undefined
  selected.value = ''
  searched.value = 0
  discovered.value.clear()
  const active = () => at.valid() && scan === request && !request.signal.aborted
  function accept(group: Model<'SearchGroup'>) {
    if (!active()) return
    const matches = group.items.filter(item => item.name.trim() === keyword)
    const exact = author
      ? matches.find(item => item.author?.trim() === author) ?? matches.find(item => !item.author?.trim())
      : matches[0]
    if (exact) discovered.value.set(group.source_id, {
      name: group.source_name,
      host: group.source_host,
      bookUrl: exact.book_url,
      tocUrl: exact.toc_url,
      elapsedMs: group.elapsed_ms,
      latestChapter: exact.latest_chapter,
    })
  }
  try {
    const result = await api.search(keyword, request.signal, accept, count => { if (active()) searched.value = count })
    if (active()) {
      for (const group of result.groups ?? []) accept(group)
      // Old servers may return JSON or omit probe events.
      if (!searched.value) {
        searched.value = new Set([...(result.groups ?? []), ...(result.failures ?? [])].map(source => source.source_id)).size
      }
    }
  } catch (e) {
    if (active()) error.value = errorText(e)
  } finally {
    if (scan === request) { scan = undefined; refreshing.value = false }
  }
}

async function prepare(id: string | number) {
  const at = snapshot(), target = String(id)
  if (!at.valid() || target === props.activeSource || busy.value || version.value === undefined) return
  stopScan()
  selected.value = target
  busy.value = true
  error.value = ''
  preview.value = undefined
  try {
    if (!mappings.value.some(mapping => mapping.source_id === target)) {
      const found = discovered.value.get(target)
      if (!found) return
      const body: Model<'MappingCreate'> = { source_id: target, book_url: found.bookUrl }
      if (found.tocUrl && found.tocUrl !== found.bookUrl) body.toc_url = found.tocUrl
      const created = await api.addMapping(at.book, body)
      if (!at.valid()) return
      mappings.value = [...mappings.value, created]
    }
    await api.chapters(at.book, false, target, at.signal)
    if (!at.valid()) return
    const result = await api.switchSource(at.book, { source_id: target, dry_run: true }, at.signal)
    if (at.valid()) preview.value = result
  } catch (e) {
    if (at.valid()) error.value = errorText(e)
  } finally {
    if (at.valid()) busy.value = false
  }
  await nextTick()
  if (at.valid() && preview.value?.source_id === target) confirmButton.value?.focus()
}

async function commit() {
  const at = snapshot(), prepared = preview.value
  if (!at.valid() || busy.value || !prepared || prepared.source_id !== selected.value) return
  busy.value = true
  error.value = ''
  try {
    const progress = await api.progress(at.book, at.signal)
    if (!at.valid()) return
    if (progress.version !== version.value) { preview.value = undefined; error.value = '阅读进度已更新，请关闭后重新预览'; return }
    // Consume the preview before the mutation: an uncertain response cannot authorize replay.
    preview.value = undefined
    await api.switchSource(at.book, { source_id: prepared.source_id, dry_run: false }, at.signal)
    if (at.valid()) { close(); emit('switched') }
  } catch (e) {
    if (at.valid()) error.value = errorText(e)
  } finally {
    if (at.valid()) busy.value = false
  }
}
defineExpose({ open })
</script>

<template>
  <dialog ref="dialog" class="switch-dialog" aria-labelledby="switch-title" @cancel="busy ? $event.preventDefault() : close()" @close="!dialog?.open && reset()">
    <header>
      <h2 id="switch-title">切换书源</h2>
      <div class="header-actions">
        <button v-if="refreshing" type="button" @click="stopScan">停止</button>
        <button v-else-if="bookName" type="button" :disabled="busy" @click="refreshSources">刷新书源</button>
        <button type="button" autofocus :disabled="busy" @click="close">关闭</button>
      </div>
    </header>
    <p class="current-source">当前源：{{ currentName }}</p>
    <div class="switch-summary">
      <div class="switch-sorts" role="group" aria-label="书源排序">
        <button v-for="item in sorts" :key="item.id" type="button" :aria-pressed="sort === item.id" @click="sort = item.id">{{ item.label }}</button>
      </div>
      <p class="source-counts" title="快源低于 1500ms">{{ '快 ' + fastCount + ' · 慢 ' + slowCount + ' · 共 ' + rows.length + ' 源' }}</p>
      <p v-if="refreshing || searched" class="probe-count" role="status">已探 {{ searched }} 源</p>
    </div>
    <p v-if="error" role="alert">{{ error }}</p>
    <p v-if="busy" role="status">正在处理…</p>
    <p v-if="!busy && !refreshing && !alternatives">暂无备用源。可点击「刷新书源」搜索同名书籍。</p>
    <VirtualList v-if="rows.length" class="source-list" label="换源候选列表" :style="{ height: `${Math.min(rows.length, 4) * rowHeight}px` }"
      :items="rows" :selected="selected" :row-height="rowHeight" preserve-scroll @select="prepare">
      <template #default="{ item }">
        <span class="source-primary">
          <span class="source-identity">
            <strong class="source-name" :title="item.label">{{ item.label }}</strong>
            <small v-if="item.host" class="source-host" :title="item.host">{{ item.host }}</small>
          </span>
          <span class="source-flags">
            <span v-if="item.current" class="source-badge">当前源</span>
            <span v-if="latestRank && latestRanks.get(item.id) === latestRank" class="source-badge newest">最新</span>
          </span>
          <span v-if="item.elapsedMs !== undefined" class="source-latency" :class="item.elapsedMs < fastLimit ? 'fast' : 'slow'">{{ item.elapsedMs }}ms</span>
          <span class="source-action">{{ item.current ? '✓' : '切换' }}</span>
        </span>
        <span class="source-secondary">
          <span class="source-chapter" :title="item.latestChapter || undefined">{{ item.latestChapter || '暂无最新章节' }}</span>
          <small v-if="item.total && item.total < maximumTotal" class="source-lag">落后 {{ maximumTotal - item.total }} 章</small>
        </span>
      </template>
    </VirtualList>
    <p v-if="refreshing" class="scan-footer">正在持续扫描其他书源…</p>
    <section v-if="preview" class="switch-preview">
      <p>预计第 {{ preview.chapter_index + 1 }} 章 · 置信度 {{ Math.round(preview.confidence * 100) }}%</p>
      <p v-if="preview.confidence === 0" role="alert">服务端尚未完成可靠重定位，切换后请打开目录核对章节。</p>
      <p v-else-if="preview.strategy === 'ratio'" role="alert">按整书位置比例回退，章节可能不精确，请核对目录。</p>
      <p v-else-if="preview.strategy">定位策略：{{ preview.strategy }}</p>
      <button ref="confirmButton" type="button" :disabled="busy" @click="commit">确认切换并清除单章覆盖</button>
    </section>
  </dialog>
</template>

<style scoped>
.switch-dialog { width: min(620px, calc(100% - 24px)); max-height: 90dvh; overflow: auto; background: var(--reader-bg); color: var(--reader-ink); border: 1px solid #8885; border-radius: 16px; padding: clamp(12px, 4vw, 22px); }
.switch-dialog::backdrop { background: #10282077; }
header, .switch-summary { display: flex; flex-wrap: wrap; justify-content: space-between; align-items: center; gap: 8px 12px; }
.header-actions, .switch-sorts { display: flex; gap: 6px; }
h2 { margin: 0; font-size: 18px; }
p { font-size: 13px; line-height: 1.6; }
button { min-height: 40px; border: 1px solid #8885; border-radius: 8px; padding: 6px 10px; cursor: pointer; background: transparent; color: inherit; }
button:disabled { opacity: .55; cursor: default; }
.switch-sorts button { min-height: 34px; font-size: 12px; }
.switch-sorts [aria-pressed=true] { border-color: currentColor; background: #8882; font-weight: 650; }
[role=alert] { color: #b06437; }
.current-source { margin: 14px 0 8px; font-weight: 650; overflow-wrap: anywhere; }
.source-counts, .probe-count { margin: 0; font-variant-numeric: tabular-nums; }
.probe-count, .scan-footer { color: var(--reader-muted); }
.source-list { margin-top: 10px; }
.source-list :deep(button[data-row]) { border: 1px solid #8884; border-radius: 10px; height: calc(100% - 6px); padding: 8px; }
.source-list :deep(button[data-row]:disabled) { opacity: .75; }
.source-primary, .source-secondary { display: flex; align-items: center; gap: clamp(4px, 1.4vw, 10px); }
.source-primary { min-height: 32px; }
.source-secondary { margin-top: 4px; color: var(--reader-muted); font-size: 12px; }
.source-identity { display: flex; align-items: baseline; flex-wrap: wrap; column-gap: 8px; min-width: 0; flex: 1; }
.source-name, .source-host, .source-chapter { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.source-name { font-size: 14px; }
.source-host { color: var(--reader-muted); font-size: 11px; }
.source-flags { display: grid; gap: 2px; }
.source-badge { border-radius: 4px; padding: 2px 4px; background: #8882; font-size: 10px; white-space: nowrap; }
.source-badge.newest { background: #3d9a6229; }
.source-latency { font-size: 12px; font-variant-numeric: tabular-nums; }
.source-latency.fast { color: var(--switch-fast, #27754a); }
.source-latency.slow { color: var(--switch-slow, #a94425); }
:global(.reader[data-theme="night"] .switch-dialog) { --switch-fast: #8dd5a4; --switch-slow: #ffab85; }
.source-action { padding: 4px 6px; border-radius: 5px; background: #8882; font-size: 12px; font-weight: 650; }
.source-lag { flex-shrink: 0; margin-left: auto; }
.switch-preview { border-top: 1px solid #8884; margin-top: 12px; }
</style>
