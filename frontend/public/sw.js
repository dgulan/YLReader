/* YLReader static app-shell Service Worker.
 * Security invariant: ONLY same-origin static shell assets are cached.
 * /api/* endpoints, cross-origin requests, credentials, and non-GET requests
 * are NEVER cached or intercepted.
 */
const CACHE_PREFIX = 'ylreader-shell-'
const CACHE_VERSION = '__YLREADER_SW_VERSION__'
const CACHE_NAME = `${CACHE_PREFIX}${CACHE_VERSION}`
const STATIC_ASSETS = (() => {
  const injected = '__YLREADER_PRECACHE_ASSETS__'
  if (Array.isArray(injected)) return injected
  return []
})()
const PRECACHE_URLS = ['/', '/index.html', '/manifest.webmanifest', '/icon.svg', ...STATIC_ASSETS]

function isCacheableShellRequest(request, origin) {
  if (!request || request.method !== 'GET') return false
  let url
  try {
    url = new URL(request.url)
  } catch {
    return false
  }
  if (url.origin !== origin) return false
  if (url.pathname.startsWith('/api/')) return false
  if (url.pathname === '/sw.js') return false
  return true
}

function isCacheableResponse(response) {
  if (!response || !response.ok || response.status !== 200) return false
  if (response.type !== 'basic') return false
  const cc = (response.headers && response.headers.get('Cache-Control')) || ''
  if (/\bno-store\b/i.test(cc)) return false
  if (response.headers && response.headers.has('Set-Cookie')) return false
  return true
}

self.addEventListener('install', event => {
  event.waitUntil(
    caches
      .open(CACHE_NAME)
      .then(cache => cache.addAll(Array.from(new Set(PRECACHE_URLS))))
      .then(() => self.skipWaiting()),
  )
})

self.addEventListener('activate', event => {
  event.waitUntil(
    caches
      .keys()
      .then(keys =>
        Promise.all(
          keys
            .filter(key => key.startsWith(CACHE_PREFIX) && key !== CACHE_NAME)
            .map(key => caches.delete(key)),
        ),
      )
      .then(() => self.clients.claim()),
  )
})

self.addEventListener('fetch', event => {
  const { request } = event
  if (!isCacheableShellRequest(request, self.location.origin)) return

  const url = new URL(request.url)
  const isDocument =
    request.mode === 'navigate' ||
    url.pathname === '/' ||
    url.pathname === '/index.html' ||
    !url.pathname.includes('.')

  if (isDocument) {
    event.respondWith(
      fetch(request)
        .then(response => {
          if (isCacheableResponse(response)) {
            const copy = response.clone()
            void caches.open(CACHE_NAME).then(cache => cache.put('/index.html', copy))
          }
          return response
        })
        .catch(async () => {
          const cache = await caches.open(CACHE_NAME)
          return (
            (await cache.match('/index.html', { ignoreVary: true })) ||
            (await cache.match('/', { ignoreVary: true })) ||
            Response.error()
          )
        }),
    )
    return
  }

  event.respondWith(
    caches.open(CACHE_NAME).then(async cache => {
      const hit = await cache.match(request, { ignoreVary: true })
      if (hit) return hit
      const response = await fetch(request)
      if (isCacheableResponse(response)) {
        void cache.put(request, response.clone())
      }
      return response
    }),
  )
})
