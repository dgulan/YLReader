// Server offsets count Unicode code points, not UTF-16 code units.
export const codePointLength = (text: string) => Array.from(text).length
export function utf16Offset(text: string, charOffset: number) { return Array.from(text).slice(0, Math.max(0, charOffset)).join('').length }
const cachedOffsets = new WeakMap<Text, { text: string; offsets: number[] }>()
export function visibleOffset(node: Text, viewport: HTMLElement) {
  const text = node.data, top = viewport.getBoundingClientRect().top + 24
  let cached = cachedOffsets.get(node)
  if (!cached || cached.text !== text) { const offsets = [0]; for (const point of text) offsets.push(offsets[offsets.length - 1]! + point.length); cached = { text, offsets }; cachedOffsets.set(node, cached) }
  const offsets = cached.offsets
  let lo = 0, hi = Math.max(0, offsets.length - 2)
  while (lo < hi) {
    const mid = Math.floor((lo + hi) / 2), range = document.createRange()
    range.setStart(node, offsets[mid]!); range.setEnd(node, offsets[mid + 1]!)
    if (range.getBoundingClientRect().top < top) lo = mid + 1; else hi = mid
  }
  return lo
}

export function chapterFraction(text: string, offset: number) { const n = codePointLength(text); return n ? Math.max(0, Math.min(1, offset / n)) : 0 }
