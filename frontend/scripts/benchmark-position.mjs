// Local microbenchmark, not a browser/Core Web Vitals measurement.
import { readFile } from 'node:fs/promises'
import { createHash } from 'node:crypto'
import { performance } from 'node:perf_hooks'
import ts from 'typescript'

const source = await readFile(new URL('../src/views/reader/position.ts', import.meta.url), 'utf8')
const javascript = ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ES2022 } }).outputText
const current = await import(`data:text/javascript;base64,${Buffer.from(javascript).toString('base64')}`)
const text = '中文😀abc\n'.repeat(100_000)
const previous = {
  codePointLength: text => Array.from(text).length,
  utf16Offset: (text, offset) => Array.from(text).slice(0, Math.max(0, offset)).join('').length,
}
let checksum = 0
function measure(fn, offset) {
  for (let i = 0; i < 2; i++) checksum += fn(text, offset)
  const samples = []
  for (let i = 0; i < 7; i++) {
    const start = performance.now()
    checksum += fn(text, offset)
    samples.push(performance.now() - start)
  }
  return Number(samples.sort((a, b) => a - b)[3].toFixed(4))
}
const cases = [['codePointLength', undefined], ['utf16Offset', 128], ['utf16Offset', 650_000]]
console.log(JSON.stringify({
  scope: 'node-position-microbenchmark', node: process.version,
  source_sha256: createHash('sha256').update(source).digest('hex'),
  code_points: previous.codePointLength(text), samples: 7,
  results: cases.map(([method, offset]) => ({ method, offset, before_median_ms: measure(previous[method], offset), after_median_ms: measure(current[method], offset) })),
  // Observable use prevents treating an unused result as dead work.
  checksum,
}, null, 2))
