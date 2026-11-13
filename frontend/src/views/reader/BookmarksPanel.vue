<script setup lang="ts">
import { computed, ref, toRef, watch } from 'vue'
import type { Model } from '@/api/client'
import VirtualList from '@/components/VirtualList.vue'
import { useBookmarks } from './bookmarks'
const props = defineProps<{ bookId: string; total: number }>()
const emit = defineEmits<{ navigate: [bookmark: Model<'Bookmark'>] }>()
const { items, busy, error, message, load, create, remove } = useBookmarks(toRef(props, 'bookId'))
const dialog = ref<HTMLDialogElement>(), position = ref<Model<'BookmarkCreate'>>(), note = ref(''), selected = ref(''), deleting = ref(false)
const active = computed(() => items.value.find(b => b.id === selected.value))
const entries = computed(() => items.value.map(b => ({ id: b.id, label: `第 ${b.chapter_index + 1} 章 · ${b.chapter_title || '未命名章节'} · ${b.note || b.excerpt || `字符 ${b.char_offset}`}`, disabled: busy.value })))
const invalid = computed(() => !!active.value && active.value.chapter_index >= props.total)
watch(selected, () => { deleting.value = false })
watch(() => props.bookId, () => { dialog.value?.close(); position.value = undefined; note.value = ''; selected.value = ''; deleting.value = false })
async function open(at?: Model<'BookmarkCreate'>) { position.value = at ? { ...at } : undefined; note.value = ''; selected.value = ''; deleting.value = false; dialog.value?.showModal(); await load() }
async function save() { if (position.value && Array.from(note.value).length <= 4096 && await create({ ...position.value, note: note.value })) { note.value = ''; selected.value = items.value.find(b => b.chapter_index === position.value!.chapter_index && b.char_offset === position.value!.char_offset)?.id ?? '' } }
async function confirmDelete() { if (active.value && await remove(active.value.id)) { selected.value = ''; deleting.value = false } }
function jump() { if (!active.value || invalid.value || busy.value) return; emit('navigate', active.value); dialog.value?.close() }
defineExpose({ open })
</script>
<template>
  <dialog ref="dialog" class="bookmarks-dialog" aria-labelledby="bookmarks-title" @cancel="busy && $event.preventDefault()">
    <header><h2 id="bookmarks-title">阅读书签</h2><button :disabled="busy" @click="dialog?.close()">关闭</button></header>
    <p v-if="error" role="alert">{{ error }}</p><p v-if="message" role="status">{{ message }}</p><p v-if="busy" role="status">正在处理书签…</p>
    <form v-if="position" @submit.prevent="save"><p>当前位置：第 {{ position.chapter_index + 1 }} 章 · 字符 {{ position.char_offset ?? 0 }}</p><blockquote>{{ position.excerpt }}</blockquote><label for="bookmark-note">书签备注（可选）</label><textarea id="bookmark-note" v-model="note" maxlength="4096" rows="2" :disabled="busy" /><button type="submit" :disabled="busy">保存当前位置书签</button></form>
    <div class="list-heading"><h3>已保存书签（{{ items.length }}）</h3><button :disabled="busy" @click="selected = ''; load()">刷新书签</button></div>
    <p v-if="!busy && !error && !items.length">还没有书签。</p><VirtualList v-if="items.length" :style="{ height: `${Math.min(items.length, 6) * 44}px` }" :items="entries" :selected="selected" @select="selected = String($event)" />
    <section v-if="active" class="bookmark-detail" aria-label="书签详情"><h3>{{ active.chapter_title || `第 ${active.chapter_index + 1} 章` }}</h3><p>第 {{ active.chapter_index + 1 }} 章 · 字符 {{ active.char_offset }}</p><blockquote v-if="active.excerpt">{{ active.excerpt }}</blockquote><p v-if="active.note" class="note">{{ active.note }}</p><p>书签按章节与字符位置保存，换源后请核对位置。</p><p v-if="invalid" role="alert">此章节已不在当前目录中，请打开目录核对。</p><button :disabled="busy || invalid" @click="jump">跳到书签位置</button><button v-if="!deleting" :disabled="busy" @click="deleting = true">删除书签</button><div v-else role="group" aria-label="确认删除书签"><p>确定删除这个书签？</p><button :disabled="busy" @click="confirmDelete">确认删除书签</button><button :disabled="busy" @click="deleting = false">取消删除</button></div></section>
  </dialog>
</template>
<style scoped>
.bookmarks-dialog { width: min(580px,95vw); max-height: 90dvh; overflow: auto; background: var(--reader-bg); color: var(--reader-ink); border: 1px solid #8885; border-radius: 10px; padding: 20px; }.bookmarks-dialog::backdrop { background: #10282077; }header,.list-heading { display: flex; justify-content: space-between; align-items: center; gap: 12px; }h2 { font-size: 18px; }h3 { font-size: 14px; }p,label,blockquote { font-size: 12px; line-height: 1.8; }blockquote { margin: 10px 0; padding-left: 12px; border-left: 2px solid #8885; white-space: pre-wrap; overflow-wrap: anywhere; }.note { white-space: pre-wrap; overflow-wrap: anywhere; }textarea { display: block; width: 100%; box-sizing: border-box; resize: vertical; margin: 8px 0; padding: 8px; font: inherit; color: inherit; background: transparent; border: 1px solid #8885; border-radius: 5px; }button { border: 1px solid #8885; border-radius: 5px; padding: 8px 10px; margin-right: 6px; cursor: pointer; background: transparent; color: inherit; }button:disabled { opacity: .4; cursor: default; }[role=alert] { color: var(--reader-ink); }.bookmark-detail { border-top: 1px solid #8884; margin-top: 16px; }
</style>
