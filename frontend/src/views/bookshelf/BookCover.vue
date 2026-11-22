<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { getSession, requestBookCover, sessionEvents } from '@/api/client'

const props = defineProps<{ title: string; bookId?: string; url?: string | null; local?: boolean }>()
const host = ref<HTMLElement>(), visible = ref(false), src = ref('')
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

watch(() => [visible.value, props.bookId, props.url, owner.value] as const, async ([shown, id, raw, user], _, cleanup) => {
  src.value = ''
  if (!shown || !raw) return
  if (!id) {
    try {
      const url = new URL(raw)
      if (['http:', 'https:'].includes(url.protocol) && !url.username && !url.password) src.value = url.href
    } catch { /* Missing or invalid preview uses the placeholder. */ }
    return
  }
  if (!user) return
  const controller = new AbortController()
  let objectURL: string | undefined
  cleanup(() => {
    controller.abort()
    if (objectURL) URL.revokeObjectURL(objectURL)
  })
  try {
    const image = await requestBookCover(id, controller.signal)
    if (controller.signal.aborted || getSession()?.user.id !== user) return
    objectURL = URL.createObjectURL(image)
    src.value = objectURL
  } catch { /* A failed cover keeps the placeholder. */ }
})
</script>

<template>
  <span ref="host" class="book-cover" :class="{ local, 'has-image': Boolean(src) }">
    <img v-if="src" class="book-cover-img" :src="src" :alt="title" referrerpolicy="no-referrer" @error="src = ''" />
    <template v-else><span>{{ title.slice(0, 6) }}</span><small v-if="bookId">{{ label }}</small></template>
  </span>
</template>
