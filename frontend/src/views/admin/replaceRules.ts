import { onScopeDispose, ref } from 'vue'
import { api } from '@/api/reader'
import { ApiError, errorText, getSession, type Model } from '@/api/client'
import { db } from '@/db/chapters'
export type RuleDraft = Model<'ReplaceRuleCreate'>
export const emptyRule = (): RuleDraft => ({ name: '', group_name: '默认', pattern: '', replacement: '', scope: 'content', is_enabled: true, order_index: 0 })
export function ruleInput(draft: RuleDraft): RuleDraft {
  const value = { ...draft, name: draft.name.trim(), group_name: draft.group_name.trim() || '默认' }
  if (!value.name || !value.pattern) throw new Error('规则名称和表达式不能为空')
  for (const [text, limit] of [[value.name, 128], [value.group_name, 64], [value.pattern, 4096], [value.replacement, 4096]] as const) if (Array.from(text).length > limit) throw new Error('规则字段超过长度限制')
  if (!Number.isInteger(value.order_index) || value.order_index < -2147483648 || value.order_index > 2147483647) throw new Error('顺序必须是32位整数')
  return value
}
export function useReplaceRules() {
  const owner = getSession()!.user.id, items = ref<Model<'ReplaceRule'>[]>([]), busy = ref(false), error = ref(''), notice = ref(''), savedId = ref<string>()
  let generation = 0
  onScopeDispose(() => { generation++ })
  const valid = (token: number) => token === generation && getSession()?.user.id === owner
  async function load() {
    if (busy.value) return
    const token = generation; busy.value = true; error.value = ''
    try { const list = await api.replaceRules(); if (valid(token)) items.value = list }
    catch (e) { if (valid(token)) error.value = errorText(e) }
    finally { if (valid(token)) busy.value = false }
  }
  async function changed(token: number) {
    try { await db.chapters.where('owner').equals(owner).delete(); if (valid(token)) notice.value = '已保存。重新打开章节后应用规则；净化后字数可能变化，请核对阅读位置。' }
    catch { if (valid(token)) notice.value = '服务端已保存，但本机正文缓存清理失败，请保持联网并重新打开章节' }
  }
  async function save(id: string | undefined, draft: RuleDraft) {
    if (busy.value) return false
    const token = generation; error.value = ''; notice.value = ''
    try {
      const input = ruleInput(draft); busy.value = true
      const result = id ? await api.updateReplaceRule(id, input) : await api.createReplaceRule(input)
      if (!valid(token)) return false
      savedId.value = result.id
      items.value = [...items.value.filter(item => item.id !== result.id), result].sort((a, b) => a.order_index - b.order_index)
      await changed(token); return true
    } catch (e) { if (valid(token)) error.value = e instanceof ApiError && (e.status === 400 || e.status === 422) ? '服务端拒绝规则，请检查表达式与输入；草稿已保留' : errorText(e); return false }
    finally { if (valid(token)) busy.value = false }
  }
  async function remove(id: string) {
    if (busy.value) return false
    const token = generation; busy.value = true; error.value = ''; notice.value = ''
    try {
      try { await api.deleteReplaceRule(id) } catch (e) { if (!(e instanceof ApiError && e.status === 404)) throw e }
      if (!valid(token)) return false
      items.value = items.value.filter(item => item.id !== id); await changed(token); return true
    } catch (e) { if (valid(token)) error.value = errorText(e); return false }
    finally { if (valid(token)) busy.value = false }
  }
  return { items, busy, error, notice, savedId, load, save, remove }
}
