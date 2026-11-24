import { expect, it } from 'vitest'
import type { Model } from '@/api/client'
import { directoryEntries } from './directoryEntries'

it('filters by normalized chapter title and preserves volume headings in reversed order', () => {
  const rows = [
    { id: 1, is_volume: true, title: '前卷' },
    { id: 2, chapter_index: 0, title: '第一章 ＡＢＣ' },
    { id: 3, chapter_index: 1, title: '第二章 山路' },
    { id: 4, is_volume: true, title: '后卷' },
    { id: 5, chapter_index: 2, title: '第三章 星月' },
  ] as Model<'ChapterList'>['items']
  expect(directoryEntries(rows, ' abc ', false).map(row => row.id)).toEqual([0])
  expect(directoryEntries(rows, '第二章', true).map(row => row.id)).toEqual([1])
  expect(directoryEntries(rows, '', true).map(row => row.id)).toEqual(['v:4', 2, 'v:1', 1, 0])
})
