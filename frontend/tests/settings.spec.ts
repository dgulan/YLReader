import { test, expect } from '@playwright/test'
import { mkdir } from 'node:fs/promises'
const username = process.env.E2E_USERNAME, password = process.env.E2E_PASSWORD
test.skip(!username || !password, 'Isolated backend credentials required')

test('account reading settings persist across login, preserve other keys and retry offline changes', async ({ page }) => {
  async function login() { await page.goto('/login'); await page.getByLabel('用户名', { exact: true }).fill(username!); await page.getByLabel('密码', { exact: true }).fill(password!); await page.getByRole('button', { name: '登录书房' }).click(); await expect(page).toHaveURL(/bookshelf/) }
  await login()
  async function headers() { return page.evaluate(() => ({ Authorization: `Bearer ${JSON.parse(sessionStorage.getItem('yreader-session')!).access_token}` })) }
  async function settings() { const r = await page.request.get('/api/v1/settings', { headers: await headers() }); expect(r.ok()).toBeTruthy(); return r.json() }
  const seed = await page.request.patch('/api/v1/settings', { headers: await headers(), data: { 'reader.theme': 'paper', 'reader.font_size': 20, 'verification.unrelated': 'keep' } }); expect(seed.ok()).toBeTruthy()
  await page.getByRole('link', { name: '设置', exact: true }).click(); await page.getByRole('button', { name: '重新读取设置' }).click(); await expect(page.getByText('阅读设置已同步', { exact: true })).toBeVisible()
  await page.getByRole('combobox', { name: '阅读主题', exact: true }).press('ArrowDown'); await page.getByRole('option', { name: '夜间', exact: true }).click()
  await page.getByRole('spinbutton', { name: '正文字号' }).fill('26'); await page.getByRole('spinbutton', { name: '正文字号' }).press('Tab')
  await expect.poll(async () => (await settings())['reader.font_size']).toBe(26); expect((await settings())['verification.unrelated']).toBe('keep')
  await page.reload(); await expect(page.getByLabel('阅读外观预览')).toHaveAttribute('data-theme', 'night'); await expect(page.getByRole('spinbutton', { name: '正文字号' })).toHaveValue('26')
  await page.setViewportSize({ width: 390, height: 844 }); await mkdir('.test-artifacts', { recursive: true }); await page.screenshot({ path: '.test-artifacts/settings-mobile.png', fullPage: true }); expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  await page.getByRole('button', { name: '退出', exact: true }).click(); await expect(page).toHaveURL(/login/); await login(); await page.getByRole('link', { name: '设置', exact: true }).click(); await expect(page.getByLabel('阅读外观预览')).toHaveAttribute('data-theme', 'night'); await expect(page.getByRole('spinbutton', { name: '正文字号' })).toHaveValue('26')
  await page.route('**/api/v1/settings', route => route.abort('internetdisconnected'))
  await page.getByRole('spinbutton', { name: '正文字号' }).fill('28'); await page.getByRole('spinbutton', { name: '正文字号' }).press('Tab'); await expect(page.getByRole('alert')).toContainText('尚未同步')
  await page.reload(); await expect(page.getByRole('spinbutton', { name: '正文字号' })).toHaveValue('28'); await expect(page.getByRole('alert')).toContainText('尚未同步')
  await page.unroute('**/api/v1/settings'); await page.getByRole('button', { name: '重试设置同步' }).click(); await expect(page.getByText('阅读设置已同步', { exact: true })).toBeVisible(); expect((await settings())['reader.font_size']).toBe(28)
  const title = `阅读设置验证${Date.now()}`, text = `第一章 设置预览\n${Array.from({ length: 150 }, (_, i) => `第${i}段：调整字号后仍应读到同一处。😀 ${title}`).join('\n\n')}`
  const imported = await page.request.post(`/api/v1/books/import?filename=${encodeURIComponent(title + '.txt')}`, { headers: { ...await headers(), 'Content-Type': 'application/octet-stream' }, data: Buffer.from(text) }); expect(imported.ok()).toBeTruthy(); const book = (await imported.json()).book
  await page.goto(`/reader/${book.id}`); await expect(page.getByLabel('章节阅读区')).toHaveAttribute('aria-busy', 'false'); await expect(page.locator('.reader')).toHaveAttribute('data-theme', 'night'); await expect(page.locator('.reader')).toHaveAttribute('style', /28px/)
  await page.getByLabel('章节阅读区').evaluate(el => { el.scrollTop = 900 }); await page.getByRole('button', { name: '保存进度', exact: true }).click(); await expect(page.locator('.reader-status')).toHaveText('进度已同步')
  const progress = async () => { const r = await page.request.get(`/api/v1/books/${book.id}/progress`, { headers: await headers() }); return r.json() }
  const before = (await progress()).char_offset; expect(before).toBeGreaterThan(0)
  await page.getByRole('button', { name: '放大字号', exact: true }).click(); await expect(page.getByLabel('章节阅读区')).toHaveAttribute('aria-busy', 'false'); await expect.poll(async () => (await settings())['reader.font_size']).toBe(29); expect((await progress()).char_offset).toBe(before)
  await page.getByRole('link', { name: '返回书架' }).click(); await page.getByRole('link', { name: '设置', exact: true }).click(); await page.getByRole('button', { name: '恢复默认阅读设置' }).click(); await page.getByRole('button', { name: '取消', exact: true }).click(); expect((await settings())['reader.font_size']).toBe(29)
  await page.getByRole('button', { name: '恢复默认阅读设置' }).click(); await page.getByRole('button', { name: '恢复默认', exact: true }).click(); await expect(page.getByText('阅读设置已同步', { exact: true })).toBeVisible(); const reset = await settings(); expect(reset['reader.theme']).toBeUndefined(); expect(reset['reader.font_size']).toBeUndefined(); expect(reset['verification.unrelated']).toBe('keep')
  await page.request.patch('/api/v1/settings', { headers: await headers(), data: { 'verification.unrelated': null } })
})
