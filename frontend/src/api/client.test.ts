import { beforeEach, afterEach, describe, it, expect, vi } from 'vitest'
import { getSession, setSession, request, ApiError, errorText, type Model } from './client'
import { api } from './reader'
const session = (token = 'old'): Model<'TokensResponse'> => ({ access_token: token, refresh_token: `${token}-refresh`, token_type: 'Bearer', expires_in: 900, user: { id: 'user-a', username: 'reader', role: 'user', token_version: 1, created_at: '2026-11-12T00:00:00Z' } })
const response = (data: unknown, status = 200) => new Response(JSON.stringify(data), { status, headers: { 'Content-Type': 'application/json' } })
beforeEach(() => setSession(null))
afterEach(() => { vi.unstubAllGlobals(); vi.useRealTimers() })
describe('authenticated requests', () => {
	 it.each(['/auth/logout?all=1', '/auth/refresh?retry=0#note', ' /auth/login?mode=test '])('does not refresh or replay an auth pathname with query: %s', async path => {
	   setSession(session())
	   const fetcher = vi.fn(async (_url: string) => response({}, 401)); vi.stubGlobal('fetch', fetcher)
	   await expect(request(path, { method: 'POST' })).rejects.toMatchObject({ status: 401 })
	   expect(fetcher).toHaveBeenCalledTimes(1)
	   expect(fetcher.mock.calls[0]?.[0]).toBe(`/api/v1${path.trim()}`)
	   if (path.trim().startsWith('/auth/login')) expect(getSession()).not.toBeNull()
	 })
  it('bounds refresh, ignores a late acknowledgement and never replays its unknown token', async () => {
    vi.useFakeTimers(); setSession(session())
    let finish!: (r: Response) => void
    let refreshes = 0
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      if (url.endsWith('/auth/refresh')) { refreshes++; return new Promise<Response>(resolve => { finish = resolve }) }
      return response({}, 401)
    }))
    const pending = request('/books')
    const failure = expect(pending).rejects.toMatchObject({ code: 'session_refresh_uncertain' })
    await vi.advanceTimersByTimeAsync(15_001); await failure
    finish(response(session('late'))); await Promise.resolve(); await Promise.resolve()
    expect(getSession()?.access_token).toBe('old')
    await expect(request('/books')).rejects.toMatchObject({ code: 'session_refresh_uncertain' })
    expect(refreshes).toBe(1)
    expect(sessionStorage.getItem('yreader-refresh-uncertain-owner')).toBe('user-a')
    vi.resetModules()
    const reloaded = await import('./client')
    await expect(reloaded.request('/books')).rejects.toMatchObject({ code: 'session_refresh_uncertain' })
    expect(refreshes).toBe(1)
  })
  it('refreshes auth/me but never retries logout with a rotated refresh token', async () => {
    setSession(session()); let refreshes = 0
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      if (url.endsWith('/auth/refresh')) { refreshes++; return response(session('new')) }
      if (url.endsWith('/auth/logout')) return response({}, 401)
      return new Headers(init?.headers).get('Authorization') === 'Bearer old' ? response({}, 401) : response({ ok: true })
    }))
    await expect(request('/auth/me')).resolves.toEqual({ ok: true })
    await expect(request('/auth/logout', { method: 'POST' })).rejects.toMatchObject({ status: 401 })
    expect(refreshes).toBe(1)
  })
  it('does not refresh or clear a session for a canceled late 401', async () => {
    setSession(session())
    const controller = new AbortController()
    let finish!: (value: Response) => void
    const fetcher = vi.fn(() => new Promise<Response>(resolve => { finish = resolve }))
    vi.stubGlobal('fetch', fetcher)
    const pending = request('/books', { signal: controller.signal })
    controller.abort(); finish(response({}, 401))
    await expect(pending).rejects.toMatchObject({ name: 'AbortError' })
    expect(fetcher).toHaveBeenCalledTimes(1); expect(getSession()?.access_token).toBe('old')
  })
  it('does not start a request whose signal is already canceled', async () => {
    const controller = new AbortController(), fetcher = vi.fn()
    controller.abort(); vi.stubGlobal('fetch', fetcher)
    await expect(request('/books', { signal: controller.signal })).rejects.toMatchObject({ name: 'AbortError' })
    expect(fetcher).not.toHaveBeenCalled()
  })
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
  it('routes newly wired R3 frozen API helpers to exact OpenAPI endpoints', async () => {
    setSession(session())
    const fetcher = vi.fn(async (_url: string, _init?: RequestInit) => response({ ok: true }))
    vi.stubGlobal('fetch', fetcher)
    await api.me()
    await api.createSource({ book_source_url: 'https://s.example.com', book_source_name: 'S', book_source_group: '默认', book_source_type: 0, enabled: true, enabled_explore: true, weight: 0 })
    await api.exportSources(['id-1', 'id-2'])
    await api.addMapping('b1', { source_id: 's1', book_url: 'https://s.example.com/1' })
    await api.deleteMapping('b1', 'm1')
    await api.setChapterOverride('b1', 4, { chapter_url: 'https://s.example.com/c/4' })
    await api.deleteChapterOverride('b1', 4)

    const called = fetcher.mock.calls.map(c => [c[0], (c[1] as RequestInit | undefined)?.method ?? 'GET'])
    expect(called).toEqual([
      ['/api/v1/auth/me', 'GET'],
      ['/api/v1/sources', 'POST'],
      ['/api/v1/sources/export?ids=id-1,id-2', 'GET'],
      ['/api/v1/books/b1/mappings', 'POST'],
      ['/api/v1/books/b1/mappings/m1', 'DELETE'],
      ['/api/v1/books/b1/chapters/4/override', 'POST'],
      ['/api/v1/books/b1/chapters/4/override', 'DELETE'],
    ])
  })
})

it('delivers complete search groups before the final response, across UTF-8 chunks', async () => {
  let stream!: ReadableStreamDefaultController<Uint8Array>
  const body = new ReadableStream<Uint8Array>({ start(controller) { stream = controller } })
  const fetcher = vi.fn(async (_url: string, _options: RequestInit) => new Response(body, {
    headers: { 'Content-Type': 'application/x-ndjson' },
  }))
  vi.stubGlobal('fetch', fetcher)
  const group = {
    source_id: 'fast', source_name: '快源',
    items: [{ source_id: 'fast', name: '中文书名', book_url: 'https://fast.example/book' }],
  }
  const final = { keyword: '中文', groups: [group], failures: [], total: 1 }
  const onGroup = vi.fn()
  const search = api.search('中文', undefined, onGroup)
  const bytes = new TextEncoder().encode(JSON.stringify({ type: 'group', group }) + '\n')
  const split = bytes.findIndex(byte => byte > 127) + 1
  stream.enqueue(bytes.slice(0, split))
  stream.enqueue(bytes.slice(split))
  await vi.waitFor(() => expect(onGroup).toHaveBeenCalledWith(group))
  expect(new Headers(fetcher.mock.calls[0]?.[1].headers).get('Accept')).toBe('application/x-ndjson')
  stream.enqueue(new TextEncoder().encode(JSON.stringify({ type: 'done', result: final }) + '\n'))
  stream.close()
  await expect(search).resolves.toEqual(final)
})
