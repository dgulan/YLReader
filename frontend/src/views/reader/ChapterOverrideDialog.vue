<script setup lang="ts">
import { ref, watch, onScopeDispose } from 'vue'
import { api } from '@/api/reader'
import { errorText, getSession, type Model } from '@/api/client'

const props = defineProps<{
  bookId: string
  chapterIndex: number
  chapterTitle: string
  isOverridden: boolean
}>()

const emit = defineEmits<{ updated: [chapterIndex: number] }>()

const dialog = ref<HTMLDialogElement>()
const sources = ref<Model<'Source'>[]>([])
const chapterUrl = ref('')
const sourceId = ref('')
const preview = ref<Model<'ChapterOverride'> | undefined>()
const error = ref('')
const busy = ref(false)

let generation = 0
let openedOwner = ''
let controller = new AbortController()

function reset() {
  generation++
  controller.abort()
  controller = new AbortController()
  openedOwner = ''
  sources.value = []
  chapterUrl.value = ''
  sourceId.value = ''
  preview.value = undefined
  error.value = ''
  busy.value = false
}

function close() {
  reset()
  dialog.value?.close()
}

watch(() => props.bookId, close, { flush: 'sync' })
onScopeDispose(close)

function snapshot() {
  const token = generation
  const book = props.bookId
  const index = props.chapterIndex
  const owner = openedOwner
  const signal = controller.signal
  return {
    book,
    index,
    signal,
    valid: () =>
      Boolean(owner) &&
      owner === getSession()?.user.id &&
      token === generation &&
      book === props.bookId &&
      !signal.aborted,
  }
}

async function open() {
  reset()
  openedOwner = getSession()?.user.id ?? ''
  const at = snapshot()
  if (!at.valid()) {
    dialog.value?.close()
    return
  }
  dialog.value?.showModal()
  busy.value = true
  try {
    const page = await api.sources(1)
    if (at.valid()) {
      sources.value = page.items.filter(s => s.book_source_type !== 3 && s.enabled)
    }
  } catch (e) {
    if (at.valid()) error.value = errorText(e)
  } finally {
    if (at.valid()) busy.value = false
  }
}

async function submitOverride() {
  const at = snapshot()
  const url = chapterUrl.value.trim()
  if (!at.valid() || busy.value) return
  if (!/^https?:\/\//i.test(url)) {
    error.value = '请输入以 http:// 或 https:// 开头的章节网页地址'
    return
  }
  busy.value = true
  error.value = ''
  try {
    const payload: Model<'ChapterOverrideCreate'> = sourceId.value
      ? { chapter_url: url, source_id: sourceId.value }
      : { chapter_url: url }
    const result = await api.setChapterOverride(at.book, at.index, payload, at.signal)
    if (!at.valid()) return
    preview.value = result
    emit('updated', at.index)
  } catch (e) {
    if (at.valid()) error.value = errorText(e)
  } finally {
    if (at.valid()) busy.value = false
  }
}

async function clearOverride() {
  const at = snapshot()
  if (!at.valid() || busy.value) return
  busy.value = true
  error.value = ''
  try {
    await api.deleteChapterOverride(at.book, at.index, at.signal)
    if (!at.valid()) return
    const target = at.index
    close()
    emit('updated', target)
  } catch (e) {
    if (at.valid()) error.value = errorText(e)
  } finally {
    if (at.valid()) busy.value = false
  }
}

defineExpose({ open })
</script>

<template>
  <dialog
    ref="dialog"
    class="override-dialog"
    aria-labelledby="override-title"
    @cancel="busy ? $event.preventDefault() : close()"
    @close="!dialog?.open && reset()"
  >
    <header>
      <h2 id="override-title">单章换源</h2>
      <button type="button" autofocus :disabled="busy" @click="close">关闭</button>
    </header>
    <p class="override-meta">当前第 {{ chapterIndex + 1 }} 章：{{ chapterTitle }}</p>
    <p v-if="isOverridden" class="override-badge">本章当前正在使用单章源覆盖</p>
    <p v-if="error" role="alert">{{ error }}</p>

    <form class="override-form" @submit.prevent="submitOverride">
      <label>
        <span>目标章节 URL</span>
        <input
          v-model="chapterUrl"
          type="url"
          placeholder="https://example.com/book/chapter-1"
          :disabled="busy"
          required
        />
      </label>
      <label>
        <span>指定书源（可选，留空则按域名自动匹配）</span>
        <select v-model="sourceId" :disabled="busy">
          <option value="">自动按 URL 域名匹配</option>
          <option v-for="s in sources" :key="s.id" :value="s.id">{{ s.book_source_name }}</option>
        </select>
      </label>
      <div class="override-actions">
        <button type="submit" :disabled="busy">{{ busy ? '正在试抓…' : '试抓并应用覆盖' }}</button>
        <button v-if="isOverridden" type="button" :disabled="busy" @click="clearOverride">清除本章覆盖</button>
      </div>
    </form>

    <section v-if="preview" class="override-preview" aria-label="试抓正文预览">
      <p class="override-meta">已生效 · 书源：{{ preview.source_name }} · 状态：{{ preview.status }}</p>
      <pre>{{ preview.content_preview || '（正文试抓为空）' }}</pre>
    </section>
  </dialog>
</template>

<style scoped>
.override-dialog {
  width: min(560px, 95vw);
  max-height: 90dvh;
  overflow: auto;
  background: var(--reader-bg);
  color: var(--reader-ink);
  border: 1px solid #8885;
  border-radius: 10px;
  padding: 20px;
}
.override-dialog::backdrop { background: #10282077; }
header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 10px; }
h2 { font-size: 18px; margin: 0; }
.override-meta { font-size: 12px; color: var(--reader-muted); margin: 4px 0 12px; }
.override-badge { font-size: 12px; padding: 6px 10px; border-radius: 6px; background: #8882; margin: 0 0 12px; }
.override-form { display: flex; flex-direction: column; gap: 12px; }
label { display: flex; flex-direction: column; gap: 6px; font-size: 12px; }
input, select, button {
  border: 1px solid #8885;
  border-radius: 5px;
  padding: 9px 12px;
  background: transparent;
  color: inherit;
  font-size: 13px;
}
button { cursor: pointer; }
button:disabled { opacity: .4; cursor: default; }
.override-actions { display: flex; flex-wrap: wrap; gap: 10px; }
[role=alert] { color: #b06437; font-size: 12px; }
.override-preview { margin-top: 16px; border-top: 1px solid #8883; padding-top: 12px; }
.override-preview pre {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  font-size: 12px;
  line-height: 1.7;
  max-height: 220px;
  overflow: auto;
  margin: 0;
  padding: 10px;
  border-radius: 6px;
  background: #8881;
}
</style>
