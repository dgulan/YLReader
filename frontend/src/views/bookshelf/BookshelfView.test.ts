import { createApp, h, nextTick, type App, type VNode } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { afterEach, expect, it, vi } from 'vitest'
import { api } from '@/api/reader'
import type { Model } from '@/api/client'
import BookshelfView from './BookshelfView.vue'

vi.mock('element-plus', async () => {
  const { h } = await import('vue')
  return Object.fromEntries(['ElButton', 'ElInput', 'ElAlert', 'ElEmpty', 'ElPagination', 'ElTabs', 'ElTabPane', 'ElSkeleton']
    .map(name => [name, (_: unknown, { slots, attrs }: { slots: { default?: () => VNode[] }; attrs: Record<string, unknown> }) => h('div', attrs, slots.default?.())]))
})
let app: App | undefined
let host: HTMLDivElement
afterEach(() => { app?.unmount(); app = undefined; host?.remove(); vi.restoreAllMocks() })
async function mount() {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/', component: { render: () => h('div') } }] })
  await router.push('/')
  host = document.createElement('div'); document.body.append(host)
  app = createApp(BookshelfView); app.use(router); app.mount(host)
  await nextTick()
}
function pending() {
  let resolve!: (value: Model<'BookPage'>) => void
  return { promise: new Promise<Model<'BookPage'>>(done => { resolve = done }), resolve: (total: number) => resolve({ items: [], total, page: 1, page_size: 20 }) }
}
it('aborts the actual list request when the bookshelf unmounts', async () => {
  const held = pending(), list = vi.spyOn(api, 'books').mockReturnValue(held.promise)
  await mount()
  const signal = list.mock.calls[0]?.[2]
  expect(signal).toBeInstanceOf(AbortSignal)
  app!.unmount(); app = undefined
  expect(signal!.aborted).toBe(true)
  held.resolve(9); await nextTick()
  expect(list).toHaveBeenCalledTimes(1)
})
it('aborts on pagehide and reloads on persisted pageshow without accepting the old result', async () => {
  const held = pending(), restored = pending()
  const list = vi.spyOn(api, 'books').mockReturnValueOnce(held.promise).mockReturnValueOnce(restored.promise)
  await mount()
  const signal = list.mock.calls[0]?.[2]
  window.dispatchEvent(new PageTransitionEvent('pagehide', { persisted: true }))
  expect(signal!.aborted).toBe(true)
  window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true }))
  expect(list).toHaveBeenCalledTimes(2)
  restored.resolve(7); await vi.waitFor(() => expect(host.querySelector('.count')?.textContent).toContain('7'))
  held.resolve(9); await nextTick(); await nextTick()
  expect(host.querySelector('.count')?.textContent).toContain('7')
  app!.unmount(); app = undefined
  window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true }))
  expect(list).toHaveBeenCalledTimes(2)
})
it('cancels a superseded filter and ignores a late successful response', async () => {
  const held = pending(), latest = pending()
  const list = vi.spyOn(api, 'books').mockReturnValueOnce(held.promise).mockReturnValueOnce(latest.promise)
  await mount()
  host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
  expect(list.mock.calls[0]?.[2]?.aborted).toBe(true)
  latest.resolve(6); await vi.waitFor(() => expect(host.querySelector('.count')?.textContent).toContain('6'))
  held.resolve(8); await nextTick(); await nextTick()
  expect(host.querySelector('.count')?.textContent).toContain('6')
})
it('still surfaces an ordinary list failure', async () => {
  vi.spyOn(api, 'books').mockRejectedValue(new Error('synthetic-list-failure'))
  await mount()
  await vi.waitFor(() => expect(host.querySelector('[title="synthetic-list-failure"]')).not.toBeNull())
})
it('cancels pending GETs on early document teardown without preventing navigation', async () => {
  const list = vi.spyOn(api, 'books').mockImplementation((_page, _query, signal) => new Promise((_resolve, reject) => {
    signal!.addEventListener('abort', () => reject(new Error('synthetic-cancel')), { once: true })
  }))
  await mount()
  const event = new Event('beforeunload', { cancelable: true })
  expect(window.dispatchEvent(event)).toBe(true)
  expect(event.defaultPrevented).toBe(false)
  expect(list.mock.calls[0]?.[2]?.aborted).toBe(true)
  await nextTick(); await nextTick()
  expect(host.querySelector('[title="synthetic-cancel"]')).toBeNull()
})
it('renders book cover image when cover_url is present and falls back on error', async () => {
  vi.spyOn(api, 'books').mockResolvedValue({
    items: [{
      id: 'b1',
      book_type: 'online',
      name: '带封面书',
      author: '作者甲',
      cover_url: 'https://img.example/cover1.jpg',
      status: 'serializing',
      total_chapters: 12,
      needs_source: false,
      override_count: 0,
      version: 1,
      created_at: '',
      updated_at: '',
    }],
    total: 1,
    page: 1,
    page_size: 20,
  } as Model<'BookPage'>)
  await mount()
  await vi.waitFor(() => expect(host.querySelector('img.book-cover-img')).not.toBeNull())
  const img = host.querySelector<HTMLImageElement>('img.book-cover-img')!
  expect(img.getAttribute('src')).toBe('https://img.example/cover1.jpg')
  img.dispatchEvent(new Event('error'))
  await nextTick()
  expect(host.querySelector('img.book-cover-img')).toBeNull()
  expect(host.querySelector('.book-cover')?.textContent).toContain('带封面书')
})
