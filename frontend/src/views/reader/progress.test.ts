import { it, expect } from 'vitest'
import { reconcileProgress } from './progress'
import type { Model } from '@/api/client'
import type { LocalPosition } from '@/db/chapters'
const server: Model<'Progress'> = { book_id: 'b', chapter_index: 3, char_offset: 90, chapter_ratio: .5, version: 8 }
const local: LocalPosition = { key: 'a:b', owner: 'a', base: { ...server, version: 7 }, index: 4, offset: 200, dirty: true }
it('preserves the original version across reloads of an unresolved conflict', () => {
  const first = reconcileProgress(server, local)
  expect(first.base.version).toBe(7); expect(first.conflict?.version).toBe(8); expect(first.offset).toBe(200)
  const reloaded = reconcileProgress(server, { ...local, base: first.base })
  expect(reloaded.conflict?.version).toBe(8)
})
it('uses the remote position when no local changes are pending', () => {
  const result = reconcileProgress(server, { ...local, dirty: false })
  expect(result.index).toBe(3); expect(result.offset).toBe(90); expect(result.dirty).toBe(false)
})
it('keeps pending offline progress eligible for sync only with the matching version', () => {
  const result = reconcileProgress(server, { ...local, base: server })
  expect(result.dirty).toBe(true); expect(result.conflict).toBeUndefined(); expect(result.index).toBe(4)
})
