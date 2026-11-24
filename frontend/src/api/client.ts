import type { components } from './schema'
import { withDeadline } from './deadline'
export type Model<K extends keyof components['schemas']> = components['schemas'][K]
type Session = Model<'TokensResponse'>
const sessionKey = 'yreader-session'
const uncertainOwnerKey = 'yreader-refresh-uncertain-owner'
export const sessionEvents = new EventTarget()
export function getSession(): Session | null {
  try { const value = sessionStorage.getItem(sessionKey); const parsed = value ? JSON.parse(value) as Partial<Session> : null; return parsed && typeof parsed.access_token === 'string' && typeof parsed.refresh_token === 'string' && typeof parsed.user?.id === 'string' ? parsed as Session : null } catch { return null }
}
export function setSession(value: Session | null) {
  if (value) sessionStorage.setItem(sessionKey, JSON.stringify(value)); else sessionStorage.removeItem(sessionKey)
  sessionStorage.removeItem(uncertainOwnerKey)
  uncertainRefresh = undefined
  sessionEvents.dispatchEvent(new Event('change'))
}
export class ApiError extends Error {
  constructor(public status: number, public code: string, public data: unknown) { super(code) }
}
let refreshing: { token: string; task: Promise<void> } | undefined
let uncertainRefresh: string | undefined
async function refresh() {
  const prior = getSession()
  if (!prior) throw new ApiError(401, 'unauthorized', null)
  let persistedUncertainty = false
  try { persistedUncertainty = sessionStorage.getItem(uncertainOwnerKey) === prior.user.id } catch { /* Runtime latch still fails closed. */ }
  if (uncertainRefresh === prior.refresh_token || persistedUncertainty) throw new ApiError(503, 'session_refresh_uncertain', null)
  if (!refreshing || refreshing.token !== prior.refresh_token) {
    const entry = { token: prior.refresh_token, task: Promise.resolve() }
    entry.task = (async () => {
      if (!prior) throw new ApiError(401, 'unauthorized', null)
      const next = await withDeadline(async signal => {
        const r = await fetch('/api/v1/auth/refresh', { method: 'POST', signal, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ refresh_token: prior.refresh_token }) })
        signal.throwIfAborted()
        if (getSession()?.refresh_token !== prior.refresh_token) return undefined
        if (!r.ok) {
          if (r.status === 401) setSession(null)
          throw new ApiError(r.status, 'session_refresh_failed', null)
        }
        const value = await r.json() as Session
        signal.throwIfAborted()
        if (!value?.access_token || !value.refresh_token || value.user?.id !== prior.user.id) throw new Error('invalid refresh response')
        return value
      })
      if (!next) return
      if (getSession()?.refresh_token === prior.refresh_token) setSession(next)
    })().catch(error => {
      if (getSession()?.refresh_token !== prior.refresh_token) return
      if (!(error instanceof ApiError && error.status >= 400 && error.status < 500)) {
        uncertainRefresh = prior.refresh_token
        try { sessionStorage.setItem(uncertainOwnerKey, prior.user.id) } catch { /* No extra credential is persisted. */ }
        throw new ApiError(503, 'session_refresh_uncertain', null)
      }
      throw error
    }).finally(() => { if (refreshing === entry) refreshing = undefined })
    refreshing = entry
  }
  await refreshing.task
}
async function requestResponse(path: string, options: RequestInit, retry: boolean): Promise<Response> {
	path = path.trim()
	const pathname = path.split(/[?#]/, 1)[0]!
  options.signal?.throwIfAborted()
  const before = getSession()
  const headers = new Headers(options.headers)
  if (before) headers.set('Authorization', `Bearer ${before.access_token}`)
  const r = await fetch(`/api/v1${path}`, { ...options, headers })
  options.signal?.throwIfAborted()
  if (r.status === 401 && before && retry && !['/auth/login', '/auth/refresh', '/auth/logout'].includes(pathname)) {
    if (getSession()?.user.id !== before.user.id) throw new ApiError(401, 'unauthorized', null)
    if (getSession()?.access_token === before.access_token) await refresh()
    if (getSession()?.user.id !== before.user.id) throw new ApiError(401, 'unauthorized', null)
    return requestResponse(path, options, false)
  }
  if (!r.ok) {
    let data: { error?: { code?: string } } | null = null
    try { data = await r.json() } catch { /* A proxy may return a non-JSON error. */ }
    options.signal?.throwIfAborted()
    if (r.status === 401 && before && getSession()?.access_token === before.access_token && pathname !== '/auth/login') setSession(null)
    throw new ApiError(r.status, data?.error?.code ?? `http_${r.status}`, data)
  }
  return r
}
export async function request<T>(path: string, options: RequestInit = {}, retry = true): Promise<T> {
  const r = await requestResponse(path, options, retry)
  if (r.status === 204) return undefined as T
  const data = await r.json() as T
  options.signal?.throwIfAborted()
  return data
}
type SearchEvent =
  | { type: 'group'; group: Model<'SearchGroup'> }
  | { type: 'done'; result: Model<'SearchResponse'> }

export async function requestSearchStream(path: string, signal: AbortSignal | undefined, onGroup: (group: Model<'SearchGroup'>) => void): Promise<Model<'SearchResponse'>> {
  const response = await requestResponse(path, { signal, headers: { Accept: 'application/x-ndjson' } }, true)
  if (!response.headers.get('Content-Type')?.startsWith('application/x-ndjson')) {
    return response.json() as Promise<Model<'SearchResponse'>>
  }
  if (!response.body) throw new Error('搜索连接已中断')
  const reader = response.body.getReader()
  const decoder = new TextDecoder()
  let pending = ''
  try {
    while (true) {
      const { value, done } = await reader.read()
      signal?.throwIfAborted()
      if (done) throw new Error('搜索连接提前中断，已保留收到的结果')
      pending += decoder.decode(value, { stream: true })
      let end = pending.indexOf('\n')
      while (end >= 0) {
        const event = JSON.parse(pending.slice(0, end)) as SearchEvent
        pending = pending.slice(end + 1)
        if (event.type === 'done') return event.result
        if (event.type === 'group') onGroup(event.group)
        end = pending.indexOf('\n')
      }
    }
  } finally {
    await reader.cancel().catch(() => {})
    reader.releaseLock()
  }
}

export async function requestBookCover(bookId: string, signal: AbortSignal): Promise<Blob> {
  const response = await requestResponse('/books/' + encodeURIComponent(bookId) + '/cover', { signal, cache: 'no-store', headers: { Accept: 'image/*' } }, true)
  if (!response.headers.get('Content-Type')?.startsWith('image/')) throw new Error('Invalid cover response')
  const blob = await response.blob()
  signal.throwIfAborted()
  return blob
}
export function jsonBody(data: unknown): RequestInit { return { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(data) } }
export function errorText(error: unknown) {
  if (error instanceof ApiError && error.code === 'session_refresh_uncertain') return '登录续期结果待确认，请重新登录；请勿重复刷新旧令牌'
  if (error instanceof ApiError) {
    const messages: Record<string, string> = { import_timeout: '导入处理超时，结果尚未确认。请先刷新书架，必要时重新选择同一文件。', unauthorized: '登录已失效，请重新登录', validation_error: '输入格式不正确', progress_conflict: '另一端已更新阅读进度', relocation_failed: '目标源目录为空或无法定位，请先同步目录', blocked_private_address: '书源地址被安全策略阻止', session_refresh_failed: '暂时无法续期，请稍后重试', source_request_failed: '书源请求失败，请稍后重试' }
    return messages[error.code] ?? `请求失败（${error.code}）`
  }
  return error instanceof Error ? error.message : '操作失败，请重试'
}
