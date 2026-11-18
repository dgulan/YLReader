import { createApp, h, nextTick, ref, type App } from 'vue'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { api } from '@/api/reader'
import { setSession, type Model } from '@/api/client'
import ChapterOverrideDialog from './ChapterOverrideDialog.vue'

let app: App
let host: HTMLDivElement
const bookId = ref('book-1')
const isOverridden = ref(false)
const updatedEvents: number[] = []
const child = ref<InstanceType<typeof ChapterOverrideDialog>>()

if (!HTMLDialogElement.prototype.showModal) {
  Object.defineProperty(HTMLDialogElement.prototype, 'showModal', { configurable: true, writable: true, value() {} })
}
if (!HTMLDialogElement.prototype.close) {
  Object.defineProperty(HTMLDialogElement.prototype, 'close', { configurable: true, writable: true, value() {} })
}

beforeEach(() => {
  bookId.value = 'book-1'
  isOverridden.value = false
  updatedEvents.length = 0
  setSession({
    access_token: 'a',
    refresh_token: 'r',
    token_type: 'Bearer',
    expires_in: 900,
    user: { id: 'u1', username: 'reader', role: 'user', token_version: 1, created_at: '' },
  })
  vi.spyOn(HTMLDialogElement.prototype, 'showModal').mockImplementation(function (this: HTMLDialogElement) {
    this.open = true
  })
  vi.spyOn(HTMLDialogElement.prototype, 'close').mockImplementation(function (this: HTMLDialogElement) {
    this.open = false
  })
  host = document.createElement('div')
  document.body.append(host)
  app = createApp({
    render: () =>
      h(ChapterOverrideDialog, {
        ref: child,
        bookId: bookId.value,
        chapterIndex: 3,
        chapterTitle: '第四章 试抓测试',
        isOverridden: isOverridden.value,
        onUpdated: (idx: number) => updatedEvents.push(idx),
      }),
  })
  app.mount(host)
})

afterEach(() => {
  app?.unmount()
  host?.remove()
  setSession(null)
  vi.restoreAllMocks()
})

it('submits trial fetch override, displays content_preview, and clears override', async () => {
  vi.spyOn(api, 'sources').mockResolvedValue({
    items: [{ id: 's2', book_source_name: '备用源二', book_source_type: 0, enabled: true }],
    page: 1,
    page_size: 100,
    total: 1,
  } as Model<'SourcePage'>)
  const setSpy = vi.spyOn(api, 'setChapterOverride').mockResolvedValue({
    book_id: 'book-1',
    chapter_index: 3,
    source_id: 's2',
    source_name: '备用源二',
    chapter_url: 'https://s2.example.com/c/3',
    content_preview: '这是试抓回来的正文前512字符预览内容',
    status: 'active',
    created_at: '',
    updated_at: '',
  })
  const delSpy = vi.spyOn(api, 'deleteChapterOverride').mockResolvedValue(undefined)

  await child.value!.open()
  await nextTick()

  const input = host.querySelector<HTMLInputElement>('input[type="url"]')!
  input.value = 'https://s2.example.com/c/3'
  input.dispatchEvent(new Event('input'))
  const form = host.querySelector('form')!
  form.dispatchEvent(new Event('submit', { cancelable: true }))

  await vi.waitFor(() => expect(host.textContent).toContain('这是试抓回来的正文前512字符预览内容'))
  expect(setSpy).toHaveBeenCalledTimes(1)
  expect(setSpy).toHaveBeenCalledWith('book-1', 3, { chapter_url: 'https://s2.example.com/c/3' }, expect.any(AbortSignal))
  expect(updatedEvents).toEqual([3])

  isOverridden.value = true
  await nextTick()
  const clearBtn = [...host.querySelectorAll('button')].find(b => b.textContent?.includes('清除本章覆盖'))!
  clearBtn.click()
  await vi.waitFor(() => expect(delSpy).toHaveBeenCalledTimes(1))
  expect(delSpy).toHaveBeenCalledWith('book-1', 3, expect.any(AbortSignal))
  expect(updatedEvents).toEqual([3, 3])
})
