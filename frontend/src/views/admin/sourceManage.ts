import { api } from '@/api/reader'
import type { Model } from '@/api/client'
import { jsonObject } from './sourceDraft'

export interface NewSourceInput {
  url: string
  name: string
  group: string
  type: number
  enabled: boolean
  enabledExplore: boolean
  weight: number
  rules: string
  header: string
}

export function buildSourceCreate(input: NewSourceInput): Model<'SourceCreate'> {
  const url = input.url.trim()
  if (!/^https?:\/\//i.test(url)) {
    throw new Error('书源地址必须以 http:// 或 https:// 开头')
  }
  try {
    new URL(url)
  } catch {
    throw new Error('书源地址格式不正确')
  }
  const name = input.name.trim()
  if (!name) {
    throw new Error('书源名称不能为空')
  }
  const group = input.group.trim() || '默认'
  const weight = Number.isFinite(input.weight) ? Math.trunc(input.weight) : 0
  return {
    book_source_url: url,
    book_source_name: name,
    book_source_group: group,
    book_source_type: input.type,
    enabled: input.enabled,
    enabled_explore: input.enabledExplore,
    weight,
    rules: jsonObject(input.rules) as Model<'SourceRules'>,
    header: jsonObject(input.header),
  }
}

export function exportFilename(now = new Date()): string {
  const pad = (n: number) => String(n).padStart(2, '0')
  return `ylreader-sources-${now.getFullYear()}${pad(now.getMonth() + 1)}${pad(now.getDate())}.json`
}

export async function exportSourcesBundle(ids?: string[]) {
  const items = await api.exportSources(ids)
  return {
    count: items.length,
    filename: exportFilename(),
    json: JSON.stringify(items, null, 2),
  }
}

export async function deleteSelectedSources<T extends { id: string; book_source_type: number }>(
  sources: T[],
  remove: (id: string) => Promise<void>,
): Promise<{ deleted: number; failed: T[] }> {
  const failed: T[] = []
  let deleted = 0
  for (const source of sources) {
    if (source.book_source_type === 3) continue
    try { await remove(source.id); deleted++ }
    catch { failed.push(source) }
  }
  return { deleted, failed }
}
