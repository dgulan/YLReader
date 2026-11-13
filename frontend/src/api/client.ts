import type { components } from './schema'
export type Model<K extends keyof components['schemas']> = components['schemas'][K]
type Session = Model<'TokensResponse'>
const sessionKey = 'yreader-session'
export const sessionEvents = new EventTarget()
export function getSession(): Session | null {
  try { const value = sessionStorage.getItem(sessionKey); const parsed = value ? JSON.parse(value) as Partial<Session> : null; return parsed && typeof parsed.access_token === 'string' && typeof parsed.refresh_token === 'string' && typeof parsed.user?.id === 'string' ? parsed as Session : null } catch { return null }
}
export function setSession(value: Session | null) {
  if (value) sessionStorage.setItem(sessionKey, JSON.stringify(value)); else sessionStorage.removeItem(sessionKey)
  sessionEvents.dispatchEvent(new Event('change'))
}
export class ApiError extends Error {
  constructor(public status: number, public code: string, public data: unknown) { super(code) }
}
let refreshing: Promise<void> | null = null
async function refresh() {
  if (!refreshing) {
    const prior = getSession()
    refreshing = (async () => {
      if (!prior) throw new ApiError(401, 'unauthorized', null)
      const r = await fetch('/api/v1/auth/refresh', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ refresh_token: prior.refresh_token }) })
      if (getSession()?.refresh_token !== prior.refresh_token) return
      if (!r.ok) {
        if (r.status === 401) setSession(null)
        throw new ApiError(r.status, 'session_refresh_failed', null)
      }
      const next = await r.json() as Session
      if (getSession()?.refresh_token === prior.refresh_token) setSession(next)
    })().finally(() => { refreshing = null })
  }
  await refreshing
}
export async function request<T>(path: string, options: RequestInit = {}, retry = true): Promise<T> {
  const before = getSession()
  const headers = new Headers(options.headers)
  if (before) headers.set('Authorization', `Bearer ${before.access_token}`)
  const r = await fetch(`/api/v1${path}`, { ...options, headers })
  if (r.status === 401 && before && retry && !path.startsWith('/auth/')) {
    if (getSession()?.user.id !== before.user.id) throw new ApiError(401, 'unauthorized', null)
    if (getSession()?.access_token === before.access_token) await refresh()
    if (getSession()?.user.id !== before.user.id) throw new ApiError(401, 'unauthorized', null)
    return request(path, options, false)
  }
  if (!r.ok) {
    let data: { error?: { code?: string } } | null = null
    try { data = await r.json() } catch { /* A proxy may return a non-JSON error. */ }
    if (r.status === 401 && before && getSession()?.access_token === before.access_token && !path.startsWith('/auth/login')) setSession(null)
    throw new ApiError(r.status, data?.error?.code ?? `http_${r.status}`, data)
  }
  if (r.status === 204) return undefined as T
  return await r.json() as T
}
export function jsonBody(data: unknown): RequestInit { return { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(data) } }
export function errorText(error: unknown) {
  if (error instanceof ApiError) {
    const messages: Record<string, string> = { import_timeout: '导入处理超时，结果尚未确认。请先刷新书架，必要时重新选择同一文件。', unauthorized: '登录已失效，请重新登录', validation_error: '输入格式不正确', progress_conflict: '另一端已更新阅读进度', relocation_failed: '目标源目录为空或无法定位，请先同步目录', blocked_private_address: '书源地址被安全策略阻止', session_refresh_failed: '暂时无法续期，请稍后重试', source_request_failed: '书源请求失败，请稍后重试' }
    return messages[error.code] ?? `请求失败（${error.code}）`
  }
  return error instanceof Error ? error.message : '操作失败，请重试'
}
