import { it, expect } from 'vitest'
import { codePointLength, utf16Offset, chapterFraction } from './position'
it('uses code point offsets compatible with the Go reader, including supplementary characters', () => {
  const text = '甲😀乙\n丙'
  expect(codePointLength(text)).toBe(5); expect(utf16Offset(text, 2)).toBe(3); expect(text.slice(utf16Offset(text, 2))).toBe('乙\n丙'); expect(utf16Offset(text, -1)).toBe(0)
})

it("stores an intra-chapter fraction rather than an overall book ratio", () => { expect(chapterFraction("甲😀乙丙", 2)).toBe(.5); expect(chapterFraction("", 10)).toBe(0); expect(chapterFraction("甲", 10)).toBe(1) })
