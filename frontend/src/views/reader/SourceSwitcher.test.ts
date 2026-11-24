import { createApp, h, nextTick, ref, type App } from 'vue'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { api } from '@/api/reader'
import { setSession, type Model } from '@/api/client'
import SourceSwitcher from './SourceSwitcher.vue'
let app: App, host: HTMLDivElement
const book = ref('first'), child = ref<InstanceType<typeof SourceSwitcher>>()
if (!HTMLDialogElement.prototype.showModal) Object.defineProperty(HTMLDialogElement.prototype, 'showModal', { configurable: true, writable: true, value() {} })
if (!HTMLDialogElement.prototype.close) Object.defineProperty(HTMLDialogElement.prototype, 'close', { configurable: true, writable: true, value() {} })
function held<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>(done => { resolve = done }); return { promise, resolve } }
beforeEach(() => {
  book.value = 'first'
  setSession({ access_token: 'a', refresh_token: 'r', token_type: 'Bearer', expires_in: 900, user: { id: 'a', username: 'a', role: 'user', token_version: 1, created_at: '' } })
  vi.spyOn(HTMLDialogElement.prototype, 'showModal').mockImplementation(function (this: HTMLDialogElement) { this.open = true })
  vi.spyOn(HTMLDialogElement.prototype, 'close').mockImplementation(function (this: HTMLDialogElement) { this.open = false })
  host = document.createElement('div'); document.body.append(host)
  app = createApp({ render: () => h(SourceSwitcher, { bookId: book.value, ref: child }) }); app.mount(host)
})
afterEach(() => { app?.unmount(); host?.remove(); setSession(null); vi.restoreAllMocks() })
it('aborts on book change and ignores the old mappings response', async () => {
  const pending = held<Model<'BookSourceMapping'>[]>()
  const mappings = vi.spyOn(api, 'mappings').mockReturnValue(pending.promise)
  vi.spyOn(api, 'progress').mockResolvedValue({ version: 1 } as Model<'Progress'>)
  const open = child.value!.open(); book.value = 'second'; await nextTick()
  expect(mappings.mock.calls[0]![1]!.aborted).toBe(true)
  pending.resolve([{ source_id: 'old', source_name: 'OLD SOURCE', book_url: 'old' } as Model<'BookSourceMapping'>]); await open
  expect(host.textContent).not.toContain('OLD SOURCE'); expect(host.querySelector('dialog')!.open).toBe(false)
})
it('cannot commit a previous book after its progress read finishes late', async () => {
  vi.spyOn(api, 'mappings').mockResolvedValue([{ source_id: 'source', source_name: 'available', book_url: 'url' } as Model<'BookSourceMapping'>])
  const progress = vi.spyOn(api, 'progress').mockResolvedValue({ version: 1 } as Model<'Progress'>)
  vi.spyOn(api, 'chapters').mockResolvedValue({} as Model<'ChapterList'>)
  const switchSource = vi.spyOn(api, 'switchSource').mockResolvedValue({ source_id: 'source', chapter_index: 0, confidence: 1 } as Model<'SwitchSourceResult'>)
  await child.value!.open(); await nextTick()
  host.querySelector<HTMLElement>('[data-row="0"]')!.click()
  await nextTick()
  const button = (label: string) => [...host.querySelectorAll('button')].find(b => b.textContent?.includes(label))!
  await vi.waitFor(() => expect(button('确认切换')).toBeDefined())
  const pending = held<Model<'Progress'>>(); progress.mockReturnValueOnce(pending.promise)
  button('确认切换').click(); await nextTick(); book.value = 'second'; await nextTick()
  pending.resolve({ version: 1 } as Model<'Progress'>); await nextTick(); await nextTick()
  expect(switchSource).toHaveBeenCalledTimes(1); expect(switchSource.mock.calls[0]![1].dry_run).toBe(true)
})

