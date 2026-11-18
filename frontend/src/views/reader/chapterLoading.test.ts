import { describe, it, expect, vi } from 'vitest'
import type { Model } from '@/api/client'
import { loadChapter, preloadChapters, type ChapterRequest } from './chapterLoading'

const content = { content: '原书正文😀' } as Model<'ChapterContent'>
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(r => { resolve = r })
  return { promise, resolve }
}
function fixture() {
  const controller = new AbortController()
  const request: ChapterRequest = { owner: 'user-a', bookId: 'book-a', context: 'source-a:1', index: 5, signal: controller.signal }
  const io = { fetch: vi.fn(async () => content), read: vi.fn(async () => content), write: vi.fn(async () => {}), cacheError: vi.fn() }
  return { controller, request, io }
}

describe('chapter request ownership and cancellation', () => {
  it('uses the original book and source even if reactive caller fields change during fetch', async () => {
    const { request, io } = fixture(), pending = deferred<Model<'ChapterContent'>>()
    io.fetch.mockImplementation(() => pending.promise)
    const result = loadChapter(request, io)
    Object.assign(request, { owner: 'user-b', bookId: 'book-b', context: 'source-b:2', index: 9 })
    pending.resolve(content)
    expect((await result).cached).toBe(false)
    expect(io.write).toHaveBeenCalledExactlyOnceWith('user-a', 'book-a', 5, 'source-a:1', content)
  })
  it('discards a late response even when the fetch implementation ignores cancellation', async () => {
    const { request, io, controller } = fixture(), pending = deferred<Model<'ChapterContent'>>()
    io.fetch.mockImplementation(() => pending.promise)
    const result = loadChapter(request, io)
    controller.abort(); pending.resolve(content)
    await expect(result).rejects.toMatchObject({ name: 'AbortError' })
    expect(io.write).not.toHaveBeenCalled(); expect(io.read).not.toHaveBeenCalled()
  })
  it('uses only the matching offline cache for network errors', async () => {
    const { request, io } = fixture()
    io.fetch.mockRejectedValue(new TypeError('offline'))
    expect(await loadChapter(request, io)).toEqual({ result: content, cached: true })
    expect(io.read).toHaveBeenCalledExactlyOnceWith('user-a', 'book-a', 5, 'source-a:1')
    expect(io.write).not.toHaveBeenCalled()
  })
  it('does not fall back for server errors or canceled network failures', async () => {
    const { request, io, controller } = fixture()
    io.fetch.mockRejectedValue(new Error('server failure'))
    await expect(loadChapter(request, io)).rejects.toThrow('server failure')
    controller.abort()
    await expect(loadChapter(request, io)).rejects.toMatchObject({ name: 'AbortError' })
    expect(io.read).not.toHaveBeenCalled()
  })
  it('ignores canceled cache completions and does not report a spurious cache error', async () => {
    const { request, io, controller } = fixture(), pending = deferred<void>()
    io.write.mockImplementation(() => pending.promise)
    const result = loadChapter(request, io)
    await vi.waitFor(() => expect(io.write).toHaveBeenCalledTimes(1))
    controller.abort(); pending.resolve()
    await expect(result).rejects.toMatchObject({ name: 'AbortError' })
    expect(io.cacheError).not.toHaveBeenCalled()
  })
  it('preserves online content when storage is unavailable', async () => {
    const { request, io } = fixture()
    io.write.mockRejectedValue(new Error('quota'))
    expect(await loadChapter(request, io)).toEqual({ result: content, cached: false })
    expect(io.cacheError).toHaveBeenCalledTimes(1)
  })
})

describe('neighbor preloading', () => {
  it('limits concurrency to two and continues after a failed neighbor', async () => {
    const controller = new AbortController(), gates = [deferred<void>(), deferred<void>()]
    let running = 0, peak = 0
    const load = vi.fn(async (i: number) => {
      running++; peak = Math.max(peak, running)
      try { if (i < 2) await gates[i]!.promise; if (i === 2) throw new Error('unavailable') }
      finally { running-- }
    })
    const done = preloadChapters([0, 1, 2, 3], controller.signal, load)
    expect(load.mock.calls.map(([i]) => i)).toEqual([0, 1])
    gates[0]!.resolve(); gates[1]!.resolve(); await done
    expect(peak).toBe(2); expect(load.mock.calls.map(([i]) => i)).toEqual([0, 1, 2, 3])
  })
  it('never starts queued neighbors after cancellation', async () => {
    const controller = new AbortController(), gate = deferred<void>()
    const load = vi.fn(() => gate.promise)
    const done = preloadChapters([1, 2, 3, 4], controller.signal, load)
    controller.abort(); gate.resolve(); await done
    expect(load).toHaveBeenCalledTimes(2)
    await preloadChapters([5], controller.signal, load)
    expect(load).toHaveBeenCalledTimes(2)
  })
})
