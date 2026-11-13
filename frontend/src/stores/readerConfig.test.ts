import 'fake-indexeddb/auto'
import Dexie from 'dexie'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { api } from '@/api/reader'
import { setSession, type Model } from '@/api/client'
import { db, clearUserCache } from '@/db/chapters'
import { useReaderConfig } from './readerConfig'
import { parsePreferences } from './readerPreferences'
const session = (id: string): Model<'TokensResponse'> => ({ access_token: id, refresh_token: `${id}-refresh`, token_type: 'Bearer', expires_in: 900, user: { id, username: id, role: 'user', token_version: 1, created_at: '2026-11-12T00:00:00Z' } })
let store: ReturnType<typeof useReaderConfig>
beforeEach(async () => { await db.preferences.clear(); setActivePinia(createPinia()); setSession(session('a')); store = useReaderConfig() })
afterEach(() => { store.$dispose(); setSession(null); vi.restoreAllMocks() })
it('uses safe defaults for missing, malformed or out-of-range server values', () => {
  expect(parsePreferences({ 'reader.theme': '<script>', 'reader.font_size': 99 })).toEqual({ theme: 'paper', fontSize: 20 })
  expect(parsePreferences({ 'reader.theme': 'night', 'reader.font_size': 18 })).toEqual({ theme: 'night', fontSize: 18 })
  expect(parsePreferences({ 'reader.font_size': '24' }).fontSize).toBe(20)
})
it('upgrades the existing offline database without removing saved reading positions', async () => {
  await db.delete()
  const legacy = new Dexie('yreader-offline-v1')
  legacy.version(1).stores({ chapters: 'key, owner, [owner+bookId]', books: 'key, owner' })
  legacy.version(2).stores({ positions: 'key, owner' })
  await legacy.table('positions').put({ key: 'a:legacy', owner: 'a', index: 7, offset: 99 })
  legacy.close(); await db.open()
  expect(await db.positions.get('a:legacy')).toMatchObject({ index: 7, offset: 99 })
  await db.preferences.put({ owner: 'a', values: { theme: 'night', fontSize: 24 }, pending: {} })
  expect((await db.preferences.get('a'))?.values.theme).toBe('night'); await db.positions.delete('a:legacy')
})
it('loads account settings and patches only changed keys, preserving unrelated settings', async () => {
  vi.spyOn(api, 'settings').mockResolvedValue({ 'reader.theme': 'green', 'reader.font_size': 24, 'other.value': 7 })
  const patch = vi.spyOn(api, 'patchSettings').mockResolvedValue({ 'reader.theme': 'night', 'reader.font_size': 24, 'other.value': 7 })
  await store.initialize('a'); store.setTheme('night'); await store.sync()
  expect(store.theme).toBe('night'); expect(store.fontSize).toBe(24); expect(patch).toHaveBeenCalledExactlyOnceWith({ 'reader.theme': 'night' }); expect(store.dirty).toBe(false)
  expect((await db.preferences.get('a'))?.values).toEqual({ theme: 'night', fontSize: 24 })
})
it('serializes rapid edits without allowing an old response to replace newer choices', async () => {
  vi.spyOn(api, 'settings').mockResolvedValue({})
  let finish!: (value: Model<'Settings'>) => void
  const patch = vi.spyOn(api, 'patchSettings').mockImplementationOnce(() => new Promise(resolve => { finish = resolve })).mockResolvedValueOnce({ 'reader.theme': 'green', 'reader.font_size': 26 })
  await store.initialize('a'); store.setTheme('night'); await vi.waitFor(() => expect(patch).toHaveBeenCalledTimes(1))
  store.setTheme('green'); store.setFont(26); finish({ 'reader.theme': 'night' }); await store.sync()
  expect(patch).toHaveBeenNthCalledWith(2, { 'reader.theme': 'green', 'reader.font_size': 26 }); expect(store.theme).toBe('green'); expect(store.fontSize).toBe(26); expect(store.dirty).toBe(false)
})
it('keeps offline changes in IndexedDB and retries after a reload without losing other keys', async () => {
  vi.spyOn(api, 'settings').mockResolvedValue({ 'reader.theme': 'day', 'reader.font_size': 20 })
  const patch = vi.spyOn(api, 'patchSettings').mockRejectedValueOnce(new TypeError('offline')).mockResolvedValueOnce({ 'reader.theme': 'day', 'reader.font_size': 28, 'other.value': true })
  await store.initialize('a'); store.setFont(28); await store.sync(); expect(store.dirty).toBe(true); expect(store.error).toContain('尚未同步')
  store.$dispose(); setActivePinia(createPinia()); store = useReaderConfig(); await store.initialize('a')
  expect(store.fontSize).toBe(28); expect(store.theme).toBe('day'); expect(store.dirty).toBe(false); expect(patch).toHaveBeenLastCalledWith({ 'reader.font_size': 28 })
})
it('ignores late reads and writes after account changes and clears cached settings on logout', async () => {
  let read!: (v: Model<'Settings'>) => void
  vi.spyOn(api, 'settings').mockImplementationOnce(() => new Promise(resolve => { read = resolve })).mockResolvedValueOnce({})
  const pending = store.initialize('a'); await vi.waitFor(() => expect(api.settings).toHaveBeenCalledTimes(1))
  setSession(session('b')); await store.initialize('b'); read({ 'reader.theme': 'night' }); await pending; expect(store.theme).toBe('paper')
  let write!: (v: Model<'Settings'>) => void
  const patch = vi.spyOn(api, 'patchSettings').mockImplementationOnce(() => new Promise(resolve => { write = resolve }))
  store.setTheme('green'); await vi.waitFor(() => expect(patch).toHaveBeenCalledTimes(1)); setSession(null); await clearUserCache('b'); write({ 'reader.theme': 'green' }); await store.sync()
  expect(store.theme).toBe('paper'); expect(store.dirty).toBe(false); expect(await db.preferences.get('b')).toBeUndefined()
})
it('restores defaults by deleting only reader keys, and allows retry of a failed initial read', async () => {
  vi.spyOn(api, 'settings').mockRejectedValueOnce(new TypeError('offline')).mockResolvedValueOnce({ 'reader.theme': 'night', 'reader.font_size': 30 })
  await store.initialize('a'); expect(store.error).toContain('尚未同步'); await store.retry(); expect(store.theme).toBe('night')
  const patch = vi.spyOn(api, 'patchSettings').mockResolvedValue({ 'other.value': 7 })
  store.restoreDefaults(); await store.sync(); expect(patch).toHaveBeenCalledExactlyOnceWith({ 'reader.theme': null, 'reader.font_size': null }); expect(store.fontSize).toBe(20); expect(store.theme).toBe('paper')
})
