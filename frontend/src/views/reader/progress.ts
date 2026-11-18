import type { Model } from '@/api/client'
import type { LocalPosition } from '@/db/chapters'
export function remoteProgressState(base: Model<'Progress'>, remote: Model<'Progress'>): 'invalid' | 'unchanged' | 'advanced' {
  if (remote.book_id !== base.book_id || !Number.isSafeInteger(remote.version) || remote.version < base.version || !Number.isSafeInteger(remote.chapter_index) || remote.chapter_index < 0 || !Number.isSafeInteger(remote.char_offset) || remote.char_offset < 0) return 'invalid'
  return remote.version === base.version ? 'unchanged' : 'advanced'
}
export function reconcileProgress(server: Model<'Progress'>, local?: LocalPosition) {
  if (!local?.dirty) return { base: server, index: server.chapter_index, offset: server.char_offset, dirty: false, conflict: undefined }
  // Keep the original optimistic-lock version until the user resolves the conflict.
  // Persisting the newer server version here would silently authorize a local overwrite on reload.
  return { base: local.base, index: local.index, offset: local.offset, dirty: true, conflict: local.base.version === server.version ? undefined : server }
}
