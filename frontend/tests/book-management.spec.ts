import { test, expect } from '@playwright/test'
import { mkdir } from 'node:fs/promises'
const username = process.env.E2E_USERNAME, password = process.env.E2E_PASSWORD
test.skip(!username || !password, 'Isolated backend credentials required')
test('book metadata preserves conflicts and deletion requires confirmation', async ({ page }) => {
  await page.goto('/login'); await page.getByLabel('用户名', { exact: true }).fill(username!); await page.getByLabel('密码', { exact: true }).fill(password!); await page.getByRole('button', { name: '登录书房' }).click(); await expect(page).toHaveURL(/bookshelf/)
  const session = await page.evaluate(() => JSON.parse(sessionStorage.getItem('yreader-session')!)), headers = { Authorization: `Bearer ${session.access_token}` }
  const name = `管理验证${Date.now()}`
  const imported = await page.request.post(`/api/v1/books/import?filename=${encodeURIComponent(name + '.txt')}`, { headers: { ...headers, 'Content-Type': 'application/octet-stream' }, data: Buffer.from(`第一章 整理书房\n${name}的正文内容。\n第二章 明天继续\n继续阅读。`) })
  expect(imported.ok()).toBeTruthy(); const { book } = await imported.json(), url = `/api/v1/books/${book.id}`
  await page.reload(); await page.locator('.book-card').filter({ has: page.getByRole('heading', { name, exact: true }) }).getByRole('button', { name: '管理书籍', exact: true }).click()
  await expect(page.getByLabel('编辑书名')).toHaveValue(name); await page.getByLabel('编辑书名').fill(`${name}草稿`)
  // Simulate another device through the real API, retaining the original UI version.
  const current = await (await page.request.get(url, { headers })).json()
  const concurrent = await page.request.patch(url, { headers, data: { version: current.version, intro: '另一端修改' } }); expect(concurrent.ok(), await concurrent.text()).toBeTruthy()
  await page.getByRole('button', { name: '保存书籍信息', exact: true }).click(); await expect(page.getByRole('alert')).toContainText('草稿已保留'); await expect(page.getByLabel('编辑书名')).toHaveValue(`${name}草稿`)
  await page.getByRole('button', { name: '重新加载书籍' }).click(); await page.getByRole('button', { name: '继续编辑', exact: true }).click(); await expect(page.getByLabel('编辑书名')).toHaveValue(`${name}草稿`)
  await page.getByRole('button', { name: '重新加载书籍' }).click(); await page.getByRole('button', { name: '放弃修改', exact: true }).click(); await expect(page.getByLabel('编辑简介')).toHaveValue('另一端修改')
  const renamed = `${name}已整理`; await page.getByLabel('编辑书名').fill(renamed); await page.getByLabel('编辑作者').fill(' 新作者 '); await page.getByLabel('编辑简介').fill('')
  await page.getByRole('button', { name: '保存书籍信息', exact: true }).click(); await expect(page.getByRole('alert')).toContainText('书籍信息已保存')
  const saved = await (await page.request.get(url, { headers })).json(); expect(saved.name).toBe(renamed); expect(saved.author).toBe('新作者'); expect(saved.intro).toBe('')
  await page.reload(); await expect(page.getByLabel('编辑书名')).toHaveValue(renamed)
  await page.setViewportSize({ width: 390, height: 844 }); await expect.poll(() => page.evaluate(() => Math.max(document.body.scrollWidth, document.documentElement.scrollWidth) <= innerWidth)).toBe(true)
  await mkdir('.test-artifacts', { recursive: true }); await page.screenshot({ path: '.test-artifacts/book-management-mobile.png' })
  await page.getByRole('button', { name: '删除书籍', exact: true }).click(); const dialog = page.getByRole('dialog', { name: '删除书籍', exact: true }); await expect(dialog).toContainText('本地书还将清理服务端文件'); await dialog.getByRole('button', { name: '取消', exact: true }).click(); expect((await page.request.get(url, { headers })).ok()).toBeTruthy()
  await page.getByRole('button', { name: '删除书籍', exact: true }).click(); await dialog.getByRole('button', { name: '确认删除书籍', exact: true }).click(); await expect(page.getByRole('alert')).toContainText('书籍已删除'); expect((await page.request.get(url, { headers })).status()).toBe(404)
  await page.getByRole('link', { name: '返回书架查看', exact: true }).click(); await expect(page).toHaveURL(/bookshelf/); await expect(page.getByRole('heading', { name: renamed, exact: true })).toHaveCount(0)
})
