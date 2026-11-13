import type { Model } from '@/api/client'
export function jsonObject(text: string): Record<string, unknown> {
  const parsed: unknown = JSON.parse(text)
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) throw new Error('规则和请求头必须是 JSON 对象')
  return parsed as Record<string, unknown>
}
export function sourcePatch(version: number, name: string, group: string, enabled: boolean, rules: string, header: string): Model<'SourceUpdate'> {
  if (!name.trim()) throw new Error('书源名称不能为空')
  return { version, book_source_name: name.trim(), book_source_group: group, enabled, rules: jsonObject(rules), header: jsonObject(header) }
}
