import { test, expect } from '@playwright/test'
import { mkdir } from 'node:fs/promises'
const username = process.env.E2E_USERNAME, password = process.env.E2E_PASSWORD
test.skip(!username || !password, 'Isolated backend credentials required')

test('bookmarks preserve Unicode positions and notes, survive reload, navigate and require deletion confirmation', async ({ page }) => {
  await page.goto('/login'); await page.getByLabel('用户名', { exact: true }).fill(username!); await page.getByLabel('密码', { exact: true }).fill(password!); await page.getByRole('button', { name: '登录书房' }).click(); await expect(page).toHaveURL(/bookshelf/)
  const title = `书签验证${Date.now()}`
  const text = ['一', '二'].map(n => `第${n}章 书签旅程\n` + Array.from({ length: 100 }, (_, i) => `第${i + 1}段：书页间留下位置。😀 以后继续读这一段。${title}`).join('\n\n')).join('\n\n')
  await page.getByLabel('导入本地书', { exact: true }).setInputFiles({ name: `${title}.txt`, mimeType: 'text/plain', buffer: Buffer.from(text) })
  await page.locator('.book-card').filter({ has: page.getByRole('heading', { name: title, exact: true }) }).getByRole('button', { name: '开始阅读' }).click()
  await expect(page).toHaveURL(/\/reader\/[^/]+$/)
  const bookId = page.url().split('/reader/')[1]!
  const session = await page.evaluate(() => JSON.parse(sessionStorage.getItem('yreader-session')!)), headers = { Authorization: `Bearer ${session.access_token}` }
  const list = async () => { const r = await page.request.get(`/api/v1/books/${bookId}/bookmarks`, { headers }); expect(r.ok()).toBeTruthy(); return r.json() }
  await expect(page.getByLabel('章节阅读区')).toHaveAttribute('aria-busy', 'false'); await page.getByLabel('章节阅读区').evaluate(el => { el.scrollTop = 950 }); await page.getByRole('button', { name: '保存进度', exact: true }).click()
  await page.getByRole('button', { name: '书签', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: '阅读书签' }), note = '留给明天😀 <script>window.bookmarkInjected=true</script>'
  await expect(dialog.getByText('还没有书签。')).toBeVisible(); await dialog.getByLabel('书签备注（可选）').fill(note)
  // A transport failure must leave the draft intact and allow an explicit retry.
  await page.route(`**/api/v1/books/${bookId}/bookmarks`, async route => { if (route.request().method() === 'POST') { await route.fulfill({ status: 503, json: { error: { code: 'temporarily_unavailable' } } }); await page.unroute(`**/api/v1/books/${bookId}/bookmarks`) } else await route.continue() })
  await dialog.getByRole('button', { name: '保存当前位置书签' }).click(); await expect(dialog.getByRole('alert')).toContainText('temporarily_unavailable'); await expect(dialog.getByLabel('书签备注（可选）')).toHaveValue(note)
  await dialog.getByRole('button', { name: '保存当前位置书签' }).click(); await expect(dialog.getByText('书签已保存', { exact: true })).toBeVisible()
  const saved = (await list())[0]; expect(saved.chapter_index).toBe(0); expect(saved.char_offset).toBeGreaterThan(0); expect(saved.note).toBe(note)
  const body = await page.locator('.chapter-body').textContent(); expect(saved.excerpt).toBe(Array.from(body!).slice(saved.char_offset, saved.char_offset + 120).join(''))
  expect(await page.evaluate(() => 'bookmarkInjected' in window)).toBe(false)
  await dialog.getByLabel('书签备注（可选）').fill('不应覆盖的草稿'); await dialog.getByRole('button', { name: '保存当前位置书签' }).click(); await expect(dialog.getByRole('alert')).toContainText('此位置已有书签'); await expect(dialog.getByLabel('书签备注（可选）')).toHaveValue('不应覆盖的草稿'); expect((await list())[0].note).toBe(note)
  await dialog.getByRole('button', { name: '关闭', exact: true }).click()
  const nextSave = page.waitForResponse(r => r.request().method() === 'PUT' && r.url().endsWith(`/books/${bookId}/progress`) && r.request().postDataJSON().chapter_index === 1)
  await page.getByRole('button', { name: '下一章', exact: true }).click(); expect((await nextSave).ok()).toBeTruthy(); await expect(page.locator('.reader-kicker')).toContainText('第 2 章'); await expect(page.locator('.reader-status')).toHaveText('进度已同步'); await page.reload(); await expect(page.getByLabel('章节阅读区')).toHaveAttribute('aria-busy', 'false')
  await page.getByRole('button', { name: '书签', exact: true }).click(); await dialog.locator('.virtual-list button').click(); await expect(dialog.getByLabel('书签详情')).toContainText(note)
  await page.setViewportSize({ width: 390, height: 844 }); await mkdir('.test-artifacts', { recursive: true }); await page.screenshot({ path: '.test-artifacts/bookmarks-mobile.png', fullPage: true })
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  await dialog.getByRole('button', { name: '跳到书签位置' }).click(); await expect(dialog).toBeHidden(); await expect(page.locator('.reader-kicker')).toContainText('第 1 章')
  await expect.poll(async () => { const r = await page.request.get(`/api/v1/books/${bookId}/progress`, { headers }); return (await r.json()).char_offset }).toBe(saved.char_offset)
  expect(await page.getByLabel('章节阅读区').evaluate(el => el.scrollTop)).toBeGreaterThan(500)
  await page.getByRole('button', { name: '书签', exact: true }).click(); await dialog.locator('.virtual-list button').click(); await dialog.getByRole('button', { name: '删除书签', exact: true }).click(); await dialog.getByRole('button', { name: '取消删除' }).click(); expect(await list()).toHaveLength(1)
  await dialog.getByRole('button', { name: '删除书签', exact: true }).click(); await dialog.getByRole('button', { name: '确认删除书签', exact: true }).click(); await expect(dialog.getByText('还没有书签。')).toBeVisible(); expect(await list()).toEqual([])
  // Long lists stay virtualized; an obsolete chapter cannot be opened silently.
  await page.route(`**/api/v1/books/${bookId}/bookmarks`, route => route.fulfill({ json: Array.from({ length: 125 }, (_, i) => ({ ...saved, id: `fixture-${i}`, chapter_index: i, chapter_title: `样本第${i + 1}章`, note: '' })) }))
  await dialog.getByRole('button', { name: '刷新书签' }).click(); await expect(dialog.getByRole('heading', { name: '已保存书签（125）' })).toBeVisible(); expect(await dialog.locator('.virtual-list button').count()).toBeLessThanOrEqual(13)
  await dialog.locator('.virtual-list').evaluate(el => { el.scrollTop = 44 * 120 }); await dialog.locator('.virtual-list button').filter({ hasText: '样本第121章' }).click(); await expect(dialog.getByRole('button', { name: '跳到书签位置' })).toBeDisabled(); await expect(dialog.getByRole('alert')).toContainText('此章节已不在当前目录中')
})
