import { defineStore } from 'pinia'
import { computed, onScopeDispose, ref } from 'vue'
import { api } from '@/api/reader'
import { errorText, getSession, sessionEvents } from '@/api/client'
import { db } from '@/db/chapters'
import { defaultPreferences, parsePreferences, type PreferencePatch, type ReaderTheme } from './readerPreferences'
export const useReaderConfig = defineStore('readerConfig', () => {
  const values = ref(defaultPreferences()), pending = ref<PreferencePatch>({}), loading = ref(false), saving = ref(false), error = ref('')
  const theme = computed(() => values.value.theme), fontSize = computed(() => values.value.fontSize), dirty = computed(() => Object.keys(pending.value).length > 0)
  let owner: string | undefined, generation = 0, initial: Promise<void> | undefined, task: Promise<void> | undefined
  function reset() { generation++; owner = undefined; initial = undefined; task = undefined; values.value = defaultPreferences(); pending.value = {}; loading.value = false; saving.value = false; error.value = '' }
  function same(token: number) { return token === generation && owner === getSession()?.user.id }
  async function cache(token: number) {
    if (!owner || !same(token)) return
    try { await db.preferences.put({ owner, values: { ...values.value }, pending: { ...pending.value } }) }
    catch { if (same(token)) error.value = '本机设置缓存不可用，请保持联网并重试同步' }
  }
  function report(e: unknown) { error.value = e instanceof TypeError ? '当前无法连接，阅读设置尚未同步' : errorText(e) }
  async function sync() {
    if (task) return task
    if (!owner || loading.value || !same(generation)) return
    const token = generation
    task = (async () => {
      saving.value = true; error.value = ''
      try {
        await cache(token)
        while (same(token) && dirty.value) {
          const sent = { ...pending.value }
          const result = await api.patchSettings(sent)
          if (!same(token)) return
          const next = { ...pending.value }
          for (const key of Object.keys(sent) as (keyof PreferencePatch)[]) if (next[key] === sent[key]) delete next[key]
          pending.value = next
          values.value = parsePreferences({ ...result, ...next })
          await cache(token)
        }
      } catch (e) { if (same(token)) report(e) }
      finally { if (same(token)) { saving.value = false; task = undefined } }
    })()
    return task
  }
  function initialize(userId: string): Promise<void> {
    if (owner === userId && initial) return initial
    reset(); owner = userId; const token = generation; loading.value = true
    initial = (async () => {
      try {
        const local = await db.preferences.get(userId).catch(() => undefined)
        if (!same(token)) return
        if (local) {
          const cached = parsePreferences({ 'reader.theme': local.values?.theme, 'reader.font_size': local.values?.fontSize })
          values.value = cached
          const patch: PreferencePatch = {}
          if (local.pending?.['reader.theme'] === null || local.pending?.['reader.theme'] === cached.theme) patch['reader.theme'] = local.pending['reader.theme']
          if (local.pending?.['reader.font_size'] === null || local.pending?.['reader.font_size'] === cached.fontSize) patch['reader.font_size'] = local.pending['reader.font_size']
          pending.value = patch
        }
        const remote = await api.settings()
        if (!same(token)) return
        values.value = parsePreferences({ ...remote, ...pending.value })
        await cache(token)
      } catch (e) { if (same(token)) report(e) }
      finally { if (same(token)) loading.value = false }
      if (same(token) && dirty.value && !error.value) await sync()
    })()
    return initial
  }
  function update(patch: PreferencePatch) {
    if (loading.value || !owner || !same(generation)) return
    pending.value = { ...pending.value, ...patch }
    values.value = parsePreferences({ 'reader.theme': values.value.theme, 'reader.font_size': values.value.fontSize, ...patch })
    void cache(generation); void sync()
  }
  function setTheme(value: string) { if (['day', 'night', 'paper', 'green'].includes(value)) update({ 'reader.theme': value as ReaderTheme }) }
  function setFont(value: number) { if (Number.isFinite(value)) update({ 'reader.font_size': Math.max(14, Math.min(32, Math.round(value))) }) }
  function changeFont(delta: number) { setFont(fontSize.value + delta) }
  function restoreDefaults() { update({ 'reader.theme': null, 'reader.font_size': null }) }
  async function reload() { if (!owner || loading.value || saving.value || dirty.value) return; const userId = owner; initial = undefined; await initialize(userId) }
  async function retry() { if (dirty.value) await sync(); else await reload() }
  function sessionChanged() { if (owner && getSession()?.user.id !== owner) reset() }
  function online() { void retry() }
  sessionEvents.addEventListener('change', sessionChanged); window.addEventListener('online', online)
  onScopeDispose(() => { reset(); sessionEvents.removeEventListener('change', sessionChanged); window.removeEventListener('online', online) })
  return { theme, fontSize, loading, saving, dirty, error, initialize, sync, retry, reload, setTheme, setFont, changeFont, restoreDefaults }
})
