import 'fake-indexeddb/auto'
import { effectScope } from 'vue'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { api } from '@/api/reader'
import { ApiError, setSession, type Model } from '@/api/client'
import { db } from '@/db/chapters'
import { emptyRule, ruleInput, useReplaceRules, type RuleDraft } from './replaceRules'
const scopes: ReturnType<typeof effectScope>[] = []
const rule = (): Model<'ReplaceRule'> & RuleDraft => ({ ...emptyRule(), id: 'rule', name: '去除广告', pattern: '广告' })
function setup() { const scope = effectScope(); scopes.push(scope); return scope.run(useReplaceRules)! }
beforeEach(async () => { await db.chapters.clear(); setSession({ access_token: 'a', refresh_token: 'a', token_type: 'Bearer', expires_in: 900, user: { id: 'a', username: 'a', role: 'user', token_version: 1, created_at: '' } }) })
afterEach(() => { scopes.splice(0).forEach(s => s.stop()); setSession(null); vi.restoreAllMocks() })
it('validates lengths and order without interpreting a RE2 expression as JavaScript', () => {
  expect(() => ruleInput(emptyRule())).toThrow(); expect(() => ruleInput({ ...rule(), order_index: 1.5 })).toThrow()
  expect(ruleInput({ ...rule(), pattern: '(?P<word>广告)', replacement: '${word}', group_name: '' })).toMatchObject({ group_name: '默认', pattern: '(?P<word>广告)' })
})
it('preserves failed drafts and only invalidates cached chapters after successful writes', async () => {
  const state = setup(); const draft = { ...emptyRule(), name: 'test', pattern: '[' }
  const clear = vi.spyOn(db.chapters, 'where')
  vi.spyOn(api, 'createReplaceRule').mockRejectedValue(new ApiError(422, 'rule_execution_failed', null))
  expect(await state.save(undefined, draft)).toBe(false); expect(draft.pattern).toBe('['); expect(clear).not.toHaveBeenCalled(); expect(state.error.value).toContain('草稿已保留')
})
it('uses the actual returned ID even when names and patterns are duplicated', async () => {
  const state = setup(); vi.spyOn(api, 'replaceRules').mockResolvedValue([rule()]); await state.load()
  vi.spyOn(api, 'createReplaceRule').mockResolvedValue({ ...rule(), id: 'second' }); expect(await state.save(undefined, rule())).toBe(true); expect(state.savedId.value).toBe('second'); expect(state.items.value).toHaveLength(2)
})
it('clears only the current user chapters and keeps reading positions', async () => {
  const state = setup(); await db.chapters.bulkPut([{ key: 'a:book:0', owner: 'a', bookId: 'book', index: 0, context: 'x', content: {} as Model<'ChapterContent'> }, { key: 'b:book:0', owner: 'b', bookId: 'book', index: 0, context: 'x', content: {} as Model<'ChapterContent'> }])
  const positions = vi.spyOn(db.positions, 'delete'); vi.spyOn(api, 'createReplaceRule').mockResolvedValue(rule())
  await state.save(undefined, rule()); expect(await db.chapters.count()).toBe(1); expect((await db.chapters.toArray())[0]?.owner).toBe('b'); expect(positions).not.toHaveBeenCalled()
})
it('keeps records after failed deletion and reconciles an already deleted rule', async () => {
  const state = setup(); vi.spyOn(api, 'replaceRules').mockResolvedValue([rule()]); await state.load()
  vi.spyOn(api, 'deleteReplaceRule').mockRejectedValueOnce(new TypeError('offline')).mockRejectedValueOnce(new ApiError(404, 'not_found', null))
  expect(await state.remove('rule')).toBe(false); expect(state.items.value).toHaveLength(1); expect(await state.remove('rule')).toBe(true); expect(state.items.value).toHaveLength(0)
})
it('ignores a successful write returned after account logout', async () => {
  const state = setup(); let finish!: (v: Model<'ReplaceRule'>) => void
  vi.spyOn(api, 'createReplaceRule').mockImplementation(() => new Promise(resolve => { finish = resolve }))
  const request = state.save(undefined, rule()); setSession(null); finish(rule()); expect(await request).toBe(false); expect(state.items.value).toHaveLength(0)
})
