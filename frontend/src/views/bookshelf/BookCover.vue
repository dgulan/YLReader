<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { getSession, sessionEvents } from '@/api/client'
import { cachedBookCover, invalidateBookCover, loadBookCover } from './coverCache'

const props = defineProps<{ title: string; bookId?: string; url?: string | null; base?: string | null; local?: boolean }>()
const host = ref<HTMLElement>(), visible = ref(false), src = ref('')
const failed = ref(false), proxy = ref(false)
const owner = ref(getSession()?.user.id)
let observer: IntersectionObserver | undefined
const label = computed(() => props.local ? '私人藏书' : '在线阅读')

function syncOwner() { owner.value = getSession()?.user.id }
onMounted(() => {
  sessionEvents.addEventListener('change', syncOwner)
  syncOwner()
  if (!host.value || typeof IntersectionObserver === 'undefined') { visible.value = true; return }
  observer = new IntersectionObserver(entries => {
    if (entries.some(entry => entry.isIntersecting)) {
      visible.value = true
      observer?.disconnect()
    }
  }, { rootMargin: '160px' })
  observer.observe(host.value)
})
onBeforeUnmount(() => {
  sessionEvents.removeEventListener('change', syncOwner)
  observer?.disconnect()
})

function showDirect() {
  proxy.value = false
  src.value = ''
  try {
    const raw = props.url ?? ''
    const base = props.base?.startsWith('http') ? props.base : undefined
    const url = new URL(raw.startsWith('//') && !base ? 'https:' + raw : raw, base)
    if (['http:', 'https:'].includes(url.protocol) && !url.username && !url.password) src.value = url.href
  } catch { /* Invalid external cover. */ }
  failed.value = !src.value
}
function onImageError(event: Event) {
  if ((event.currentTarget as HTMLImageElement).getAttribute('src') !== src.value) return
  if (proxy.value) showDirect()
  else { src.value = ''; failed.value = true }
}
watch(() => [visible.value, props.bookId, props.url, owner.value, props.base] as const, async ([shown, id, raw, user], _, cleanup) => {
  src.value = ''
  proxy.value = false
  failed.value = !raw
  if (id && user) {
    invalidateBookCover(user, id, raw || undefined)
    const cached = raw ? cachedBookCover(user, id, raw) : undefined
    if (cached) { proxy.value = true; src.value = cached; return }
  }
  if (!shown || !raw) return
  if (!id) { showDirect(); return }
  if (!user) return
  let current = true
  cleanup(() => { current = false })
  try {
    const url = await loadBookCover(user, id, raw)
    if (!current || getSession()?.user.id !== user) return
    proxy.value = true
    src.value = url
  } catch { if (current && getSession()?.user.id === user) showDirect() }
}, { immediate: true, flush: 'sync' })
</script>

<template>
  <span ref="host" class="book-cover" :class="{ local, 'has-image': Boolean(src) }">
    <img v-if="src" class="book-cover-img" :src="src" :alt="title" referrerpolicy="no-referrer" @error="onImageError" :key="src" />
    <template v-else-if="failed"><span>{{ title.slice(0, 6) }}</span><small v-if="bookId">{{ label }}</small></template>
  </span>
</template>
