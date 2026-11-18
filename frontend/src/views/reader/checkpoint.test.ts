import { afterEach, expect, it, vi } from 'vitest'
import { singleFlight, watchPageCheckpoint } from './checkpoint'

afterEach(() => vi.restoreAllMocks())
it('preserves explicit reconnect intents after an in-flight failure without parallel saves', async () => {
  let finish!: (value: boolean) => void
  const run = vi.fn(() => new Promise<boolean>(resolve => { finish = resolve }))
  const save = singleFlight(run), first = save()
  await Promise.resolve()
  const reconnect = save.afterPending(() => save()), duplicate = save.afterPending(() => save())
  expect(run).toHaveBeenCalledTimes(1)
  finish(false); expect(await first).toBe(false)
  await vi.waitFor(() => expect(run).toHaveBeenCalledTimes(2))
  finish(true)
  expect(await Promise.all([reconnect, duplicate])).toEqual([true, true])
  expect(run).toHaveBeenCalledTimes(2)
})
it('lets a stale reconnect guard discard queued work', async () => {
  let finish!: (value: boolean) => void
  const run = vi.fn(() => new Promise<boolean>(resolve => { finish = resolve }))
  const save = singleFlight(run), first = save()
  await Promise.resolve()
  let current = true
  const reconnect = save.afterPending(() => current ? save() : Promise.resolve(false))
  current = false; finish(false)
  expect(await first).toBe(false); expect(await reconnect).toBe(false)
  expect(run).toHaveBeenCalledTimes(1)
})
it('shares a failed save without retrying for each waiting trigger', async () => {
  let finish!: (value: boolean) => void
  const run = vi.fn(() => new Promise<boolean>(resolve => { finish = resolve }))
  const save = singleFlight(run), first = save(), second = save(), third = save()
  expect(second).toBe(first); expect(third).toBe(first)
  await Promise.resolve(); expect(run).toHaveBeenCalledTimes(1)
  finish(false); expect(await Promise.all([first, second, third])).toEqual([false, false, false])
  expect(run).toHaveBeenCalledTimes(1)
  const retry = save(); await Promise.resolve(); finish(true)
  expect(await retry).toBe(true); expect(run).toHaveBeenCalledTimes(2)
})
it('releases the slot after a synchronous throw', async () => {
  const run = vi.fn((): Promise<boolean> => { throw new Error('storage') }), save = singleFlight(run)
  await expect(save()).rejects.toThrow('storage')
  run.mockResolvedValue(true)
  await expect(save()).resolves.toBe(true)
})
it('checkpoints hidden and pagehide events and removes both listeners', () => {
  const visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
  const checkpoint = vi.fn(), stop = watchPageCheckpoint(checkpoint)
  try {
    document.dispatchEvent(new Event('visibilitychange')); expect(checkpoint).not.toHaveBeenCalled()
    visibility.mockReturnValue('hidden'); document.dispatchEvent(new Event('visibilitychange'))
    window.dispatchEvent(new Event('pagehide')); expect(checkpoint).toHaveBeenCalledTimes(2)
    stop(); document.dispatchEvent(new Event('visibilitychange')); window.dispatchEvent(new Event('pagehide'))
    expect(checkpoint).toHaveBeenCalledTimes(2)
  } finally { stop() }
})
