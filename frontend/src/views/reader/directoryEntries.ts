import type { Model } from '@/api/client'

export function directoryEntries(chapters: Model<'ChapterList'>['items'], query: string, newestFirst: boolean) {
  const term = query.trim().normalize('NFKC').toLowerCase()
  const entries = chapters.map(chapter => ({
    id: chapter.is_volume ? 'v:' + chapter.id : chapter.chapter_index!,
    label: chapter.title,
    disabled: chapter.is_volume,
  }))
  const volumes: (typeof entries)[] = [[]]
  for (const entry of entries) {
    if (entry.disabled) {
      if (!term) volumes.push([entry])
    } else if (!term || entry.label.normalize('NFKC').toLowerCase().includes(term)) {
      volumes.at(-1)!.push(entry)
    }
  }
  if (!newestFirst) return volumes.flat()
  return volumes.reverse().flatMap(volume => {
    if (!volume[0]?.disabled) return volume.reverse()
    const [heading, ...chapters] = volume
    return [heading!, ...chapters.reverse()]
  })
}
