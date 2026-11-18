import { test, expect, devices } from '@playwright/test'
import { writeFile } from 'node:fs/promises'
import { loginToShelf } from './login-diagnostics'

test.skip(!process.env.E2E_USERNAME || !process.env.E2E_PASSWORD, 'Requires owned temporary backend')
test('cold login reaches the shelf in desktop and mobile sessions', async ({ browser }) => {
  test.setTimeout(120000)
  const desktop = await browser.newContext(), mobile = await browser.newContext({ ...devices['iPhone 15 Pro'] })
  const events: { at: number; context: string; event: string; detail: unknown }[] = []
  const errors: string[] = []
  const report = { mode: 'login_navigation', completed_logins: 0, events, page_errors: errors, physical_device: false }
  try {
    for (const [name, context] of [['desktop', desktop], ['mobile', mobile]] as const) {
      const page = await context.newPage()
      page.on('pageerror', error => errors.push(error.message))
      await loginToShelf(page, (event, detail) => events.push({ at: Date.now(), context: name, event, detail }))
      report.completed_logins++
    }
    expect(errors).toEqual([])
  } finally {
    await writeFile(test.info().outputPath('reading-diagnostics.json'), JSON.stringify(report, null, 2))
    await desktop.close(); await mobile.close()
  }
})
