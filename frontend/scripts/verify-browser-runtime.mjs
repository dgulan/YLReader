import assert from 'node:assert/strict'
import { fileURLToPath } from 'node:url'
import { loadConfigFromFile } from 'vite'

const keys = ['E2E_BROWSER', 'E2E_BASE_URL', 'PW_TEST_CONNECT_WS_ENDPOINT']
const before = Object.fromEntries(keys.map(key => [key, process.env[key]]))
const file = fileURLToPath(new URL('../playwright.config.ts', import.meta.url))
const load = async () => (await loadConfigFromFile({ command: 'serve', mode: 'test' }, file, undefined, 'silent')).config
try {
  process.env.E2E_BASE_URL = 'http://127.0.0.1'
  process.env.E2E_BROWSER = 'chromium'
  delete process.env.PW_TEST_CONNECT_WS_ENDPOINT
  const local = await load()
  assert.equal(local.use.channel, process.platform === 'win32' ? 'msedge' : undefined)
  process.env.PW_TEST_CONNECT_WS_ENDPOINT = 'ws://127.0.0.1:3000/test-only'
  const remoteChrome = await load()
  assert.equal(remoteChrome.use.channel, undefined)
  process.env.E2E_BROWSER = 'webkit'
  const remoteWebKit = await load()
  assert.equal(remoteWebKit.use.browserName, 'webkit')
  assert.equal(remoteWebKit.use.channel, undefined)
  for (const config of [local, remoteChrome, remoteWebKit]) {
    assert.equal(config.webServer, undefined)
    assert.equal(config.workers, 1)
    assert.equal(config.expect.timeout, 15000)
    assert.equal(config.use.baseURL, 'http://127.0.0.1')
  }
  console.log('local browser channel preserved; remote Chromium/WebKit unpinned from Windows Edge; single worker, same 15s gate, no preview server')
} finally {
  for (const key of keys) {
    if (before[key] === undefined) delete process.env[key]
    else process.env[key] = before[key]
  }
}
