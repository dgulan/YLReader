import { expect, test } from '@playwright/test'

test('source editor protects a modified draft and allows an explicit discard', async ({ page }) => {
  const sourceId = '10000000-0000-4000-8000-000000000001'
  const source = { id: sourceId, version: 1, book_source_name: 'Synthetic Source', book_source_group: '默认', book_source_url: 'https://example.com', book_source_type: 0, enabled: true, enabled_explore: true, rules: {}, header: {} }
  await page.addInitScript(() => sessionStorage.setItem('yreader-session', JSON.stringify({ access_token: 'ui-only', refresh_token: 'ui-refresh-only', user: { id: 'ui-owner', username: 'synthetic' } })))
  await page.route('**/api/v1/**', route => {
    const path = new URL(route.request().url()).pathname
    if (path === `/api/v1/sources/${sourceId}`) return route.fulfill({ json: source })
    if (path === '/api/v1/settings') return route.fulfill({ json: {} })
    if (path === '/api/v1/sources') return route.fulfill({ json: { items: [source], total: 1, page: 1, page_size: 20 } })
    throw new Error(`Unexpected synthetic route: ${path}`)
  })
  await page.goto(`/admin/sources/${sourceId}`)
  await expect(page.getByLabel('书源名称', { exact: true })).toHaveValue('Synthetic Source')
  await page.getByLabel('书源名称', { exact: true }).fill('Unsaved source')
  await page.getByRole('link', { name: '← 书源列表', exact: true }).click()
  await page.getByRole('button', { name: '继续编辑', exact: true }).click()
  await expect(page).toHaveURL(new RegExp(sourceId))
  await expect(page.getByLabel('书源名称', { exact: true })).toHaveValue('Unsaved source')
  await page.getByRole('link', { name: '← 书源列表', exact: true }).click()
  await page.getByRole('button', { name: '丢弃草稿并离开', exact: true }).click()
  await expect(page).toHaveURL(/\/admin$/)
})
