import type { Model } from '@/api/client'
import type { LocalPosition } from '@/db/chapters'
export function reconcileProgress(server: Model<'Progress'>, local?: LocalPosition) {
  if (!local?.dirty) return { base: server, index: server.chapter_index, offset: server.char_offset, dirty: false, conflict: undefined }
  // Keep the original optimistic-lock version until the user resolves the conflict.
  // Persisting the newer server version here would silently authorize a local overwrite on reload.
  return { base: local.base, index: local.index, offset: local.offset, dirty: true, conflict: local.base.version === server.version ? undefined : server }
}
