import { expect, type Page } from '@playwright/test'

type RecordEvent = (event: string, detail: unknown) => void

export async function loginToShelf(page: Page, record: RecordEvent): Promise<number> {
  let count = 0
  const log: RecordEvent = (event, detail) => { if (++count <= 300) record(event, detail) }
  for (const event of ['request', 'requestfinished', 'requestfailed'] as const) {
    page.on(event, request => {
      const path = new URL(request.url()).pathname
      if (path.startsWith('/assets/') || path === '/api/v1/auth/login') log(event, { path })
    })
  }
  const diagnostic = process.env.E2E_LOGIN_DIAGNOSTICS === '1'
  if (diagnostic) {
    await page.exposeFunction('recordLoginDiagnostic', log)
    await page.addInitScript(() => {
      const record = (window as unknown as { recordLoginDiagnostic: (event: string, detail: unknown) => Promise<void> }).recordLoginDiagnostic
      const note = (event: string, detail: unknown = {}) => { void record(event, detail).catch(() => {}) }
      window.addEventListener('load', event => {
        if (event.target instanceof HTMLLinkElement && event.target.href) note('link-loaded', { path: new URL(event.target.href).pathname })
      }, true)
      const appendBefore = Node.prototype.appendChild
      Node.prototype.appendChild = function <T extends Node>(node: T): T {
        if (this === document.head && node instanceof HTMLLinkElement && node.rel === 'stylesheet') {
          let appending = true
          node.addEventListener('load', () => note('stylesheet-load', { path: new URL(node.href).pathname, duringAppend: appending }))
          const result = appendBefore.call(this, node) as T
          appending = false
          note('stylesheet-appended', { path: new URL(node.href).pathname, sheet: !!node.sheet })
          return result
        }
        return appendBefore.call(this, node) as T
      }
      const fetchBefore = window.fetch
      window.fetch = async (input, options) => {
        const path = new URL(typeof input === 'string' ? input : input instanceof Request ? input.url : input.toString(), location.href).pathname
        const response = await fetchBefore.call(window, input, options)
        if (path === '/api/v1/auth/login') {
          note('login-headers', { status: response.status })
          const jsonBefore = response.json.bind(response)
          response.json = async () => {
            note('login-json-start')
            try { const value: unknown = await jsonBefore(); note('login-json-end'); return value }
            catch (error) { note('login-json-error'); throw error }
          }
        }
        return response
      }
      const setBefore = Storage.prototype.setItem
      Storage.prototype.setItem = function (key, value) {
        setBefore.call(this, key, value)
        if (this === sessionStorage && key === 'yreader-session') note('session-written', { present: !!value })
      }
    })
  }
  await page.bringToFront()
  await page.goto('/bookshelf')
  await page.getByLabel('用户名', { exact: true }).fill(process.env.E2E_USERNAME!)
  await page.getByLabel('密码', { exact: true }).fill(process.env.E2E_PASSWORD!)
  if (diagnostic) await page.evaluate(() => {
    type Route = { path: string }
    type Router = { beforeEach: (f: (to: Route) => void) => unknown; beforeResolve: (f: (to: Route) => void) => unknown; afterEach: (f: (to: Route) => void) => unknown; onError: (f: () => void) => unknown; getRoutes: () => { path: string; components?: { default?: unknown } }[] }
    const root = document.getElementById('app') as unknown as { __vue_app__: { config: { globalProperties: { $router: Router } } } }
    const record = (window as unknown as { recordLoginDiagnostic: (event: string, detail: unknown) => Promise<void> }).recordLoginDiagnostic
    for (const event of ['beforeEach', 'beforeResolve', 'afterEach'] as const) root.__vue_app__.config.globalProperties.$router[event](to => { void record('route-' + event, { path: to.path }) })
    root.__vue_app__.config.globalProperties.$router.onError(() => { void record('route-error', {}) })
    const shelf = root.__vue_app__.config.globalProperties.$router.getRoutes().find(route => route.path === '/bookshelf')
    const loader = shelf?.components?.default
    if (typeof loader === 'function') shelf!.components!.default = () => {
      void record('shelf-module-start', {})
      return Promise.resolve(loader()).then(value => { void record('shelf-module-end', {}); return value }, error => { void record('shelf-module-error', {}); throw error })
    }
  })
  let completed = false
  try {
    const login = page.waitForResponse(response => new URL(response.url()).pathname === '/api/v1/auth/login' && response.request().method() === 'POST', { timeout: 15000 })
    await page.getByRole('button', { name: '登录书房' }).click()
    const response = await login
    log('login-response', { status: response.status() })
    expect(response.ok()).toBeTruthy()
    if (process.env.E2E_LOGIN_URL_CHECK === 'predicate') await expect(page).toHaveURL(url => url.pathname === '/bookshelf', { timeout: 15000 })
    else await expect(page).toHaveURL(new URL('/bookshelf', page.url()).href, { timeout: 15000 })
    completed = true
    return response.status()
  } finally {
    const state = await page.evaluate(() => {
      let session = false
      try { const value = JSON.parse(sessionStorage.getItem('yreader-session') ?? 'null'); session = typeof value?.access_token === 'string' && typeof value?.user?.id === 'string' } catch { /* Only a boolean is recorded. */ }
      const resources = (performance.getEntriesByType('resource') as PerformanceResourceTiming[])
        .filter(entry => new URL(entry.name).pathname.startsWith('/assets/')).slice(-60)
        .map(entry => ({ path: new URL(entry.name).pathname, start: Math.round(entry.startTime), end: Math.round(entry.responseEnd), duration: Math.round(entry.duration) }))
      const links = [...document.querySelectorAll<HTMLLinkElement>('link[href]')].map(link => ({ path: new URL(link.href).pathname, rel: link.rel, sheet: !!link.sheet }))
      return { path: location.pathname, visibility: document.visibilityState, session, resources, links }
    }).catch(() => ({ unavailable: true }))
    log('login-final-state', state)
    if (!completed && diagnostic) {
      // Diagnostic only after the original assertion failed; never warms a passing navigation.
      const probe = await page.evaluate(async () => {
        const link = [...document.querySelectorAll<HTMLLinkElement>('link[rel="modulepreload"]')]
          .find(link => /\/assets\/BookshelfView-[^/]+\.js$/.test(new URL(link.href).pathname))
        if (!link || new URL(link.href).origin !== location.origin) return 'not-requested'
        let timer: ReturnType<typeof setTimeout> | undefined
        try { return await Promise.race([import(link.href).then(() => 'resolved', () => 'rejected'), new Promise<string>(resolve => { timer = setTimeout(() => resolve('pending'), 1000) })]) }
        finally { clearTimeout(timer) }
      }).catch(() => 'unavailable')
      log('failed-native-import-probe', probe)
    }
  }
}
