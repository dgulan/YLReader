import { effectScope, type EffectScope } from 'vue'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { api } from '@/api/reader'
import { setSession, type Model } from '@/api/client'
import { useSourceImport } from './sourceImport'
const scopes: EffectScope[] = []
const report: Model<'ImportResult'> = { imported: 1, updated: 0, skipped: 0, failed: [], dry_run: true }
function setup() { const scope = effectScope(); scopes.push(scope); return scope.run(() => useSourceImport(async () => undefined))! }
beforeEach(() => setSession({ access_token: 'a', refresh_token: 'r', token_type: 'Bearer', expires_in: 900, user: { id: 'a', username: 'a', role: 'user', token_version: 1, created_at: '' } }))
afterEach(() => { scopes.splice(0).forEach(scope => scope.stop()); setSession(null); vi.restoreAllMocks() })
it('keeps a large file out of the textarea while sending its exact contents', async () => {
  const state = setup(), payload = ' '.repeat(600000) + '[]'
  const send = vi.spyOn(api, 'importSources').mockResolvedValue(report)
  await state.readFile({ name: 'sources.json', size: payload.length, text: async () => payload })
  expect(state.text.value).toBe(''); expect(state.fileLabel.value).toContain('sources.json')
  await state.importText(true); await state.importText(false)
  expect(send.mock.calls.map(call => call[0])).toEqual([payload, payload])
})
it('shows file read failure without leaving a usable stale preview', async () => {
  const state = setup(); state.dryRun.value = report
  await state.readFile({ name: 'bad.json', size: 1, text: async () => { throw new Error('unreadable') } })
  expect(state.error.value).toContain('无法读取'); expect(state.dryRun.value).toBeUndefined(); expect(state.busy.value).toBe(false)
})
it('ignores a previous file read that finishes after a newer file', async () => {
  const state = setup(); let finish!: (text: string) => void
  const old = state.readFile({ name: 'old.json', size: 1, text: () => new Promise(resolve => { finish = resolve }) })
  await state.readFile({ name: 'new.json', size: 2, text: async () => '[]' })
  finish('old'); await old; expect(state.text.value).toBe('[]')
})
it('does not let an old preview authorize edited input', async () => {
  const state = setup(); state.text.value = '[]'
  let finish!: (value: Model<'ImportResult'>) => void
  const send = vi.spyOn(api, 'importSources').mockImplementation(() => new Promise(resolve => { finish = resolve }))
  const old = state.importText(true); state.text.value = '[{}]'; finish(report); await old
  expect(state.dryRun.value).toBeUndefined(); await state.importText(false); expect(send).toHaveBeenCalledTimes(1)
})
