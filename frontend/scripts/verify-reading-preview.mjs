import assert from 'node:assert/strict'
import { fileURLToPath } from 'node:url'
import { loadConfigFromFile } from 'vite'

const file = fileURLToPath(new URL('../tests/reading-preview.config.ts', import.meta.url))
const previous = process.env.E2E_API_TARGET
const load = () => loadConfigFromFile({ command: 'serve', mode: 'test', isPreview: true }, file, undefined, 'silent')
let rejected = 0
try {
  for (const target of [undefined, 'http://localhost:55808', 'http://10.0.0.1:55808', 'https://127.0.0.1:55808', 'http://u:p@127.0.0.1:55808', 'http://127.0.0.1:55808/path', 'http://127.0.0.1:55808/?q=1', 'http://127.0.0.1:55808/#fragment', 'http://127.0.0.1']) {
    if (target === undefined) delete process.env.E2E_API_TARGET
    else process.env.E2E_API_TARGET = target
    await assert.rejects(load, /explicit HTTP loopback API origin/)
    rejected++
  }
  process.env.E2E_API_TARGET = 'http://127.0.0.1:55808'
  const real = await load()
  assert.equal(real.config.preview.proxy['/api'].target, 'http://127.0.0.1:55808')
  const synthetic = await loadConfigFromFile({ command: 'serve', mode: 'test', isPreview: true }, fileURLToPath(new URL('../tests/preview.config.ts', import.meta.url)), undefined, 'silent')
  assert.deepEqual(synthetic.config.preview.proxy, {})
  console.log(`${rejected} unsafe/missing targets rejected; explicit loopback accepted; synthetic preview has no proxy`)
} finally {
  if (previous === undefined) delete process.env.E2E_API_TARGET
  else process.env.E2E_API_TARGET = previous
}
