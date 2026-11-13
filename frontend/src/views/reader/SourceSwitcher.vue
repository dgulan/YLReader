<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { api } from '@/api/reader'
import { errorText, type Model } from '@/api/client'
import VirtualList from '@/components/VirtualList.vue'
const props = defineProps<{ bookId: string; activeSource?: string | null }>()
const emit = defineEmits<{ switched: [] }>()
const dialog = ref<HTMLDialogElement>(), mappings = ref<Model<'BookSourceMapping'>[]>([]), selected = ref(''), preview = ref<Model<'SwitchSourceResult'>>(), error = ref(''), busy = ref(false)
let version = 0
const candidates = computed(() => mappings.value.filter(m => m.source_id !== props.activeSource).map(m => ({ id: m.source_id, label: m.source_name || m.book_url, disabled: busy.value })))
watch(selected, () => { preview.value = undefined; error.value = '' })
async function open() { selected.value = ''; preview.value = undefined; error.value = ''; dialog.value?.showModal(); busy.value = true; try { const [list, progress] = await Promise.all([api.mappings(props.bookId), api.progress(props.bookId)]); mappings.value = list; version = progress.version } catch (e) { error.value = errorText(e) } finally { busy.value = false } }
async function prepare() {
  if (!selected.value) return
  const target = selected.value; busy.value = true; error.value = ''; preview.value = undefined
  try { await api.chapters(props.bookId, false, target); const result = await api.switchSource(props.bookId, { source_id: target, dry_run: true }); if (selected.value === target) preview.value = result } catch (e) { error.value = errorText(e) } finally { busy.value = false }
}
async function commit() {
  const prepared = preview.value
  if (!prepared || prepared.source_id !== selected.value) return
  busy.value = true; error.value = ''
  try {
    const progress = await api.progress(props.bookId)
    if (progress.version !== version) { preview.value = undefined; error.value = '阅读进度已更新，请关闭后重新预览'; return }
    await api.switchSource(props.bookId, { source_id: prepared.source_id, dry_run: false })
    dialog.value?.close(); emit('switched')
  } catch (e) { error.value = errorText(e) } finally { busy.value = false }
}
defineExpose({ open })
</script>
<template>
  <dialog ref="dialog" class="switch-dialog" aria-labelledby="switch-title" @cancel="busy && $event.preventDefault()"><header><h2 id="switch-title">切换书源</h2><button :disabled="busy" @click="dialog?.close()">关闭</button></header><p>选择已绑定的备用源。切换会清除本书的单章源覆盖。</p><p v-if="error" role="alert">{{ error }}</p><p v-if="busy" role="status">正在处理…</p><p v-if="!busy && !candidates.length">没有备用源。请在找书页面从其他书源加入同名同作者书籍。</p><VirtualList :items="candidates" :selected="selected" @select="selected = String($event)" /><button :disabled="!selected || busy" @click="prepare">预览切换</button><section v-if="preview" class="switch-preview"><p>预计第 {{ preview.chapter_index + 1 }} 章 · 置信度 {{ Math.round(preview.confidence * 100) }}%</p><p v-if="preview.confidence === 0" role="alert">服务端尚未完成可靠重定位，切换后请打开目录核对章节。</p><p v-else-if="preview.strategy === 'ratio'" role="alert">按整书位置比例回退，章节可能不精确，请核对目录。</p><p v-else>定位策略：{{ preview.strategy }}。确认前仍使用原书源。</p><button :disabled="busy" @click="commit">确认切换并清除单章覆盖</button></section></dialog>
</template>
<style scoped>
.switch-dialog { width: min(550px,95vw); max-height: 90dvh; overflow: auto; background: var(--reader-bg); color: var(--reader-ink); border: 1px solid #8885; border-radius: 10px; padding: 20px; }.switch-dialog::backdrop { background: #10282077; }header { display: flex; justify-content: space-between; align-items: center; }h2 { font-size: 18px; }p { font-size: 12px; line-height: 1.8; }button { border: 1px solid #8885; border-radius: 5px; padding: 9px 12px; cursor: pointer; background: transparent; color: inherit; }button:disabled { opacity: .4; cursor: default; }[role=alert] { color: #b06437; }.switch-preview { border-top: 1px solid #8883; margin-top: 16px; }
</style>