it('does not reuse the opened account after session loss', async () => {
  vi.spyOn(api, 'mappings').mockResolvedValue([])
  const progress = vi.spyOn(api, 'progress').mockResolvedValue({ version: 1 } as Model<'Progress'>)
  await child.value!.open(); setSession(null)
  await child.value!.open()
  expect(progress).toHaveBeenCalledTimes(1)
  expect(host.querySelector('dialog')!.open).toBe(false)
})

it('discovers standby sources via search refresh and binds mapping before preview', async () => {
  app.unmount()
  app = createApp({ render: () => h(SourceSwitcher, { bookId: book.value, bookName: '测试小说', ref: child }) })
  app.mount(host)

  vi.spyOn(api, 'mappings').mockResolvedValue([])
  vi.spyOn(api, 'progress').mockResolvedValue({ version: 1 } as Model<'Progress'>)
  vi.spyOn(api, 'search').mockResolvedValue({
    keyword: '测试小说',
    total: 1,
    failures: [],
    groups: [{
      source_id: 'src-new',
      source_name: '发现源A',
      elapsed_ms: 120,
      items: [{ source_id: 'src-new', book_url: 'https://a.example/1', name: '测试小说', latest_chapter: '第10章' }],
    }],
  } as Model<'SearchResponse'>)
  const addMapping = vi.spyOn(api, 'addMapping').mockResolvedValue({
    id: 'm1', source_id: 'src-new', source_name: '发现源A', book_url: 'https://a.example/1', sync_status: 'pending',
  } as Model<'BookSourceMapping'>)
  vi.spyOn(api, 'chapters').mockResolvedValue({} as Model<'ChapterList'>)
  vi.spyOn(api, 'switchSource').mockResolvedValue({ source_id: 'src-new', chapter_index: 2, confidence: 0.9 } as Model<'SwitchSourceResult'>)

  await child.value!.open(); await nextTick()
  const button = (label: string) => [...host.querySelectorAll('button')].find(b => b.textContent?.includes(label))!
  button('刷新书源').click()
  await vi.waitFor(() => expect(host.textContent).toContain('发现源A'))
  host.querySelector<HTMLElement>('[data-row="0"]')!.click()
  await nextTick()
  await vi.waitFor(() => expect(addMapping).toHaveBeenCalledWith('first', { source_id: 'src-new', book_url: 'https://a.example/1' }))
})

it('shows the current source name and configured host without the candidate book URL', async () => {
  app.unmount()
  app = createApp({ render: () => h(SourceSwitcher, { bookId: book.value, activeSource: 'active', ref: child }) })
  app.mount(host)
  vi.spyOn(api, 'mappings').mockResolvedValue([
    { source_id: 'active', source_name: '正在使用的源', book_url: 'https://active.example/book/1', total_chapters: 10 },
    { source_id: 'backup', source_name: '备用源', source_host: 'root.example', book_url: 'https://cdn.example/private/path', latest_chapter_title: '尾声', total_chapters: 12 },
  ] as Model<'BookSourceMapping'>[])
  vi.spyOn(api, 'progress').mockResolvedValue({ version: 1 } as Model<'Progress'>)
  await child.value!.open()
  await nextTick()
  expect(host.textContent).toContain('当前源：正在使用的源')
  const rows = host.querySelectorAll('button[data-row]')
  expect(rows[0]?.querySelector('.source-primary')?.textContent).toContain('当前源✓')
  expect(rows[0]?.querySelector('.source-secondary')?.textContent).toContain('落后 2 章')
  expect(rows[1]?.querySelector('.source-name')?.textContent).toBe('备用源')
  expect(rows[1]?.querySelector('.source-host')?.textContent).toBe('root.example')
  expect(rows[1]?.querySelector('.source-badge')?.textContent).toBe('最新')
  expect(rows[1]?.querySelector('.source-secondary .source-chapter')?.textContent).toBe('尾声')
  expect(host.textContent).not.toContain('private/path')
  expect(host.textContent).not.toContain('cdn.example')
})

