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
  button('预览切换').click(); await vi.waitFor(() => expect(button('确认切换')).toBeDefined())
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
