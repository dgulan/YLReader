<script setup lang="ts">
import { computed, ref } from 'vue'
const props = defineProps<{ items: { id: string | number; label: string; disabled?: boolean }[]; selected?: string | number }>()
defineEmits<{ select: [id: string | number] }>()
const top = ref(0), rowHeight = 44
const start = computed(() => Math.max(0, Math.floor(top.value / rowHeight) - 2))
const visible = computed(() => props.items.slice(start.value, start.value + 13))
</script>
<template>
  <div class="virtual-list" @scroll="top = ($event.target as HTMLElement).scrollTop">
    <div :style="{ height: `${items.length * rowHeight}px`, position: 'relative' }">
      <button v-for="(item, i) in visible" :key="item.id" :disabled="item.disabled" :aria-current="item.id === selected ? 'true' : undefined" :style="{ position: 'absolute', top: `${(start + i) * rowHeight}px`, height: `${rowHeight}px` }" @click="$emit('select', item.id)">{{ item.label }}</button>
    </div>
  </div>
</template>
<style scoped>
.virtual-list { height: 352px; overflow: auto; overscroll-behavior: contain; }
button { width: 100%; text-align: left; border: 0; border-bottom: 1px solid #8883; background: transparent; color: inherit; padding: 8px 12px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; cursor: pointer; }
button[aria-current] { color: var(--accent, #315c4c); font-weight: 700; background: #8881; }
button:disabled { font-weight: bold; opacity: .6; cursor: default; }
</style>
