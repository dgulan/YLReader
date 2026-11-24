import type { Model } from '@/api/client'

type Variant = { key: string; source: string; book: Model<'SearchBook'> }
type BookGroup = { key: string; variants: Variant[] }

function normalized(value: string | null | undefined) {
  return (value ?? '').normalize('NFKC').trim().toLowerCase().replace(/\s+/gu, ' ')
}

export function groupSearchResults(groups: Model<'SearchGroup'>[]): BookGroup[] {
  const results = new Map<string, BookGroup>()
  const seen = new Set<string>()
  for (const group of groups) {
    for (const book of group.items) {
      const variantKey = JSON.stringify([book.source_id, book.book_url])
      if (seen.has(variantKey)) continue
      seen.add(variantKey)
      const key = JSON.stringify([normalized(book.name), normalized(book.author)])
      let result = results.get(key)
      if (!result) {
        result = { key, variants: [] }
        results.set(key, result)
      }
      result.variants.push({ key: variantKey, source: group.source_name, book })
    }
  }
  return [...results.values()]
}
