import { beforeEach, afterEach, describe, it, expect, vi } from 'vitest'
import { getSession, setSession, request, ApiError, errorText, type Model } from './client'
const session = (token = 'old'): Model<'TokensResponse'> => ({ access_token: token, refresh_token: `${token}-refresh`, token_type: 'Bearer', expires_in: 900, user: { id: 'user-a', username: 'reader', role: 'user', token_version: 1, created_at: '2026-11-12T00:00:00Z' } })
const response = (data: unknown, status = 200) => new Response(JSON.stringify(data), { status, headers: { 'Content-Type': 'application/json' } })
beforeEach(() => setSession(null))
afterEach(() => vi.unstubAllGlobals())
describe('authenticated requests', () => {
  it('keeps import timeouts distinct and advises checking the shelf before a manual retry', () => {
    const message = errorText(new ApiError(504, 'import_timeout', null))
    expect(message).toContain('结果尚未确认'); expect(message).toContain('刷新书架')
  })
  it('does not retry a previous account mutation with the new account credentials', async () => {
    setSession(session())
    let finish!: (value: Response) => void
    const fetcher = vi.fn(() => new Promise<Response>(resolve => { finish = resolve })); vi.stubGlobal('fetch', fetcher)
    const result = request('/settings', { method: 'PATCH', body: JSON.stringify({ 'reader.theme': 'night' }) }).catch(e => e)
    const other = session('other'); other.user.id = 'user-b'; setSession(other); finish(response({}, 401))
    expect(await result).toBeInstanceOf(ApiError); expect(fetcher).toHaveBeenCalledTimes(1); expect(getSession()?.user.id).toBe('user-b')
  })
  it('shares one refresh across simultaneous expired requests and retries with the rotated token', async () => {
    setSession(session())
    let refreshes = 0
    const fetcher = vi.fn(async (url: string, init?: RequestInit) => {
      if (url.endsWith('/auth/refresh')) { refreshes++; await new Promise(r => setTimeout(r, 10)); return response(session('new')) }
      return new Headers(init?.headers).get('Authorization') === 'Bearer old' ? response({}, 401) : response({ ok: true })
    })
    vi.stubGlobal('fetch', fetcher)
    const results = await Promise.all([request('/books'), request('/sources')])
    expect(results).toEqual([{ ok: true }, { ok: true }]); expect(refreshes).toBe(1); expect(getSession()?.refresh_token).toBe('new-refresh')
  })
  it('does not resurrect a session if logout occurs while refresh is pending', async () => {
    setSession(session())
    let finish!: (response: Response) => void
    const pending = new Promise<Response>(resolve => { finish = resolve })
    const fetcher = vi.fn(async (url: string) => url.endsWith('/auth/refresh') ? pending : response({}, 401))
    vi.stubGlobal('fetch', fetcher)
    const result = request('/books').catch(e => e)
    await vi.waitFor(() => expect(fetcher).toHaveBeenCalledTimes(2))
    setSession(null); finish(response(session('new')))
    expect(await result).toBeInstanceOf(ApiError); expect(getSession()).toBeNull()
  })
  it('keeps a 403 distinct from session expiry and preserves the session', async () => {
    setSession(session()); const fetcher = vi.fn(async () => response({ error: { code: 'blocked_private_address' } }, 403)); vi.stubGlobal('fetch', fetcher)
    await expect(request('/search')).rejects.toMatchObject({ status: 403, code: 'blocked_private_address' }); expect(fetcher).toHaveBeenCalledTimes(1); expect(getSession()).not.toBeNull()
  })
  it('preserves conflict details for explicit resolution', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => response({ error: { code: 'progress_conflict', details: { current: { version: 8 } } } }, 409)))
    await expect(request('/books/id/progress')).rejects.toMatchObject({ status: 409, data: { error: { details: { current: { version: 8 } } } } })
  })
  it('ignores malformed persisted sessions', () => { sessionStorage.setItem('yreader-session', '{}'); expect(getSession()).toBeNull() })
})
