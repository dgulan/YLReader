import { test, expect, devices, type BrowserContext, type Page } from '@playwright/test'
import path from 'node:path'
import { writeFile } from 'node:fs/promises'
import { loginToShelf } from './login-diagnostics'

async function watchPolicy(context: BrowserContext, record: (directive: string) => void) {
  await context.exposeFunction('recordReadingPolicyViolation', record)
  await context.addInitScript(() => {
    window.addEventListener('securitypolicyviolation', event => {
      void (window as unknown as { recordReadingPolicyViolation: (directive: string) => void }).recordReadingPolicyViolation(event.effectiveDirective)
    })
  })
}

async function resourceMetrics(page: Page) {
  return page.evaluate(() => {
    const assets = (performance.getEntriesByType('resource') as PerformanceResourceTiming[])
      .filter(entry => new URL(entry.name).origin === location.origin && new URL(entry.name).pathname.startsWith('/assets/'))
    return { asset_count: assets.length, transfer_bytes: assets.reduce((sum, entry) => sum + entry.transferSize, 0),
      decoded_bytes: assets.reduce((sum, entry) => sum + entry.decodedBodySize, 0),
      max_asset_ms: Math.round(Math.max(0, ...assets.map(entry => entry.duration))),
      slowest: assets.slice().sort((a, b) => b.duration - a.duration).slice(0, 8)
        .map(entry => ({ path: new URL(entry.name).pathname, milliseconds: Math.round(entry.duration) })) }
  })
}

test.skip(!process.env.E2E_USERNAME || !process.env.E2E_PASSWORD || !process.env.E2E_STAGE3_EPUB, 'Requires isolated stage3 backend and original EPUB')

