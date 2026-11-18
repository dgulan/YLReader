import { afterEach, expect, it, vi } from 'vitest'
import { RequestDeadlineError, withDeadline } from './deadline'

afterEach(() => vi.useRealTimers())
it('bounds waiting and aborts even when the underlying operation ignores cancellation', async () => {
  vi.useFakeTimers()
  let finish!: (value: string) => void, signal!: AbortSignal
  const result = withDeadline(s => { signal = s; return new Promise<string>(resolve => { finish = resolve }) }, 50)
  const rejected = expect(result).rejects.toBeInstanceOf(RequestDeadlineError)
  await vi.advanceTimersByTimeAsync(50)
  await rejected
  expect(signal.aborted).toBe(true)
  finish('late acknowledgement')
  await expect(result).rejects.toBeInstanceOf(RequestDeadlineError)
  expect(vi.getTimerCount()).toBe(0)
})
it('clears its timer after a response succeeds', async () => {
  vi.useFakeTimers()
  await expect(withDeadline(async () => 'ok', 50)).resolves.toBe('ok')
  expect(vi.getTimerCount()).toBe(0)
})
it('preserves an immediate error and clears its timer', async () => {
  vi.useFakeTimers()
  const failure = new Error('network')
  await expect(withDeadline(() => { throw failure }, 50)).rejects.toBe(failure)
  expect(vi.getTimerCount()).toBe(0)
})
