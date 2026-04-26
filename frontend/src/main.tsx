import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'

import App from './App.tsx'
import './index.css'
// Side-effect imports register tab renderers with the registry.
import './tabs/files'
import './tabs/git'

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