test('original Chinese EPUB imports and reads across desktop and mobile sessions', async ({ browser }) => {
  const seconds = Number(process.env.E2E_STAGE3_READ_SECONDS ?? 60)
  expect(Number.isInteger(seconds) && seconds >= 60 && seconds <= 600).toBeTruthy()
  test.setTimeout((seconds + 120) * 1000)
  const desktop = await browser.newContext()
  const phone = devices['iPhone 15 Pro']!
  const mobile = await browser.newContext({ viewport: phone.viewport, screen: phone.screen, userAgent: phone.userAgent, deviceScaleFactor: phone.deviceScaleFactor, isMobile: true, hasTouch: true })
  const diagnostics: { at: number; event: string; detail: string }[] = []
  const record = (event: string, detail: string) => diagnostics.push({ at: Date.now(), event, detail })
  const policyErrors: string[] = []
  try {
    for (const context of [desktop, mobile]) await watchPolicy(context, directive => { policyErrors.push(directive); record('csp-violation', directive) })
    const a = await desktop.newPage(), b = await mobile.newPage()
    const pageErrors: string[] = []
    const networkFailures: string[] = []
    b.on('requestfailed', request => {
      networkFailures.push(request.url())
      record('requestfailed', `${request.method()} ${new URL(request.url()).pathname}: ${request.failure()?.errorText}`)
    })
    b.on('response', response => { if (response.url().includes('/api/')) record('response', `${response.status()} ${new URL(response.url()).pathname}`) })
    for (const page of [a, b]) {
      page.on('pageerror', e => { pageErrors.push(e.message); record('pageerror', `${e.name}: ${e.message}\n${e.stack}`) })
      await page.addInitScript(() => {
        const state = window as unknown as { stage3UnhandledErrors: string[] }
        state.stage3UnhandledErrors = []
        window.addEventListener('unhandledrejection', event => state.stage3UnhandledErrors.push(String(event.reason)))
      })
    }
    for (const p of [a, b]) {
      await loginToShelf(p, (event, detail) => record(event, JSON.stringify(detail)))
    }
    await a.bringToFront()
    await a.getByLabel('导入本地书', { exact: true }).setInputFiles(path.resolve(process.env.E2E_STAGE3_EPUB!))
    const card = a.locator('.book-card').filter({ hasText: '原创中文验收' })
    await expect(card).toBeVisible()
    await card.getByRole('button', { name: '开始阅读' }).click()
    await expect(a.getByLabel('章节阅读区')).toContainText('山间的晨光')
    record('cold-resources', JSON.stringify(await resourceMetrics(a)))
    await a.getByRole('button', { name: '下一章', exact: true }).click()
    await expect(a.getByLabel('章节阅读区')).toContainText('河岸的午后')
    await expect.poll(async () => {
      const s = await a.evaluate(() => JSON.parse(sessionStorage.getItem('yreader-session')!))
      const id = a.url().split('/reader/')[1]!
      const r = await a.request.get(`/api/v1/books/${id}/progress`, { headers: { Authorization: `Bearer ${s.access_token}` } })
      return (await r.json()).chapter_index
    }).toBe(1)
    await b.bringToFront()
    record('phase', 'mobile reader first navigation')
    await b.goto(a.url())
    await expect(b.getByLabel('章节阅读区')).toContainText('河岸的午后')
    expect(await b.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBeTruthy()
    await b.getByRole('button', { name: '设置', exact: true }).click()
    const themeSaved = b.waitForResponse(response => new URL(response.url()).pathname === '/api/v1/settings' && response.request().method() === 'PATCH')
    await b.getByLabel('阅读主题').selectOption('night')
    const saved = await themeSaved
    expect(saved.ok()).toBeTruthy()
    expect((await saved.json())['reader.theme']).toBe('night')
    await b.reload()
    record('phase', 'mobile reader reload')
    await b.getByRole('button', { name: '设置', exact: true }).click()
    await expect(b.getByLabel('阅读主题')).toHaveValue('night')
    // A bounded navigation/connectivity check, not a physical-device or long-reading soak.
    let cycles = 0
    record('phase', `bounded reading ${seconds} seconds`)
    for (const end = Date.now() + seconds * 1000; Date.now() < end; cycles++) {
      await b.getByRole('button', { name: '下一章', exact: true }).click()
      await expect(b.getByLabel('章节阅读区')).toContainText('星光下的书页')
      await b.waitForTimeout(1000)
      if (cycles % 3 === 0) { record('phase', 'offline'); await mobile.setOffline(true) }
      await b.getByRole('button', { name: '上一章', exact: true }).click()
      await expect(b.getByLabel('章节阅读区')).toContainText('河岸的午后')
      if (cycles % 3 === 0) {
        await expect(b.locator('.reader-kicker')).toContainText('离线缓存')
        await mobile.setOffline(false)
        record('phase', 'online')
      }
    }
    expect(cycles).toBeGreaterThanOrEqual(10)
    record('completed', `${seconds} seconds; ${cycles} navigation cycles`)
    expect(pageErrors).toEqual([])
    expect(policyErrors).toEqual([])
    for (const page of [a, b]) expect(await page.evaluate(() => (window as unknown as { stage3UnhandledErrors: string[] }).stage3UnhandledErrors)).toEqual([])
    const session = await b.evaluate(() => JSON.parse(sessionStorage.getItem('yreader-session')!))
    const recoveredSettings = await b.request.get('/api/v1/settings', { headers: { Authorization: `Bearer ${session.access_token}` } })
    expect(recoveredSettings.ok()).toBeTruthy()
    expect((await recoveredSettings.json())['reader.theme']).toBe('night')
    test.info().annotations.push({ type: 'failed-network-requests', description: String(networkFailures.length) })
  } finally {
    await writeFile(test.info().outputPath('reading-diagnostics.json'), JSON.stringify(diagnostics, null, 2))
    await desktop.close(); await mobile.close()
  }
})

test('original numbered TXT keeps 120 chapters and resumes cached reading across sessions', async ({ browser }) => {
  test.skip(!process.env.E2E_STAGE3_TXT, 'Requires the generated original numbered TXT')
  test.setTimeout(120000)
  const desktop = await browser.newContext()
  const phone = devices['iPhone 15 Pro']!
  const mobile = await browser.newContext({ viewport: phone.viewport, screen: phone.screen, userAgent: phone.userAgent, deviceScaleFactor: phone.deviceScaleFactor, isMobile: true, hasTouch: true })
  const errors: string[] = []
  const report: Record<string, unknown> = { sample: 'original_numbered_txt', physical_device: false }
  const policyErrors: string[] = []
  report.csp_violations = policyErrors
  const connectivity: { event: string; at: number; status?: number }[] = []
  report.connectivity = connectivity
  const logins: number[] = []
  report.login_statuses = logins
  const loginDiagnostics: { at: number; event: string; detail: unknown }[] = []
  report.login_diagnostics = loginDiagnostics
  try {
    for (const context of [desktop, mobile]) await watchPolicy(context, directive => policyErrors.push(directive))
    const a = await desktop.newPage(), b = await mobile.newPage()
    await b.exposeFunction('numberedConnectivity', (event: string) => connectivity.push({ event, at: Date.now() }))
    await b.addInitScript(() => {
      const record = (window as unknown as { numberedConnectivity: (event: string) => void }).numberedConnectivity
      window.addEventListener('online', () => { void record('online') })
      window.addEventListener('offline', () => { void record('offline') })
    })
    b.on('requestfailed', request => { if (new URL(request.url()).pathname.endsWith('/progress')) connectivity.push({ event: 'progress_failed', at: Date.now() }) })
    b.on('response', response => { if (response.request().method() === 'PUT' && new URL(response.url()).pathname.endsWith('/progress')) connectivity.push({ event: 'progress_response', at: Date.now(), status: response.status() }) })
    for (const page of [a, b]) {
      page.on('pageerror', error => errors.push(error.message))
      await page.addInitScript(() => {
        const state = window as unknown as { numberedUnhandled: string[] }
        state.numberedUnhandled = []
        window.addEventListener('unhandledrejection', event => state.numberedUnhandled.push(String(event.reason)))
      })
      logins.push(await loginToShelf(page, (event, detail) => loginDiagnostics.push({ at: Date.now(), event, detail })))
    }
    await a.bringToFront()
    await a.getByLabel('导入本地书', { exact: true }).setInputFiles(path.resolve(process.env.E2E_STAGE3_TXT!))
    const card = a.locator('.book-card').filter({ hasText: '原创编号验收' })
    await expect(card).toBeVisible()
    await card.getByRole('button', { name: '开始阅读' }).click()
    await expect(a.getByLabel('章节阅读区')).toContainText('原创编号001')
    report.cold_resources = await resourceMetrics(a)
    const bookId = a.url().split('/reader/')[1]!
    const session = await a.evaluate(() => JSON.parse(sessionStorage.getItem('yreader-session')!))
    const headers = { Authorization: `Bearer ${session.access_token}` }
    const response = await a.request.get(`/api/v1/books/${bookId}/chapters`, { headers })
    expect(response.ok()).toBeTruthy()
    const toc = await response.json()
    expect(toc.total).toBe(120)
    expect(toc.items).toHaveLength(120)
    expect(toc.items.map((c: { chapter_index: number }) => c.chapter_index)).toEqual(Array.from({ length: 120 }, (_, i) => i))
    expect(toc.items.map((c: { title: string }) => c.title)).toEqual(Array.from({ length: 120 }, (_, i) => `第${i + 1}章 原创编号验收`))
    report.strict_numbering_passed = true
    await a.getByRole('button', { name: '目录', exact: true }).click()
    const dialog = a.getByRole('dialog', { name: '章节目录', exact: true })
    const list = dialog.getByRole('list', { name: '章节目录列表' })
    await list.getByRole('button', { name: '第1章 原创编号验收', exact: true }).focus()
    expect(await list.getByRole('listitem').count()).toBeLessThan(120)
    await a.keyboard.press('End')
    await expect(list.getByRole('button', { name: '第120章 原创编号验收', exact: true })).toBeFocused()
    await a.keyboard.press('Enter')
    await expect(a.getByLabel('章节阅读区')).toContainText('原创编号120')
    const remoteIndex = async () => {
      const r = await a.request.get(`/api/v1/books/${bookId}/progress`, { headers })
      expect(r.ok()).toBeTruthy()
      return (await r.json()).chapter_index
    }
    await expect.poll(remoteIndex).toBe(119)
    await b.bringToFront()
    await b.goto(a.url())
    await expect(b.getByLabel('章节阅读区')).toContainText('原创编号120')
    await expect.poll(() => b.evaluate(id => new Promise<boolean>((resolve, reject) => {
      const open = indexedDB.open('yreader-offline-v1')
      open.onerror = () => reject(open.error)
      open.onsuccess = () => {
        const database = open.result
        const request = database.transaction('chapters').objectStore('chapters').getAll()
        request.onsuccess = () => { database.close(); resolve(request.result.some(row => row.bookId === id && row.index === 118)) }
        request.onerror = () => { database.close(); reject(request.error) }
      }
    }), bookId)).toBeTruthy()
    await mobile.setOffline(true)
    await b.getByRole('button', { name: '上一章', exact: true }).click()
    await expect(b.getByLabel('章节阅读区')).toContainText('原创编号119')
    await expect(b.locator('.reader-kicker')).toContainText('离线缓存')
    await mobile.setOffline(false)
    report.online_after_reconnect = await b.evaluate(() => navigator.onLine)
    await expect.poll(remoteIndex).toBe(118)
    await a.bringToFront()
    await a.reload()
    await expect(a.getByLabel('章节阅读区')).toContainText('原创编号119')
    for (const page of [a, b]) expect(await page.evaluate(() => (window as unknown as { numberedUnhandled: string[] }).numberedUnhandled)).toEqual([])
    expect(errors).toEqual([])
    expect(policyErrors).toEqual([])
    report.cached_offline_and_cross_session_passed = true
  } finally {
    report.page_errors = errors
    await writeFile(test.info().outputPath('reading-diagnostics.json'), JSON.stringify(report, null, 2))
    await desktop.close(); await mobile.close()
  }
})
