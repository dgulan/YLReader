import { test, expect } from '@playwright/test'
import { execFileSync } from 'node:child_process'
import { mkdir } from 'node:fs/promises'
const username = process.env.E2E_USERNAME, password = process.env.E2E_PASSWORD
test.skip(!username || !password, 'Isolated backend credentials required')
function sql(statement: string) {
  if (process.env.E2E_DB_CONTAINER) execFileSync('docker', ['exec', '-i', process.env.E2E_DB_CONTAINER, 'psql', '-U', 'yreader', '-d', 'yreader', '-v', 'ON_ERROR_STOP=1'], { input: statement })
  else if (process.env.E2E_DATABASE_URL) execFileSync(process.env.E2E_PSQL ?? 'psql', ['-d', process.env.E2E_DATABASE_URL, '-v', 'ON_ERROR_STOP=1'], { input: statement })
  else throw new Error('Cache task tests require an isolated E2E_DB_CONTAINER or E2E_DATABASE_URL')
}
test('server cache tasks validate ranges, persist tracking, expose failure and confirm cancellation', async ({ page }) => {
  await page.goto('/login'); await page.getByLabel('用户名', { exact: true }).fill(username!); await page.getByLabel('密码', { exact: true }).fill(password!); await page.getByRole('button', { name: '登录书房' }).click(); await expect(page).toHaveURL(/bookshelf/)
  const session = await page.evaluate(() => JSON.parse(sessionStorage.getItem('yreader-session')!)), headers = { Authorization: `Bearer ${session.access_token}` }, title = `缓存任务${Date.now()}`
  const sr = await page.request.post('/api/v1/sources', { headers, data: { book_source_name: title, book_source_url: `https://cache-${Date.now()}.example.com`, enabled: true, rules: {} } }); expect(sr.ok()).toBeTruthy(); const source = await sr.json()
  const br = await page.request.post('/api/v1/books', { headers, data: { source_id: source.id, book_url: source.book_source_url + '/book', name: title, author: '验证作者' } }); expect(br.ok()).toBeTruthy(); const book = await br.json()
  // The real worker receives deterministic SSRF-blocked URLs; no public upstream is needed.
  sql(`INSERT INTO chapters(book_id,source_id,chapter_index,sort_order,title,normalized_title,url,url_hash) SELECT '${book.id}'::uuid,'${source.id}'::uuid,i,i,'第'||(i+1)||'章','n:'||i,'http://127.0.0.1:9/'||i,encode(sha256(i::text::bytea),'hex') FROM generate_series(0,2) i; UPDATE books SET total_chapters=3 WHERE id='${book.id}'::uuid;`)
  await page.reload(); await page.locator('.book-card').filter({ has: page.getByRole('heading', { name: title, exact: true }) }).getByRole('button', { name: '后台缓存', exact: true }).click()
  await expect(page.getByText('目标目录 3 章，以下章节编号从 1 开始。', { exact: true })).toBeVisible()
  await page.getByRole('spinbutton', { name: '缓存起始章' }).fill('3'); await page.getByRole('spinbutton', { name: '缓存起始章' }).press('Tab'); await page.getByRole('spinbutton', { name: '缓存结束章' }).fill('2'); await page.getByRole('spinbutton', { name: '缓存结束章' }).press('Tab'); await page.getByRole('button', { name: '提交后台缓存' }).click(); await expect(page.getByRole('alert')).toContainText('起始章不能晚于结束章')
  await page.getByRole('spinbutton', { name: '缓存起始章' }).fill('2'); await page.getByRole('spinbutton', { name: '缓存起始章' }).press('Tab'); await page.getByRole('spinbutton', { name: '缓存结束章' }).fill('3'); await page.getByRole('spinbutton', { name: '缓存结束章' }).press('Tab')
  const response = page.waitForResponse(r => r.url().endsWith(`/books/${book.id}/cache`) && r.request().method() === 'POST'); await page.getByRole('button', { name: '提交后台缓存' }).click(); const created = await response; expect(created.status()).toBe(202); expect(created.request().postDataJSON()).toEqual({ source_id: source.id, from_chapter: 1, to_chapter: 2 }); const task = await created.json()
  const getJob = async () => { const r = await page.request.get(`/api/v1/jobs/${task.id}`, { headers }); expect(r.ok()).toBeTruthy(); return r.json() }
  await expect.poll(async () => (await getJob()).status).toBe('failed'); await page.getByRole('button', { name: '刷新任务', exact: true }).click(); await expect(page.getByLabel('缓存任务状态').getByRole('status')).toHaveText('失败')
  // Create a controlled running checkpoint after the real worker has finished, then exercise the real cancel endpoint.
  sql(`UPDATE jobs SET status='running',finished_at=NULL,worker_id='e2e-controlled',expires_at=now()+interval '1 hour',error_code=NULL,error_message=NULL WHERE id='${task.id}'::uuid;`)
  await page.reload(); await expect(page.getByLabel('缓存任务状态')).toContainText(task.id); await expect(page.getByLabel('缓存任务状态').getByRole('status')).toHaveText('正在缓存'); await expect(page.getByRole('button', { name: '提交后台缓存' })).toBeDisabled()
  await page.route(`**/api/v1/jobs/${task.id}`, route => route.abort('internetdisconnected')); await page.getByRole('button', { name: '刷新任务', exact: true }).click(); await expect(page.getByRole('alert')).toContainText('已暂停刷新'); await page.unroute(`**/api/v1/jobs/${task.id}`); await page.getByRole('button', { name: '刷新任务', exact: true }).click()
  await page.setViewportSize({ width: 390, height: 844 }); await mkdir('.test-artifacts', { recursive: true }); await page.screenshot({ path: '.test-artifacts/cache-task-mobile.png', fullPage: true }); expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  await page.getByRole('button', { name: '取消缓存任务', exact: true }).click(); await page.getByRole('button', { name: '继续缓存', exact: true }).click(); expect((await getJob()).status).toBe('running')
  await page.getByRole('button', { name: '取消缓存任务', exact: true }).click(); await page.getByRole('button', { name: '确认取消任务', exact: true }).click(); await expect(page.getByLabel('缓存任务状态').getByRole('status')).toHaveText('已取消'); expect((await getJob()).status).toBe('canceled')
})
