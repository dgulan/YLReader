import { effectScope } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { api } from '@/api/reader'
import { setSession, type Model } from '@/api/client'
import { useAccountProfile } from './settingsProfile'

const scopes: ReturnType<typeof effectScope>[] = []

function setup() {
  const scope = effectScope()
  scopes.push(scope)
  return scope.run(() => useAccountProfile())!
}

afterEach(() => {
  scopes.splice(0).forEach(s => s.stop())
  setSession(null)
  vi.restoreAllMocks()
})

describe('useAccountProfile', () => {
  it('loads GET /auth/me for active session and ignores stale responses after account change', async () => {
    setSession({
      access_token: 't1',
      refresh_token: 'r1',
      token_type: 'Bearer',
      expires_in: 900,
      user: { id: 'u1', username: 'alice', role: 'admin', token_version: 2, created_at: '2026-11-01T00:00:00Z' },
    })

    let resolveFirst!: (u: Model<'User'>) => void
    const first = new Promise<Model<'User'>>(r => { resolveFirst = r })
    const meSpy = vi.spyOn(api, 'me').mockReturnValueOnce(first)

    const state = setup()
    const p = state.load()

    setSession({
      access_token: 't2',
      refresh_token: 'r2',
      token_type: 'Bearer',
      expires_in: 900,
      user: { id: 'u2', username: 'bob', role: 'user', token_version: 1, created_at: '2026-11-02T00:00:00Z' },
    })
    resolveFirst({ id: 'u1', username: 'alice', role: 'admin', token_version: 2, created_at: '2026-11-01T00:00:00Z' })
    await p
    expect(state.profile.value).toBeUndefined()

    meSpy.mockResolvedValueOnce({ id: 'u2', username: 'bob', display_name: 'Bob', role: 'user', token_version: 1, created_at: '2026-11-02T00:00:00Z' })
    await state.load()
    expect(state.profile.value?.username).toBe('bob')
    expect(state.profile.value?.display_name).toBe('Bob')
  })
})
