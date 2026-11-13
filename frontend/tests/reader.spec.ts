import { test, expect, type Page } from '@playwright/test'
import { mkdir } from 'node:fs/promises'
const username = process.env.E2E_USERNAME, password = process.env.E2E_PASSWORD
if (process.env.CI && (!username || !password)) throw new Error('Isolated E2E credentials are required in CI')
test.skip(!username || !password, 'Set E2E_USERNAME/E2E_PASSWORD for an isolated test backend')
async function login(page: Page) {
  await page.goto('/bookshelf'); await expect(page).toHaveURL(/login/)
  await page.getByLabel('用户名', { exact: true }).fill(username!); await page.getByLabel('密码', { exact: true }).fill(password!); await page.getByRole('button', { name: '登录书房' }).click(); await expect(page).toHaveURL(/bookshelf/)
}
async function auth(page: Page) { return page.evaluate(() => JSON.parse(sessionStorage.getItem('yreader-session')!)) }
async function progress(page: Page, bookId: string) { const s = await auth(page); const r = await page.request.get(`/api/v1/books/${bookId}/progress`, { headers: { Authorization: `Bearer ${s.access_token}` } }); expect(r.ok()).toBeTruthy(); return r.json() }
async function countCache(page: Page) { return page.evaluate(() => new Promise<number>((resolve, reject) => { const req = indexedDB.open('yreader-offline-v1'); req.onerror = () => reject(req.error); req.onsuccess = () => { const d = req.result, tx = d.transaction('chapters'), count = tx.objectStore('chapters').count(); count.onsuccess = () => { resolve(count.result); d.close() } } })) }
test('local import, explicit conflict resolution, exact restore, offline chapters and logout isolation', async ({ page }) => {
  await mkdir('.test-artifacts', { recursive: true }); await login(page)
  const title = `山间来信·${Date.now().toString().slice(-6)}`
  const text = ['一','二','三','四','五'].map((n, i) => `第${n}章 故事${i + 1}\n` + Array.from({ length: 100 }, (_, line) => `第${line + 1}段：山间的风吹过书页，读者继续旅行。😀 这是第${i + 1}章的正文。${title}`).join('\n\n')).join('\n\n')
  await page.getByLabel('导入本地书', { exact: true }).setInputFiles({ name: `${title}.txt`, mimeType: 'text/plain', buffer: Buffer.from(text) })
  const card = page.locator('.book-card').filter({ has: page.getByRole('heading', { name: title, exact: true }) })
  await expect(card).toBeVisible(); await page.screenshot({ path: '.test-artifacts/library-desktop.png', fullPage: true })
  await card.getByRole('button', { name: '开始阅读' }).click(); await expect(page.locator('.reader-kicker')).toContainText('第 1 章')
  const bookId = page.url().split('/reader/')[1]!
  await expect(page.getByLabel('章节阅读区')).toHaveAttribute('aria-busy', 'false')
  const p = await progress(page, bookId), s = await auth(page)
  const remote = await page.request.put(`/api/v1/books/${bookId}/progress`, { headers: { Authorization: `Bearer ${s.access_token}`, 'If-Match': `"${p.version}"` }, data: { chapter_index: 0, char_offset: 10 } }); expect(remote.ok()).toBeTruthy()
  await page.getByLabel('章节阅读区').evaluate(el => { el.scrollTop = 900 })
  await expect(page.locator('.reader-conflict')).toBeVisible(); expect((await progress(page, bookId)).char_offset).toBe(10)
  await page.reload(); await expect(page.locator('.reader-conflict')).toBeVisible(); expect((await progress(page, bookId)).char_offset).toBe(10)
  await page.getByRole('button', { name: '保留本机位置' }).click(); await expect(page.locator('.reader-conflict')).toBeHidden()
  await expect.poll(async () => (await progress(page, bookId)).char_offset).toBeGreaterThan(10); expect((await progress(page, bookId)).chapter_ratio).toBeGreaterThan(0)
  await page.getByLabel('阅读主题').selectOption('night'); await page.screenshot({ path: '.test-artifacts/reader-desktop.png', fullPage: true })
  await page.getByRole('link', { name: '返回书架' }).click(); await expect(card).toBeVisible(); await card.getByRole('button', { name: '继续阅读' }).click()
  await expect.poll(() => page.getByLabel('章节阅读区').evaluate(el => el.scrollTop)).toBeGreaterThan(100)
  await page.getByRole('button', { name: '下一章', exact: true }).click(); await expect(page.locator('.reader-kicker')).toContainText('第 2 章')
  await expect.poll(() => countCache(page)).toBeGreaterThanOrEqual(4)
  await page.context().setOffline(true); await page.getByRole('button', { name: '下一章', exact: true }).click(); await expect(page.locator('.reader-kicker')).toContainText('离线缓存'); await expect(page.locator('.reader-kicker')).toContainText('第 3 章')
  await page.setViewportSize({ width: 390, height: 844 }); await page.screenshot({ path: '.test-artifacts/reader-mobile.png', fullPage: true }); expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBeTruthy()
  await page.context().setOffline(false); await expect.poll(async () => (await progress(page, bookId)).chapter_index).toBe(2)
  await page.getByRole('link', { name: '返回书架' }).click(); await page.getByRole('button', { name: '退出', exact: true }).click(); await expect(page).toHaveURL(/login/); expect(await page.evaluate(() => sessionStorage.getItem('yreader-session'))).toBeNull(); await expect.poll(() => countCache(page)).toBe(0)
})
test('source import preview, partial search failure and adding a search result to bookshelf', async ({ page }) => {
  await login(page); await page.getByRole('link', { name: '书源', exact: true }).click()
  const bookName = `山海之间${Date.now()}`, sourceName = `演示书源${Date.now()}`, sourceURL = `https://source-${Date.now()}.example.com`
  await page.getByLabel('书源 JSON', { exact: true }).fill(JSON.stringify([{ bookSourceUrl: sourceURL, bookSourceName: sourceName, enabled: true, searchUrl: '/search?key={{key}}', ruleSearch: { bookList: 'a', name: 'text', bookUrl: 'href' }, ruleToc: { chapterList: 'a', chapterName: 'text', chapterUrl: 'href' }, ruleContent: { content: 'body@text' } }]))
  await expect(page.getByRole('button', { name: '确认导入' })).toBeDisabled(); await page.getByRole('button', { name: '预检', exact: true }).click(); await expect(page.getByText('预检：', { exact: false })).toBeVisible(); await page.getByRole('button', { name: '确认导入' }).click(); await expect(page.getByText('已导入 1，跳过 0，失败 0。', { exact: true })).toBeVisible()
  const s = await auth(page), sources = await page.request.get('/api/v1/sources?page_size=100', { headers: { Authorization: `Bearer ${s.access_token}` } }), data = await sources.json(), source = data.items.find((x: { book_source_name: string }) => x.book_source_name === sourceName)
  expect(source).toBeTruthy()
  // Search transport is mocked; import and add-book still hit the real isolated backend.
  await page.route('**/api/v1/search?*', route => route.fulfill({ json: { keyword: '山海', total: 1, groups: [{ source_id: source.id, source_name: sourceName, items: [{ source_id: source.id, book_url: `${sourceURL}/book/1`, name: bookName, author: '测试作者' }] }], failures: [{ source_id: '00000000-0000-0000-0000-000000000009', code: 'source_request_failed', message: '另一书源暂时不可用' }] } }))
  await page.getByRole('link', { name: '书架', exact: true }).click(); await page.getByRole('tab', { name: '发现新书' }).click(); await page.getByLabel('搜索书籍', { exact: true }).fill('山海'); await page.getByRole('button', { name: '搜索', exact: true }).click(); await expect(page.getByText('部分书源搜索失败：另一书源暂时不可用')).toBeVisible(); await page.getByRole('button', { name: '加入书架', exact: true }).click(); await expect(page.getByText(`《${bookName}》已加入书架`)).toBeVisible(); await page.getByRole('tab', { name: '我的书架' }).click(); await expect(page.getByRole('heading', { name: bookName, exact: true })).toBeVisible()
  const shelfResponse = await page.request.get('/api/v1/books?page_size=100', { headers: { Authorization: `Bearer ${s.access_token}` } }), shelfData = await shelfResponse.json(), onlineBook = shelfData.items.find((b: { name: string }) => b.name === bookName)
  await page.route(`**/api/v1/books/${onlineBook.id}/chapters?*`, route => route.fulfill({ json: { book_id: onlineBook.id, source_id: source.id, total: 2, items: [0, 1].map(i => ({ id: i + 1, chapter_index: i, sort_order: i, title: `在线第${i + 1}章`, normalized_title: `n:${i}`, url: `${sourceURL}/chapter/${i}`, is_volume: false, word_count: 10 })) } }))
  await page.route(`**/api/v1/books/${onlineBook.id}/content?*`, route => { const i = Number(new URL(route.request().url()).searchParams.get('chapter_index')); return route.fulfill({ json: { chapter: { id: i + 1, chapter_index: i, sort_order: i, title: `在线第${i + 1}章`, normalized_title: `n:${i}`, url: `${sourceURL}/chapter/${i}`, is_volume: false, word_count: 10 }, content: `线上正文${i + 1}`, cached: false, word_count: 10, source_id: source.id, prev_chapter_index: i === 1 ? 0 : null, next_chapter_index: i === 0 ? 1 : null } }) })
  await page.locator('.book-card').filter({ has: page.getByRole('heading', { name: bookName, exact: true }) }).getByRole('button', { name: '开始阅读' }).click(); await expect(page.getByRole('heading', { name: '在线第1章', exact: true })).toBeVisible(); await page.getByRole('button', { name: '下一章', exact: true }).click(); await expect(page.getByRole('heading', { name: '在线第2章', exact: true })).toBeVisible()

})


