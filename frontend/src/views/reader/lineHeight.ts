export const LINE_HEIGHT_MIN = 1.4, LINE_HEIGHT_MAX = 2.4, LINE_HEIGHT_DEFAULT = 1.7
const KEY = 'yreader.reader.line_height'
export function clampLineHeight(value: unknown): number {
  const n = typeof value === 'string' ? Number(value) : value
  if (typeof n !== 'number' || !Number.isFinite(n)) return LINE_HEIGHT_DEFAULT
  return Math.round(Math.min(LINE_HEIGHT_MAX, Math.max(LINE_HEIGHT_MIN, n)) * 10) / 10
}
export function loadLineHeight(storage: Pick<Storage, 'getItem'> | undefined = globalThis.localStorage): number {
  try { const raw = storage?.getItem(KEY); return raw == null ? LINE_HEIGHT_DEFAULT : clampLineHeight(raw) } catch { return LINE_HEIGHT_DEFAULT }
}
export function saveLineHeight(value: number, storage: Pick<Storage, 'setItem'> | undefined = globalThis.localStorage) {
  try { storage?.setItem(KEY, String(clampLineHeight(value))) } catch { /* Best effort: preference stays in memory. */ }
}
export function chapterPercent(index: number, total: number): number {
  if (!Number.isFinite(index) || !Number.isFinite(total) || total <= 0) return 0
  return Math.max(0, Math.min(100, Math.round(((index + 1) / total) * 100)))
}
export function cacheRange(index: number, total: number, count: number | 'all'): { from_chapter: number; to_chapter: number } {
  const last = Math.max(0, total - 1), from = Math.max(0, Math.min(index, last))
  return { from_chapter: from, to_chapter: count === 'all' ? last : Math.min(last, from + count - 1) }
}
