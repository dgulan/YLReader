import { expect, it } from 'vitest'
import { cacheRange, chapterPercent, clampLineHeight, loadLineHeight, saveLineHeight } from './lineHeight'

it('clamps and persists line height with a safe default', () => {
  expect(clampLineHeight(9)).toBe(2.4); expect(clampLineHeight(0.2)).toBe(1.4); expect(clampLineHeight('abc')).toBe(1.7); expect(clampLineHeight(1.84)).toBe(1.8)
  const store = new Map<string, string>()
  const fake = { getItem: (k: string) => store.get(k) ?? null, setItem: (k: string, v: string) => void store.set(k, v) }
  expect(loadLineHeight(fake)).toBe(1.7); saveLineHeight(2.1, fake); expect(loadLineHeight(fake)).toBe(2.1)
})
it('computes chapter percent and bounded cache ranges', () => {
  expect(chapterPercent(0, 10)).toBe(10); expect(chapterPercent(9, 10)).toBe(100); expect(chapterPercent(0, 0)).toBe(0)
  expect(cacheRange(3, 200, 15)).toEqual({ from_chapter: 3, to_chapter: 17 })
  expect(cacheRange(190, 200, 50)).toEqual({ from_chapter: 190, to_chapter: 199 })
  expect(cacheRange(5, 200, 'all')).toEqual({ from_chapter: 5, to_chapter: 199 })
})
