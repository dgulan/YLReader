import { expect, it } from 'vitest'
import { groupSearchResults } from './searchResults'

it('groups normalized title and author, while keeping distinct authors and selectable sources', () => {
  const first = { source_id: 'a', book_url: 'https://a.example/book', name: 'Ａ 记', author: '甲' }
  const groups = groupSearchResults([
    { source_id: 'a', source_name: '快源', items: [first, first] },
    { source_id: 'b', source_name: '慢源', items: [
      { ...first, source_id: 'b', book_url: 'https://b.example/1', name: 'a  记', author: '甲 ' },
      { ...first, source_id: 'b', book_url: 'https://b.example/2', author: '乙' },
    ] },
  ])
  expect(groups.map(group => group.variants.map(variant => variant.source))).toEqual([['快源', '慢源'], ['慢源']])
})
