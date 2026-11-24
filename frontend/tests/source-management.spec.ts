import { execFileSync } from 'node:child_process'
import { test, expect, type Page } from '@playwright/test'
const username = process.env.E2E_USERNAME, password = process.env.E2E_PASSWORD
test.skip(!username || !password, 'Isolated backend credentials required')
async function login(page: Page) { await page.goto('/login'); await page.getByLabel('用户名', { exact: true }).fill(username!); await page.getByLabel('密码', { exact: true }).fill(password!); await page.getByRole('button', { name: '登录书房' }).click(); await expect(page).toHaveURL(/bookshelf/); const session = await page.evaluate(() => JSON.parse(sessionStorage.getItem('yreader-session')!)); return { Authorization: `Bearer ${session.access_token}` } }
async function createSource(page: Page, headers: Record<string, string>, name: string) { const r = await page.request.post('/api/v1/sources', { headers, data: { book_source_name: name, book_source_url: `https://source-${Date.now()}-${Math.random().toString(16).slice(2)}.example.com`, book_source_group: '测试', enabled: true, rules: { future: { value: 7 } } } }); expect(r.ok()).toBeTruthy(); return r.json() }
test('source editing keeps unknown rules, rejects stale versions, debugs sample input and confirms deletion', async ({ page }) => {
  const headers = await login(page), source = await createSource(page, headers, `编辑测试${Date.now()}`)
  await page.goto(`/admin/sources/${source.id}`); await expect(page.getByLabel('书源名称', { exact: true })).toHaveValue(source.book_source_name)
  await page.getByLabel('书源名称', { exact: true }).fill('已编辑书源'); await page.getByRole('button', { name: '保存修改' }).click(); await expect(page.getByText('书源已保存', { exact: true })).toBeVisible()
  let current = await (await page.request.get(`/api/v1/sources/${source.id}`, { headers })).json(); expect(current.rules.future).toEqual({ value: 7 })
  const external = await page.request.put(`/api/v1/sources/${source.id}`, { headers, data: { version: current.version, book_source_group: '另一端分组' } }); expect(external.ok()).toBeTruthy()
  await page.getByLabel('书源名称', { exact: true }).fill('尚未保存的草稿'); await page.getByRole('button', { name: '保存修改' }).click(); await expect(page.getByText('书源已被其他操作修改，草稿已保留；请重新加载后合并修改')).toBeVisible(); await expect(page.getByLabel('书源名称', { exact: true })).toHaveValue('尚未保存的草稿')
  await page.getByRole('button', { name: '重新加载', exact: true }).click(); await page.getByRole('button', { name: '丢弃草稿并重新加载', exact: true }).click(); await expect(page.getByLabel('书源分组', { exact: true })).toHaveValue('另一端分组')
  await page.getByLabel('调试规则', { exact: true }).fill('h1@text'); await page.getByLabel('输入样本', { exact: true }).fill('<main><h1>调试正文</h1></main>'); await page.getByRole('button', { name: '运行调试' }).click(); await expect(page.locator('.debug-output')).toContainText('调试正文'); await page.screenshot({ path: '.test-artifacts/source-editor.png', fullPage: true })
  await page.getByRole('button', { name: '删除书源', exact: true }).click(); await page.getByRole('button', { name: '取消', exact: true }).click(); expect((await page.request.get(`/api/v1/sources/${source.id}`, { headers })).ok()).toBeTruthy()
  await page.getByRole('button', { name: '删除书源', exact: true }).click(); await page.getByRole('button', { name: '确认删除', exact: true }).click(); await expect(page).toHaveURL(/\/admin$/); expect((await page.request.get(`/api/v1/sources/${source.id}`, { headers })).status()).toBe(404)
})
test('switch preview does not mutate active source and confirmed switch invalidates old cached chapters', async ({ page }) => {
  const headers = await login(page), a = await createSource(page, headers, '源A'), b = await createSource(page, headers, '备用源B')
  const r = await page.request.post('/api/v1/books', { headers, data: { source_id: a.id, name: `换源测试${Date.now()}`, author: '测试作者', book_url: a.book_source_url + '/book' } }); expect(r.ok()).toBeTruthy(); const book = await r.json()
  const mapping = await page.request.post(`/api/v1/books/${book.id}/mappings`, { headers, data: { source_id: b.id, book_url: b.book_source_url + '/book' } }); expect(mapping.ok()).toBeTruthy()
  const sql = `INSERT INTO chapters(book_id,source_id,chapter_index,sort_order,title,normalized_title,url,url_hash) SELECT '${book.id}'::uuid,s,i,i,'第'||(i+1)||'章','n:'||(i+1),'https://example.com/'||i,encode(sha256(i::text::bytea),'hex') FROM unnest(ARRAY['${a.id}'::uuid,'${b.id}'::uuid]) s CROSS JOIN generate_series(0,1) i; UPDATE books SET total_chapters=2 WHERE id='${book.id}'::uuid;`
  if (process.env.E2E_DB_CONTAINER) execFileSync('docker', ['exec','-i',process.env.E2E_DB_CONTAINER,'psql','-U',process.env.E2E_DB_USER ?? 'yreader','-d',process.env.E2E_DB_NAME ?? 'yreader','-v','ON_ERROR_STOP=1'], { input: sql })
  else if (process.env.E2E_DATABASE_URL) execFileSync(process.env.E2E_PSQL ?? 'psql', ['-d',process.env.E2E_DATABASE_URL,'-v','ON_ERROR_STOP=1'], { input: sql })
  else throw new Error('Switch integration tests require an isolated E2E_DB_CONTAINER or E2E_DATABASE_URL')
  await page.route(`**/api/v1/books/${book.id}/chapters?*`, async route => { const requested = new URL(route.request().url()).searchParams.get('source_id'); const current = await (await page.request.get(`/api/v1/books/${book.id}`, { headers })).json(); return route.fulfill({ json: { book_id: book.id, source_id: requested ?? current.active_source_id, total: 2, items: [0,1].map(i => ({ id: i + 1, chapter_index: i, sort_order: i, title: `第${i + 1}章`, normalized_title: `n:${i + 1}`, url: 'https://example.com/chapter', is_volume: false, word_count: 10 })) } }) })
  await page.route(`**/api/v1/books/${book.id}/content?*`, async route => { const i = Number(new URL(route.request().url()).searchParams.get('chapter_index')), current = await (await page.request.get(`/api/v1/books/${book.id}`, { headers })).json(); return route.fulfill({ json: { chapter: { id: i + 1, chapter_index: i, sort_order: i, title: `第${i + 1}章`, normalized_title: `n:${i + 1}`, url: 'https://example.com/chapter', is_volume: false, word_count: 10 }, content: current.active_source_id === a.id ? '源A正文' : '源B正文', source_id: current.active_source_id, cached: false, word_count: 10, prev_chapter_index: i ? 0 : null, next_chapter_index: i ? null : 1 } }) })
  await page.goto(`/reader/${book.id}`); await expect(page.getByLabel('章节阅读区')).toHaveAttribute('aria-busy','false'); await expect(page.locator('.chapter-body')).toHaveText('源A正文'); await page.getByRole('button', { name: '换源', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: '切换书源' })
  const sources = dialog.getByRole('list', { name: '换源候选列表' })
  const cachedSources = () => page.evaluate(bookId => new Promise<string[]>((resolve, reject) => {
    const request = indexedDB.open('yreader-offline-v1')
    request.onerror = () => reject(request.error)
    request.onsuccess = () => {
      const db = request.result
      const read = db.transaction('chapters').objectStore('chapters').getAll()
      read.onerror = () => { db.close(); reject(read.error) }
      read.onsuccess = () => {
        db.close()
        resolve(read.result.filter(chapter => chapter.bookId === bookId).map(chapter => chapter.content.source_id))
      }
    }
  }), book.id)
  await expect.poll(cachedSources).toContain(a.id)
  await expect(dialog).toBeVisible()
  await sources.getByRole('button', { name: /备用源B.*预览切换/ }).click()
  await expect(dialog.getByText('定位策略：exact', { exact: true })).toBeVisible()
  await expect(dialog.getByText('当前源：源A', { exact: true })).toBeVisible()
  await page.screenshot({ path: '.test-artifacts/source-switch-preview.png', fullPage: true }); let current = await (await page.request.get(`/api/v1/books/${book.id}`, { headers })).json(); expect(current.active_source_id).toBe(a.id)
  await dialog.getByRole('button', { name: '确认切换并清除单章覆盖', exact: true }).click(); await expect(dialog).toBeHidden(); await expect(page.locator('.chapter-body')).toHaveText('源B正文'); current = await (await page.request.get(`/api/v1/books/${book.id}`, { headers })).json(); expect(current.active_source_id).toBe(b.id)
  expect(await cachedSources()).not.toContain(a.id)
  await page.route(`**/api/v1/books/${book.id}/chapters?*`, async route => { const requested = new URL(route.request().url()).searchParams.get('source_id'); if (!requested) return route.fulfill({ status: 502, json: { error: { code: 'source_request_failed' } } }); return route.fallback() })
  // A recoverable TOC failure now uses the matching cached directory. Prove
  // the injected failure occurred without requiring the obsolete fatal alert.
  const failedTOC = page.waitForResponse(r => r.url().includes(`/books/${book.id}/chapters?`) && r.status() === 502)
  await page.reload(); await failedTOC
  await expect(page.locator('.chapter-body')).toHaveText('源B正文')
  await page.getByRole('button', { name: '换源', exact: true }).click()
  await expect(sources.getByRole('button', { name: /备用源B.*当前源/ })).toBeDisabled()
  await expect(sources.getByRole('button', { name: /源A.*预览切换/ })).toBeEnabled()

})
