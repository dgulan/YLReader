import { test, expect } from '@playwright/test'
test('import timeout gives an uncertain-result notice and requires manual retry', async ({ page }) => {
  await page.addInitScript(() => sessionStorage.setItem('yreader-session', JSON.stringify({ access_token: 'fixture', refresh_token: 'fixture-refresh', token_type: 'Bearer', expires_in: 900, user: { id: '00000000-0000-0000-0000-000000000018', username: 'timeout-fixture', role: 'user', token_version: 1, created_at: '2026-11-13T00:00:00Z' } })))
  await page.route('**/api/v1/settings', route => route.fulfill({ json: {} }))
  await page.route('**/api/v1/books?*', route => route.fulfill({ json: { items: [], total: 0, page: 1, page_size: 20 } }))
  let submissions = 0
  await page.route('**/api/v1/books/import?*', route => { submissions++; return route.fulfill({ status: 504, headers: { 'Retry-After': '10' }, json: { error: { code: 'import_timeout', message: 'local import timeout' } } }) })
  await page.goto('/bookshelf')
  const file = { name: 'timeout.txt', mimeType: 'text/plain', buffer: Buffer.from('第一章 测试\n正文') }
  await page.getByLabel('导入本地书', { exact: true }).setInputFiles(file)
  await expect(page.getByRole('alert')).toContainText('结果尚未确认'); await expect(page.getByRole('alert')).toContainText('刷新书架'); expect(submissions).toBe(1)
  await expect(page.getByLabel('导入本地书', { exact: true })).toBeEnabled()
  await page.getByLabel('导入本地书', { exact: true }).setInputFiles(file)
  await expect.poll(() => submissions).toBe(2); await expect(page.getByRole('alert')).toContainText('结果尚未确认')
})
