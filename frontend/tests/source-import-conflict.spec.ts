import { expect, test } from '@playwright/test'

for (const overwrite of [false, true]) {
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
      submissions.push(body)
      if (body.conflict_mode === 'overwrite') {
        return route.fulfill({ json: { imported: 0, updated: 1, overwritten_count: 1, skipped: 0, failed: [], dry_run: false } })
      }
      return route.fulfill({ json: { imported: 1, updated: 0, skipped: 1, failed: [], dry_run: false,
        duplicate_count: 1, duplicates: [{ book_source_url: old.bookSourceUrl, book_source_name: '旧书源示例' }] } })
    })
    await page.goto('/admin')
    await page.getByRole('textbox', { name: '书源 JSON', exact: true }).fill(JSON.stringify([old, added]))
    await page.getByRole('button', { name: '导入', exact: true }).click()
    const dialog = page.getByRole('dialog')
    await expect(dialog).toContainText('旧书源示例')
    expect(submissions.map(entry => entry.conflict_mode)).toEqual(['skip'])
    await dialog.getByRole('button', { name: overwrite ? '覆盖旧书源' : '跳过重复' }).click()
    await expect(dialog).toBeHidden()
    if (overwrite) {
      await expect(page.getByText(/覆盖更新 1 条/)).toBeVisible()
      expect(submissions.map(entry => entry.conflict_mode)).toEqual(['skip', 'overwrite'])
      expect(JSON.parse(submissions[1]!.data)).toEqual([old])
    } else {
      expect(submissions).toHaveLength(1)
    }
  })
}
