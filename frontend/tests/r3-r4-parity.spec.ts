import { readFile } from 'node:fs/promises'
import { test, expect, type Page } from '@playwright/test'

const username = process.env.E2E_USERNAME
const password = process.env.E2E_PASSWORD

interface ErrorTracker {
  pageErrors: string[]
  assertClean: () => Promise<void>
}

async function attachErrorTracker(page: Page): Promise<ErrorTracker> {
  const pageErrors: string[] = []
  page.on('pageerror', err => pageErrors.push(err.message))
  await page.addInitScript(() => {
    const w = window as Window & { __r6UnhandledRejections?: string[] }
    w.__r6UnhandledRejections = []
    window.addEventListener('unhandledrejection', event => {
      w.__r6UnhandledRejections?.push(String(event.reason))
    })
  })
  return {
    pageErrors,
    assertClean: async () => {
      expect(pageErrors).toEqual([])
      const rejections = await page.evaluate(() => {
        const w = window as Window & { __r6UnhandledRejections?: string[] }
        return w.__r6UnhandledRejections ?? []
      })
      expect(rejections).toEqual([])
    },
  }
}

async function login(page: Page): Promise<Record<string, string>> {
  await page.goto('/login')
  await page.getByLabel('用户名', { exact: true }).fill(username!)
  await page.getByLabel('密码', { exact: true }).fill(password!)
  await page.getByRole('button', { name: '登录书房' }).click()
  await expect(page).toHaveURL(/bookshelf/)
  const session = await page.evaluate(() => JSON.parse(sessionStorage.getItem('yreader-session')!))
  return { Authorization: `Bearer ${session.access_token}` }
}

async function createSourceAPI(page: Page, headers: Record<string, string>, name: string) {
  const url = `https://r6-${Date.now()}-${Math.random().toString(16).slice(2)}.example.com`
  const r = await page.request.post('/api/v1/sources', {
    headers,
    data: {
      book_source_name: name,
      book_source_url: url,
      book_source_group: 'R6测试',
      enabled: true,
      rules: {
        search: { bookList: 'a', name: 'text', bookUrl: 'href' },
        toc: { chapterList: 'ul li', chapterName: 'a@text', chapterUrl: 'a@href' },
        content: { content: 'div#content@text' },
      },
    },
  })
  expect(r.ok()).toBeTruthy()
  return r.json()
}

