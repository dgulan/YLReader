import { afterEach, describe, expect, it, vi } from 'vitest'
import swSource from '../public/sw.js?raw'
import manifestRaw from '../public/manifest.webmanifest?raw'
import { isCacheableShellURL, registerAppShell } from './pwa'

afterEach(() => {
  vi.restoreAllMocks()
})

describe('PWA app shell', () => {
  it('never caches /api/*, non-GET, cross-origin, or sw.js requests', () => {
    const origin = 'https://reader.example.com'
    expect(isCacheableShellURL('https://reader.example.com/assets/app.js', origin)).toBe(true)
    expect(isCacheableShellURL('https://reader.example.com/bookshelf', origin)).toBe(true)
    expect(isCacheableShellURL('https://reader.example.com/api/v1/books', origin)).toBe(false)
    expect(isCacheableShellURL('https://reader.example.com/api/v1/auth/me', origin)).toBe(false)
    expect(isCacheableShellURL('https://reader.example.com/sw.js', origin)).toBe(false)
    expect(isCacheableShellURL('https://other.example.com/assets/app.js', origin)).toBe(false)
    expect(isCacheableShellURL('https://reader.example.com/assets/app.js', origin, 'POST')).toBe(false)
  })

  it('public/sw.js explicitly rejects /api/*, Set-Cookie, and non-basic responses and manifest is valid JSON', () => {
    expect(swSource).toContain("url.pathname.startsWith('/api/')")
    expect(swSource).toContain("response.type !== 'basic'")
    expect(swSource).toContain("response.headers.has('Set-Cookie')")
    expect(swSource).toContain('no-store')
    const manifest = JSON.parse(manifestRaw) as { start_url?: string; display?: string }
    expect(manifest.start_url).toBe('/bookshelf')
    expect(manifest.display).toBe('standalone')
  })

  it('registers /sw.js only in production builds', async () => {
    const register = vi.fn(async () => ({}) as ServiceWorkerRegistration)
    Object.defineProperty(navigator, 'serviceWorker', {
      configurable: true,
      value: { register },
    })
    await registerAppShell(false)
    expect(register).not.toHaveBeenCalled()
    await registerAppShell(true)
    expect(register).toHaveBeenCalledWith('/sw.js', { scope: '/' })
  })
})
