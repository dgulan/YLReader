import { test, expect } from '@playwright/test'
import { mkdir } from 'node:fs/promises'
const username = process.env.E2E_USERNAME, password = process.env.E2E_PASSWORD
test.skip(!username || !password, 'Isolated backend credentials required')
test('replacement editor preserves invalid drafts, updates real content and confirms deletion', async ({ page }) => {
  await page.goto('/login'); await page.getByLabel('用户名', { exact: true }).fill(username!); await page.getByLabel('密码', { exact: true }).fill(password!); await page.getByRole('button', { name: '登录书房' }).click(); await expect(page).toHaveURL(/bookshelf/)
  const session = await page.evaluate(() => JSON.parse(sessionStorage.getItem('yreader-session')!)), headers = { Authorization: `Bearer ${session.access_token}` }
  const title = `净化验证${Date.now()}`, content = `第一章 天气\n广告😀，故事在这里继续。${title}`
  const imported = await page.request.post(`/api/v1/books/import?filename=${encodeURIComponent(title + '.txt')}`, { headers: { ...headers, 'Content-Type': 'application/octet-stream' }, data: Buffer.from(content) }); expect(imported.ok()).toBeTruthy(); const book = (await imported.json()).book
  const body = async () => { const r = await page.request.get(`/api/v1/books/${book.id}/content?chapter_index=0`, { headers }); expect(r.ok()).toBeTruthy(); return (await r.json()).content as string }
  expect(await body()).toContain('广告😀')
  await page.goto(`/reader/${book.id}`); await expect(page.locator('.chapter-body')).toContainText('广告😀'); await page.getByRole('link', { name: '返回书架' }).click()
  await page.getByRole('link', { name: '设置', exact: true }).click(); await page.getByRole('link', { name: '管理替换净化规则 →' }).click()
  await page.getByLabel('规则名称', { exact: true }).fill(title); await page.getByLabel('替换表达式', { exact: true }).fill('['); await page.getByLabel('替换文本', { exact: true }).fill('晴天')
  await page.getByRole('button', { name: '保存替换规则' }).click(); await expect(page.getByRole('alert')).toContainText('草稿已保留'); await expect(page.getByLabel('替换表达式')).toHaveValue('[')
  await page.getByLabel('替换表达式').fill('广告'); await page.getByRole('spinbutton', { name: '规则执行顺序' }).fill('5'); await page.getByRole('spinbutton', { name: '规则执行顺序' }).press('Tab'); await page.getByRole('button', { name: '保存替换规则' }).click(); await expect(page.getByRole('alert')).toContainText('重新打开章节后应用')
  expect(await body()).toContain('晴天😀')
  const rows = await page.evaluate(() => new Promise<number>((resolve, reject) => { const req = indexedDB.open('yreader-offline-v1'); req.onerror = () => reject(req.error); req.onsuccess = () => { const db = req.result; try { const get = db.transaction('chapters').objectStore('chapters').count(); get.onerror = () => reject(get.error); get.onsuccess = () => { resolve(get.result); db.close() } } catch (e) { db.close(); reject(e) } } })); expect(rows).toBe(0)
  const enabled = page.getByRole('switch', { name: '启用规则' }), save = page.getByRole('button', { name: '保存替换规则' })
  await expect(enabled).toBeEnabled(); await expect(enabled).toBeChecked(); await enabled.press('Enter'); await expect(enabled).not.toBeChecked()
  await save.click(); await expect(save).toBeEnabled(); await expect.poll(body).toContain('广告😀')
  await expect(enabled).toBeEnabled(); await expect(enabled).not.toBeChecked(); await enabled.press('Enter'); await expect(enabled).toBeChecked()
  await save.click(); await expect(save).toBeEnabled(); await expect.poll(body).toContain('晴天😀')
  await page.getByLabel('规则名称', { exact: true }).fill('未保存修改'); await page.getByRole('button', { name: '新建规则', exact: true }).click(); await page.getByRole('button', { name: '继续编辑', exact: true }).click(); await expect(page.getByRole('button', { name: '继续编辑', exact: true })).toBeHidden(); await expect(page.getByLabel('规则名称', { exact: true })).toHaveValue('未保存修改'); await page.getByLabel('规则名称', { exact: true }).fill(title)
  await page.setViewportSize({ width: 390, height: 844 }); await mkdir('.test-artifacts', { recursive: true }); await expect.poll(() => page.evaluate(() => Math.max(document.documentElement.scrollWidth, document.body.scrollWidth) <= innerWidth)).toBe(true); await page.getByRole('heading', { name: '替换净化规则.' }).scrollIntoViewIfNeeded(); await page.screenshot({ path: '.test-artifacts/replace-rules-mobile.png' })
  await page.getByRole('button', { name: '删除替换规则', exact: true }).click(); await page.getByRole('button', { name: '取消', exact: true }).click(); expect(await body()).toContain('晴天😀')
  await page.getByRole('button', { name: '删除替换规则', exact: true }).click(); await page.getByRole('button', { name: '确认删除规则', exact: true }).click(); await expect(page.getByRole('heading', { name: '新建替换规则', exact: true })).toBeVisible(); expect(await body()).toContain('广告😀')
  const rulesResponse = await page.request.get('/api/v1/replace-rules', { headers }), rules = await rulesResponse.json(); expect(rules.some((r: { name: string }) => r.name === title)).toBe(false)
  await page.route('**/api/v1/replace-rules', route => route.fulfill({ json: Array.from({ length: 125 }, (_, i) => ({ id: `fixture-${i}`, name: `规则${i}`, group_name: '测试', pattern: 'a', replacement: '', scope: 'content', is_enabled: true, order_index: i })) }))
  await page.getByRole('button', { name: '刷新规则' }).click(); await expect(page.getByRole('heading', { name: '规则列表（125）' })).toBeVisible(); expect(await page.getByRole('row').count()).toBeLessThan(30)
})