test.describe('R3/R4 Parity', () => {
  test('single source create/export and 409 draft preservation with version sync', async ({ page }) => {
    test.skip(!username || !password, 'Isolated backend credentials required')
    const tracker = await attachErrorTracker(page)
    const headers = await login(page)
    const suffix = `${Date.now()}-${Math.random().toString(16).slice(2, 6)}`
    const sourceName = `R6单源_${suffix}`
    const sourceUrl = `https://r6-single-${suffix}.example.com`

    await page.getByRole('link', { name: '书源', exact: true }).click()
    await expect(page).toHaveURL(/\/admin$/)

    await page.getByRole('button', { name: '新建单个书源', exact: true }).click()
    await page.getByLabel('新建书源地址', { exact: true }).fill(sourceUrl)
    await page.getByLabel('新建书源名称', { exact: true }).fill(sourceName)
    await page.getByLabel('新建书源分组', { exact: true }).fill('R6分组')
    await page.getByLabel('新建书源规则 JSON', { exact: true }).fill(
      JSON.stringify({
        search: { bookList: 'a', name: 'text', bookUrl: 'href' },
        toc: { chapterList: 'ul li', chapterName: 'a@text', chapterUrl: 'a@href' },
        content: { content: 'div#content@text' },
      }),
    )
    await page.getByLabel('新建书源请求头 JSON', { exact: true }).fill(JSON.stringify({ 'User-Agent': 'YLReader-R6/1.0' }))
    await page.getByRole('button', { name: '创建书源', exact: true }).click()
    await expect(page.getByText(`已创建书源《${sourceName}》`, { exact: true })).toBeVisible()

    const downloadPromise = page.waitForEvent('download')
    await page.getByRole('button', { name: '导出 JSON', exact: true }).click()
    const download = await downloadPromise
    expect(download.suggestedFilename()).toMatch(/^ylreader-sources-\d{8}\.json$/)
    const filePath = await download.path()
    expect(filePath).toBeTruthy()
    const exported = JSON.parse(await readFile(filePath!, 'utf8')) as Array<{ bookSourceName?: string; bookSourceUrl?: string }>
    expect(exported.some(item => item.bookSourceName === sourceName && item.bookSourceUrl === sourceUrl)).toBe(true)
    await expect(page.getByText(/已导出 \d+ 个书源/)).toBeVisible()

    const listResp = await page.request.get('/api/v1/sources?page_size=100', { headers })
    expect(listResp.ok()).toBeTruthy()
    const listData = await listResp.json()
    const created = listData.items.find((s: { book_source_url: string }) => s.book_source_url === sourceUrl)
    expect(created).toBeTruthy()

    try {
      await page.goto(`/admin/sources/${created.id}`)
      await expect(page.getByLabel('书源名称', { exact: true })).toHaveValue(sourceName)

      // 外部并发更新制造 409 版本冲突
      const concurrent = await page.request.put(`/api/v1/sources/${created.id}`, {
        headers,
        data: { version: created.version, book_source_group: '外部同步分组' },
      })
      expect(concurrent.ok()).toBeTruthy()

      const draftName = `R6草稿保留_${suffix}`
      await page.getByLabel('书源名称', { exact: true }).fill(draftName)
      await page.getByRole('button', { name: '保存修改', exact: true }).click()
      await expect(page.getByText('书源已被其他操作修改，草稿已保留；请重新加载后合并修改', { exact: true })).toBeVisible()
      await expect(page.getByLabel('书源名称', { exact: true })).toHaveValue(draftName)

      // 同步最新版本号同时保留当前草稿再保存
      await page.getByRole('button', { name: '同步最新版本号（保留草稿）', exact: true }).click()
      await expect(page.getByText(/已同步服务端版本 v\d+（保留当前草稿，可重新保存）/)).toBeVisible()
      await expect(page.getByLabel('书源名称', { exact: true })).toHaveValue(draftName)

      await page.getByRole('button', { name: '保存修改', exact: true }).click()
      await expect(page.getByText('书源已保存', { exact: true })).toBeVisible()

      const updatedResp = await page.request.get(`/api/v1/sources/${created.id}`, { headers })
      expect(updatedResp.ok()).toBeTruthy()
      const updated = await updatedResp.json()
      expect(updated.book_source_name).toBe(draftName)
    } finally {
      await page.request.delete(`/api/v1/sources/${created.id}`, { headers })
    }

    await tracker.assertClean()
  })

  test('auth/me profile card and online book backup source mappings', async ({ page }) => {
    test.skip(!username || !password, 'Isolated backend credentials required')
    const tracker = await attachErrorTracker(page)
    const headers = await login(page)

    await page.getByRole('link', { name: '设置', exact: true }).click()
    await expect(page).toHaveURL(/\/settings$/)
    const profileCard = page.getByRole('region', { name: '当前账户信息' }).or(page.locator('section[aria-label="当前账户信息"]'))
    await expect(profileCard).toBeVisible()
    await expect(profileCard).toContainText(username!)
    await expect(profileCard).toContainText(/管理员|普通用户/)
    await expect(profileCard).toContainText(/v\d+/)

    const refreshMe = page.waitForResponse(r => r.url().endsWith('/api/v1/auth/me') && r.status() === 200)
    await page.getByRole('button', { name: '刷新账户信息', exact: true }).click()
    await refreshMe

    const srcA = await createSourceAPI(page, headers, `R6主源_${Date.now()}`)
    const srcB = await createSourceAPI(page, headers, `R6备用源_${Date.now()}`)
    const bookResp = await page.request.post('/api/v1/books', {
      headers,
      data: {
        source_id: srcA.id,
        name: `R6映射测试书_${Date.now()}`,
        author: 'R6作者',
        book_url: `${srcA.book_source_url}/book/1`,
      },
    })
    expect(bookResp.ok()).toBeTruthy()
    const book = await bookResp.json()

    try {
      await page.goto(`/books/${book.id}/edit`)
      const mappingSection = page.locator('section', { hasText: '备用书源绑定' })
      await expect(mappingSection).toBeVisible()

      const activeRow = mappingSection.locator('.mapping-row', { hasText: '当前激活' })
      await expect(activeRow).toBeVisible()
      await expect(activeRow.getByRole('button', { name: '移除', exact: true })).toBeDisabled()

      await mappingSection.locator('.el-select').click()
      await page.getByRole('option', { name: srcB.book_source_name, exact: true }).click()
      await mappingSection.getByLabel('备用书源书籍地址', { exact: true }).fill(`${srcB.book_source_url}/book/backup`)
      await mappingSection.getByRole('button', { name: '追加备用源', exact: true }).click()

      await expect(mappingSection.getByText('已添加备用书源绑定', { exact: true })).toBeVisible()
      const backupRow = mappingSection.locator('.mapping-row', { hasText: `${srcB.book_source_url}/book/backup` })
      await expect(backupRow).toBeVisible()

      await backupRow.getByRole('button', { name: '移除', exact: true }).click()
      await expect(mappingSection.getByText('已移除备用书源绑定', { exact: true })).toBeVisible()
      await expect(backupRow).toHaveCount(0)
    } finally {
      await page.request.delete(`/api/v1/books/${book.id}`, { headers })
      await page.request.delete(`/api/v1/sources/${srcA.id}`, { headers })
      await page.request.delete(`/api/v1/sources/${srcB.id}`, { headers })
    }

    await tracker.assertClean()
  })

  test('native dialog chapter override trial fetch, preview, and clear', async ({ page }) => {
    test.skip(!username || !password, 'Isolated backend credentials required')
    const tracker = await attachErrorTracker(page)
    const headers = await login(page)

    const srcA = await createSourceAPI(page, headers, `R6阅读主源_${Date.now()}`)
    const srcB = await createSourceAPI(page, headers, `R6覆盖目标源_${Date.now()}`)
    const bookResp = await page.request.post('/api/v1/books', {
      headers,
      data: {
        source_id: srcA.id,
        name: `R6单章覆盖书_${Date.now()}`,
        author: 'R6作者',
        book_url: `${srcA.book_source_url}/book/ov`,
      },
    })
    expect(bookResp.ok()).toBeTruthy()
    const book = await bookResp.json()

    let overridden = false
    await page.route(`**/api/v1/books/${book.id}/chapters?*`, route =>
      route.fulfill({
        json: {
          book_id: book.id,
          source_id: srcA.id,
          total: 2,
          items: [0, 1].map(i => ({
            id: i + 1,
            chapter_index: i,
            sort_order: i,
            title: `第${i + 1}章 覆盖验证`,
            normalized_title: `第${i + 1}章覆盖验证`,
            url: `${srcA.book_source_url}/c/${i}`,
            is_volume: false,
            word_count: 20,
          })),
        },
      }),
    )

    await page.route(`**/api/v1/books/${book.id}/content?*`, route => {
      const i = Number(new URL(route.request().url()).searchParams.get('chapter_index'))
      const useOverride = overridden && i === 0
      return route.fulfill({
        json: {
          chapter: {
            id: i + 1,
            chapter_index: i,
            sort_order: i,
            title: `第${i + 1}章 覆盖验证`,
            normalized_title: `第${i + 1}章覆盖验证`,
            url: `${srcA.book_source_url}/c/${i}`,
            is_volume: false,
            word_count: 20,
          },
          content: useOverride ? '覆盖源第一章正文内容。' : `主源第${i + 1}章正文内容。`,
          cached: false,
          word_count: 20,
          source_id: useOverride ? srcB.id : srcA.id,
          is_overridden: useOverride,
          prev_chapter_index: i > 0 ? i - 1 : null,
          next_chapter_index: i === 0 ? 1 : null,
        },
      })
    })

    await page.route(`**/api/v1/books/${book.id}/chapters/0/override`, async route => {
      const method = route.request().method()
      if (method === 'POST') {
        const body = route.request().postDataJSON() as { chapter_url: string; source_id?: string }
        // 放行内网/回环 URL 到真实后端以验证 SSRF 拦截；仅合成外部覆盖目标站点响应。
        if (body.chapter_url.includes('127.0.0.1')) {
          return route.fallback()
        }
        overridden = true
        return route.fulfill({
          json: {
            id: '00000000-0000-4000-8000-000000000099',
            book_id: book.id,
            chapter_index: 0,
            source_id: body.source_id ?? srcB.id,
            source_name: srcB.book_source_name,
            chapter_url: body.chapter_url,
            status: 'active',
            content_preview: '覆盖源第一章试抓预览文本……',
          },
        })
      }
      if (method === 'DELETE') {
        overridden = false
        return route.fulfill({ status: 204, body: '' })
      }
      return route.fallback()
    })

    try {
      await page.goto(`/reader/${book.id}`)
      await expect(page.getByLabel('章节阅读区')).toHaveAttribute('aria-busy', 'false')
      await expect(page.locator('.chapter-body')).toHaveText('主源第1章正文内容。')

      // 确认核心阅读器页面不加载 Element Plus 样式
      const hasElementStyles = await page.evaluate(() =>
        Array.from(document.styleSheets).some(sheet => {
          try {
            return Array.from(sheet.cssRules).some(rule => rule.cssText.includes('.el-dialog') || rule.cssText.includes('.el-button'))
          } catch {
            return false
          }
        }),
      )
      expect(hasElementStyles).toBe(false)

      await page.getByRole('button', { name: '单章换源', exact: true }).click()
      const dialog = page.locator('dialog.override-dialog')
      await expect(dialog).toBeVisible()
      expect(await dialog.evaluate(el => el.tagName)).toBe('DIALOG')

      // 1. 验证真实后端对私有回环地址的 SSRF 拦截
      await dialog.getByLabel('目标章节 URL').fill('http://127.0.0.1:8080/ssrf-check')
      await dialog.getByRole('button', { name: '试抓并应用覆盖', exact: true }).click()
      await expect(dialog.getByRole('alert')).toBeVisible()

      // 2. 应用外部章节覆盖并查看预览与正文更新
      await dialog.getByLabel('目标章节 URL').fill(`${srcB.book_source_url}/c/override-0`)
      await dialog.locator('select').selectOption(srcB.id)
      await dialog.getByRole('button', { name: '试抓并应用覆盖', exact: true }).click()

      const preview = dialog.getByRole('region', { name: '试抓正文预览' }).or(dialog.locator('section[aria-label="试抓正文预览"]'))
      await expect(preview).toContainText('覆盖源第一章试抓预览文本')
      await dialog.getByRole('button', { name: '关闭', exact: true }).click()
      await expect(dialog).toBeHidden()

      await expect(page.locator('.chapter-body')).toHaveText('覆盖源第一章正文内容。')
      await expect(page.locator('.reader-kicker')).toContainText('已单章换源')
      await expect(page.getByRole('button', { name: '单章覆盖中', exact: true })).toBeVisible()

      // 3. 打开并一键清除本章覆盖
      await page.getByRole('button', { name: '单章覆盖中', exact: true }).click()
      await expect(dialog.getByText('本章当前正在使用单章源覆盖', { exact: true })).toBeVisible()
      await dialog.getByRole('button', { name: '清除本章覆盖', exact: true }).click()
      await expect(dialog).toBeHidden()
      await expect(page.locator('.chapter-body')).toHaveText('主源第1章正文内容。')
      await expect(page.getByRole('button', { name: '单章换源', exact: true })).toBeVisible()
    } finally {
      await page.request.delete(`/api/v1/books/${book.id}`, { headers })
      await page.request.delete(`/api/v1/sources/${srcA.id}`, { headers })
      await page.request.delete(`/api/v1/sources/${srcB.id}`, { headers })
    }

    await tracker.assertClean()
  })

  test('PWA manifest, service worker shell cache isolation, and offline reload', async ({ page }) => {
    const tracker = await attachErrorTracker(page)

    const manifestResp = await page.request.get('/manifest.webmanifest')
    expect(manifestResp.ok()).toBeTruthy()
    const manifest = await manifestResp.json()
    expect(manifest.name).toBe('YLReader')
    expect(manifest.display).toBe('standalone')
    expect(manifest.start_url).toBe('/bookshelf')

    const swResp = await page.request.get('/sw.js')
    expect(swResp.ok()).toBeTruthy()
    const swText = await swResp.text()
    expect(swText).toContain("url.pathname.startsWith('/api/')")

    await page.goto('/login')
    await expect(page.getByRole('button', { name: '登录书房' })).toBeVisible()

    // 生产预览模式下由 pwa.ts 自动注册；开发模式下显式注册 /sw.js 以检验缓存不变量。
    const cacheInfo = await page.evaluate(async () => {
      if (!('serviceWorker' in navigator)) return { supported: false, keys: [] as string[], urls: [] as string[] }
      let reg = await navigator.serviceWorker.getRegistration('/')
      if (!reg) {
        reg = await navigator.serviceWorker.register('/sw.js', { scope: '/' })
      }
      await navigator.serviceWorker.ready
      const keys = await caches.keys()
      const urls: string[] = []
      for (const k of keys) {
        const cache = await caches.open(k)
        const reqs = await cache.keys()
        for (const req of reqs) urls.push(new URL(req.url).pathname)
      }
      return { supported: true, keys, urls }
    })

    expect(cacheInfo.supported).toBe(true)
    expect(cacheInfo.keys.some(k => k.startsWith('ylreader-shell-'))).toBe(true)
    expect(cacheInfo.urls).toContain('/index.html')
    expect(cacheInfo.urls).toContain('/manifest.webmanifest')
    expect(cacheInfo.urls.some(u => u.startsWith('/api/'))).toBe(false)

    // 等待 Service Worker 激活并接管当前页面控制权
    await expect.poll(() => page.evaluate(() => Boolean(navigator.serviceWorker.controller))).toBe(true)

    // 在生产构建预览（静态资源带指纹预缓存）下验证断网重载应用壳仍可渲染。
    if (process.env.E2E_PRODUCTION === '1' || process.env.E2E_READING_PREVIEW === '1' || process.env.E2E_BASE_URL) {
      await page.context().setOffline(true)
      try {
        await page.reload({ waitUntil: 'domcontentloaded' })
        await expect(page.getByRole('button', { name: '登录书房' })).toBeVisible()
      } finally {
        await page.context().setOffline(false)
      }
    }

    await tracker.assertClean()
  })
})
