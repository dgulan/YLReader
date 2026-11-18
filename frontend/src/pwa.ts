export function registerAppShell(prod = import.meta.env.PROD): Promise<ServiceWorkerRegistration | undefined> {
  if (!prod || typeof window === 'undefined' || !('serviceWorker' in navigator)) {
    return Promise.resolve(undefined)
  }
  return navigator.serviceWorker.register('/sw.js', { scope: '/' }).catch(() => undefined)
}

export function isCacheableShellURL(rawUrl: string, origin: string, method = 'GET'): boolean {
  if (method.toUpperCase() !== 'GET') return false
  let parsed: URL
  try {
    parsed = new URL(rawUrl, origin)
  } catch {
    return false
  }
  if (parsed.origin !== origin) return false
  if (parsed.pathname.startsWith('/api/')) return false
  if (parsed.pathname === '/sw.js') return false
  return true
}