test('invalid credentials and a virtualized thousand-row reader directory', async ({ page }) => {
  await page.goto('/login'); await page.getByLabel('用户名', { exact: true }).fill(username!); await page.getByLabel('密码', { exact: true }).fill('incorrect-test-password'); await page.getByRole('button', { name: '登录书房' }).click(); await expect(page.getByText('用户名或密码不正确')).toBeVisible()
  await page.getByLabel('密码', { exact: true }).fill(password!); await page.getByRole('button', { name: '登录书房' }).click(); await expect(page).toHaveURL(/bookshelf/)
  const s = await auth(page), r = await page.request.get('/api/v1/books?page_size=100', { headers: { Authorization: `Bearer ${s.access_token}` } }), shelf = await r.json(), book = shelf.items.find((b: { book_type: string }) => b.book_type === 'local')
  expect(book).toBeTruthy()
  await page.route(`**/api/v1/books/${book.id}/chapters?*`, route => route.fulfill({ json: { book_id: book.id, source_id: book.active_source_id, total: 1000, items: Array.from({ length: 1000 }, (_, i) => ({ id: i + 1, chapter_index: i, sort_order: i, title: `第${i + 1}章 目录测试`, normalized_title: `n:${i + 1}`, url: `local://chapter/${i}`, is_volume: false, word_count: 100 })) } }))
  await page.goto(`/reader/${book.id}`); await expect(page.getByLabel('章节阅读区')).toHaveAttribute('aria-busy', 'false'); await page.getByRole('button', { name: '目录', exact: true }).click(); const directory = page.getByRole('dialog'); await expect(directory).toBeVisible(); expect(await directory.locator('.virtual-list button').count()).toBeLessThanOrEqual(13)
  await directory.locator('.virtual-list').evaluate(el => { el.scrollTop = 44 * 990 }); await expect(directory.getByRole('button', { name: '第991章 目录测试', exact: true })).toBeVisible(); await directory.getByRole('button', { name: '关闭', exact: true }).click(); await expect(directory).toBeHidden()
})
