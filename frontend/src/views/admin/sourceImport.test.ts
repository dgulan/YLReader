import { effectScope, type EffectScope } from 'vue'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { api } from '@/api/reader'
import { setSession, type Model } from '@/api/client'
import { useSourceImport } from './sourceImport'
const scopes: EffectScope[] = []
const report: Model<'ImportResult'> = { imported: 1, updated: 0, skipped: 0, failed: [], dry_run: true }
function setup(confirm?: (report: Model<'ImportResult'>) => Promise<boolean>) { const scope = effectScope(); scopes.push(scope); return scope.run(() => useSourceImport(async () => undefined, confirm))! }
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
it('previews and imports online book sources from URL via importSourcesFromUrl', async () => {
  const state = setup()
  const sendUrl = vi.spyOn(api, 'importSourcesFromUrl').mockResolvedValue(report)
  state.sourceUrl.value = 'https://example.com/sources.json'
  expect(state.canPreviewUrl.value).toBe(true)
  await state.importUrl(true)
  expect(state.canCommitUrl.value).toBe(true)
  await state.importUrl(false)
  expect(sendUrl.mock.calls).toEqual([
    ['https://example.com/sources.json', true, 'skip'],
    ['https://example.com/sources.json', false, 'skip'],
  ])
})

it.each([true, false])('asks before importing duplicate online sources, decision %s', async overwrite => {
  const confirm = vi.fn().mockResolvedValue(overwrite)
  const state = setup(confirm), url = 'https://example.com/sources.json'
  const conflict: Model<'ImportResult'> = {
    ...report, skipped: 1, duplicate_count: 1,
    duplicates: [{ book_source_url: 'https://old.example/', book_source_name: '旧书源' }],
  }
  const final = { ...report, dry_run: false, skipped: overwrite ? 0 : 1, updated: overwrite ? 1 : 0 }
  const send = vi.spyOn(api, 'importSourcesFromUrl').mockResolvedValueOnce(conflict).mockResolvedValueOnce(final)
  state.sourceUrl.value = url
  await state.importUrl(true)
  expect(confirm).not.toHaveBeenCalled()
  await state.importUrl(false)
  expect(confirm).toHaveBeenCalledWith(conflict)
  expect(send.mock.calls).toEqual([[url, true, 'skip'], [url, false, overwrite ? 'overwrite' : 'skip']])
  expect(state.result.value).toEqual(final)
})

it.each([true, false])('asks before overwriting, decision %s', async overwrite => {
  const confirm = vi.fn().mockResolvedValue(overwrite)
  const state = setup(confirm)
  const existing = { bookSourceUrl: 'https://old.example', bookSourceName: '新规则' }
  const added = { bookSourceUrl: 'https://new.example', bookSourceName: '新增' }
  state.text.value = JSON.stringify([existing, added])
  const conflict = {
    ...report, dry_run: false, skipped: 1, duplicate_count: 1,
    duplicates: [{ book_source_url: existing.bookSourceUrl, book_source_name: '旧规则' }],
  }
  const send = vi.spyOn(api, 'importSources').mockResolvedValueOnce(report).mockResolvedValueOnce(conflict)
    .mockResolvedValueOnce({ ...report, dry_run: false, imported: 0, updated: 1, overwritten_count: 1 })
  await state.importText(true)
  await state.importText(false)
  expect(confirm).toHaveBeenCalledWith(conflict)
  expect(send.mock.calls[1]).toEqual([JSON.stringify([existing, added]), false, 'skip'])
  if (overwrite) {
    expect(send.mock.calls[2]).toEqual([JSON.stringify([existing]), false, 'overwrite'])
    expect(state.result.value?.overwritten_count).toBe(1)
    expect(state.result.value?.imported).toBe(1)
  } else {
    expect(send).toHaveBeenCalledTimes(2)
    expect(state.result.value).toEqual(conflict)
  }
})
