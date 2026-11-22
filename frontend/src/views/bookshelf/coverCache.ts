import { getSession, requestBookCover } from '@/api/client'

type Entry = { raw: string; controller: AbortController; url?: string; pending?: Promise<string> }
const covers = new Map<string, Entry>()
const queue: (() => void)[] = []
let active = 0

function pump() {
  while (active < 4 && queue.length) queue.shift()!()
}

function schedule<T>(work: () => Promise<T>): Promise<T> {
  return new Promise((resolve, reject) => {
    queue.push(() => {
      active++
      void work().then(resolve, reject).finally(() => { active--; pump() })
    })
    pump()
  })
}

function key(owner: string, id: string) { return JSON.stringify([owner, id]) }

export function invalidateBookCover(owner: string, id: string, next?: string) {
  const slot = key(owner, id)
  const current = covers.get(slot)
  if (!current || current.raw === next) return
  covers.delete(slot)
  current.controller.abort()
  if (current.url) URL.revokeObjectURL(current.url)
}

export function cachedBookCover(owner: string, id: string, raw: string): string | undefined {
  const entry = covers.get(key(owner, id))
  if (entry?.raw === raw) return entry.url
}

export function loadBookCover(owner: string, id: string, raw: string): Promise<string> {
  invalidateBookCover(owner, id, raw)
  const slot = key(owner, id)
  const current = covers.get(slot)
  if (current?.url) return Promise.resolve(current.url)
  if (current?.pending) return current.pending
  const entry: Entry = { raw, controller: new AbortController() }
  covers.set(slot, entry)
  const signal = entry.controller.signal
  entry.pending = schedule(async () => {
    signal.throwIfAborted()
    if (getSession()?.user.id !== owner) throw new Error('Cover owner changed')
    const blob = await requestBookCover(id, signal)
    signal.throwIfAborted()
    if (getSession()?.user.id !== owner) throw new Error('Cover owner changed')
    entry.url = URL.createObjectURL(blob)
    return entry.url
  }).catch(error => {
    if (covers.get(slot) === entry) covers.delete(slot)
    throw error
  })
  return entry.pending
}
