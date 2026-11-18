import { test, expect, type Page } from '@playwright/test'
import { mkdir } from 'node:fs/promises'

const bookId = '20000000-0000-4000-8000-000000000001'
const sourceId = '10000000-0000-4000-8000-000000000001'

// UI-only fixture: all /api requests are intercepted; no database or real session.
async function readerFixture(page: Page) {
  await mkdir('.test-artifacts', { recursive: true })
  await page.addInitScript(() => sessionStorage.setItem('yreader-session', JSON.stringify({
    access_token: 'synthetic-ui-only', refresh_token: 'synthetic-ui-only-refresh',
    user: { id: '00000000-0000-4000-8000-000000000001', username: 'ui-fixture' },
  })))
  let settings = { 'reader.theme': 'night', 'reader.font_size': 20 }
  let progress = { book_id: bookId, chapter_index: 499, char_offset: 0, chapter_ratio: 0, version: 7 }
  const chapter = (i: number) => ({ id: i + 1, chapter_index: i, sort_order: i, title: `第${i + 1}章`, normalized_title: `n:${i}`, url: `local:${bookId}:${i}`, is_volume: false, word_count: 1000 })
  const chapters = Array.from({ length: 1000 }, (_, i) => chapter(i))
  const rows = [...chapters.slice(0, 500), { ...chapter(500), id: 1001, chapter_index: null, is_volume: true, title: '第二卷' }, ...chapters.slice(500)]
  const book = { id: bookId, name: '用于小屏布局和长目录验收的很长书籍名称', book_type: 'local', author: 'UI fixture', active_source_id: sourceId, active_book_url: `local:${bookId}`, total_chapters: 1000 }
  await page.route('**/api/v1/**', async route => {
    const url = new URL(route.request().url()), path = url.pathname.replace('/api/v1', '')
    if (path === '/settings') {
      if (route.request().method() === 'PATCH') settings = { ...settings, ...route.request().postDataJSON() }
      return route.fulfill({ json: settings })
    }
    if (path === `/books/${bookId}`) return route.fulfill({ json: book })
    if (path === `/books/${bookId}/chapters`) return route.fulfill({ json: { book_id: bookId, source_id: sourceId, total: 1000, items: rows } })
    if (path === `/books/${bookId}/content`) {
      const i = Number(url.searchParams.get('chapter_index'))
      return route.fulfill({ json: { chapter: chapter(i), content: '中文正文 😀\n'.repeat(150), cached: false, word_count: 1000, source_id: sourceId, prev_chapter_index: i > 0 ? i - 1 : null, next_chapter_index: i < 999 ? i + 1 : null } })
    }
    if (path === `/books/${bookId}/progress`) {
      if (route.request().method() === 'PUT') progress = { ...progress, ...route.request().postDataJSON(), version: progress.version + 1 }
      return route.fulfill({ json: progress })
    }
    if (path === `/books/${bookId}/bookmarks`) return route.fulfill({ json: [] })
    if (path === '/books') return route.fulfill({ json: { items: [book], total: 1, page: 1, page_size: 20 } })
    throw new Error(`Unhandled synthetic API: ${route.request().method()} ${path}`)
  })
  await page.goto(`/reader/${bookId}`)
  await expect(page.getByLabel('章节阅读区')).toHaveAttribute('aria-busy', 'false')
  await expect(page.getByRole('heading', { name: '第500章', exact: true })).toBeVisible()
}

