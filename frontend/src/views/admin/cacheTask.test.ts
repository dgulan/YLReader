import 'fake-indexeddb/auto'
import { effectScope, ref } from 'vue'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { api } from '@/api/reader'
import { ApiError, setSession, type Model } from '@/api/client'
import { bookKey, clearUserCache, db } from '@/db/chapters'
import { cacheRequest, isActiveJob, useCacheTask } from './cacheTask'
const scopes: ReturnType<typeof effectScope>[] = []
const job = (status: Model<'Job'>['status'] = 'queued'): Model<'Job'> => ({ id: 'job-1', type: 'full_cache', status, progress: status === 'succeeded' ? 100 : 0, created_at: '2026-11-13T00:00:00Z' })
function setup() { const scope = effectScope(); scopes.push(scope); const book = ref('book-a'); return { scope, book, state: scope.run(() => useCacheTask(book))! } }
beforeEach(async () => { await db.jobs.clear(); setSession({ access_token: 'a', refresh_token: 'a', token_type: 'Bearer', expires_in: 900, user: { id: 'a', username: 'a', role: 'user', token_version: 1, created_at: '' } }) })
afterEach(() => { scopes.splice(0).forEach(s => s.stop()); setSession(null); vi.restoreAllMocks(); vi.useRealTimers() })
it('converts inclusive visible chapter numbers and rejects invalid ranges', () => {
  expect(cacheRequest(1, 10, 10, 'source')).toEqual({ from_chapter: 0, to_chapter: 9, source_id: 'source' })
  for (const [first, last] of [[0, 2], [3, 2], [1, 11], [1.5, 2], [NaN, 2]]) expect(() => cacheRequest(first!, last!, 10, 's')).toThrow()
  expect(isActiveJob(job('failed'))).toBe(false); expect(isActiveJob(job('running'))).toBe(true)
})
it('persists the latest job and restores the authoritative state after reopening', async () => {
  const { state, scope } = setup(); vi.spyOn(api, 'cacheBook').mockResolvedValue(job()); await state.submit({ source_id: 's' }); scope.stop()
  const next = setup(); vi.spyOn(api, 'job').mockResolvedValue({ ...job('succeeded'), result: { total: 2, succeeded: 2 } }); await next.state.load()
  expect(next.state.job.value?.status).toBe('succeeded'); expect(next.state.notice.value).toBe(''); expect((await db.jobs.get(bookKey('a', 'book-a')))?.job.result).toEqual({ total: 2, succeeded: 2 })
})
it('polls active jobs and stops after a terminal state or leaving the view', async () => {
  const { state, scope } = setup(); const scheduled = vi.spyOn(globalThis, 'setTimeout'), cleared = vi.spyOn(globalThis, 'clearTimeout')
  vi.spyOn(api, 'cacheBook').mockResolvedValue(job()); const get = vi.spyOn(api, 'job').mockResolvedValue(job('succeeded')); await state.submit({})
  await vi.waitFor(() => expect(state.job.value?.status).toBe('succeeded'), { timeout: 4500 })
  expect(get).toHaveBeenCalledTimes(1)
  get.mockResolvedValue(job('running')); await state.refresh()
  const at = scheduled.mock.calls.reduce((found, call, i) => call[1] === 2000 ? i : found, -1), timer = scheduled.mock.results[at]!.value
  scope.stop(); expect(cleared).toHaveBeenCalledWith(timer)
})
it('checks the server after cancellation, including a concurrent transition to completed', async () => {
  const { state } = setup(); vi.spyOn(api, 'cacheBook').mockResolvedValue(job()); await state.submit({})
  vi.spyOn(api, 'cancelJob').mockRejectedValue(new ApiError(409, 'conflict', null)); vi.spyOn(api, 'job').mockResolvedValue(job('succeeded')); await state.cancel()
  expect(state.job.value?.status).toBe('succeeded'); expect(state.active.value).toBe(false); expect(state.notice.value).toContain('状态已变化')
})
it('preserves the last known state on network failure and permits manual refresh', async () => {
  const { state } = setup(); vi.spyOn(api, 'cacheBook').mockResolvedValue(job()); await state.submit({})
  vi.spyOn(api, 'job').mockRejectedValueOnce(new TypeError('offline')).mockResolvedValueOnce(job('canceled'))
  await state.refresh(); expect(state.job.value?.status).toBe('queued'); expect(state.error.value).toContain('已暂停刷新'); await state.refresh(); expect(state.job.value?.status).toBe('canceled')
})
it('ignores late responses after changing book or account and clears account records on logout', async () => {
  const { state, book } = setup(); let finish!: (v: Model<'Job'>) => void
  vi.spyOn(api, 'cacheBook').mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
  const request = state.submit({}); book.value = 'book-b'; finish(job()); await request; expect(state.job.value).toBeUndefined(); expect(await db.jobs.count()).toBe(0)
  vi.mocked(api.cacheBook).mockResolvedValue(job()); await state.submit({}); expect(await db.jobs.count()).toBe(1)
  setSession(null); await clearUserCache('a'); expect(await db.jobs.count()).toBe(0)
})
it('marks a missing task as unavailable without inventing a canceled state', async () => {
  const { state } = setup(); vi.spyOn(api, 'cacheBook').mockResolvedValue(job()); await state.submit({})
  vi.spyOn(api, 'job').mockRejectedValue(new ApiError(404, 'not_found', null)); await state.refresh()
  expect(state.missing.value).toBe(true); expect(state.active.value).toBe(false); expect(state.job.value?.status).toBe('queued')
})
