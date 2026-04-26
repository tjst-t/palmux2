// Palmux app-shell service worker. Goal: make the SPA available offline /
// instantly on subsequent loads. Anything under /api/ or /auth always goes
// to the network — those are live state.
//
// VERSION must change every time we want browsers to drop the previous
// cache. We bake the bundle's content hash from the Vite build into it via
// the build pipeline; if the JS changes, this string changes, the browser
// detects sw.js as different, and reinstalls.

const VERSION = 'palmux-shell-v2'
const APP_SHELL = ['/favicon.svg', '/manifest.webmanifest']

self.addEventListener('install', (event) => {
  event.waitUntil(caches.open(VERSION).then((c) => c.addAll(APP_SHELL)))
  self.skipWaiting()
})

self.addEventListener('activate', (event) => {
  event.waitUntil(
    (async () => {
      const keys = await caches.keys()
      await Promise.all(keys.filter((k) => k !== VERSION).map((k) => caches.delete(k)))
      await self.clients.claim()
    })(),
  )
})

self.addEventListener('fetch', (event) => {
  const req = event.request
  if (req.method !== 'GET') return
  const url = new URL(req.url)

  // Never intercept API or auth — those need to round-trip live.
  if (url.pathname.startsWith('/api/') || url.pathname === '/auth') return

  // Navigation requests → serve cached index for offline; refresh in background.
  if (req.mode === 'navigate') {
    event.respondWith(
      (async () => {
        try {
          const fresh = await fetch(req)
          const cache = await caches.open(VERSION)
          cache.put('/index.html', fresh.clone())
          return fresh
        } catch {
          const cached = await caches.match('/index.html')
          return cached ?? Response.error()
        }
      })(),
    )
    return
  }

  // Hashed assets and static files → stale-while-revalidate.
  event.respondWith(
    (async () => {
      const cache = await caches.open(VERSION)
      const cached = await cache.match(req)
      const network = fetch(req)
        .then((res) => {
          if (res.ok) cache.put(req, res.clone())
          return res
        })
        .catch(() => undefined)
      return cached ?? (await network) ?? Response.error()
    })(),
  )
})