test('thousand chapter directory supports keyboard navigation and native modal focus', async ({ page }) => {
  await readerFixture(page)
  const trigger = page.getByRole('button', { name: '目录', exact: true })
  await trigger.click()
  const dialog = page.getByRole('dialog', { name: '章节目录', exact: true })
  const list = dialog.getByRole('list', { name: '章节目录列表' })
  await expect(dialog.getByRole('button', { name: '关闭', exact: true })).toBeFocused()
  await expect(list.getByRole('button', { name: '第500章', exact: true })).toBeVisible()
  expect(await list.getByRole('button').count()).toBeLessThanOrEqual(13)
  await page.keyboard.press('Tab')
  await expect(list.getByRole('button', { name: '第500章', exact: true })).toBeFocused()
  await page.keyboard.press('ArrowDown')
  await expect(list.getByRole('button', { name: '第501章', exact: true })).toBeFocused()
  await page.keyboard.press('End')
  await expect(list.getByRole('button', { name: '第1000章', exact: true })).toBeFocused()
  await expect(list.getByRole('listitem').last()).toHaveAttribute('aria-setsize', '1001')
  await page.screenshot({ path: '.test-artifacts/modern-web-directory.png' })
  await page.keyboard.press('Home')
  await expect(list.getByRole('button', { name: '第1章', exact: true })).toBeFocused()
  await page.keyboard.press('PageDown')
  await expect(list.getByRole('button', { name: '第9章', exact: true })).toBeFocused()
  await page.keyboard.press('PageUp')
  await expect(list.getByRole('button', { name: '第1章', exact: true })).toBeFocused()
  await page.keyboard.press('Escape')
  await expect(dialog).not.toBeVisible()
  await expect(trigger).toBeFocused()
  await trigger.click()
  await expect(list.getByRole('button', { name: '第500章', exact: true })).toBeVisible()
  await page.keyboard.press('Tab')
  await page.keyboard.press('End')
  await page.keyboard.press('Enter')
  await expect(dialog).not.toBeVisible()
  await expect(page.getByRole('heading', { name: '第1000章', exact: true })).toBeVisible()
})

test('small screens keep native controls and modal actions inside the viewport', async ({ page }) => {
  await page.setViewportSize({ width: 320, height: 640 })
  await readerFixture(page)
  const theme = page.getByLabel('阅读主题')
  expect(await theme.evaluate(el => getComputedStyle(el).colorScheme)).toBe('dark')
  await page.screenshot({ path: '.test-artifacts/modern-web-mobile-night.png' })
  await theme.selectOption('day')
  await expect(page.locator('.reader')).toHaveAttribute('data-theme', 'day')
  expect(await theme.evaluate(el => getComputedStyle(el).colorScheme)).toBe('light')
  for (const name of ['目录', '书签', '下一章', '保存进度']) {
    const bounds = await page.getByRole('button', { name, exact: true }).boundingBox()
    expect(bounds).toBeTruthy()
    expect(bounds!.height).toBeGreaterThanOrEqual(44)
    expect(bounds!.x).toBeGreaterThanOrEqual(0)
    expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(320)
  }
  await page.getByRole('button', { name: '目录', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: '章节目录', exact: true })
  const bounds = await dialog.boundingBox()
  expect(bounds!.y).toBeGreaterThanOrEqual(0)
  expect(bounds!.y + bounds!.height).toBeLessThanOrEqual(640)
  expect(await dialog.evaluate(el => getComputedStyle(el).overscrollBehaviorY)).toBe('contain')
  await page.screenshot({ path: '.test-artifacts/modern-web-mobile-dialog.png' })
  await page.keyboard.press('Escape')
  await page.getByRole('button', { name: '书签', exact: true }).click()
  await expect(page.getByRole('dialog', { name: '阅读书签' })).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(page.getByRole('button', { name: '书签', exact: true })).toBeFocused()
  await page.setViewportSize({ width: 812, height: 375 })
  await page.getByRole('button', { name: '目录', exact: true }).click()
  const landscape = await dialog.boundingBox()
  expect(landscape!.y).toBeGreaterThanOrEqual(0)
  expect(landscape!.y + landscape!.height).toBeLessThanOrEqual(375)
  await expect(dialog.getByRole('button', { name: '关闭', exact: true })).toBeInViewport()
  await expect(dialog.getByRole('button', { name: '第500章', exact: true })).toBeInViewport()
})

test('production reader avoids management CSS until navigating to the bookshelf', async ({ page }) => {
  test.skip(!process.env.E2E_PRODUCTION, 'Requires the production preview build')
  const css: string[] = []
  page.on('response', response => { if (new URL(response.url()).pathname.endsWith('.css')) css.push(response.url()) })
  await readerFixture(page)
  const hasElementStyles = () => page.evaluate(() => Array.from(document.styleSheets).some(sheet => {
    try { return Array.from(sheet.cssRules).some(rule => rule.cssText.includes('.el-button')) } catch { return false }
  }))
  expect(await hasElementStyles()).toBe(false)
  const before = css.length
  await page.getByRole('link', { name: '返回书架' }).click()
  await expect(page).toHaveURL(/bookshelf/)
  await expect(page.getByRole('heading', { name: /^我的书房/ })).toBeVisible()
  expect(css.length).toBeGreaterThan(before)
  expect(await hasElementStyles()).toBe(true)
})
