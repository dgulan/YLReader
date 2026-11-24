import { expect, test } from '@playwright/test'

for (const overwrite of [undefined, false, true]) {
  test(`source import duplicate dialog decision: ${overwrite}`, async ({ page }) => {
    await page.addInitScript(() => sessionStorage.setItem('yreader-session', JSON.stringify({
      access_token: 'synthetic', refresh_token: 'synthetic-refresh', user: { id: 'synthetic-reader' },
    })))
    const old = { bookSourceUrl: 'https://old.example/', bookSourceName: '更新规则' }
    const added = { bookSourceUrl: 'https://new.example/', bookSourceName: '新书源' }
    const submissions: { conflict_mode: string; data: string }[] = []
    await page.route('**/api/v1/settings', route => route.fulfill({ json: {} }))
    await page.route('**/api/v1/sources**', route => {
      if (route.request().method() === 'GET') return route.fulfill({ json: { items: [], page: 1, page_size: 100, total: 0 } })
      const body = route.request().postDataJSON()
      if (body.dry_run) {
        return route.fulfill({ json: { imported: 1, updated: 0, skipped: 0, failed: [], dry_run: true } })
      }
      submissions.push(body)
      if (overwrite === undefined) {
        return route.fulfill({ json: { imported: 1, updated: 0, skipped: 0, failed: [], dry_run: false, duplicate_count: 0 } })
      }
      if (body.conflict_mode === 'overwrite') {
        return route.fulfill({ json: { imported: 0, updated: 1, overwritten_count: 1, skipped: 0, failed: [], dry_run: false } })
      }
      return route.fulfill({ json: { imported: 1, updated: 0, skipped: 1, failed: [], dry_run: false,
        duplicate_count: 1, duplicates: [{ book_source_url: old.bookSourceUrl, book_source_name: '旧书源示例' }] } })
    })
    await page.goto('/admin')
    await page.getByRole('textbox', { name: '书源 JSON', exact: true }).fill(JSON.stringify([old, added]))
    const commit = page.getByRole('button', { name: '确认导入', exact: true })
    await expect(commit).toBeDisabled()
    await page.getByRole('button', { name: '预检', exact: true }).click()
    await expect(commit).toBeEnabled()
    await commit.click()
    const dialog = page.getByRole('dialog')
    if (overwrite === undefined) {
      await expect(page.getByText('已导入 1，跳过 0，失败 0。', { exact: true })).toBeVisible()
      await expect(dialog).toBeHidden()
      expect(submissions).toHaveLength(1)
      return
    }
    await expect(dialog).toContainText('旧书源示例')
    expect(submissions.map(entry => entry.conflict_mode)).toEqual(['skip'])
    await dialog.getByRole('button', { name: overwrite ? '覆盖旧书源' : '跳过重复' }).click()
    await expect(dialog).toBeHidden()
    if (overwrite) {
      await expect(page.getByText(/覆盖更新 1 条/)).toBeVisible()
      await expect(page.getByText('已导入 1，跳过 0，失败 0。', { exact: true })).toBeVisible()
      expect(submissions.map(entry => entry.conflict_mode)).toEqual(['skip', 'overwrite'])
      expect(JSON.parse(submissions[1]!.data)).toEqual([old])
    } else {
      expect(submissions).toHaveLength(1)
    }
  })
}

