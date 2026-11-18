<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { enabledRow } from './virtualNavigation'
const props = withDefaults(defineProps<{ items: { id: string | number; label: string; disabled?: boolean }[]; selected?: string | number; label?: string }>(), { label: '可选列表' })
defineEmits<{ select: [id: string | number] }>()
const container = ref<HTMLElement>()
const top = ref(0), active = ref(-1), rowHeight = 44
const start = computed(() => Math.min(Math.max(0, props.items.length - 13), Math.max(0, Math.floor(top.value / rowHeight) - 2)))
const visible = computed(() => props.items.slice(start.value, start.value + 13))
function onScroll() {
  top.value = container.value?.scrollTop ?? 0
  if (active.value < start.value || active.value >= start.value + visible.value.length) {
    const next = enabledRow(props.items, start.value, 1)
    active.value = next < start.value + visible.value.length ? next : -1
  }
}
function reveal(index: number) {
  const el = container.value
  if (!el || index < 0) return
  const height = el.clientHeight || 352
  const current = el.scrollTop
  el.scrollTop = Math.max(0, Math.min(current, index * rowHeight, Math.max(0, props.items.length * rowHeight - height)))
  if ((index + 1) * rowHeight > el.scrollTop + height) el.scrollTop = (index + 1) * rowHeight - height
  top.value = el.scrollTop
}
function revealSelected() {
  const selected = props.items.findIndex(item => item.id === props.selected && !item.disabled)
  active.value = selected >= 0 ? selected : enabledRow(props.items, 0, 1)
  reveal(active.value)
}
watch([() => props.items, () => props.selected], revealSelected, { immediate: true, flush: 'post' })
async function navigate(event: KeyboardEvent) {
  const keys = ['ArrowDown', 'ArrowUp', 'Home', 'End', 'PageDown', 'PageUp']
  if (!keys.includes(event.key) || event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return
  event.preventDefault()
  const direction = event.key === 'ArrowUp' || event.key === 'PageUp' || event.key === 'End' ? -1 : 1
  const page = Math.max(1, Math.floor((container.value?.clientHeight || 352) / rowHeight))
  let from = active.value + direction
  if (event.key === 'Home') from = 0
  if (event.key === 'End') from = props.items.length - 1
  if (event.key === 'PageDown') from = Math.min(props.items.length - 1, active.value + page)
  if (event.key === 'PageUp') from = Math.max(0, active.value - page)
  const target = enabledRow(props.items, from, direction)
  if (target < 0) return
  active.value = target
  reveal(target)
  await nextTick()
  container.value?.querySelector<HTMLButtonElement>(`button[data-row="${target}"]`)?.focus({ preventScroll: true })
}
defineExpose({ revealSelected })
</script>
<template>
  <div ref="container" class="virtual-list" role="list" :aria-label="label" :tabindex="active < 0 && items.length ? 0 : undefined" @scroll="onScroll" @keydown="navigate">
    <div role="presentation" :style="{ height: `${items.length * rowHeight}px`, position: 'relative' }">
      <div v-for="(item, i) in visible" :key="item.id" role="listitem" :aria-posinset="start + i + 1" :aria-setsize="items.length" :style="{ position: 'absolute', top: `${(start + i) * rowHeight}px`, height: `${rowHeight}px`, width: '100%' }">
        <button :data-row="start + i" :disabled="item.disabled" :tabindex="start + i === active ? 0 : -1" :aria-current="item.id === selected ? 'true' : undefined" @focus="active = start + i" @click="$emit('select', item.id)">{{ item.label }}</button>
      </div>
    </div>
  </div>
</template>
<style scoped>
.virtual-list { height: 352px; max-height: 50dvh; overflow: auto; overscroll-behavior: contain; scrollbar-gutter: stable; }
button { width: 100%; height: 100%; text-align: left; border: 0; border-bottom: 1px solid #8883; background: transparent; color: inherit; padding: 8px 12px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; cursor: pointer; }
button:focus-visible { outline: 2px solid var(--reader-ink, var(--accent, #315d4d)); outline-offset: -3px; }
button[aria-current] { color: var(--reader-ink, var(--accent, #315c4c)); font-weight: 700; background: #8881; }
button:disabled { font-weight: bold; opacity: .6; cursor: default; }
</style>