it('renders partial groups, sorts by latency and latest chapter, and cancels on stop or source selection', async () => {
  app.unmount()
  app = createApp({ render: () => h(SourceSwitcher, { bookId: book.value, bookName: '测试小说', ref: child }) })
  app.mount(host)
  vi.spyOn(api, 'mappings').mockResolvedValue([])
  vi.spyOn(api, 'progress').mockResolvedValue({ version: 1 } as Model<'Progress'>)
  const searches = [held<Model<'SearchResponse'>>(), held<Model<'SearchResponse'>>()]
  const search = vi.spyOn(api, 'search').mockReturnValueOnce(searches[0]!.promise).mockReturnValueOnce(searches[1]!.promise)
  const addMapping = vi.spyOn(api, 'addMapping').mockResolvedValue({ source_id: 'fast' } as Model<'BookSourceMapping'>)
  vi.spyOn(api, 'chapters').mockResolvedValue({} as Model<'ChapterList'>)
  vi.spyOn(api, 'switchSource').mockResolvedValue({ source_id: 'fast', chapter_index: 0, confidence: 1 } as Model<'SwitchSourceResult'>)

  const slow = { source_id: 'slow', source_name: '慢源', source_host: 'slow.example', elapsed_ms: 4200,
    items: [{ source_id: 'slow', name: '测试小说', book_url: 'https://slow.example/book', latest_chapter: '第150章 新进展' }] }
  const fast = { source_id: 'fast', source_name: '快源', source_host: 'fast.example', elapsed_ms: 659,
    items: [{ source_id: 'fast', name: '测试小说', book_url: 'https://fast.example/book', toc_url: 'https://fast.example/toc', latest_chapter: '第120章 很长的章节标题' }] }
  const button = (label: string) => [...host.querySelectorAll('button')].find(node => node.textContent?.trim() === label)!
  const results = () => [...host.querySelectorAll('.source-name')].map(node => node.textContent)
  await child.value!.open()
  button('刷新书源').click()
  const [, firstSignal, firstGroup, probe] = search.mock.calls[0]!
  firstGroup!(slow)
  probe!(2)
  await nextTick()
  expect(results()).toEqual(['慢源'])
  expect(host.querySelector('.probe-count')?.textContent).toBe('已探 2 源')
  expect(host.querySelector('.scan-footer')).not.toBeNull()
  firstGroup!(fast)
  await nextTick()
  expect(host.querySelector('.source-latency.fast')?.textContent).toBe('659ms')
  expect(host.querySelector('.source-latency.slow')?.textContent).toBe('4200ms')
  expect(host.querySelector('.source-counts')?.textContent).toBe('快 1 · 慢 1 · 共 2 源')
  button('最快').click()
  await nextTick()
  expect(results()).toEqual(['快源', '慢源'])
  button('最新').click()
  await nextTick()
  expect(results()).toEqual(['慢源', '快源'])
  button('停止').click()
  await nextTick()
  expect(firstSignal?.aborted).toBe(true)
  expect(host.querySelector('.scan-footer')).toBeNull()
  expect(results()).toEqual(['慢源', '快源'])

  button('刷新书源').click()
  const [, secondSignal, secondGroup] = search.mock.calls[1]!
  firstGroup!(slow)
  secondGroup!(fast)
  await nextTick()
  expect(results()).toEqual(['快源'])
  host.querySelector<HTMLButtonElement>('button[data-row]')!.click()
  expect(secondSignal?.aborted).toBe(true)
  await vi.waitFor(() => expect(button('确认切换并清除单章覆盖')).toBeDefined())
  expect(addMapping).toHaveBeenCalledWith('first', { source_id: 'fast', book_url: 'https://fast.example/book', toc_url: 'https://fast.example/toc' })
  for (const pending of searches) pending.resolve({ keyword: '测试小说', total: 0, groups: [], failures: [] })
})
