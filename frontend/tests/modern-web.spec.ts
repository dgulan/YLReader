import { test, expect, type Page } from '@playwright/test'
import { mkdir } from 'node:fs/promises'

const bookId = '20000000-0000-4000-8000-000000000001'
const sourceId = '10000000-0000-4000-8000-000000000001'

// UI-only fixture: all /api requests are intercepted; no database or real session.
async function readerFixture(page: Page, bookType: 'local' | 'online' = 'local') {
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
  const book = { id: bookId, name: '用于小屏布局和长目录验收的很长书籍名称', book_type: bookType, author: 'UI fixture', active_source_id: sourceId, active_book_url: `local:${bookId}`, total_chapters: 1000, intro: '这是一段可完整阅读的作品简介。', status: 'completed', latest_chapter_title: '第1000章' }
  await page.route('**/api/v1/**', async route => {
    const url = new URL(route.request().url()), path = url.pathname.replace('/api/v1', '')
    if (path === '/settings') {
      if (route.request().method() === 'PATCH') settings = { ...settings, ...route.request().postDataJSON() }
      return route.fulfill({ json: settings })
    }
    if (path === `/books/${bookId}`) return route.fulfill({ json: { ...book, progress } })
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
  return book
}

test('mobile login keeps 16px rounded inputs and shows focus', async ({ page }) => {
  await page.setViewportSize({ width: 320, height: 640 })
  await page.goto('/login')
  for (const label of ['用户名', '密码']) {
    const input = page.getByLabel(label, { exact: true })
    const wrapper = input.locator('..')
    const before = await wrapper.evaluate(node => getComputedStyle(node).boxShadow)
    await input.focus()
    await expect(input).toHaveCSS('font-size', '16px')
    await expect(wrapper).toHaveCSS('border-radius', '12px')
    await expect(wrapper).not.toHaveCSS('box-shadow', before)
  }
  await expect(page.getByRole('button', { name: '登录书房' })).toHaveCSS('border-radius', '12px')
})

test('mobile source switcher keeps duration, action and the second chapter row within each card', async ({ page }) => {
  await page.setViewportSize({ width: 320, height: 640 })
  const book = await readerFixture(page, 'online')
  await page.route(`**/api/v1/books/${bookId}/mappings`, route => route.fulfill({ json: [{ source_id: sourceId, source_name: '当前书源', source_host: 'current.example', total_chapters: 1000 }] }))
  const group = {
    source_id: 'backup', source_name: '备用书源', source_host: 'www.backup.example', elapsed_ms: 659,
    items: [{ source_id: 'backup', name: book.name, author: book.author, book_url: 'https://www.backup.example/books/1', latest_chapter: '第1001章 ' + '很长的章节标题'.repeat(10) }],
  }
  await page.route('**/api/v1/search?*', route => {
    expect(route.request().headers().accept).toBe('application/x-ndjson')
    expect(new URL(route.request().url()).searchParams.get('scan')).toBe('true')
    return route.fulfill({ contentType: 'application/x-ndjson', body: [
      { type: 'group', group }, { type: 'probe', searched: 2 },
      { type: 'done', result: { groups: [group], total: 1, failures: [], keyword: book.name } },
    ].map(event => JSON.stringify(event)).join('\n') + '\n' })
  })
  await page.getByRole('button', { name: '换源', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: '切换书源' })
  await dialog.getByRole('button', { name: '刷新书源' }).click()
  const card = dialog.getByRole('button', { name: /备用书源/ })
  await expect(card.locator('.source-host')).toHaveText('www.backup.example')
  await expect(card.locator('.source-latency')).toHaveText('659ms')
  await expect(card.locator('.source-action')).toHaveText('预览切换')
  await expect(dialog.getByText('已探 2 源')).toBeVisible()
  const bounds = (await card.boundingBox())!
  const latency = (await card.locator('.source-latency').boundingBox())!
  const chapter = (await card.locator('.source-chapter').boundingBox())!
  expect(latency.x + latency.width).toBeLessThan(bounds.x + bounds.width)
  expect(chapter.y).toBeGreaterThan(latency.y)
  await expect(card.locator('.source-chapter')).toHaveAttribute('title', group.items[0]!.latest_chapter)
})

test('thousand chapter directory supports keyboard navigation and native modal focus', async ({ page }) => {
  await readerFixture(page)
  const trigger = page.getByRole('button', { name: '目录', exact: true })
  await trigger.click()
  const dialog = page.getByRole('dialog', { name: '章节目录', exact: true })
  const list = dialog.getByRole('list', { name: '章节目录列表' })
  await expect(dialog.getByRole('button', { name: '关闭', exact: true })).toBeFocused()
  await expect(list.getByRole('button', { name: '第500章', exact: true })).toBeVisible()
  expect(await list.getByRole('button').count()).toBeLessThanOrEqual(13)
  for (let i = 0; i < 4; i++) await page.keyboard.press('Tab')
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
  for (let i = 0; i < 4; i++) await page.keyboard.press('Tab')
  await page.keyboard.press('End')
  await page.keyboard.press('Enter')
  await expect(dialog).not.toBeVisible()
  await expect(page.getByRole('heading', { name: '第1000章', exact: true })).toBeVisible()
})

test('small screens keep native controls and modal actions inside the viewport', async ({ page }) => {
  await page.setViewportSize({ width: 320, height: 640 })
  await readerFixture(page)
  // 主题、字号、书签、保存进度都收纳进了「设置」面板。
  await page.getByRole('button', { name: '设置', exact: true }).click()
  await expect(page.locator('#reader-settings')).toBeVisible()
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

test('floating bars preserve the text position and reappear at the chapter end', async ({ page }) => {
  await readerFixture(page)
  const viewport = page.getByLabel('章节阅读区')
  const header = page.locator('.reader-header')
  const footer = page.locator('.reader-footer')
  const text = page.locator('.chapter-paragraph').first()
  const geometry = () => viewport.evaluate(el => ({
    scroll: el.scrollTop, height: el.clientHeight,
    textTop: el.querySelector('.chapter-paragraph')!.getBoundingClientRect().top,
  }))
  await viewport.evaluate(el => { el.scrollTop = 240 })
  const before = await geometry()
  await viewport.click({ position: { x: 200, y: 300 } })
  await expect(header).toBeHidden()
  await expect(footer).toBeHidden()
  expect(await geometry()).toEqual(before)
  await viewport.evaluate(el => { el.scrollTop = el.scrollHeight })
  await expect(footer).toBeVisible()
  await expect(header).toBeHidden()
  await expect(page.getByRole('button', { name: '下一章' })).toBeEnabled()
  await viewport.evaluate(el => { el.scrollTop = 0 })
  await expect(footer).toBeHidden()
  await expect(text).toBeVisible()
})

test('chapter search filters titles and can reveal the active chapter in reverse order', async ({ page }) => {
  await page.setViewportSize({ width: 320, height: 640 })
  await readerFixture(page)
  await page.getByRole('button', { name: '目录', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: '章节目录', exact: true })
  const search = dialog.getByRole('searchbox', { name: '搜索章节' })
  expect(await search.evaluate(input => getComputedStyle(input).fontSize)).toBe('16px')
  await search.fill('第1000章')
  await expect(dialog.getByRole('listitem')).toHaveCount(1)
  await expect(dialog.getByRole('button', { name: '第1000章', exact: true })).toBeVisible()
  await dialog.getByRole('button', { name: '正序' }).click()
  await dialog.getByRole('button', { name: '定位当前' }).click()
  await expect(search).toHaveValue('')
  await expect(dialog.getByRole('button', { name: '第500章', exact: true })).toHaveAttribute('aria-current', 'true')
})

test('small bookshelf opens book management from the cover menu and groups search sources', async ({ page }) => {
  await page.setViewportSize({ width: 320, height: 640 })
  await readerFixture(page)
  const first = { source_id: 'one', name: '测试聚合书名', author: '测试作者', book_url: 'https://first.example/book/1' }
  const groups = [
    { source_id: 'one', source_name: '快源', items: [first] },
    { source_id: 'two', source_name: '另一个书源', items: [{ ...first, source_id: 'two', book_url: 'https://second.example/book/1' }] },
  ]
  await page.route('**/api/v1/search?*', route => {
    expect(route.request().headers().accept).toBe('application/x-ndjson')
    const events = [ ...groups.map(group => ({ type: 'group', group })), {
      type: 'done', result: { keyword: first.name, groups, total: 2, failures: [] },
    } ]
    return route.fulfill({ contentType: 'application/x-ndjson', body: events.map(event => JSON.stringify(event)).join('\n') + '\n' })
  })
  await page.getByRole('link', { name: '返回书架' }).click()
  await expect(page.getByRole('navigation', { name: '主导航' })).toBeVisible()
  await expect(page.getByText('＋ 导入本地书', { exact: true })).toBeVisible()
  const filter = page.getByRole('textbox', { name: '筛选书架' })
  expect(await filter.evaluate(input => getComputedStyle(input).fontSize)).toBe('16px')
  const card = page.locator('.book-card').first()
  await expect(card.getByRole('button', { name: '管理书籍', exact: true })).toBeHidden()
  await card.locator('summary').click()
  await expect(card.getByRole('button', { name: '管理书籍', exact: true })).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(card.getByRole('button', { name: '管理书籍', exact: true })).toBeHidden()
  await card.locator('summary').click()
  const menu = await card.locator('.book-menu-items').boundingBox()
  const cover = await card.locator('.cover-frame').boundingBox()
  expect(menu!.x).toBeGreaterThanOrEqual(cover!.x)
  expect(menu!.x + menu!.width).toBeLessThanOrEqual(320 - 14)
  await card.getByRole('button', { name: '管理书籍', exact: true }).click()
  const editor = page.getByRole('dialog', { name: /^管理书籍 ·/ })
  await expect(editor.getByRole('tab', { name: '概览' })).toHaveAttribute('aria-selected', 'true')
  await expect(editor.getByRole('heading', { name: '作品简介' })).toBeVisible()
  await expect(editor.getByText('这是一段可完整阅读的作品简介。')).toBeVisible()
  await expect(editor.getByRole('heading', { name: '当前读至：第 500 章' })).toBeVisible()
  await expect(editor.getByLabel('编辑书名')).toBeHidden()
  await editor.getByRole('tab', { name: '编辑与绑定' }).click()
  await expect(editor.getByLabel('编辑书名')).toHaveValue('用于小屏布局和长目录验收的很长书籍名称')
  await expect(page).toHaveURL(/\/bookshelf$/)
  await page.keyboard.press('Escape')
  await expect(editor).toBeHidden()
  await page.getByRole('tab', { name: '发现新书' }).click()
  const search = page.getByRole('textbox', { name: '搜索书籍' })
  expect(await search.evaluate(input => getComputedStyle(input).fontSize)).toBe('16px')
  await search.fill(first.name)
  await page.getByRole('button', { name: '搜索', exact: true }).click()
  await expect(page.getByRole('heading', { name: first.name, exact: true })).toHaveCount(1)
  await expect(page.getByRole('combobox', { name: first.name + ' 的书源' }).getByRole('option')).toHaveCount(2)
})
