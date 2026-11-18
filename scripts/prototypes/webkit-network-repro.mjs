// Original minimal loopback fixture. No YLReader API, Vue, database or external network.
import http from 'node:http'
import { createRequire } from 'node:module'
import { mkdir, readFile, writeFile } from 'node:fs/promises'
import { randomUUID } from 'node:crypto'
import { fileURLToPath } from 'node:url'
import path from 'node:path'
const require = createRequire(new URL('../../frontend/package.json', import.meta.url))
const { webkit, chromium } = require('playwright')
const { expect } = require('@playwright/test')
const root = fileURLToPath(new URL('../../', import.meta.url))
const out = path.join(root, '.toolchain', 'webkit-network-repro-' + randomUUID().slice(0, 12))
await mkdir(out)
const requests = [], runs = []
const appFixture = process.argv.includes('--app-fixture')
const dist = path.join(root, 'frontend', 'dist')
const server = http.createServer(async (request, response) => {
  const url = new URL(request.url, 'http://127.0.0.1')
  let body, type = 'text/javascript'
  if (appFixture) {
    if (url.pathname.startsWith('/api/v1/')) {
      request.resume(); type = 'application/json'
      if (url.pathname === '/api/v1/auth/login') body = JSON.stringify({ access_token: 'synthetic', refresh_token: 'synthetic-refresh', token_type: 'Bearer', expires_in: 900, user: { id: '00000000-0000-4000-8000-000000000001', username: 'synthetic', role: 'user', token_version: 1, created_at: '2026-11-15T00:00:00Z' } })
      else if (url.pathname === '/api/v1/settings') body = '{}'
      else if (url.pathname === '/api/v1/books') body = '{"items":[],"total":0,"page":1,"page_size":20}'
      else { response.writeHead(404); response.end(); return }
    } else {
      const file = url.pathname.startsWith('/assets/') ? path.resolve(dist, '.' + url.pathname) : ['/', '/bookshelf', '/login'].includes(url.pathname) ? path.join(dist, 'index.html') : ''
      if (!file.startsWith(dist + path.sep)) { response.writeHead(404); response.end(); return }
      try { body = await readFile(file) } catch { response.writeHead(404); response.end(); return }
      type = file.endsWith('.css') ? 'text/css' : file.endsWith('.html') ? 'text/html' : 'text/javascript'
    }
  } else if (url.pathname === '/') {
    type = 'text/html'
    body = `<button id="start">Start</button><script type="module">
      document.querySelector('#start').onclick = async () => {
        await (await fetch('/json')).json();
        await new Promise((resolve, reject) => { const link = document.createElement('link'); link.rel = 'stylesheet'; link.href = '/style.css'; link.onload = resolve; link.onerror = reject; document.head.append(link) });
        await import('/entry.js'); history.pushState({}, '', '/done');
      };
    </script>`
  } else if (url.pathname === '/json') { type = 'application/json'; body = '{"ok":true}' }
  else if (url.pathname === '/style.css') { type = 'text/css'; body = 'body{color:#123}' }
  else if (url.pathname === '/entry.js') body = Array.from({ length: 8 }, (_, i) => `import '/module-${i}.js';`).join('\n')
  else if (/^\/module-\d\.js$/.test(url.pathname)) body = 'export const marker=1;'
  else { response.writeHead(404); response.end(); return }
  const data = Buffer.from(body)
  requests.push({ path: url.pathname, at: Date.now(), bytes: data.length })
  response.writeHead(200, { 'Content-Type': type, 'Content-Length': data.length, 'Cache-Control': 'no-store' })
  response.end(data)
})
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve))
const base = `http://127.0.0.1:${server.address().port}`
try {
  for (const [name, engine] of [['webkit', webkit], ['chromium', chromium]]) {
    const browser = await engine.launch({ headless: true, ...(name === 'chromium' ? { channel: 'msedge' } : {}) })
    try {
      for (const waiter of ['predicate', 'exact']) {
        const context = await browser.newContext(), page = await context.newPage()
        const events = [], record = { browser: name, waiter, passed: false, events }
        runs.push(record)
        for (const event of ['request', 'requestfinished', 'requestfailed']) page.on(event, r => events.push({ event, path: new URL(r.url()).pathname, at: Date.now() }))
        try {
          const fullStart = Date.now()
          if (appFixture) {
            await page.goto(base + '/bookshelf')
            await page.getByLabel('用户名', { exact: true }).fill('synthetic')
            await page.getByLabel('密码', { exact: true }).fill('synthetic')
            await page.getByRole('button', { name: '登录书房', exact: true }).click()
          } else {
            await page.goto(base)
            await page.getByRole('button', { name: 'Start', exact: true }).click()
          }
          const start = Date.now()
          record.beforeWaitMilliseconds = start - fullStart
          try {
            const target = appFixture ? '/bookshelf' : '/done'
            if (waiter === 'predicate') await expect(page).toHaveURL(url => url.pathname === target, { timeout: 15000 })
            else await expect(page).toHaveURL(base + target, { timeout: 15000 })
            record.passed = true
          } catch { record.failure = 'navigation_timeout' }
          record.milliseconds = Date.now() - start
          record.totalMilliseconds = Date.now() - fullStart
          record.state = await page.evaluate(() => ({ path: location.pathname, resources: performance.getEntriesByType('resource').map(e => ({ path: new URL(e.name).pathname, duration: Math.round(e.duration), end: Math.round(e.responseEnd) })) }))
        } finally { await context.close() }
      }
    } finally { await browser.close() }
  }
} finally {
  server.closeAllConnections()
  await new Promise(resolve => server.close(resolve))
  const passed = runs.length === 4 && runs.every(run => run.passed) && !server.listening
  await writeFile(path.join(out, 'result.json'), JSON.stringify({ fixture: appFixture ? 'built_frontend_mock_api' : 'minimal_loopback', passed, requests, runs, server_closed: !server.listening }, null, 2))
  console.log(path.join(out, 'result.json'))
  if (!passed) process.exitCode = 1
}
