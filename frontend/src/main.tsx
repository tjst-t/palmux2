import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'

import App from './App.tsx'
import './index.css'
// Side-effect imports register tab renderers with the registry.
import './tabs/files'
import './tabs/git'
import './tabs/claude-agent'
import './tabs/claude-tui'
import './tabs/sprint'
import './tabs/ports'
import './tabs/browser'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <BrowserRouter>
      <App />
    </BrowserRouter>
  </StrictMode>,
)

// Register the service worker only on a real (non-dev) host. Vite's HMR
// pages serve from a different origin which makes SW registration noisy.
//
// updateViaCache: 'none' keeps the browser from HTTP-caching sw.js itself,
// so a new bundle (with bumped VERSION) is picked up on the next visit
// without users having to clear cache by hand.
if ('serviceWorker' in navigator && import.meta.env.PROD) {
  // When a NEW service worker (a new release — its version is baked into sw.js by
  // the server) installs, skipWaiting()s and claims this client, `controllerchange`
  // fires. Reload once to swap onto the new bundle — this is what makes a GUI
  // self-update actually refresh the frontend (the app-shell SW otherwise pins the
  // old bundle across updates). Guarded: only when the page STARTED controlled (an
  // update, not first-ever registration) and only once.
  const hadController = !!navigator.serviceWorker.controller
  let reloaded = false
  navigator.serviceWorker.addEventListener('controllerchange', () => {
    if (!hadController || reloaded) return
    reloaded = true
    window.location.reload()
  })
  window.addEventListener('load', () => {
    navigator.serviceWorker
      .register('/sw.js', { updateViaCache: 'none' })
      .then((reg) => {
        // Probe for an updated worker on every load so a fresh tab picks it up.
        reg.update().catch(() => {})
      })
      .catch(() => {
        // SW failures are non-fatal — the app still works.
      })
  })
}