for (const overwrite of [false, true]) {
  test(`online source import duplicate dialog decision: ${overwrite}`, async ({ page }) => {
    await page.addInitScript(() => sessionStorage.setItem('yreader-session', JSON.stringify({
      access_token: 'synthetic', refresh_token: 'synthetic-refresh', user: { id: 'synthetic-reader' },
    })))
    const url = 'https://example.com/sources.json', writes: string[] = []
    await page.route('**/api/v1/settings', route => route.fulfill({ json: {} }))
    await page.route('**/api/v1/sources?*', route => route.fulfill({ json: { items: [], page: 1, page_size: 100, total: 0 } }))
    await page.route('**/api/v1/sources/import-url', route => {
      const body = route.request().postDataJSON()
      expect(body.url).toBe(url)
      if (body.dry_run) return route.fulfill({ json: {
        dry_run: true, imported: 1, skipped: 1, updated: 0, failed: [],
        duplicate_count: 1, duplicates: [{ book_source_url: 'https://old.example/', book_source_name: '旧在线书源' }],
      } })
      writes.push(body.conflict_mode)
      return route.fulfill({ json: {
        dry_run: false, imported: 1, skipped: overwrite ? 0 : 1, updated: overwrite ? 1 : 0, failed: [],
      } })
    })
    await page.goto('/admin')
    await page.getByRole('textbox', { name: '在线书源网址', exact: true }).fill(url)
    await page.getByRole('button', { name: '网址预检' }).click()
    await expect(page.getByText('预检：可导入 1，跳过 1，失败 0。确认后才会保存。', { exact: true })).toBeVisible()
    await page.getByRole('button', { name: '导入在线书源' }).click()
    const dialog = page.getByRole('dialog')
    await expect(dialog).toContainText('旧在线书源')
    expect(writes).toEqual([])
    await dialog.getByRole('button', { name: overwrite ? '覆盖旧书源' : '跳过重复' }).click()
    await expect(page.getByText(`已导入 1，跳过 ${overwrite ? 0 : 1}，失败 0。`, { exact: true })).toBeVisible()
    if (overwrite) await expect(page.getByText('覆盖更新 1 条。', { exact: true })).toBeVisible()
    expect(writes).toEqual([overwrite ? 'overwrite' : 'skip'])
  })
}

test('mobile source cards support selecting and deleting the current page', async ({ page }) => {
  await page.setViewportSize({ width: 320, height: 640 })
  await page.addInitScript(() => sessionStorage.setItem('yreader-session', JSON.stringify({
    access_token: 'synthetic', refresh_token: 'synthetic-refresh', user: { id: 'synthetic-reader' },
  })))
  let items = [
    { id: 'local', book_source_type: 3, book_source_name: '本地书库', enabled: true },
    { id: 'first', book_source_type: 0, book_source_name: '第一书源', book_source_url: 'https://first.example/path', book_source_group: '试用', enabled: true },
    { id: 'second', book_source_type: 0, book_source_name: '第二书源', book_source_url: 'https://second.example/path', enabled: false },
  ]
  const deleted: string[] = []
  await page.route('**/api/v1/settings', route => route.fulfill({ json: {} }))
  await page.route('**/api/v1/sources**', route => {
    if (route.request().method() === 'DELETE') {
      const id = new URL(route.request().url()).pathname.split('/').at(-1)!
      deleted.push(id)
      items = items.filter(item => item.id !== id)
      return route.fulfill({ status: 204 })
    }
    return route.fulfill({ json: { items, total: items.length, page: 1, page_size: 100 } })
  })
  await page.goto('/admin')
  const filter = page.getByRole('textbox', { name: '筛选书源' })
  expect(await filter.evaluate(input => getComputedStyle(input).fontSize)).toBe('16px')
  await expect(page.getByText('first.example · 试用', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: '多选', exact: true }).click()
  await page.getByRole('button', { name: '全选本页' }).click()
  await expect(page.getByRole('checkbox', { name: '选择 本地书库' })).toBeDisabled()
  await page.getByRole('button', { name: '删除所选 (2)', exact: true }).click()
  await page.getByRole('dialog').getByRole('button', { name: '取消' }).click()
  expect(deleted).toEqual([])
  await page.getByRole('button', { name: '删除所选 (2)', exact: true }).click()
  await page.getByRole('dialog').getByRole('button', { name: '确认删除' }).click()
  await expect(page.getByText('已删除 2 个书源', { exact: true })).toBeVisible()
  await expect(page.getByText('第一书源', { exact: true })).toHaveCount(0)
  expect(deleted).toEqual(['first', 'second'])
})
