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
if ('serviceWorker' in navigator && import.meta.env.PROD) {
  window.addEventListener('load', () => {
    navigator.serviceWorker.register('/sw.js').catch(() => {
      // SW failures are non-fatal — the app still works.
    })
  })
}
