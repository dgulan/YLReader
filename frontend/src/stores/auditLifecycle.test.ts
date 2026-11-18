import { effectScope, ref } from 'vue'
import { createPinia, disposePinia } from 'pinia'
import { afterEach, expect, it, vi } from 'vitest'
import { setSession, sessionEvents } from '@/api/client'
import { api } from '@/api/reader'
import { useUser } from './user'
import { useCacheTask } from '@/views/admin/cacheTask'
import { useReplaceRules, emptyRule } from '@/views/admin/replaceRules'
import { useBookEditor } from '@/views/bookshelf/bookEditor'

afterEach(() => { setSession(null); vi.restoreAllMocks() })
it('does not mount or send requests with a missing owner', async () => {
  setSession(null)
  const scope = effectScope()
  const book = vi.spyOn(api, 'book'), rules = vi.spyOn(api, 'replaceRules'), create = vi.spyOn(api, 'createReplaceRule'), cache = vi.spyOn(api, 'cacheBook')
  try {
    const state = scope.run(() => ({ book: useBookEditor(ref('book')), rules: useReplaceRules(), cache: useCacheTask(ref('book')) }))!
    await state.book.load(); await state.rules.load(); await state.rules.save(undefined, emptyRule()); await state.cache.load()
    await state.cache.submit({ source_id: 'source', from_chapter: 0, to_chapter: 1 })
    expect(book).not.toHaveBeenCalled(); expect(rules).not.toHaveBeenCalled(); expect(create).not.toHaveBeenCalled(); expect(cache).not.toHaveBeenCalled()
  } finally { scope.stop() }
})
it('removes exactly the session listener belonging to a disposed user store', () => {
  const add = vi.spyOn(sessionEvents, 'addEventListener'), remove = vi.spyOn(sessionEvents, 'removeEventListener')
  const pinia = createPinia(); useUser(pinia)
  const listener = add.mock.calls.find(call => call[0] === 'change')?.[1]
  expect(listener).toBeTypeOf('function')
  disposePinia(pinia)
  expect(remove).toHaveBeenCalledWith('change', listener)
})
