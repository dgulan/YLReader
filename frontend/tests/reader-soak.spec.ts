import { test, expect, devices, type Page, type BrowserContext } from '@playwright/test'
import { writeFile, rename } from 'node:fs/promises'
import path from 'node:path'
import { loginToShelf } from './login-diagnostics'

test.skip(!process.env.E2E_USERNAME || !process.env.E2E_STAGE3_TXT || !process.env.E2E_SOAK_SECONDS, 'Requires owned soak services')
test('continuous original TXT reading preserves progress, cache and session renewal', async ({ browser }) => {
  const probe = process.env.E2E_SOAK_PROBE === '1', seconds = Number(process.env.E2E_SOAK_SECONDS)
  expect(Number.isInteger(seconds) && (probe ? seconds === 60 : seconds >= 1800 && seconds <= 3600)).toBeTruthy()
  test.setTimeout((seconds + 180) * 1000)
  const desktop = await browser.newContext(), mobile = await browser.newContext({ ...devices['iPhone 15 Pro'] })
  const faults = { page_errors: [] as unknown[], unhandled_rejections: [] as unknown[], csp_violations: [] as unknown[] }
  const report = { mode: probe ? 'reader_soak_probe' : 'reader_soak', requested_seconds: seconds, elapsed_seconds: 0,
    completed: false, cycles: 0, chapter_index: 0, offline_rounds: 0, desktop_checks: 0, appearance_changes: 0,
    viewport_changes: 0, mobile_refreshes: 0, desktop_refreshes: 0, final_resume: false, physical_device: false,
    ...faults, refresh_events: [] as { context: string; status: number; at: string }[],
    shelf_statuses: [] as number[], auth_injection: probe, started_at: '', updated_at: '' }
  const heartbeat = process.env.E2E_SOAK_HEARTBEAT
  if (heartbeat) expect(path.resolve(heartbeat).startsWith(path.resolve('../.toolchain') + path.sep)).toBeTruthy()
  let start = 0, loopFinished = false, mobileClosed = false
  async function checkpoint() {
    if (start && !loopFinished) report.elapsed_seconds = (performance.now() - start) / 1000
    report.updated_at = new Date().toISOString()
    const data = JSON.stringify(report, null, 2)
    await writeFile(test.info().outputPath('reader-soak-diagnostics.json'), data)
    if (heartbeat) { await writeFile(heartbeat + '.tmp', data); await rename(heartbeat + '.tmp', heartbeat) }
  }
  async function watch(context: BrowserContext, name: 'mobile' | 'desktop') {
    await context.exposeFunction('recordSoakFault', (kind: keyof typeof faults, detail: string) => faults[kind].push({ context: name, detail }))
    await context.addInitScript(() => {
      const record = (window as unknown as { recordSoakFault: (kind: string, detail: string) => Promise<void> }).recordSoakFault
      window.addEventListener('unhandledrejection', () => { void record('unhandled_rejections', 'unhandledrejection').catch(() => {}) })
      window.addEventListener('securitypolicyviolation', event => { void record('csp_violations', event.effectiveDirective).catch(() => {}) })
    })
    context.on('page', page => page.on('pageerror', error => faults.page_errors.push({ context: name, name: error.name })))
    context.on('response', response => {
      if (name === 'desktop' && new URL(response.url()).pathname === '/api/v1/books' && response.request().method() === 'GET') report.shelf_statuses.push(response.status())
      if (new URL(response.url()).pathname !== '/api/v1/auth/refresh' || response.request().method() !== 'POST') return
      report.refresh_events.push({ context: name, status: response.status(), at: new Date().toISOString() })
      if (response.ok()) { if (name === 'mobile') report.mobile_refreshes++; else report.desktop_refreshes++ }
    })
  }
  function noFaults() { for (const errors of Object.values(faults)) expect(errors).toEqual([]) }
  try {
    await watch(desktop, 'desktop'); await watch(mobile, 'mobile')
    const a = await desktop.newPage(), b = await mobile.newPage()
    await loginToShelf(a, () => {}); await loginToShelf(b, () => {})
    await a.bringToFront()
    await a.getByLabel('导入本地书', { exact: true }).setInputFiles(path.resolve(process.env.E2E_STAGE3_TXT!))
    const card = a.locator('.book-card').filter({ hasText: '原创编号验收' })
    await expect(card).toBeVisible()
    // The desktop remains a read-only shelf observer throughout the loop.
    const open = card.getByRole('button', { name: '开始阅读', exact: true })
    await open.click()
    await expect(a.getByLabel('章节阅读区')).toContainText('原创编号001')
    const readerUrl = a.url(), bookId = readerUrl.split('/reader/')[1]!
    await a.getByRole('link', { name: '返回书架', exact: true }).click()
    await b.bringToFront(); await b.goto(readerUrl)
    await expect(b.getByLabel('章节阅读区')).toContainText('原创编号001')
    // 主题、字号、书签、保存进度都在「设置」面板内，保持面板常开。
    await b.getByRole('button', { name: '设置', exact: true }).click(); await expect(b.locator('#reader-settings')).toBeVisible()
    if (probe) {
      // Only the short fixture probe damages access credentials; the long run waits for real expiry.
      await a.evaluate(() => {
        const session = JSON.parse(sessionStorage.getItem('yreader-session')!)
        session.access_token = 'probe-invalid-access'
        sessionStorage.setItem('yreader-session', JSON.stringify(session))
      })
    }
    const marker = (index: number) => `原创编号${String(index + 1).padStart(3, '0')}`
    const viewport = b.getByLabel('章节阅读区')
    const progress = async (page: Page) => {
      const session = await page.evaluate(() => JSON.parse(sessionStorage.getItem('yreader-session')!))
      const response = await page.request.get(`/api/v1/books/${bookId}/progress`, { headers: { Authorization: `Bearer ${session.access_token}` } })
      expect(response.ok()).toBeTruthy()
      return response.json() as Promise<{ chapter_index: number; char_offset: number; version: number }>
    }
    let scrollFraction = .4
    async function saveScrolled() {
      await expect(viewport).toHaveAttribute('aria-busy', 'false')
      scrollFraction = scrollFraction === .4 ? .55 : .4
      await viewport.evaluate((element, fraction) => { element.scrollTop = (element.scrollHeight - element.clientHeight) * fraction }, scrollFraction)
      expect(await viewport.evaluate(element => element.scrollTop)).toBeGreaterThan(0)
      await b.waitForTimeout(700)
      await b.getByRole('button', { name: '保存进度', exact: true }).click()
      await expect(b.locator('.reader-status')).toHaveText('进度已同步')
      await expect.poll(async () => { const state = await progress(b); return state.chapter_index === report.chapter_index && state.char_offset > 0 }).toBeTruthy()
    }
    let index = 0, direction = 1, nextAppearance = probe ? 10 : 120, nextViewport = probe ? 20 : 300, nextObserver = probe ? 20 : 300
    let fontUp = true
    const outages = probe ? [10, 25, 40] : [300, 900, 1500]
    start = performance.now(); report.started_at = new Date().toISOString()
    await checkpoint()
    while ((performance.now() - start) / 1000 < seconds) {
      const cycleStart = performance.now(), elapsed = (cycleStart - start) / 1000
      if (index === 119) direction = -1
      if (index === 0) direction = 1
      const target = index + direction
      const outage = outages.length > 0 && elapsed >= outages[0]!
      if (outage) {
        await expect.poll(() => b.evaluate(({ bookId, target }) => new Promise<boolean>((resolve, reject) => {
          const open = indexedDB.open('yreader-offline-v1')
          open.onerror = () => reject(open.error)
          open.onsuccess = () => {
            const database = open.result, request = database.transaction('chapters').objectStore('chapters').getAll()
            request.onsuccess = () => { database.close(); resolve(request.result.some(row => row.bookId === bookId && row.index === target)) }
            request.onerror = () => { database.close(); reject(request.error) }
          }
        }), { bookId, target })).toBeTruthy()
        await mobile.setOffline(true)
      }
      await b.getByRole('button', { name: direction === 1 ? '下一章' : '上一章', exact: true }).click()
      index = target; report.chapter_index = index
      await expect(viewport).toContainText(marker(index))
      if (outage) {
        await expect(b.locator('.reader-kicker')).toContainText('离线缓存')
        await mobile.setOffline(false)
        await expect(b.locator('.reader-status')).toHaveText('进度已同步')
        report.offline_rounds++; outages.shift()
      }
      await saveScrolled()
      if (elapsed >= nextAppearance) {
        const themes = ['green', 'night', 'paper', 'day']
        const theme = themes[report.appearance_changes % themes.length]!
        const changed = b.waitForResponse(response => new URL(response.url()).pathname === '/api/v1/settings' && response.request().method() === 'PATCH')
        await b.getByLabel('阅读主题').selectOption(theme)
        expect((await changed).ok()).toBeTruthy()
        const resized = b.waitForResponse(response => new URL(response.url()).pathname === '/api/v1/settings' && response.request().method() === 'PATCH')
        await b.getByLabel(fontUp ? '放大字号' : '缩小字号', { exact: true }).click()
        expect((await resized).ok()).toBeTruthy(); fontUp = !fontUp
        report.appearance_changes++; nextAppearance += probe ? 15 : 120
        await saveScrolled()
      }
      if (elapsed >= nextViewport) {
        await b.setViewportSize({ width: 852, height: 393 })
        await expect(viewport).toContainText(marker(index))
        expect(await b.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBeTruthy()
        await b.setViewportSize(devices['iPhone 15 Pro']!.viewport)
        report.viewport_changes++; nextViewport += probe ? 20 : 300
        await saveScrolled()
      }
      if (elapsed >= nextObserver) {
        await a.bringToFront()
        // A 401 may precede the application's single-flight refresh and retry; require the final 200.
        const shelf = a.waitForResponse(response => new URL(response.url()).pathname === '/api/v1/books' && response.request().method() === 'GET' && response.status() === 200, { timeout: 15000 })
        await a.reload(); expect((await shelf).ok()).toBeTruthy()
        await expect(a.locator('.book-card').filter({ hasText: '原创编号验收' }).locator('.book-meta')).toContainText(`读至第 ${index + 1} 章`)
        expect((await progress(a)).chapter_index).toBe(index)
        report.desktop_checks++; nextObserver += probe ? 20 : 300
        await b.bringToFront()
      }
      report.cycles++; noFaults(); await checkpoint()
      await b.waitForTimeout(Math.max(0, (probe ? 5000 : 15000) - (performance.now() - cycleStart)))
    }
    report.elapsed_seconds = (performance.now() - start) / 1000; loopFinished = true
    await saveScrolled(); noFaults()
    const final = await progress(b)
    await mobile.close(); mobileClosed = true
    await a.bringToFront(); await a.reload()
    await a.locator('.book-card').filter({ hasText: '原创编号验收' }).getByRole('button', { name: '继续阅读', exact: true }).click()
    await expect(a.getByLabel('章节阅读区')).toContainText(marker(final.chapter_index))
    await expect.poll(() => a.getByLabel('章节阅读区').evaluate(element => element.scrollTop > 0)).toBeTruthy()
    report.final_resume = true
    expect(report.cycles).toBeGreaterThanOrEqual(probe ? 3 : 60)
    expect(report.offline_rounds).toBe(3)
    expect(report.desktop_checks).toBeGreaterThanOrEqual(1)
    expect(report.desktop_refreshes).toBeGreaterThanOrEqual(1)
    if (!probe) expect(report.mobile_refreshes).toBeGreaterThanOrEqual(1)
    noFaults(); report.completed = true
  } finally {
    await checkpoint()
    await desktop.close(); if (!mobileClosed) await mobile.close()
  }
})
